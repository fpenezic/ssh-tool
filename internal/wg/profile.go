// Package wg runs userspace WireGuard tunnels (wireguard-go +
// gVisor netstack) entirely in-process: no TUN adapter, no admin
// rights, no system routes. A tunnel exposes DialContext, which the
// SSH layer uses as the first-hop dialer when a connection resolves
// to a network profile.
package wg

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Connect policy for a profile. Not wg-quick keys - app-level,
// set from the profiles UI and carried in the stored config JSON.
const (
	// ModeAlways: the first hop always dials through the tunnel.
	// Tunnel failure fails the connect - never a silent direct dial.
	ModeAlways = "always"
	// ModeAuto: probe a direct TCP dial first (short timeout); only
	// when that fails bring the tunnel up and dial through it. For
	// hosts that are reachable directly when on-site and need the
	// tunnel from everywhere else.
	ModeAuto = "auto"
)

// Profile is a parsed wg-quick style configuration. Secrets
// (PrivateKey, PresharedKey) are populated from the vault just
// before a tunnel starts and never persisted in the DB - the
// stored config JSON carries them empty.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Connect policy: ModeAlways (default when empty) or ModeAuto.
	Mode string `json:"mode,omitempty"`
	// Paused is the per-profile kill switch: while true every
	// connection using this profile dials DIRECT (tunnel never
	// starts). For "I'm on-site, leave the network alone".
	Paused bool `json:"paused,omitempty"`

	// [Interface]
	PrivateKey string   `json:"-"`         // base64, vault-only
	Addresses  []string `json:"addresses"` // CIDR or bare IP
	DNS        []string `json:"dns"`       // resolver IPs inside the tunnel
	MTU        int      `json:"mtu"`       // 0 -> 1420

	Peers []Peer `json:"peers"`
}

// Peer is one [Peer] section.
type Peer struct {
	PublicKey    string   `json:"public_key"`  // base64
	PresharedKey string   `json:"-"`           // base64, vault-only
	HasPSK       bool     `json:"has_psk"`     // remembers a PSK exists while the value lives in the vault
	Endpoint     string   `json:"endpoint"`    // host:port
	AllowedIPs   []string `json:"allowed_ips"` // CIDRs
	Keepalive    int      `json:"keepalive"`   // seconds, 0 = off
}

const defaultMTU = 1420

// ParseConf parses a wg-quick configuration file. wg-quick-only
// directives that require root / a real interface (Table, FwMark,
// PostUp/Down, PreUp/Down, SaveConfig) are ignored: netstack has no
// routing table to manipulate.
func ParseConf(text string) (*Profile, error) {
	p := &Profile{}
	var cur *Peer
	section := ""
	for ln, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if low == "[interface]" {
			section = "interface"
			continue
		}
		if low == "[peer]" {
			p.Peers = append(p.Peers, Peer{})
			cur = &p.Peers[len(p.Peers)-1]
			section = "peer"
			continue
		}
		if strings.HasPrefix(line, "[") {
			return nil, fmt.Errorf("line %d: unknown section %s", ln+1, line)
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key = value", ln+1)
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch section {
		case "interface":
			switch key {
			case "privatekey":
				p.PrivateKey = val
			case "address":
				p.Addresses = append(p.Addresses, splitList(val)...)
			case "dns":
				p.DNS = append(p.DNS, splitList(val)...)
			case "mtu":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("line %d: bad MTU %q", ln+1, val)
				}
				p.MTU = n
			case "listenport", "table", "fwmark", "saveconfig",
				"preup", "postup", "predown", "postdown":
				// no-op in userspace netstack mode
			default:
				return nil, fmt.Errorf("line %d: unknown [Interface] key %q", ln+1, k)
			}
		case "peer":
			switch key {
			case "publickey":
				cur.PublicKey = val
			case "presharedkey":
				cur.PresharedKey = val
				cur.HasPSK = val != ""
			case "endpoint":
				cur.Endpoint = val
			case "allowedips":
				cur.AllowedIPs = append(cur.AllowedIPs, splitList(val)...)
			case "persistentkeepalive":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("line %d: bad PersistentKeepalive %q", ln+1, val)
				}
				cur.Keepalive = n
			default:
				return nil, fmt.Errorf("line %d: unknown [Peer] key %q", ln+1, k)
			}
		default:
			return nil, fmt.Errorf("line %d: key outside a section", ln+1)
		}
	}
	return p, p.Validate()
}

// Validate checks everything needed to bring the tunnel up EXCEPT
// secrets - those are vault-resolved later, so a stored (secretless)
// profile still validates.
func (p *Profile) Validate() error {
	if len(p.Addresses) == 0 {
		return fmt.Errorf("no Address in [Interface]")
	}
	for _, a := range p.Addresses {
		if _, err := parseAddr(a); err != nil {
			return fmt.Errorf("bad Address %q: %w", a, err)
		}
	}
	for _, d := range p.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("bad DNS %q: %w", d, err)
		}
	}
	if len(p.Peers) == 0 {
		return fmt.Errorf("no [Peer] section")
	}
	for i, peer := range p.Peers {
		if peer.PublicKey == "" {
			return fmt.Errorf("peer %d: no PublicKey", i+1)
		}
		if _, err := keyToHex(peer.PublicKey); err != nil {
			return fmt.Errorf("peer %d: bad PublicKey: %w", i+1, err)
		}
		if peer.Endpoint == "" {
			return fmt.Errorf("peer %d: no Endpoint (userspace mode cannot wait for inbound)", i+1)
		}
		if len(peer.AllowedIPs) == 0 {
			return fmt.Errorf("peer %d: no AllowedIPs", i+1)
		}
		for _, c := range peer.AllowedIPs {
			if _, err := netip.ParsePrefix(normalizeCIDR(c)); err != nil {
				return fmt.Errorf("peer %d: bad AllowedIPs entry %q: %w", i+1, c, err)
			}
		}
	}
	return nil
}

// InterfaceAddrs returns the interface addresses without prefix
// length, ready for netstack.CreateNetTUN.
func (p *Profile) InterfaceAddrs() ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(p.Addresses))
	for _, a := range p.Addresses {
		addr, err := parseAddr(a)
		if err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, nil
}

// DNSAddrs returns the tunnel resolvers as netip addresses.
func (p *Profile) DNSAddrs() ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(p.DNS))
	for _, d := range p.DNS {
		addr, err := netip.ParseAddr(d)
		if err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, nil
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseAddr accepts both bare IPs and CIDR notation, returning the
// address part.
func parseAddr(s string) (netip.Addr, error) {
	if pfx, err := netip.ParsePrefix(s); err == nil {
		return pfx.Addr(), nil
	}
	return netip.ParseAddr(s)
}

// normalizeCIDR turns a bare IP into a host prefix so users can write
// AllowedIPs = 10.0.0.5 and mean /32.
func normalizeCIDR(s string) string {
	if !strings.Contains(s, "/") {
		if a, err := netip.ParseAddr(s); err == nil {
			if a.Is4() {
				return s + "/32"
			}
			return s + "/128"
		}
	}
	return s
}

// keyToHex converts a base64 WireGuard key to the lowercase hex the
// device UAPI (IpcSet) expects.
func keyToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("not base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key is %d bytes, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
