package main

// A browser through a network profile: an isolated browser (its own
// persistent profile per VPN, apart from the everyday one) behind a
// loopback SOCKS proxy that sends the profile's networks into the tunnel
// and everything else out directly, so the customer's intranet opens
// while the internet stays on the normal network. Browsers cannot send
// SOCKS credentials, so this proxy has none - like the SSH dynamic
// forwards - and listens on 127.0.0.1 only, closing after an idle spell.
// Web bookmarks in the tree (protocol "web") open through it.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

const browserProxyIdle = 30 * time.Minute

type browserProxy struct {
	ln       net.Listener
	port     int
	mu       sync.Mutex
	active   int
	lastUsed time.Time
}

// browserProxies is one listener per profile, on a port kept for the
// app's lifetime: a Chromium instance already running for the profile
// keeps the --proxy-server it was started with, so a reopened listener
// must come back on the same port.
var browserProxies = struct {
	sync.Mutex
	m     map[string]*browserProxy
	ports map[string]int
}{m: map[string]*browserProxy{}, ports: map[string]int{}}

// NetworkProfileOpenBrowser opens url ("" = a blank page) in the
// profile's isolated browser.
func (a *App) NetworkProfileOpenBrowser(id, url string) (*BrowserLaunchResult, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil || row == nil {
		return nil, fmt.Errorf("network profile %s not found", id)
	}
	url = strings.TrimSpace(url)
	if url != "" && !strings.Contains(url, "://") {
		url = "http://" + url
	}
	// The URL becomes a browser argument: only http(s), never something a
	// browser would read as a switch.
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("only http and https addresses can be opened")
	}
	port, err := a.ensureBrowserProxy(row)
	if err != nil {
		return nil, err
	}
	preferred, _, _ := a.db.GetSetting("preferred_browser_path")
	pid, err := sshlayer.LaunchIsolatedBrowser("127.0.0.1", uint16(port), url, sshlayer.LaunchOptions{
		PreferredPath:  preferred,
		Persistent:     true, // logins to the customer's tools survive
		ProfileBaseDir: store.DataDir(),
		ProfileKey:     "vpn-" + id,
	})
	if err != nil {
		return nil, err
	}
	a.recordAudit("network.profile.browser", id, map[string]string{"name": row.Name})
	return &BrowserLaunchResult{PID: pid}, nil
}

func (a *App) ensureBrowserProxy(row *store.NetworkProfile) (int, error) {
	browserProxies.Lock()
	defer browserProxies.Unlock()
	if p := browserProxies.m[row.ID]; p != nil {
		p.mu.Lock()
		p.lastUsed = time.Now()
		p.mu.Unlock()
		return p.port, nil
	}
	addr := "127.0.0.1:" + strconv.Itoa(browserProxies.ports[row.ID])
	ln, err := net.Listen("tcp", addr)
	if err != nil && browserProxies.ports[row.ID] != 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:0") // the old port got taken
	}
	if err != nil {
		return 0, fmt.Errorf("browser proxy: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	p := &browserProxy{ln: ln, port: port, lastUsed: time.Now()}
	browserProxies.m[row.ID] = p
	browserProxies.ports[row.ID] = port
	id := row.ID
	log.Printf("network: browser proxy for %q on 127.0.0.1:%d", row.Name, port)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.active++
			p.lastUsed = time.Now()
			p.mu.Unlock()
			go func() {
				a.serveSOCKS(c, nil, func(host string) (string, string, error) {
					return a.browserPick(id, host)
				}, "browser")
				p.mu.Lock()
				p.active--
				p.lastUsed = time.Now()
				p.mu.Unlock()
			}()
		}
	}()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			p.mu.Lock()
			idle := p.active == 0 && time.Since(p.lastUsed) > browserProxyIdle
			p.mu.Unlock()
			if !idle {
				continue
			}
			browserProxies.Lock()
			if browserProxies.m[id] == p {
				delete(browserProxies.m, id)
			}
			browserProxies.Unlock()
			_ = ln.Close()
			log.Printf("network: browser proxy for profile %s idle, closed", id)
			return
		}
	}()
	return port, nil
}

func stopBrowserProxies() {
	browserProxies.Lock()
	defer browserProxies.Unlock()
	for id, p := range browserProxies.m {
		_ = p.ln.Close()
		delete(browserProxies.m, id)
	}
}

// browserPick sends a destination into the profile's tunnel when it is
// one of the profile's networks, a connection's name, or a name only the
// customer's DNS knows; anything else goes out directly (""). dialHost is
// what to dial: a name the local resolver (a hosts entry, say) put inside
// the profile's networks is dialed by that address, since the customer's
// DNS may not know it; the browser still sends the name (Host, SNI).
func (a *App) browserPick(id, host string) (pid, dialHost string, err error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil || row == nil {
		return "", "", errors.New("profile gone")
	}
	routes, _ := a.profileRoutes(row, profileLocalProxy(row.ConfigJSON))
	in := func(ip netip.Addr) bool {
		for _, r := range routes {
			if p, err := netip.ParsePrefix(r); err == nil && p.Contains(ip) {
				return true
			}
		}
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if in(ip.Unmap()) {
			return id, host, nil
		}
		return "", host, nil
	}
	if a.profileHostname(id, host) {
		return id, host, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	cancel()
	if err != nil || len(addrs) == 0 {
		// Unknown here: most likely an intranet name, resolved inside
		// the VPN by the tunnel's dialer.
		return id, host, nil
	}
	if ip := addrs[0].Unmap(); in(ip) {
		return id, ip.String(), nil
	}
	return "", host, nil
}

// profileHostname reports whether a connection using the profile has
// this host name.
func (a *App) profileHostname(id, host string) bool {
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return false
	}
	folders, _ := a.db.ListFolders()
	for _, c := range conns {
		if !strings.EqualFold(strings.TrimSpace(c.Hostname), host) {
			continue
		}
		rs := resolver.ResolveWith(c, folders)
		if rs.NetworkProfileID != nil && *rs.NetworkProfileID == id {
			return true
		}
	}
	return false
}

// ConnectionOpenWeb opens a web bookmark connection (protocol "web", the
// address in hostname): through the browser of the network profile it
// resolves to (inherited from its folders like any setting), else in the
// user's normal browser.
func (a *App) ConnectionOpenWeb(id string) error {
	c, err := a.db.GetConnection(id)
	if err != nil || c == nil {
		return fmt.Errorf("connection %s not found", id)
	}
	if c.Protocol != "web" {
		return errors.New("not a web bookmark")
	}
	url := strings.TrimSpace(c.Hostname)
	if url == "" {
		return errors.New("the bookmark has no address")
	}
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return errors.New("only http(s) addresses")
	}
	a.touchRecent(id)
	folders, _ := a.db.ListFolders()
	rs := resolver.ResolveWith(*c, folders)
	if rs.NetworkProfileID != nil && *rs.NetworkProfileID != "" {
		_, err := a.NetworkProfileOpenBrowser(*rs.NetworkProfileID, url)
		return err
	}
	return BrowserOpenURL(url)
}

// NetworkProfileUsage is how many connections and web bookmarks resolve
// to each profile (inherited from folders included), for the profile
// page's "Used by".
type NetworkProfileUsage struct {
	Connections int `json:"connections"`
	Web         int `json:"web"`
}

func (a *App) NetworkProfilesUsage() (map[string]NetworkProfileUsage, error) {
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	folders, _ := a.db.ListFolders()
	out := map[string]NetworkProfileUsage{}
	for _, c := range conns {
		rs := resolver.ResolveWith(c, folders)
		if rs.NetworkProfileID == nil || *rs.NetworkProfileID == "" {
			continue
		}
		u := out[*rs.NetworkProfileID]
		if c.Protocol == "web" {
			u.Web++
		} else {
			u.Connections++
		}
		out[*rs.NetworkProfileID] = u
	}
	return out, nil
}
