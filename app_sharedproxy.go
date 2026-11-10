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
	// Priority orders profiles for overlapping networks: the first one
	// listed whose networks contain a destination takes it, however
	// specific another profile's route is. Profiles not listed fall back
	// to the most specific route. Addresses pinned to a profile (added by
	// hand, or a connection's address) win over both.
	Priority []string `json:"priority,omitempty"`
	// Use is the older single override, read as Priority = [Use].
	Use string `json:"use,omitempty"`
	// JumpConnections lets the tree's connections with a jump host through
	// the proxy, reached as a terminal would. Off unless chosen: it opens
	// every such connection to the proxy's clients.
	JumpConnections bool `json:"jump_connections,omitempty"`
}

const (
	sharedProxyKey      = "network_shared_proxy"
	sharedProxyVaultKey = "local_proxy:shared"
)

var sharedProxy struct {
	sync.Mutex
	ln net.Listener
}

// sharedProxyListen is the machine-local part of the shared proxy
// setting: whether it listens and where (local state, not synced).
type sharedProxyListen struct {
	Enabled bool   `json:"enabled"`
	Bind    string `json:"bind,omitempty"`
	Port    int    `json:"port"`
}

const sharedProxyListenKey = "network_shared_proxy_listen"

func (a *App) sharedProxyConfig() SharedProxyConfig {
	var c SharedProxyConfig
	if v, ok, _ := a.db.GetSetting(sharedProxyKey); ok {
		_ = json.Unmarshal([]byte(v), &c)
	}
	// Listening and the address are this machine's; a profile synced from
	// before the split still brings them, until this machine saves.
	if v, ok := localStateStore.get(sharedProxyListenKey); ok {
		var l sharedProxyListen
		if json.Unmarshal([]byte(v), &l) == nil {
			c.Enabled, c.Bind, c.Port = l.Enabled, l.Bind, l.Port
		}
	}
	if c.Bind == "" {
		c.Bind = "127.0.0.1"
	}
	if len(c.Priority) == 0 && c.Use != "" {
		c.Priority = []string{c.Use}
	}
	c.Use = ""
	return c
}

// NetworkSharedProxyGet returns the shared proxy setting and whether it
// is listening.
func (a *App) NetworkSharedProxyGet() (SharedProxyConfig, bool) {
	sharedProxy.Lock()
	defer sharedProxy.Unlock()
	return a.sharedProxyConfig(), sharedProxy.ln != nil
}

// sharedProxySetMu serializes NetworkSharedProxySet: stop, start and the
// settings write of two calls must not interleave (the last call wins).
var sharedProxySetMu sync.Mutex

// NetworkSharedProxySet saves the shared proxy setting and (re)starts it.
func (a *App) NetworkSharedProxySet(c SharedProxyConfig) error {
	sharedProxySetMu.Lock()
	defer sharedProxySetMu.Unlock()
	c.Bind = strings.TrimSpace(c.Bind)
	if c.Bind == "" {
		c.Bind = "127.0.0.1"
	}
	if len(c.Priority) == 0 && c.Use != "" {
		c.Priority = []string{c.Use}
	}
	c.Use = ""
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
	prevListen, hadListen := localStateStore.get(sharedProxyListenKey)
	lb, _ := json.Marshal(sharedProxyListen{Enabled: c.Enabled, Bind: c.Bind, Port: c.Port})
	if err := localStateStore.set(sharedProxyListenKey, string(lb)); err != nil {
		stopSharedProxy()
		return err
	}
	// The synced part: the order of the profiles and the jump-host switch;
	// listening is left off for a machine that pulls it.
	synced := c
	synced.Enabled, synced.Bind, synced.Port = false, "", 0
	b, _ := json.Marshal(synced)
	if err := a.db.SetSetting(sharedProxyKey, string(b)); err != nil {
		// Not saved after all: this machine's listen setting goes back to
		// what it was, so the next start does not bring this one up.
		if hadListen {
			_ = localStateStore.set(sharedProxyListenKey, prevListen)
		} else {
			_ = localStateStore.delete(sharedProxyListenKey)
		}
		stopSharedProxy()
		return err
	}
	sharedRoutesInvalidate()
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
		return listenErr(addr, c.Port, err)
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
				if err != nil {
					log.Printf("network: app proxy: cannot check the password: %v", err)
					return false
				}
				return subtleEqual(pw, want)
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
	// pinned: added by hand on the profile, or a connection's address -
	// the user said where this goes, so it beats every other route.
	pinned bool
}

// sharedTable is the routing table of the shared proxy, rebuilt at most
// every few seconds (it reads profiles and the connection tree).
var sharedTable struct {
	sync.Mutex
	at     time.Time
	routes []sharedRoute
	hosts  map[string]string // lower-case connection hostname -> profile id
	// DNS domains of the shared profiles: a name in one goes through that
	// profile as a name, resolved inside the VPN; search is the per-profile
	// list a short name is tried in.
	domains []sharedDomain
	search  []sharedSearch
}

// sharedDomain is one DNS suffix and the profile behind it.
type sharedDomain struct {
	suffix    string
	profileID string
	name      string
	gateway   bool // reported by the gateway (else added by hand)
}

// sharedSearch is a profile's search domains, for short names.
type sharedSearch struct {
	profileID string
	name      string
	domains   []string
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
	var domains []sharedDomain
	var search []sharedSearch
	hosts := map[string]string{}
	rows, err := a.db.ListNetworkProfiles()
	if err == nil {
		shared := map[string]*sharedPins{}
		for i := range rows {
			r := &rows[i]
			cfg := profileLocalProxy(r.ConfigJSON)
			if !cfg.Shared {
				continue
			}
			pins := &sharedPins{set: map[netip.Prefix]bool{}}
			for _, n := range cfg.Networks {
				if p, err := parseRoute(n); err == nil {
					pins.set[p] = true
				}
			}
			shared[r.ID] = pins
		}
		// Connections that use a shared profile: a job asking for
		// "app01.customer.lan" goes where that connection goes, and the
		// name resolves inside the VPN; a connection's address is pinned
		// to its profile.
		if conns, err := a.db.ListConnections(nil); err == nil {
			folders, _ := a.db.ListFolders()
			for _, c := range conns {
				rs := resolver.ResolveWith(c, folders)
				if rs.NetworkProfileID == nil || rs.Hostname == "" {
					continue
				}
				pins := shared[*rs.NetworkProfileID]
				if pins == nil {
					continue
				}
				// An address with a trailing dot is an address too.
				if ip, err := netip.ParseAddr(strings.TrimSuffix(rs.Hostname, ".")); err == nil {
					pins.set[netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen())] = true
				} else {
					hosts[hostKey(rs.Hostname)] = *rs.NetworkProfileID
				}
			}
		}
		for i := range rows {
			r := &rows[i]
			pins := shared[r.ID]
			if pins == nil {
				continue
			}
			cfg := profileLocalProxy(r.ConfigJSON)
			list, _ := a.profileRoutes(r, cfg)
			for _, s := range list {
				if p, err := netip.ParsePrefix(s); err == nil {
					routes = append(routes, sharedRoute{p, r.ID, r.Name, pins.set[p]})
				}
			}
			doms, gw, srch := profileDomains(r, cfg)
			for _, d := range doms {
				domains = append(domains, sharedDomain{d, r.ID, r.Name, gw[d]})
			}
			if len(srch) > 0 {
				search = append(search, sharedSearch{r.ID, r.Name, srch})
			}
		}
	}
	// A route id's path: a jump connection's chain (two connections with
	// the same chain are one path), a profile its own id.
	jumpPaths := map[string]string{}
	if conns, err := a.db.ListConnections(nil); err == nil {
		folders, _ := a.db.ListFolders()
		if a.sharedProxyConfig().JumpConnections {
			jr, jh, jp := jumpConnRoutes(conns, folders)
			jumpPaths = jp
			routes = append(routes, jr...)
			for name, id := range jh {
				hosts[hostKey(name)] = id
			}
		}
	}
	// The CI runner's extra hosts: a job resolves a name itself and sends
	// the address, so a name the tree routes (a connection behind a jump
	// host or a VPN) pins the address it is given there to the same path.
	// Two names on one address with different paths cannot both be pinned:
	// the job sends only the address, so either pin would send the other
	// name's traffic the wrong way. Such an address is left unpinned. Two
	// connections behind the same chain are one path, kept under the
	// first one's id.
	pathOf := func(id string) string {
		if p, ok := jumpPaths[id]; ok {
			return "jump:" + p
		}
		return "id:" + id
	}
	pinIDs := map[netip.Addr]map[string]string{}
	var pinOrder []netip.Addr
	for _, h := range a.ciExtraHosts() {
		name, addr, _ := strings.Cut(h, ":")
		id, ok := hosts[hostKey(name)]
		if !ok {
			continue
		}
		ip, err := netip.ParseAddr(strings.Trim(addr, "[]"))
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if pinIDs[ip] == nil {
			pinIDs[ip] = map[string]string{}
			pinOrder = append(pinOrder, ip)
		}
		if _, seen := pinIDs[ip][pathOf(id)]; !seen {
			pinIDs[ip][pathOf(id)] = id
		}
	}
	for _, ip := range pinOrder {
		if len(pinIDs[ip]) != 1 {
			continue
		}
		for _, id := range pinIDs[ip] {
			routes = append(routes, sharedRoute{netip.PrefixFrom(ip, ip.BitLen()), id, routeName(routes, id), true})
		}
	}
	sharedTable.at, sharedTable.routes, sharedTable.hosts = time.Now(), routes, hosts
	sharedTable.domains, sharedTable.search = domains, search
	return routes, hosts
}

// hostKey is a host name as the connection-name map keys it: lower case,
// without the trailing dot of a fully qualified name, so db1.example.com.
// finds the connection db1.example.com before any domain does.
func hostKey(h string) string {
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// sharedLookup is the local resolver the pick asks (a test swaps it).
var sharedLookup = net.DefaultResolver.LookupNetIP

// sharedNames is the domain side of the table (fresh as sharedRoutes).
func (a *App) sharedNames() ([]sharedDomain, []sharedSearch) {
	a.sharedRoutes()
	sharedTable.Lock()
	defer sharedTable.Unlock()
	return sharedTable.domains, sharedTable.search
}

// domainCandidates are the domains host is in that can take it: only the
// ones added by hand when there is one (a domain added by hand beats one a
// gateway reported, whatever their lengths: the user said where those
// names go, and a gateway can push any domain - a customer's VPN claiming
// another customer's domain, or "com"; pins beat gateway routes the same
// way), then of those the longest suffix.
func domainCandidates(host string, domains []sharedDomain) []sharedDomain {
	h := hostKey(host)
	var match []sharedDomain
	hand := false
	for _, d := range domains {
		if h == d.suffix || strings.HasSuffix(h, "."+d.suffix) {
			match = append(match, d)
			hand = hand || !d.gateway
		}
	}
	longest := 0
	for _, d := range match {
		if !hand || !d.gateway {
			longest = max(longest, len(d.suffix))
		}
	}
	var pick []sharedDomain
	for _, d := range match {
		if (!hand || !d.gateway) && len(d.suffix) == longest {
			pick = append(pick, d)
		}
	}
	return pick
}

// rankLess orders two profiles by the app proxy's priority: a ranked one
// before an unranked one, then the lower rank. decided is false when the
// priority does not tell them apart (the tunnel that is up, then the
// name, decide in sharedByDomain). One comparator for the pick and for
// the winner domainClashes names, so the two cannot drift apart.
func rankLess(rank map[string]int, a, b string) (less, decided bool) {
	ra, aok := rank[a]
	rb, bok := rank[b]
	if aok != bok {
		return aok, true
	}
	if aok && ra != rb {
		return ra < rb, true
	}
	return false, false
}

// byRank orders candidates by the priority; ok is false when the first
// two are not told apart by it.
func byRank(pick []sharedDomain, rank map[string]int) bool {
	sort.SliceStable(pick, func(i, j int) bool {
		less, _ := rankLess(rank, pick[i].profileID, pick[j].profileID)
		return less
	})
	if len(pick) < 2 {
		return true
	}
	_, decided := rankLess(rank, pick[0].profileID, pick[1].profileID)
	return decided
}

// sharedByDomain is the profile whose DNS domain host is in (see
// domainCandidates); between profiles with the same domain, the priority,
// then a profile whose tunnel is up, then the name.
func (a *App) sharedByDomain(host string, domains []sharedDomain, rank map[string]int) (sharedDomain, bool) {
	pick := domainCandidates(host, domains)
	if len(pick) == 0 {
		return sharedDomain{}, false
	}
	sort.SliceStable(pick, func(i, j int) bool {
		if less, decided := rankLess(rank, pick[i].profileID, pick[j].profileID); decided {
			return less
		}
		ui, uj := a.tunnelStatus(pick[i].profileID).Running, a.tunnelStatus(pick[j].profileID).Running
		if ui != uj {
			return ui
		}
		return pick[i].name < pick[j].name
	})
	return pick[0], true
}

// domainClashes lists the DNS domains that more than one profile claims
// (the same suffix, or a profile's domain inside another's) with every
// claim and the profile that takes names in it, by the same rule as
// sharedByDomain, for the app proxy's details. Nothing is blocked.
func domainClashes(domains []sharedDomain, rank map[string]int) []string {
	var suffixes []string
	seen := map[string]bool{}
	for _, d := range domains {
		if !seen[d.suffix] {
			seen[d.suffix] = true
			suffixes = append(suffixes, d.suffix)
		}
	}
	sort.Strings(suffixes)
	var out []string
	for _, s := range suffixes {
		var claims []sharedDomain
		profiles := map[string]bool{}
		for _, d := range domains {
			if s == d.suffix || strings.HasSuffix(s, "."+d.suffix) {
				claims = append(claims, d)
				profiles[d.profileID] = true
			}
		}
		// Only where a profile with this very suffix meets another one.
		own := false
		for _, d := range claims {
			own = own || d.suffix == s
		}
		if len(profiles) < 2 || !own {
			continue
		}
		sort.SliceStable(claims, func(i, j int) bool {
			if len(claims[i].suffix) != len(claims[j].suffix) {
				return len(claims[i].suffix) > len(claims[j].suffix)
			}
			return claims[i].name < claims[j].name
		})
		var parts []string
		for _, d := range claims {
			from := "from the gateway"
			if !d.gateway {
				from = "by hand"
			}
			if d.suffix == s {
				parts = append(parts, fmt.Sprintf("%s (%s)", d.name, from))
			} else {
				parts = append(parts, fmt.Sprintf("%s (%s, as %s)", d.name, from, d.suffix))
			}
		}
		pick := domainCandidates(s, domains)
		win := "whichever is connected"
		if byRank(pick, rank) {
			win = pick[0].name
		}
		out = append(out, fmt.Sprintf("domain %s: %s - %s wins", s, strings.Join(parts, ", "), win))
	}
	return out
}

// sharedBySearch is the profile a short name (no dot) is tried in, with
// the names to dial in its order: the first profile by priority, then a
// tunnel that is up, then the name, that has search domains.
func (a *App) sharedBySearch(host string, search []sharedSearch, rank map[string]int) (sharedSearch, []string, bool) {
	if host == "" || strings.Contains(host, ".") || len(search) == 0 {
		return sharedSearch{}, nil, false
	}
	pick := append([]sharedSearch(nil), search...)
	sort.SliceStable(pick, func(i, j int) bool {
		ri, iok := rank[pick[i].profileID]
		rj, jok := rank[pick[j].profileID]
		if iok != jok {
			return iok
		}
		if iok && ri != rj {
			return ri < rj
		}
		ui, uj := a.tunnelStatus(pick[i].profileID).Running, a.tunnelStatus(pick[j].profileID).Running
		if ui != uj {
			return ui
		}
		return pick[i].name < pick[j].name
	})
	s := pick[0]
	names := make([]string, 0, len(s.domains))
	for _, d := range s.domains {
		names = append(names, strings.ToLower(host)+"."+d)
	}
	return s, names, true
}

// routeName is the summary name of route id among routes (a jump route
// counts under one line, a profile under its name).
func routeName(routes []sharedRoute, id string) string {
	if jumpConnOf(id) != "" {
		return jumpRouteName
	}
	for _, r := range routes {
		if r.profileID == id {
			return r.name
		}
	}
	return id
}

type sharedPins struct{ set map[netip.Prefix]bool }

// sharedPick chooses the profile for a destination.
// It returns the host to dial as well: the address the decision was made
// on. A name only the local resolver knows is decided by its local
// address, so that address is dialed - sending the name on would let the
// far side (a bastion, the VPN's DNS) resolve it to a host nobody allowed.
// A connection's own name is dialed as is: that name is what was allowed.
func (a *App) sharedPick(host string) (id string, dialHosts []string, err error) {
	id, _, dialHosts, err = a.sharedPickHosts(host, a.sharedProxyConfig().Priority)
	if err != nil {
		return "", nil, err
	}
	return id, dialHosts, nil
}

// sharedPickWith chooses the profile for a destination and says why:
//  1. a connection's name maps to its profile;
//  2. a name in a profile's DNS domain goes there as a name, resolved
//     inside the VPN (the longest domain first);
//  3. an address pinned to a profile (by hand, or a connection's
//     address), the most specific pin first;
//  4. the first profile in priority whose networks contain it;
//  5. the most specific route; between equal ones a profile whose
//     tunnel is up (two customers with the same range are rarely
//     connected at once), then by name.
//
// A name the local resolver does not know and no domain takes is a
// short name (no dot) tried in a profile's search domains, last.
func (a *App) sharedPickWith(host string, priority []string) (id, why string, err error) {
	id, why, _, err = a.sharedPickAddr(host, priority)
	return id, why, err
}

// sharedPickAddr is sharedPickWith plus the host to dial (see sharedPick).
func (a *App) sharedPickAddr(host string, priority []string) (id, why, dialHost string, err error) {
	id, why, hosts, err := a.sharedPickHosts(host, priority)
	if err != nil {
		return "", "", "", err
	}
	return id, why, hosts[0], nil
}

// sharedPickHosts is sharedPickAddr with every host to try: a short name
// expanded with search domains is tried in each, in order.
func (a *App) sharedPickHosts(host string, priority []string) (id, why string, dialHosts []string, err error) {
	routes, hosts := a.sharedRoutes()
	if id, ok := hosts[hostKey(host)]; ok {
		if jumpConnOf(id) != "" {
			return id, "through its jump host, as its terminal connects", []string{host}, nil
		}
		return id, "a connection in the tree uses this name", []string{host}, nil
	}
	rank := map[string]int{}
	for i, p := range priority {
		if _, ok := rank[p]; !ok {
			rank[p] = i
		}
	}
	// An address written with a trailing dot ("192.0.2.1.") is an address:
	// read it as one, so it goes by the networks and cannot pass as a name
	// in some domain.
	ip, err := netip.ParseAddr(strings.TrimSuffix(host, "."))
	resolved := ""
	if err != nil {
		// A name in a profile's DNS domain: the name goes on, resolved by
		// the VPN's DNS - the domain is what was allowed, and a local
		// lookup would miss it or (split horizon) find another host.
		domains, search := a.sharedNames()
		if d, ok := a.sharedByDomain(host, domains, rank); ok {
			from := "added by hand"
			if d.gateway {
				from = "from the gateway"
			} else {
				// Say when the gateway domain that would have taken the name
				// (the longest one) is another profile's and lost.
				h := hostKey(host)
				var top sharedDomain
				for _, o := range domains {
					if o.gateway && len(o.suffix) > len(top.suffix) && (h == o.suffix || strings.HasSuffix(h, "."+o.suffix)) {
						top = o
					}
				}
				if top.suffix != "" && top.profileID != d.profileID && len(top.suffix) > len(d.suffix) {
					from += fmt.Sprintf("; beats %s from the gateway of %s", top.suffix, top.name)
				}
			}
			return d.profileID, fmt.Sprintf("domain %s (%s)", d.suffix, from), []string{strings.ToLower(strings.TrimSuffix(host, "."))}, nil
		}
		// A name no connection uses: try the local resolver.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		addrs, lerr := sharedLookup(ctx, "ip", host)
		cancel()
		if lerr != nil || len(addrs) == 0 {
			// Unknown here: a short name may be a host in a VPN's search
			// domain (db1 -> db1.example.com).
			if s, names, ok := a.sharedBySearch(host, search, rank); ok {
				return s.profileID, "short name, tried in the search domains: " + strings.Join(s.domains, ", "), names, nil
			}
			return "", "", nil, fmt.Errorf("no shared profile knows %q", host)
		}
		// The first address some shared network contains: a name can resolve
		// to several, and the first need not be the one behind a VPN.
		ip = addrs[0].Unmap()
	pick:
		for _, a := range addrs {
			for _, r := range routes {
				if r.prefix.Contains(a.Unmap()) {
					ip = a.Unmap()
					break pick
				}
			}
		}
		resolved = fmt.Sprintf("%s resolves to %s; ", host, ip)
	}
	var pins, all []sharedRoute
	for _, r := range routes {
		if !r.prefix.Contains(ip) {
			continue
		}
		all = append(all, r)
		if r.pinned {
			pins = append(pins, r)
		}
	}
	if len(all) == 0 {
		return "", "", nil, fmt.Errorf("%s is in no shared profile's networks", ip)
	}
	if len(pins) > 0 {
		r := a.mostSpecific(pins, rank)
		if jumpConnOf(r.profileID) != "" {
			return r.profileID, resolved + "through its jump host, as its terminal connects", []string{ip.String()}, nil
		}
		return r.profileID, resolved + "pinned to it (" + r.prefix.String() + ")", []string{ip.String()}, nil
	}
	best := -1
	var top sharedRoute
	for _, r := range all {
		if i, ok := rank[r.profileID]; ok && (best < 0 || i < best) {
			best, top = i, r
		}
	}
	if best >= 0 {
		return top.profileID, fmt.Sprintf("%s%s, priority %d", resolved, top.prefix, best+1), []string{ip.String()}, nil
	}
	r := a.mostSpecific(all, nil)
	return r.profileID, resolved + "most specific network (" + r.prefix.String() + ")", []string{ip.String()}, nil
}

// mostSpecific picks the longest prefix; ties go to priority, then a
// profile whose tunnel is up, then the name.
func (a *App) mostSpecific(rs []sharedRoute, rank map[string]int) sharedRoute {
	best := -1
	var pick []sharedRoute
	for _, r := range rs {
		switch b := r.prefix.Bits(); {
		case b > best:
			best, pick = b, []sharedRoute{r}
		case b == best:
			pick = append(pick, r)
		}
	}
	if len(pick) > 1 {
		sort.SliceStable(pick, func(i, j int) bool {
			// A connection's own path (its jump chain) before a profile pin
			// of the same address: the profile only takes it to the first
			// hop, the address is opened from the last.
			ji, jj := jumpConnOf(pick[i].profileID) != "", jumpConnOf(pick[j].profileID) != ""
			if ji != jj {
				return ji
			}
			ri, iok := rank[pick[i].profileID]
			rj, jok := rank[pick[j].profileID]
			if iok != jok {
				return iok
			}
			if iok && ri != rj {
				return ri < rj
			}
			ui, uj := a.tunnelStatus(pick[i].profileID).Running, a.tunnelStatus(pick[j].profileID).Running
			if ui != uj {
				return ui
			}
			return pick[i].name < pick[j].name
		})
	}
	return pick[0]
}

// SharedProxyWhich answers "which VPN does this address go through".
type SharedProxyWhich struct {
	Profile string `json:"profile"`
	Why     string `json:"why"`
}

// NetworkSharedProxyWhich tells which profile the app proxy would use
// for a destination (an address or a name), and why.
func (a *App) NetworkSharedProxyWhich(host string) (*SharedProxyWhich, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("type an address or a name")
	}
	sharedRoutesInvalidate()
	id, why, err := a.sharedPickWith(host, a.sharedProxyConfig().Priority)
	if err != nil {
		return nil, err
	}
	name := id
	if c := jumpConnOf(id); c != "" {
		if conn, err := a.db.GetConnection(c); err == nil && conn != nil {
			name = conn.Name
		}
	} else if row, err := a.db.GetNetworkProfile(id); err == nil {
		name = row.Name
	}
	return &SharedProxyWhich{Profile: name, Why: why}, nil
}

// SharedProxyDetails is the shared proxy's URL, routes and gateway.
type SharedProxyDetails struct {
	URL       string            `json:"url"`
	Routes    []string          `json:"routes"`
	Profiles  map[string]int    `json:"profiles"` // profile name -> routes
	Domains   map[string]int    `json:"domains"`  // profile name -> DNS domains routed by name
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
		Domains:   map[string]int{},
		Listening: listening,
	}
	doms, _ := a.sharedNames()
	for _, dm := range doms {
		d.Domains[dm.name]++
	}
	rank := map[string]int{}
	for i, p := range c.Priority {
		if _, ok := rank[p]; !ok {
			rank[p] = i
		}
	}
	d.Overlaps = append(d.Overlaps, domainClashes(doms, rank)...)
	seen := map[netip.Prefix]sharedRoute{}
	for _, r := range routes {
		d.Profiles[r.name]++
		if other, ok := seen[r.prefix]; ok && other.profileID != r.profileID {
			win := "whichever is connected"
			for _, p := range c.Priority {
				if p == r.profileID {
					win = r.name
					break
				}
				if p == other.profileID {
					win = other.name
					break
				}
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
