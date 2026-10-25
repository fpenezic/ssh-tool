package main

// Bastions in the app proxy: networks reached through an SSH connection
// from the tree instead of a VPN. A destination in a bastion's networks is
// dialled as a direct-tcpip channel on one SSH session to that connection
// (the same LazySSH the Database and Kafka tabs use), with the
// credentials, jump hosts and network profile a terminal connect would
// use. The client (a CI job, curl) never sees a key or a password.
//
// Bastions live in the app proxy's setting; their routes join the shared
// table as pinned (the user typed them), with the profile id
// "conn:<connection id>".

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/sshchan"
	"ssh-tool/internal/store"
)

// ProxyBastion is one bastion of the app proxy.
type ProxyBastion struct {
	ConnectionID string   `json:"connection_id"`
	Networks     []string `json:"networks"`
}

const bastionRoutePrefix = "conn:"

func bastionRouteID(connectionID string) string { return bastionRoutePrefix + connectionID }

// bastionOf returns the connection id of a bastion route id ("" when id
// is a network profile).
func bastionOf(id string) string {
	if c, ok := strings.CutPrefix(id, bastionRoutePrefix); ok {
		return c
	}
	return ""
}

// normalizeBastions checks the bastions of a setting: the connection
// exists, the networks parse, one entry per connection. A bastion whose
// connection was deleted since it was saved is dropped quietly (the tree
// has many ways to delete one: a folder, a sync pull, an import); an
// unknown connection that was not saved before is an error.
func (a *App) normalizeBastions(in []ProxyBastion) ([]ProxyBastion, error) {
	saved := map[string]bool{}
	for _, b := range a.sharedProxyConfig().Bastions {
		saved[b.ConnectionID] = true
	}
	seen := map[string]bool{}
	var out []ProxyBastion
	for _, b := range in {
		b.ConnectionID = strings.TrimSpace(b.ConnectionID)
		if b.ConnectionID == "" {
			continue
		}
		conn, err := a.db.GetConnection(b.ConnectionID)
		missing := errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrNotFound) || (err == nil && conn == nil)
		if err != nil && !missing {
			// A lookup that failed is not a deleted connection: keep the
			// setting as it is.
			return nil, fmt.Errorf("bastion: %w", err)
		}
		if missing {
			if saved[b.ConnectionID] {
				continue
			}
			return nil, fmt.Errorf("bastion: connection %s not found", b.ConnectionID)
		}
		if seen[b.ConnectionID] {
			return nil, fmt.Errorf("bastion %q is listed twice", conn.Name)
		}
		seen[b.ConnectionID] = true
		var nets []string
		for _, n := range b.Networks {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			p, err := parseRoute(n)
			if err != nil {
				return nil, fmt.Errorf("bastion %q: network %q: %w", conn.Name, n, err)
			}
			if p.Bits() == 0 {
				return nil, fmt.Errorf("bastion %q: %s would take every address", conn.Name, p)
			}
			nets = append(nets, p.String())
		}
		if len(nets) == 0 {
			return nil, fmt.Errorf("bastion %q has no networks", conn.Name)
		}
		b.Networks = collapseRoutes(nets)
		out = append(out, b)
	}
	return out, nil
}

// bastionRoutes are the shared table's routes of the configured bastions.
func (a *App) bastionRoutes() []sharedRoute {
	var routes []sharedRoute
	for _, b := range a.sharedProxyConfig().Bastions {
		conn, err := a.db.GetConnection(b.ConnectionID)
		if err != nil || conn == nil {
			continue
		}
		for _, n := range b.Networks {
			if p, err := parseRoute(n); err == nil {
				routes = append(routes, sharedRoute{p, bastionRouteID(b.ConnectionID), conn.Name + " (bastion)", true})
			}
		}
	}
	return routes
}

// Connections of the tree that have a jump host (a connection from the
// tree or a host typed, on the connection or a folder) are reached
// through the proxy the way a terminal reaches them: DialVia brings the
// first hop up over the connection's network profile (a VPN comes up as
// its mode says), walks the jump chain through the shared bastion pool,
// and opens the address from the last hop. Route id "jump:<connection
// id>"; the address is pinned, a name joins the names map (a socks5h
// client has it resolved on the last hop; a CI job resolves names itself
// and needs extra hosts for them).
const jumpRoutePrefix = "jump:"

func jumpRouteID(connectionID string) string { return jumpRoutePrefix + connectionID }

// jumpConnOf returns the connection id of a jump route id, else "".
func jumpConnOf(id string) string {
	if c, ok := strings.CutPrefix(id, jumpRoutePrefix); ok {
		return c
	}
	return ""
}

// jumpRouteName is what the summary counts the jump connections under:
// one line, however many connections.
const jumpRouteName = "connections with a jump host"

func jumpConnRoutes(conns []store.Connection, folders []store.Folder) ([]sharedRoute, map[string]string) {
	// Group by address or name: two connections with the same one and
	// different paths (another jump chain or network profile) leave it
	// ambiguous, so neither routes it and it goes by the normal route
	// selection; the same path twice is fine.
	type entry struct {
		id, path string
		names    []string
	}
	byKey := map[string]*entry{}
	var order []string
	for _, c := range conns {
		rs := resolver.ResolveWith(c, folders)
		if rs.JumpHost == nil || rs.Hostname == "" {
			continue
		}
		key := strings.ToLower(rs.Hostname)
		if ip, err := netip.ParseAddr(rs.Hostname); err == nil {
			key = ip.Unmap().String()
		}
		pj, _ := json.Marshal(rs.JumpHost)
		path := string(pj)
		if rs.NetworkProfileID != nil {
			path += "|" + *rs.NetworkProfileID
		}
		e := byKey[key]
		if e == nil {
			byKey[key] = &entry{id: jumpRouteID(c.ID), path: path, names: []string{c.Name}}
			order = append(order, key)
			continue
		}
		e.names = append(e.names, c.Name)
		if e.path != path {
			e.id = ""
		}
	}
	var routes []sharedRoute
	hosts := map[string]string{}
	for _, key := range order {
		e := byKey[key]
		if e.id == "" {
			log.Printf("network: app proxy: %s is used by %s with different jump paths; not routed by jump host", key, strings.Join(e.names, ", "))
			continue
		}
		if ip, err := netip.ParseAddr(key); err == nil {
			routes = append(routes, sharedRoute{netip.PrefixFrom(ip, ip.BitLen()), e.id, jumpRouteName, true})
		} else {
			hosts[key] = e.id
		}
	}
	return routes, hosts
}

// jumpDial opens addr the way a connect to the connection would reach its
// host. The returned release closes the connection's share of the chain.
func (a *App) jumpDial(ctx context.Context, connectionID, addr string) (net.Conn, func(), error) {
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, nil, err
	}
	c, cleanup, err := sshlayer.DialVia(ctx, a.db, a.vault, settings, addr,
		a.makeHostKeyCallback(), a.makeAlgoLookup(), a.connectTimeout())
	if err != nil {
		return nil, nil, err
	}
	a.recordAudit("proxy.jump_dial", connectionID, map[string]string{"addr": addr})
	return c, cleanup, nil
}

// bastionDialers holds one SSH session per bastion while the proxy uses
// it; a session nobody used for bastionIdle is closed.
var bastionDialers = struct {
	sync.Mutex
	m map[string]*bastionDialer
}{m: map[string]*bastionDialer{}}

type bastionDialer struct {
	lazy  *sshchan.LazySSH
	conns int
	idle  *time.Timer
}

const bastionIdle = 2 * time.Minute

// bastionDial opens addr through the bastion connection. The returned
// release must be called when the proxied connection ends.
func (a *App) bastionDial(ctx context.Context, connectionID, addr string) (net.Conn, func(), error) {
	bastionDialers.Lock()
	d := bastionDialers.m[connectionID]
	if d == nil {
		d = &bastionDialer{lazy: &sshchan.LazySSH{}}
		lazy := d.lazy
		// Resolved on every connect, not once: a reconnect after the
		// connection or its folders changed uses the new settings.
		lazy.Connect = func(ctx context.Context) (*gossh.Client, func(), error) {
			resolved, err := resolver.ResolveConnection(a.db, connectionID)
			if err != nil {
				return nil, nil, err
			}
			conn, err := a.db.GetConnection(connectionID)
			if err != nil || conn == nil {
				return nil, nil, errors.New("bastion connection not found")
			}
			if resolved.Hostname == "" {
				return nil, nil, fmt.Errorf("bastion %q has no hostname", conn.Name)
			}
			return a.sshChanConnect("proxy", connectionID, conn, resolved, lazy)(ctx)
		}
		bastionDialers.m[connectionID] = d
	}
	d.conns++
	if d.conns == 1 {
		defer bastionSessionsChanged()
	}
	if d.idle != nil {
		d.idle.Stop()
		d.idle = nil
	}
	bastionDialers.Unlock()

	release := func() {
		bastionDialers.Lock()
		defer bastionDialers.Unlock()
		d.conns--
		if d.conns > 0 || bastionDialers.m[connectionID] != d {
			return
		}
		defer bastionSessionsChanged()
		d.idle = time.AfterFunc(bastionIdle, func() {
			bastionDialers.Lock()
			if d.conns > 0 || bastionDialers.m[connectionID] != d {
				bastionDialers.Unlock()
				return
			}
			delete(bastionDialers.m, connectionID)
			bastionDialers.Unlock()
			d.lazy.Close()
			bastionSessionsChanged()
			log.Printf("network: app proxy: bastion session %s closed after %s idle", connectionID, bastionIdle)
		})
	}
	c, err := d.lazy.Dial(ctx, "tcp", addr)
	if err != nil {
		release()
		return nil, nil, err
	}
	return c, release, nil
}

// closeBastionDialer ends one bastion's session (its connection changed
// or was deleted); the next dial connects with the current settings.
func closeBastionDialer(connectionID string) {
	bastionDialers.Lock()
	d := bastionDialers.m[connectionID]
	delete(bastionDialers.m, connectionID)
	bastionDialers.Unlock()
	if d != nil {
		if d.idle != nil {
			d.idle.Stop()
		}
		d.lazy.Close()
		bastionSessionsChanged()
	}
}

// closeBastionDialers ends the sessions of bastions not in keep (taken
// off the list); keep == nil ends them all.
func closeBastionDialers(keep map[string]bool) {
	bastionDialers.Lock()
	var gone []*bastionDialer
	for id, d := range bastionDialers.m {
		if keep == nil || !keep[id] {
			gone = append(gone, d)
			delete(bastionDialers.m, id)
		}
	}
	bastionDialers.Unlock()
	for _, d := range gone {
		if d.idle != nil {
			d.idle.Stop()
		}
		d.lazy.Close()
	}
	if len(gone) > 0 {
		bastionSessionsChanged()
	}
}

// BastionSession is one app proxy bastion session, for the status bar.
type BastionSession struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Conns        int    `json:"conns"` // proxied connections open on it now
	State        string `json:"state"` // sshchan transport state
}

// BastionSessions lists the app proxy's bastion sessions (open, or idle
// and closing after bastionIdle).
func (a *App) BastionSessions() []BastionSession {
	bastionDialers.Lock()
	type entry struct {
		id    string
		conns int
		lazy  *sshchan.LazySSH
	}
	var es []entry
	for id, d := range bastionDialers.m {
		// A dial that never got a session (connect failed) is not an
		// open session; it is dropped with its idle timer.
		if state, _ := d.lazy.State(); d.conns == 0 && state != sshchan.TransportConnected {
			continue
		}
		es = append(es, entry{id, d.conns, d.lazy})
	}
	bastionDialers.Unlock()
	out := make([]BastionSession, 0, len(es))
	for _, e := range es {
		name := e.id
		if c, err := a.db.GetConnection(e.id); err == nil && c != nil {
			name = c.Name
		}
		state, _ := e.lazy.State()
		out = append(out, BastionSession{ConnectionID: e.id, Name: name, Conns: e.conns, State: state})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// bastionSessionsChanged tells the status bar to reload the list: a
// session opened or closed, or went from idle to in use and back.
func bastionSessionsChanged() { EventsEmit("bastion_sessions_changed", nil) }
