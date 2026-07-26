package main

import (
	"context"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// probeTimeout bounds a single liveness TCP connect. Generous enough that a
// slow-but-reachable host (over WireGuard, through a bastion, or behind slow
// DNS) isn't painted red just because it answered late - a false "down" on a
// host you can clearly reach is worse than a dot that takes a moment longer.
const probeTimeout = 4 * time.Second

// probeConcurrency caps how many liveness probes run at once. The probe set is
// scoped to expanded folders, but "expand all" can still fan out to hundreds -
// bound the in-flight sockets while keeping enough parallelism that a big
// expand finishes within one re-probe interval.
const probeConcurrency = 32

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
	return a.probeResolved(s)
}

// probeResolved TCP-probes an already-resolved connection and returns its
// state. Shared by static connections (probeOne) and dynamic-inventory entries
// (probeDynamicOne), which resolve their host/port/profile/jump through the
// same folder cascade.
func (a *App) probeResolved(s *store.ResolvedSettings) string {
	if s.Hostname == "" {
		return probeUnknown
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(s.Hostname, strconv.Itoa(int(port)))

	profile := "none"
	if s.NetworkProfileID != nil {
		profile = *s.NetworkProfileID
	}
	jump := "none"
	if s.JumpHost != nil {
		jump = sshlayer.JumpPrefixKey(s)
	}

	// Jump-host connection: probe THROUGH an already-live shared bastion only.
	if s.JumpHost != nil {
		client := a.jumpPool.peek(sshlayer.JumpPrefixKey(s))
		if client == nil {
			log.Printf("probe: %s UNKNOWN (jump, no live bastion) addr=%s jumpKey=%s", addr, addr, jump)
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
		d, derr := a.wgProbeDialerFor(*s.NetworkProfileID)
		if derr != nil {
			// Tunnel down (ErrTunnelWaiting) or profile error - unknown, not
			// down, so a WG-only host behind a sleeping tunnel isn't a false red.
			log.Printf("probe: %s UNKNOWN (wg profile=%s no live tunnel) err=%v", addr, profile, derr)
			return probeUnknown
		}
		dialer = d
	} else {
		// Plain direct dial - let the OS pick the outbound interface, exactly
		// like a real connect. Do NOT source-bind to the physical NIC here:
		// wg_bind_physical is a WireGuard-tunnel workaround (survive a hijacking
		// VPN), and forcing the probe's source IP to the primary adapter makes
		// every host on a different subnet than that adapter time out - a false
		// "down" on hosts you can plainly reach.
		dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := dialer(ctx, "tcp", addr)
	if err != nil {
		log.Printf("probe: %s DOWN addr=%s profile=%s jump=%s err=%v", addr, addr, profile, jump, err)
		return probeDown
	}
	_ = conn.Close()
	return probeUp
}

// DynamicProbeRequest asks to probe a set of dynamic-inventory entries in one
// folder (external ids as the frontend renders them).
type DynamicProbeRequest struct {
	FolderID string   `json:"folder_id"`
	EntryIDs []string `json:"entry_ids"`
}

// DynamicProbeResult mirrors ProbeResult but keys on the entry id.
type DynamicProbeResult struct {
	EntryID string `json:"entry_id"`
	State   string `json:"state"`
}

// ProbeDynamicEntries TCP-probes dynamic-inventory entries. Gated by the
// separate liveness_probe_dynamic setting. Only entries the provider reports as
// "running" are probed (a stopped VM has no SSH to reach); anything else is
// unknown. Each entry resolves its host/port/profile/jump through the same
// folder cascade a real dynamic connect uses, then rides probeResolved.
func (a *App) ProbeDynamicEntries(req DynamicProbeRequest) []DynamicProbeResult {
	out := make([]DynamicProbeResult, len(req.EntryIDs))
	if len(req.EntryIDs) == 0 {
		return out
	}
	if !a.boolSetting("liveness_probe_enabled") || !a.boolSetting("liveness_probe_dynamic") {
		for i, id := range req.EntryIDs {
			out[i] = DynamicProbeResult{EntryID: id, State: probeUnknown}
		}
		return out
	}

	folders, ferr := a.db.ListFolders()
	if ferr != nil {
		for i, id := range req.EntryIDs {
			out[i] = DynamicProbeResult{EntryID: id, State: probeUnknown}
		}
		return out
	}
	// Per-folder jump credential (same lookup dynamic connect uses).
	jumpCred := ""
	if df, err := a.db.GetDynamicFolder(req.FolderID); err == nil && df != nil {
		if s, ok := df.Config["jump_credential_id"].(string); ok {
			jumpCred = s
		}
	}

	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i, id := range req.EntryIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = DynamicProbeResult{EntryID: id, State: a.probeDynamicOne(req.FolderID, id, folders, jumpCred)}
		}(i, id)
	}
	wg.Wait()
	return out
}

func (a *App) probeDynamicOne(folderID, entryID string, folders []store.Folder, jumpCred string) string {
	entry, err := a.db.GetDynamicEntry(entryID)
	if err != nil || entry == nil || entry.FolderID != folderID {
		return probeUnknown
	}
	// Only probe running hosts - a stopped VM has no SSH to reach.
	if entry.Status != "running" {
		return probeUnknown
	}
	syntheticConn := store.Connection{
		ID:        "dyn:" + entryID,
		FolderID:  &folderID,
		Name:      entry.Name,
		Hostname:  entry.Hostname,
		Overrides: store.InheritableSettings{},
	}
	// Lift Ansible per-host vars (port/user/jump) exactly like a real connect,
	// so the resolved port/profile/jump match what a connect would use.
	applyAnsibleVarsToConnection(&syntheticConn, entry.Raw, jumpCred)
	s := resolver.ResolveWith(syntheticConn, folders)
	if !s.ProbeLiveness {
		return probeUnknown
	}
	return a.probeResolved(&s)
}
