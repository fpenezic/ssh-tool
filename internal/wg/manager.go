package wg

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Tunnel is one live userspace WireGuard device + its netstack.
type Tunnel struct {
	ProfileID string
	Name      string // profile display name, for logs / UI
	dev       *device.Device
	tnet      *netstack.Net
	startedAt time.Time
}

// DialContext dials host:port through the tunnel. Hostnames resolve
// via the profile's DNS servers inside the tunnel (netstack's own
// resolver); without DNS in the profile only IP literals work.
func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.tnet.DialContext(ctx, network, addr)
}

// Status is a point-in-time snapshot for the UI, parsed from the
// device's UAPI dump.
type Status struct {
	ProfileID     string `json:"profile_id"`
	Running       bool   `json:"running"`
	StartedAt     int64  `json:"started_at"` // unix seconds, 0 when not running
	LastHandshake int64  `json:"last_handshake"`
	RxBytes       int64  `json:"rx_bytes"`
	TxBytes       int64  `json:"tx_bytes"`
}

// Manager owns the running tunnels, keyed by profile id. Tunnels
// start lazily on first dial and stay up until StopAll / Stop - an
// SSH reconnect through the same profile reuses the running device.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*Tunnel
}

func NewManager() *Manager {
	return &Manager{tunnels: make(map[string]*Tunnel)}
}

// Ensure returns the running tunnel for the profile, starting it if
// needed. The profile must arrive with secrets already resolved
// (PrivateKey and any PresharedKeys populated from the vault).
func (m *Manager) Ensure(p *Profile) (*Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tunnels[p.ID]; ok {
		return t, nil
	}
	t, err := startTunnel(p)
	if err != nil {
		return nil, err
	}
	m.tunnels[p.ID] = t
	log.Printf("wg: tunnel up for profile %s (%s)", p.Name, p.ID)
	return t, nil
}

// Get returns a running tunnel or nil.
func (m *Manager) Get(profileID string) *Tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tunnels[profileID]
}

// Stop tears down one tunnel. Existing net.Conns dialed through it
// die with it - callers own that lifecycle (same contract as killing
// an SSH session with forwards on it).
func (m *Manager) Stop(profileID string) {
	m.mu.Lock()
	t, ok := m.tunnels[profileID]
	if ok {
		delete(m.tunnels, profileID)
	}
	m.mu.Unlock()
	if ok {
		t.dev.Close()
		log.Printf("wg: tunnel %s stopped", t.Name)
	}
}

// StopAll tears down everything (app shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	ts := m.tunnels
	m.tunnels = make(map[string]*Tunnel)
	m.mu.Unlock()
	for _, t := range ts {
		t.dev.Close()
	}
}

// Status reports the tunnel state for one profile; Running=false
// with zero fields when it isn't up.
func (m *Manager) Status(profileID string) Status {
	m.mu.Lock()
	t := m.tunnels[profileID]
	m.mu.Unlock()
	st := Status{ProfileID: profileID}
	if t == nil {
		return st
	}
	st.Running = true
	st.StartedAt = t.startedAt.Unix()
	dump, err := t.dev.IpcGet()
	if err != nil {
		return st
	}
	for _, line := range strings.Split(dump, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			if n, err := parseI64(v); err == nil && n > st.LastHandshake {
				st.LastHandshake = n
			}
		case "rx_bytes":
			if n, err := parseI64(v); err == nil {
				st.RxBytes += n
			}
		case "tx_bytes":
			if n, err := parseI64(v); err == nil {
				st.TxBytes += n
			}
		}
	}
	return st
}

func parseI64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

// startTunnel builds the netstack TUN + wireguard device from the
// profile and brings it up.
func startTunnel(p *Profile) (*Tunnel, error) {
	if p.PrivateKey == "" {
		return nil, fmt.Errorf("profile %s: private key not available (vault locked?)", p.Name)
	}
	addrs, err := p.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("interface address: %w", err)
	}
	dns, err := p.DNSAddrs()
	if err != nil {
		return nil, fmt.Errorf("dns: %w", err)
	}
	mtu := p.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}
	tunDev, tnet, err := netstack.CreateNetTUN(addrs, dns, mtu)
	if err != nil {
		return nil, fmt.Errorf("netstack: %w", err)
	}

	uapi, err := buildUAPI(p)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, fmt.Sprintf("wg[%s] ", p.Name)))
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("device up: %w", err)
	}
	return &Tunnel{ProfileID: p.ID, Name: p.Name, dev: dev, tnet: tnet, startedAt: time.Now()}, nil
}

// buildUAPI renders the device.IpcSet key=value block: hex keys,
// resolved endpoints. Endpoints get a DNS resolve HERE (over the
// normal network - the tunnel isn't up yet), matching what wg-quick
// does.
func buildUAPI(p *Profile) (string, error) {
	var b strings.Builder
	priv, err := keyToHex(p.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\n", priv)
	for i, peer := range p.Peers {
		pub, err := keyToHex(peer.PublicKey)
		if err != nil {
			return "", fmt.Errorf("peer %d public key: %w", i+1, err)
		}
		fmt.Fprintf(&b, "public_key=%s\n", pub)
		if peer.PresharedKey != "" {
			psk, err := keyToHex(peer.PresharedKey)
			if err != nil {
				return "", fmt.Errorf("peer %d preshared key: %w", i+1, err)
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", psk)
		}
		ep, err := resolveEndpoint(peer.Endpoint)
		if err != nil {
			return "", fmt.Errorf("peer %d endpoint %q: %w", i+1, peer.Endpoint, err)
		}
		fmt.Fprintf(&b, "endpoint=%s\n", ep)
		if peer.Keepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", peer.Keepalive)
		}
		for _, cidr := range peer.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", normalizeCIDR(cidr))
		}
	}
	return b.String(), nil
}

// resolveEndpoint turns host:port into ip:port (UAPI refuses names).
// IPv4 is preferred when both families resolve - the common case for
// WG endpoints on dual-stack names.
func resolveEndpoint(ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", err
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return netip.AddrPortFrom(a, mustPort(port)).String(), nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", err
	}
	var pick net.IP
	for _, ip := range ips {
		if ip.To4() != nil {
			pick = ip
			break
		}
	}
	if pick == nil && len(ips) > 0 {
		pick = ips[0]
	}
	if pick == nil {
		return "", fmt.Errorf("no addresses")
	}
	a, ok := netip.AddrFromSlice(pick)
	if !ok {
		return "", fmt.Errorf("bad resolved address %v", pick)
	}
	return netip.AddrPortFrom(a.Unmap(), mustPort(port)).String(), nil
}

func mustPort(s string) uint16 {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return uint16(n)
}
