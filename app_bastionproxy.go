package main

// Tree connections with a jump host in the app proxy, and the shared
// bastions the status bar shows.

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/netip"
	"sort"
	"strings"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

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

// BastionSession is one shared bastion of the jump pool, for the status
// bar: terminals, the app proxy and the other jump users ride it.
type BastionSession struct {
	Label string `json:"label"` // the hops to the bastion, "a > b"
	Refs  int    `json:"refs"`  // users on it now (0 = idle, closing soon)
	Via   string `json:"via"`   // how its first hop is reached
}

// BastionSessions lists the jump pool's open shared bastions.
func (a *App) BastionSessions() []BastionSession {
	if a.jumpPool == nil {
		return nil
	}
	out := a.jumpPool.sessions()
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// bastionSessionsChanged tells the status bar to reload the list: a shared
// bastion opened or closed, or went between idle and in use.
func bastionSessionsChanged() { EventsEmit("bastion_sessions_changed", nil) }

// jumpKeyLabel turns a jump prefix key (np=<profile>, then one
// "host:port|user|auth" per hop, separated by 0x1f) into "host > host".
func jumpKeyLabel(key string) string {
	parts := strings.Split(key, "\x1f")
	var hosts []string
	for _, p := range parts[1:] {
		hp, _, _ := strings.Cut(p, "|")
		if h, port, err := net.SplitHostPort(hp); err == nil {
			if port != "22" {
				h = net.JoinHostPort(h, port)
			}
			hp = h
		}
		hosts = append(hosts, hp)
	}
	return strings.Join(hosts, " > ")
}
