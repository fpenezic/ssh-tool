package main

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
)

// probeTimeout bounds a single liveness TCP connect. Shorter than the auto-mode
// directProbeTimeout (3s) because a tree dot wants a quick answer, not a
// thorough reachability decision.
const probeTimeout = 2 * time.Second

// probeConcurrency caps how many liveness probes run at once. The probe set is
// already scoped to expanded folders, but a big folder can still hold dozens of
// hosts - bound the in-flight sockets so a fan-out can't spike.
const probeConcurrency = 16

// ProbeState is the tri-value result of a liveness probe.
const (
	probeUp      = "up"      // TCP connect succeeded
	probeDown    = "down"    // refused / timed out / dial error
	probeUnknown = "unknown" // not probed (disabled, no host, jump w/o live bastion, tunnel down)
)

// ProbeResult is one connection's liveness state, returned over IPC.
type ProbeResult struct {
	ConnectionID string `json:"connection_id"`
	State        string `json:"state"`
}

// ProbeConnections probes a batch of connection ids with a bounded worker pool
// and returns each one's up/down/unknown state. It never forces a WireGuard
// tunnel up and never builds a new bastion - a jump-host connection is probed
// only through an already-live shared bastion, otherwise it is unknown. The
// caller (the frontend) should only pass ids inside expanded folders while the
// global liveness_probe_enabled setting is on, but we still guard that here.
func (a *App) ProbeConnections(ids []string) []ProbeResult {
	out := make([]ProbeResult, len(ids))
	if len(ids) == 0 {
		return out
	}
	// Global gate: if probing is off, everything is unknown and we dial nothing.
	if !a.boolSetting("liveness_probe_enabled") {
		for i, id := range ids {
			out[i] = ProbeResult{ConnectionID: id, State: probeUnknown}
		}
		return out
	}

	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = ProbeResult{ConnectionID: id, State: a.probeOne(id)}
		}(i, id)
	}
	wg.Wait()
	return out
}

// probeOne resolves a connection and returns its liveness state.
func (a *App) probeOne(id string) string {
	s, err := resolver.ResolveConnection(a.db, id)
	if err != nil || s.Hostname == "" {
		return probeUnknown
	}
	if !s.ProbeLiveness {
		return probeUnknown
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(s.Hostname, strconv.Itoa(int(port)))

	// Jump-host connection: probe THROUGH an already-live shared bastion only.
	if s.JumpHost != nil {
		client := a.jumpPool.peek(sshlayer.JumpPrefixKey(s))
		if client == nil {
			return probeUnknown // no live bastion; don't build one to probe
		}
		// client.Dial has no context; run it in a goroutine we can time out so a
		// hung channel can't stall the worker.
		type dialRes struct {
			c   net.Conn
			err error
		}
		ch := make(chan dialRes, 1)
		go func() {
			c, derr := client.Dial("tcp", addr)
			ch <- dialRes{c, derr}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return probeDown
			}
			_ = r.c.Close()
			return probeUp
		case <-time.After(probeTimeout):
			// The dial goroutine will finish and close its own conn via the
			// buffered channel drain below.
			go func() {
				if r := <-ch; r.c != nil {
					_ = r.c.Close()
				}
			}()
			return probeDown
		}
	}

	// Direct / WireGuard connection.
	var dialer sshlayer.ContextDialer
	if s.NetworkProfileID != nil {
		d, derr := a.wgBackgroundDialerFor(*s.NetworkProfileID)
		if derr != nil {
			// Tunnel down (ErrTunnelWaiting) or profile error - unknown, not
			// down, so a sleeping VPN doesn't paint everything red.
			return probeUnknown
		}
		dialer = d
	} else {
		localAddr := a.physicalDialLocalAddr()
		dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{LocalAddr: localAddr}
			return d.DialContext(ctx, network, addr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := dialer(ctx, "tcp", addr)
	if err != nil {
		return probeDown
	}
	_ = conn.Close()
	return probeUp
}
