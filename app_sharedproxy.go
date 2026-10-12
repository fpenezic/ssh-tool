package main

// Shared proxy: one SOCKS5 proxy for every profile marked "shared",
// picking the profile by destination. Lets one CI runner (one gateway
// container) reach several customers. A destination no shared profile
// covers is refused: the proxy is not a way out to the internet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/resolver"
)

// SharedProxyConfig is the app setting sharedProxyKey.
type SharedProxyConfig struct {
	Enabled bool   `json:"enabled"`
	Bind    string `json:"bind,omitempty"`
	Port    int    `json:"port"`
	// Use is a manual override: this profile takes every destination its
	// networks contain, whatever another profile also covers. "" = by
	// route (most specific wins).
	Use string `json:"use,omitempty"`
}

const (
	sharedProxyKey      = "network_shared_proxy"
	sharedProxyVaultKey = "local_proxy:shared"
)

var sharedProxy struct {
	sync.Mutex
	ln net.Listener
}

func (a *App) sharedProxyConfig() SharedProxyConfig {
	var c SharedProxyConfig
	if v, ok, _ := a.db.GetSetting(sharedProxyKey); ok {
		_ = json.Unmarshal([]byte(v), &c)
	}
	if c.Bind == "" {
		c.Bind = "127.0.0.1"
	}
	return c
}

// NetworkSharedProxyGet returns the shared proxy setting and whether it
// is listening.
func (a *App) NetworkSharedProxyGet() (SharedProxyConfig, bool) {
	sharedProxy.Lock()
	defer sharedProxy.Unlock()
	return a.sharedProxyConfig(), sharedProxy.ln != nil
}

// NetworkSharedProxySet saves the shared proxy setting and (re)starts it.
func (a *App) NetworkSharedProxySet(c SharedProxyConfig) error {
	c.Bind = strings.TrimSpace(c.Bind)
	if c.Bind == "" {
		c.Bind = "127.0.0.1"
	}
	if c.Enabled {
		if _, err := netip.ParseAddr(c.Bind); err != nil {
			return fmt.Errorf("listen address %q is not an IP address", c.Bind)
		}
		if c.Port < 1 || c.Port > 65535 {
			return errors.New("port must be 1-65535")
		}
		if _, err := a.sharedProxyPassword(); err != nil {
			return err
		}
	}
	stopSharedProxy()
	if c.Enabled {
		if err := a.startSharedProxy(c); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(c)
	if err := a.db.SetSetting(sharedProxyKey, string(b)); err != nil {
		stopSharedProxy()
		return err
	}
	a.recordAudit("network.shared_proxy", "", map[string]string{
		"enabled": strconv.FormatBool(c.Enabled), "listen": net.JoinHostPort(c.Bind, strconv.Itoa(c.Port)),
	})
	return nil
}

func (a *App) sharedProxyPassword() (string, error) {
	return a.proxyPasswordAt(sharedProxyVaultKey)
}

func (a *App) startSharedProxy(c SharedProxyConfig) error {
	addr := net.JoinHostPort(c.Bind, strconv.Itoa(c.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	sharedProxy.Lock()
	sharedProxy.ln = ln
	sharedProxy.Unlock()
	log.Printf("network: shared proxy listening on %s", addr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serveSOCKS(conn, func(pw string) bool {
				want, err := a.sharedProxyPassword()
				return err == nil && subtleEqual(pw, want)
			}, a.sharedPick, "shared")
		}
	}()
	return nil
}

func stopSharedProxy() {
	sharedProxy.Lock()
	ln := sharedProxy.ln
	sharedProxy.ln = nil
	sharedProxy.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// sharedRoute is one destination prefix and the profile behind it.
type sharedRoute struct {
	prefix    netip.Prefix
	profileID string
	name      string
}

// sharedTable is the routing table of the shared proxy, rebuilt at most
// every few seconds (it reads profiles and the connection tree).
var sharedTable struct {
	sync.Mutex
	at     time.Time
	routes []sharedRoute
	hosts  map[string]string // lower-case connection hostname -> profile id
}

func sharedRoutesInvalidate() {
	sharedTable.Lock()
	sharedTable.at = time.Time{}
	sharedTable.Unlock()
}

func (a *App) sharedRoutes() ([]sharedRoute, map[string]string) {
	sharedTable.Lock()
	defer sharedTable.Unlock()
	if !sharedTable.at.IsZero() && time.Since(sharedTable.at) < 15*time.Second {
		return sharedTable.routes, sharedTable.hosts
	}
	var routes []sharedRoute
	hosts := map[string]string{}
	rows, err := a.db.ListNetworkProfiles()
	if err == nil {
		shared := map[string]bool{}
		for i := range rows {
			r := &rows[i]
			cfg := profileLocalProxy(r.ConfigJSON)
			if !cfg.Shared {
				continue
			}
			shared[r.ID] = true
			list, _ := a.profileRoutes(r, cfg)
			for _, s := range list {
				if p, err := netip.ParsePrefix(s); err == nil {
					routes = append(routes, sharedRoute{p, r.ID, r.Name})
				}
			}
		}
		// Names of connections that use a shared profile: a job asking
		// for "app01.customer.lan" goes where that connection goes, and
		// the name resolves inside the VPN.
		if conns, err := a.db.ListConnections(nil); err == nil {
			folders, _ := a.db.ListFolders()
			for _, c := range conns {
				rs := resolver.ResolveWith(c, folders)
				if rs.NetworkProfileID != nil && shared[*rs.NetworkProfileID] && rs.Hostname != "" {
					if _, err := netip.ParseAddr(rs.Hostname); err != nil {
						hosts[strings.ToLower(rs.Hostname)] = *rs.NetworkProfileID
					}
				}
			}
		}
	}
	sharedTable.at, sharedTable.routes, sharedTable.hosts = time.Now(), routes, hosts
	return routes, hosts
}

// sharedPick chooses the profile for a destination: the manual "use"
// profile when its networks contain it; else the most specific route,
// between equal ones a profile whose tunnel is up (two customers with
// the same range are rarely connected at once), then by name.
func (a *App) sharedPick(host string) (string, error) {
	return a.sharedPickWith(host, a.sharedProxyConfig().Use)
}

func (a *App) sharedPickWith(host, use string) (string, error) {
	routes, hosts := a.sharedRoutes()
	if id, ok := hosts[strings.ToLower(host)]; ok {
		return id, nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// A name no connection uses: try the local resolver.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		addrs, lerr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		cancel()
		if lerr != nil || len(addrs) == 0 {
			return "", fmt.Errorf("no shared profile knows %q", host)
		}
		ip = addrs[0].Unmap()
	}
	if use != "" {
		for _, r := range routes {
			if r.profileID == use && r.prefix.Contains(ip) {
				return use, nil
			}
		}
	}
	best := -1
	var pick []sharedRoute
	for _, r := range routes {
		if !r.prefix.Contains(ip) {
			continue
		}
		switch b := r.prefix.Bits(); {
		case b > best:
			best, pick = b, []sharedRoute{r}
		case b == best:
			pick = append(pick, r)
		}
	}
	if len(pick) == 0 {
		return "", fmt.Errorf("%s is in no shared profile's networks", ip)
	}
	sort.SliceStable(pick, func(i, j int) bool {
		ui, uj := a.tunnelStatus(pick[i].profileID).Running, a.tunnelStatus(pick[j].profileID).Running
		if ui != uj {
			return ui
		}
		return pick[i].name < pick[j].name
	})
	return pick[0].profileID, nil
}

// SharedProxyDetails is the shared proxy's URL, routes and gateway.
type SharedProxyDetails struct {
	URL       string            `json:"url"`
	Routes    []string          `json:"routes"`
	Profiles  map[string]int    `json:"profiles"` // profile name -> routes
	Overlaps  []string          `json:"overlaps"`
	Gateway   string            `json:"gateway"`
	Listening bool              `json:"listening"`
	Hosts     map[string]string `json:"hosts"` // connection name -> profile, for the hint
}

// NetworkSharedProxyDetails builds the URL, the union of routes and the
// gateway command for the shared proxy.
func (a *App) NetworkSharedProxyDetails() (*SharedProxyDetails, error) {
	c, listening := a.NetworkSharedProxyGet()
	if !c.Enabled {
		return nil, errors.New("the shared proxy is off")
	}
	pw, err := a.sharedProxyPassword()
	if err != nil {
		return nil, err
	}
	sharedRoutesInvalidate()
	routes, _ := a.sharedRoutes()
	d := &SharedProxyDetails{
		URL:       fmt.Sprintf("socks5://%s:%s@%s", localProxyUser, pw, net.JoinHostPort(displayHost(c.Bind), strconv.Itoa(c.Port))),
		Profiles:  map[string]int{},
		Listening: listening,
	}
	seen := map[netip.Prefix]sharedRoute{}
	for _, r := range routes {
		d.Profiles[r.name]++
		if other, ok := seen[r.prefix]; ok && other.profileID != r.profileID {
			win := "whichever is connected"
			switch c.Use {
			case r.profileID:
				win = r.name
			case other.profileID:
				win = other.name
			}
			d.Overlaps = append(d.Overlaps, fmt.Sprintf("%s: %s and %s - %s wins", r.prefix, other.name, r.name, win))
			continue
		}
		seen[r.prefix] = r
	}
	for p := range seen {
		d.Routes = append(d.Routes, p.String())
	}
	d.Routes = collapseRoutes(d.Routes)
	d.Gateway = gatewayCommand("sshtool-gw", pw, c.Port, d.Routes)
	return d, nil
}
