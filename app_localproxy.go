package main

// Local proxy: a network profile exposed as a SOCKS5 proxy on a fixed
// port, so something outside the app (a CI runner's gateway container,
// curl, a browser) can reach the profile's network. Every connection
// brings the tunnel up on demand and holds it while open; the idle stop
// applies after the last one. Always behind a username and password:
// the listener can be reached by other local processes and containers.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
	"ssh-tool/internal/wg"
)

// LocalProxyConfig is the profile's local proxy setting (config_json
// "local_proxy").
type LocalProxyConfig struct {
	// Enabled: the profile's own proxy on Port.
	Enabled bool `json:"enabled"`
	// Shared: the profile takes part in the shared proxy, which picks a
	// profile by destination (app_sharedproxy.go).
	Shared bool   `json:"shared,omitempty"`
	Bind   string `json:"bind,omitempty"` // listen address, default 127.0.0.1
	Port   int    `json:"port"`
	// Networks are extra destinations for the gateway's routes, beyond
	// what the profile knows (pushed routes, connections in the tree).
	Networks []string `json:"networks,omitempty"`
}

const localProxyUser = "ssh-tool"

func localProxyVaultKey(profileID string) string { return "local_proxy:" + profileID }

type localProxy struct {
	ln   net.Listener
	addr string
}

var localProxies = struct {
	sync.Mutex
	m map[string]*localProxy
}{m: map[string]*localProxy{}}

func profileLocalProxy(configJSON string) LocalProxyConfig {
	var v struct {
		LocalProxy LocalProxyConfig `json:"local_proxy"`
	}
	_ = json.Unmarshal([]byte(configJSON), &v)
	return v.LocalProxy
}

func (c *LocalProxyConfig) normalize() error {
	c.Bind = strings.TrimSpace(c.Bind)
	if c.Bind == "" {
		c.Bind = "127.0.0.1"
	}
	if c.Enabled {
		if _, err := netip.ParseAddr(c.Bind); err != nil {
			return fmt.Errorf("listen address %q is not an IP address", c.Bind)
		}
		if c.Port < 1 || c.Port > 65535 {
			return fmt.Errorf("port must be 1-65535")
		}
	}
	var nets []string
	for _, n := range c.Networks {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		p, err := parseRoute(n)
		if err != nil {
			return fmt.Errorf("network %q: %w", n, err)
		}
		nets = append(nets, p.String())
	}
	c.Networks = nets
	return nil
}

// parseRoute reads a CIDR or a bare address (a host route).
func parseRoute(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(strings.SplitN(s, " ", 2)[0])
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, errors.New("not an address or CIDR")
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// NetworkProfileSetLocalProxy turns a profile's local proxy on or off and
// (re)starts its listener. The password is generated once and kept in
// the vault.
func (a *App) NetworkProfileSetLocalProxy(id string, cfg LocalProxyConfig) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if cfg.Enabled {
		if _, err := a.localProxyPassword(id); err != nil {
			return nil, err
		}
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(row.ConfigJSON), &m); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	m["local_proxy"], _ = json.Marshal(cfg)
	out, _ := json.Marshal(m)
	stopLocalProxy(id)
	if cfg.Enabled {
		if err := a.startLocalProxy(id, cfg); err != nil {
			return nil, err
		}
	}
	updated, err := a.db.UpdateNetworkProfile(id, row.Name, string(out))
	if err != nil {
		stopLocalProxy(id)
		return nil, err
	}
	a.recordAudit("network.profile.local_proxy", id, map[string]string{
		"enabled": strconv.FormatBool(cfg.Enabled), "shared": strconv.FormatBool(cfg.Shared),
		"listen": net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)),
	})
	sharedRoutesInvalidate()
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// localProxyPassword returns the profile's proxy password, creating it on
// first use.
func (a *App) localProxyPassword(id string) (string, error) {
	return a.proxyPasswordAt(localProxyVaultKey(id))
}

// proxyPasswordAt returns the proxy password under a vault key, creating
// it on first use.
func (a *App) proxyPasswordAt(key string) (string, error) {
	if pw, ok, err := a.vault.Get(key); err != nil {
		return "", fmt.Errorf("local proxy password: %w", err)
	} else if ok && pw != "" {
		return pw, nil
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pw := base64.RawURLEncoding.EncodeToString(b)
	if err := a.vault.Put(key, pw); err != nil {
		return "", fmt.Errorf("local proxy password: %w", err)
	}
	return pw, nil
}

// startLocalProxies brings up every enabled local proxy (app start).
func (a *App) startLocalProxies() {
	if c := a.sharedProxyConfig(); c.Enabled {
		if err := a.startSharedProxy(c); err != nil {
			log.Printf("network: shared proxy: %v", err)
		}
	}
	rows, err := a.db.ListNetworkProfiles()
	if err != nil {
		return
	}
	for _, r := range rows {
		if cfg := profileLocalProxy(r.ConfigJSON); cfg.Enabled {
			if err := a.startLocalProxy(r.ID, cfg); err != nil {
				log.Printf("network: local proxy for %q: %v", r.Name, err)
			}
		}
	}
}

func (a *App) startLocalProxy(id string, cfg LocalProxyConfig) error {
	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	localProxies.Lock()
	localProxies.m[id] = &localProxy{ln: ln, addr: addr}
	localProxies.Unlock()
	log.Printf("network: local proxy for profile %s listening on %s", id, addr)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serveLocalProxy(id, c)
		}
	}()
	return nil
}

func stopLocalProxy(id string) {
	localProxies.Lock()
	p := localProxies.m[id]
	delete(localProxies.m, id)
	localProxies.Unlock()
	if p != nil {
		_ = p.ln.Close()
	}
}

func stopLocalProxies() {
	stopSharedProxy()
	localProxies.Lock()
	ids := make([]string, 0, len(localProxies.m))
	for id := range localProxies.m {
		ids = append(ids, id)
	}
	localProxies.Unlock()
	for _, id := range ids {
		stopLocalProxy(id)
	}
}

// serveLocalProxy is one client of a profile's own proxy.
func (a *App) serveLocalProxy(id string, c net.Conn) {
	a.serveSOCKS(c, func(pw string) bool {
		want, err := a.localProxyPassword(id)
		return err == nil && subtleEqual(pw, want)
	}, func(string) (string, error) { return id, nil }, "profile "+id)
}

// serveSOCKS is one SOCKS5 client: username/password auth (RFC 1929),
// or none when checkPass is nil (the browser proxy: browsers cannot send
// SOCKS credentials), then CONNECT through the tunnel of the profile
// pick chooses for the destination host; pick returning "" dials
// directly.
func (a *App) serveSOCKS(c net.Conn, checkPass func(string) bool, pick func(host string) (string, error), what string) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	want := byte(2)
	if checkPass == nil {
		want = 0
	}
	offered := false
	for _, m := range methods {
		if m == want {
			offered = true
		}
	}
	if !offered {
		_, _ = c.Write([]byte{5, 0xff})
		return
	}
	_, _ = c.Write([]byte{5, want})
	if checkPass != nil {
		user, pass, ok := readSOCKSAuth(c)
		if !ok {
			return
		}
		if user != localProxyUser || !checkPass(pass) {
			_, _ = c.Write([]byte{1, 1})
			log.Printf("network: local proxy (%s): refused a client with wrong credentials", what)
			return
		}
		_, _ = c.Write([]byte{1, 0})
	}
	// request: VER CMD RSV ATYP DST.ADDR DST.PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	reply := func(code byte) { _, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	if req[1] != 1 { // CONNECT only
		reply(7)
		return
	}
	var host string
	switch req[3] {
	case 1, 4:
		n := 4
		if req[3] == 4 {
			n = 16
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		ip, _ := netip.AddrFromSlice(b)
		host = ip.String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	default:
		reply(8)
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))

	id, err := pick(host)
	if err != nil {
		log.Printf("network: local proxy (%s): %s: %v", what, target, err)
		reply(2) // not allowed by ruleset
		return
	}
	var d tunnelDialer = &net.Dialer{}
	name := "direct"
	if id != "" {
		row, err := a.db.GetNetworkProfile(id)
		if err != nil || row == nil {
			reply(1)
			return
		}
		name = row.Name
		// A login prompt (one-time code) can take a while; the client waits.
		_ = c.SetDeadline(time.Now().Add(6 * time.Minute))
		if d, err = a.ensureTunnel(row); err != nil {
			log.Printf("network: local proxy %q: tunnel: %v", row.Name, err)
			reply(1)
			return
		}
		hold := fmt.Sprintf("proxy:%s:%d", id, time.Now().UnixNano())
		a.wgAcquire(id, hold)
		defer a.wgRelease(hold)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	up, err := d.DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		log.Printf("network: local proxy %q: dial %s: %v", name, target, err)
		reply(5)
		return
	}
	defer up.Close()
	reply(0)
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); closeWrite(up); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	// A tunnel conn may have no half-close, so the other side never sees
	// EOF and its copy (with the profile hold) would last until the far
	// end gives up. After a grace period both ends are closed.
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = c.Close()
		_ = up.Close()
		<-done
	}
}

// readSOCKSAuth reads the RFC 1929 username / password request.
func readSOCKSAuth(c net.Conn) (user, pass string, ok bool) {
	h := make([]byte, 2)
	if _, err := io.ReadFull(c, h); err != nil || h[0] != 1 {
		return "", "", false
	}
	u := make([]byte, h[1])
	if _, err := io.ReadFull(c, u); err != nil {
		return "", "", false
	}
	pl := make([]byte, 1)
	if _, err := io.ReadFull(c, pl); err != nil {
		return "", "", false
	}
	p := make([]byte, pl[0])
	if _, err := io.ReadFull(c, p); err != nil {
		return "", "", false
	}
	return string(u), string(p), true
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// LocalProxyDetails is what the UI shows / copies for a profile's proxy.
type LocalProxyDetails struct {
	URL       string   `json:"url"`     // socks5://user:pass@host:port, for the host
	Routes    []string `json:"routes"`  // destinations to send to the proxy
	Sources   []string `json:"sources"` // where they came from, for the hint
	Gateway   string   `json:"gateway"` // docker run for a tun2socks gateway
	Listening bool     `json:"listening"`
}

// NetworkProfileLocalProxyDetails builds the proxy URL, the routes the
// profile reaches and a ready gateway command. With the profile's own
// proxy off it carries the routes only.
func (a *App) NetworkProfileLocalProxyDetails(id string) (*LocalProxyDetails, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	cfg := profileLocalProxy(row.ConfigJSON)
	routes, sources := a.profileRoutes(row, cfg)
	d := &LocalProxyDetails{Routes: collapseRoutes(routes), Sources: sources}
	if !cfg.Enabled {
		// Routes only: what the app proxy sends through this profile.
		return d, nil
	}
	pw, err := a.localProxyPassword(id)
	if err != nil {
		return nil, err
	}
	d.URL = fmt.Sprintf("socks5://%s:%s@%s", localProxyUser, pw, net.JoinHostPort(displayHost(cfg.Bind), strconv.Itoa(cfg.Port)))
	localProxies.Lock()
	d.Listening = localProxies.m[id] != nil
	localProxies.Unlock()
	d.Gateway = gatewayCommand("sshtool-gw-"+slugName(row.Name), pw, cfg.Port, d.Routes)
	return d, nil
}

// gatewayCommand is the docker run for a tun2socks gateway sending routes
// into the proxy, on one line so it pastes into PowerShell as well as a
// POSIX shell. From a container the host is host.docker.internal
// (Docker Desktop forwards it to the host's loopback; on Linux,
// host-gateway). tun2socks sends everything to the proxy by default:
// EXTRA_COMMANDS drops its catch-all rule so only the routes go there
// (gotcha 108).
func gatewayCommand(name, pw string, port int, routes []string) string {
	return strings.Join([]string{
		"docker run -d --name " + name + " --restart unless-stopped",
		"--cap-add NET_ADMIN --device /dev/net/tun",
		"--add-host host.docker.internal:host-gateway",
		fmt.Sprintf("-e PROXY=socks5://%s:%s@host.docker.internal:%d", localProxyUser, pw, port),
		"-e TUN_INCLUDED_ROUTES=" + strings.Join(routes, ","),
		"-e EXTRA_COMMANDS='ip rule del not fwmark 0x22b table 0x22b'",
		tun2socksImage,
	}, " ")
}

// displayHost is where a client on this machine reaches a listener: a
// wildcard bind is reached on loopback.
func displayHost(bind string) string {
	switch bind {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	}
	return bind
}

// collapseRoutes drops routes another route in the list already covers
// (a connection's /32 inside a pushed /16), sorted.
func collapseRoutes(routes []string) []string {
	var ps []netip.Prefix
	for _, r := range routes {
		if p, err := netip.ParsePrefix(r); err == nil {
			ps = append(ps, p.Masked())
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Bits() < ps[j].Bits() })
	var keep []netip.Prefix
next:
	for _, p := range ps {
		for _, k := range keep {
			if k.Bits() <= p.Bits() && k.Contains(p.Addr()) {
				continue next
			}
		}
		keep = append(keep, p)
	}
	out := make([]string, 0, len(keep))
	for _, k := range keep {
		out = append(out, k.String())
	}
	sort.Strings(out)
	return out
}

// tun2socksImage is the gateway image the generated command uses.
const tun2socksImage = "xjasonlyu/tun2socks:v2.6.0"

func slugName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// profileRoutes is what a profile reaches: its routes (WireGuard
// AllowedIPs, or what an OpenConnect / OpenVPN server pushed last time),
// the IP addresses of connections that use it, and the extra networks
// on the proxy. Default routes are left out: a full-tunnel server would
// otherwise pull every destination into the customer's VPN.
func (a *App) profileRoutes(row *store.NetworkProfile, cfg LocalProxyConfig) ([]string, []string) {
	set := map[netip.Prefix]bool{}
	var sources []string
	add := func(s string) bool {
		p, err := parseRoute(s)
		if err != nil || p.Bits() == 0 {
			return false
		}
		set[p] = true
		return true
	}
	n := 0
	switch parsePolicy(row.ConfigJSON).Kind {
	case kindWireguard:
		var p wg.Profile
		_ = json.Unmarshal([]byte(row.ConfigJSON), &p)
		for _, peer := range p.Peers {
			for _, r := range peer.AllowedIPs {
				if add(r) {
					n++
				}
			}
		}
		if n > 0 {
			sources = append(sources, fmt.Sprintf("%d from the WireGuard peers", n))
		}
	default:
		var v struct {
			LastRoutes []string `json:"last_routes"`
		}
		_ = json.Unmarshal([]byte(row.ConfigJSON), &v)
		for _, r := range v.LastRoutes {
			if add(r) {
				n++
			}
		}
		if n > 0 {
			sources = append(sources, fmt.Sprintf("%d pushed by the server (last connect)", n))
		} else {
			sources = append(sources, "none pushed yet - connect once, or the server sends a full tunnel only")
		}
	}
	n = 0
	if conns, err := a.db.ListConnections(nil); err == nil {
		folders, _ := a.db.ListFolders()
		for _, c := range conns {
			rs := resolver.ResolveWith(c, folders)
			if rs.NetworkProfileID == nil || *rs.NetworkProfileID != row.ID {
				continue
			}
			if ip, err := netip.ParseAddr(rs.Hostname); err == nil && add(ip.String()) {
				n++
			}
		}
	}
	if n > 0 {
		sources = append(sources, fmt.Sprintf("%d connection address(es) using this profile", n))
	}
	n = 0
	for _, r := range cfg.Networks {
		if add(r) {
			n++
		}
	}
	if n > 0 {
		sources = append(sources, fmt.Sprintf("%d added by hand", n))
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p.String())
	}
	sort.Strings(out)
	return out, sources
}

// rememberRoutes stores the routes a helper reported (OpenConnect /
// OpenVPN), so the local proxy can name them while the tunnel is down.
func (a *App) rememberRoutes(id string, info json.RawMessage) {
	var v struct {
		Routes []string `json:"routes"`
	}
	if json.Unmarshal(info, &v) != nil || len(v.Routes) == 0 {
		return
	}
	var routes []string
	for _, r := range v.Routes {
		if p, err := parseRoute(r); err == nil && p.Bits() > 0 {
			routes = append(routes, p.String())
		}
	}
	row, err := a.db.GetNetworkProfile(id)
	if err != nil || row == nil {
		return
	}
	m := map[string]json.RawMessage{}
	if json.Unmarshal([]byte(row.ConfigJSON), &m) != nil {
		return
	}
	nb, _ := json.Marshal(routes)
	if string(m["last_routes"]) == string(nb) {
		return
	}
	m["last_routes"] = nb
	out, _ := json.Marshal(m)
	_, _ = a.db.UpdateNetworkProfile(id, row.Name, string(out))
}
