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
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/sshchan"
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
// exists, the networks parse, one entry per connection.
func (a *App) normalizeBastions(in []ProxyBastion) ([]ProxyBastion, error) {
	seen := map[string]bool{}
	var out []ProxyBastion
	for _, b := range in {
		b.ConnectionID = strings.TrimSpace(b.ConnectionID)
		if b.ConnectionID == "" {
			continue
		}
		conn, err := a.db.GetConnection(b.ConnectionID)
		if err != nil || conn == nil {
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
		resolved, err := resolver.ResolveConnection(a.db, connectionID)
		if err != nil {
			bastionDialers.Unlock()
			return nil, nil, err
		}
		conn, err := a.db.GetConnection(connectionID)
		if err != nil || conn == nil {
			bastionDialers.Unlock()
			return nil, nil, errors.New("bastion connection not found")
		}
		if resolved.Hostname == "" {
			bastionDialers.Unlock()
			return nil, nil, fmt.Errorf("bastion %q has no hostname", conn.Name)
		}
		d = &bastionDialer{lazy: &sshchan.LazySSH{}}
		d.lazy.Connect = a.sshChanConnect("proxy", connectionID, conn, resolved, d.lazy)
		bastionDialers.m[connectionID] = d
	}
	d.conns++
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
		d.idle = time.AfterFunc(bastionIdle, func() {
			bastionDialers.Lock()
			if d.conns > 0 || bastionDialers.m[connectionID] != d {
				bastionDialers.Unlock()
				return
			}
			delete(bastionDialers.m, connectionID)
			bastionDialers.Unlock()
			d.lazy.Close()
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
}
