package main

// Tunnel manager: connections marked tunnel_only run as shell-less SSH
// sessions (the `ssh -N` case) that hold their port forwards with no tab and
// no PTY. The manager owns their lifecycle - start, stop, start on app
// launch, and reconnect with backoff for as long as the tunnel is wanted -
// and the status bar's Tunnels panel shows it.
//
// The session itself is an ordinary pool session opened through
// sshConnectWith(tunnel=true), so forwards, the shared bastion pool, network
// profiles, host-key and auth prompts all behave as they do for a terminal.
// What differs: sessionMeta marks it as a tunnel (SshActiveSessions skips it,
// so no window rebuilds a tab for it), and its close hook reports to
// tunnelSessionClosed instead of the terminal reconnect loop.

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"time"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// Tunnel states as the frontend sees them. A tunnel with no entry is
// "stopped".
const (
	tunnelConnecting   = "connecting"
	tunnelConnected    = "connected"
	tunnelReconnecting = "reconnecting"
	tunnelError        = "error"
	tunnelStopped      = "stopped"
)

// tunnelRetryMaxDelay caps the reconnect backoff. A tunnel is meant to stay
// up, so unlike the terminal auto-reconnect there is no attempt limit - only
// a ceiling on how long it waits between tries.
const tunnelRetryMaxDelay = 60 * time.Second

type tunnelEntry struct {
	state     string
	sessionID string
	err       string
	since     int64 // unix seconds of the last state change
	attempt   int   // reconnect attempt counter, 0 while connected
	nextRetry int64 // unix seconds of the next reconnect try, 0 = none
	// vaultLocked: the last failure was a locked vault, which an unlock
	// fixes; TunnelsAutostart retries those after the gate.
	vaultLocked bool
	// handStopped: forwards the user stopped in this running tunnel
	// (ForwardsStop). They are not faults, so they do not make the tunnel
	// degraded; a reconnect keeps them stopped (only the running set is
	// restored). A new start (a new entry) starts everything again.
	handStopped map[string]bool
	// fwdErr: why a forward did not come up when the tunnel started or
	// reconnected, shown in the panel until it runs again.
	fwdErr map[string]string
	// gen numbers this start (App.tunnelGen), so the frontend can tell the
	// outcome of the start it asked for from a list that predates it.
	gen uint64
	// byHand: started by the user (TunnelStart), whose window reports the
	// outcome; false for a launch start and once the tunnel reconnects.
	byHand bool
	// stop is closed by TunnelStop. It ends a pending connect / reconnect
	// loop; the entry is also removed from the map, which is how a loop
	// that wakes up later learns it was stopped.
	stop chan struct{}
}

func tunnelCancelKey(connectionID string) string { return "tunnel:" + connectionID }

// TunnelForwardInfo is one saved forward of a tunnel, with its live state
// when the tunnel is up.
type TunnelForwardInfo struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	LocalAddr   string `json:"local_addr"`
	LocalPort   uint16 `json:"local_port"`
	RemoteHost  string `json:"remote_host"`
	RemotePort  uint16 `json:"remote_port"`
	Description string `json:"description"`
	AutoStart   bool   `json:"auto_start"`
	// State is the live listener state ("listening", "error", ...) or ""
	// when the forward is not running.
	State string `json:"state"`
	// HandStopped: stopped by the user in the running tunnel, so not
	// listening on purpose.
	HandStopped bool   `json:"hand_stopped,omitempty"`
	Error       string `json:"error,omitempty"`
	BytesIn     uint64 `json:"bytes_in"`
	BytesOut    uint64 `json:"bytes_out"`
}

// TunnelInfo is one row of the Tunnels panel.
type TunnelInfo struct {
	ConnectionID string              `json:"connection_id"`
	Name         string              `json:"name"`
	Hostname     string              `json:"hostname"`
	FolderPath   string              `json:"folder_path"`
	Autostart    bool                `json:"autostart"`
	State        string              `json:"state"`
	SessionID    string              `json:"session_id,omitempty"`
	Error        string              `json:"error,omitempty"`
	Since        int64               `json:"since"`
	Attempt      int                 `json:"attempt"`
	NextRetry    int64               `json:"next_retry"`
	Forwards     []TunnelForwardInfo `json:"forwards"`
	// Degraded: connected, but a forward that should listen does not (it
	// failed to start, or was stopped by something other than the user).
	Degraded bool `json:"degraded"`
	// Gen numbers the current start (0 = stopped); ByHand: that start was
	// asked for by the user. See tunnelEntry.
	Gen    uint64 `json:"gen"`
	ByHand bool   `json:"by_hand"`
}

func (a *App) emitTunnelsChanged() {
	EventsEmit("tunnels_changed", nil)
}

// tunnelHoldsForwards reports whether the connection's tunnel is up (or on
// its way up), i.e. whether it owns the connection's auto-start forwards.
func (a *App) tunnelHoldsForwards(connectionID string) bool {
	a.tunnelsMu.Lock()
	defer a.tunnelsMu.Unlock()
	e, ok := a.tunnels[connectionID]
	// A failed tunnel holds nothing; a terminal may bring the forwards up.
	return ok && e.state != tunnelError
}

// TunnelsList returns every tunnel-only connection plus any tunnel still
// running for a connection that has since stopped being one, sorted by
// folder path and name.
func (a *App) TunnelsList() ([]TunnelInfo, error) {
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	paths := a.folderPathIndex()
	specs, err := a.db.ListAllPortForwards()
	if err != nil {
		return nil, err
	}
	specsByConn := map[string][]store.PortForward{}
	for _, f := range specs {
		specsByConn[f.ConnectionID] = append(specsByConn[f.ConnectionID], f)
	}

	a.tunnelsMu.Lock()
	entries := make(map[string]tunnelEntry, len(a.tunnels))
	for id, e := range a.tunnels {
		cp := *e
		// The map is shared with the live entry; copy it under the lock.
		cp.handStopped = make(map[string]bool, len(e.handStopped))
		for k, v := range e.handStopped {
			cp.handStopped[k] = v
		}
		cp.fwdErr = make(map[string]string, len(e.fwdErr))
		for k, v := range e.fwdErr {
			cp.fwdErr[k] = v
		}
		entries[id] = cp
	}
	a.tunnelsMu.Unlock()

	// Live state by spec id from every session: a forward a terminal tab
	// started before the tunnel came up runs there, not in the tunnel, and
	// must not read as down (the pool keys specs by id, one session each).
	live := map[string]sshlayer.ForwardStatus{}
	for _, st := range a.forwards.List("") {
		live[st.ID] = st
	}

	out := []TunnelInfo{}
	seen := make(map[string]bool, len(conns))
	for _, c := range conns {
		seen[c.ID] = true
		if c.Protocol != "" && c.Protocol != "ssh" {
			continue
		}
		e, running := entries[c.ID]
		rs := resolver.ResolveWith(c, folders)
		if !rs.TunnelOnly && !running {
			continue
		}
		info := TunnelInfo{
			ConnectionID: c.ID,
			Name:         c.Name,
			Hostname:     rs.Hostname,
			Autostart:    rs.TunnelOnly && rs.TunnelAutostart,
			State:        tunnelStopped,
		}
		if c.FolderID != nil {
			info.FolderPath = paths[*c.FolderID]
		}
		if running {
			info.State = e.state
			info.SessionID = e.sessionID
			info.Error = e.err
			info.Since = e.since
			info.Attempt = e.attempt
			info.NextRetry = e.nextRetry
			info.Gen = e.gen
			info.ByHand = e.byHand
		}
		info.Forwards = []TunnelForwardInfo{}
		for _, f := range specsByConn[c.ID] {
			fi := TunnelForwardInfo{
				ID:          f.ID,
				Kind:        f.Kind,
				Description: f.Description,
				AutoStart:   f.AutoStart,
			}
			if f.LocalAddr != nil {
				fi.LocalAddr = *f.LocalAddr
			}
			if f.LocalPort != nil {
				fi.LocalPort = *f.LocalPort
			}
			if f.RemoteHost != nil {
				fi.RemoteHost = *f.RemoteHost
			}
			if f.RemotePort != nil {
				fi.RemotePort = *f.RemotePort
			}
			if st, ok := live[f.ID]; ok {
				fi.State = string(st.State)
				fi.Error = st.Error
				fi.BytesIn, fi.BytesOut = st.BytesIn, st.BytesOut
				// An auto-port forward can land on another port than the
				// saved one; show where it actually listens.
				if st.LocalPort != 0 {
					fi.LocalPort = st.LocalPort
				}
				if st.LocalAddr != "" {
					fi.LocalAddr = st.LocalAddr
				}
			}
			fi.HandStopped = running && e.handStopped[f.ID] && fi.State != string(sshlayer.StateListening)
			if fi.State == "" && fi.Error == "" && !fi.HandStopped && info.State == tunnelConnected {
				fi.Error = e.fwdErr[f.ID]
			}
			if info.State == tunnelConnected && fi.State != string(sshlayer.StateListening) && !fi.HandStopped {
				info.Degraded = true
			}
			info.Forwards = append(info.Forwards, fi)
		}
		out = append(out, info)
	}
	// A tunnel whose connection is gone (deleted here, by an MCP plan or a
	// sync pull) would keep its ports with no row to stop it from. The
	// frontend lists on every tree reload, so this is where it ends.
	for id := range entries {
		if !seen[id] {
			log.Printf("tunnels: connection %s is gone, stopping its tunnel", id)
			go func() { _ = a.TunnelStop(id) }()
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FolderPath != out[j].FolderPath {
			return strings.ToLower(out[i].FolderPath) < strings.ToLower(out[j].FolderPath)
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// TunnelStart brings a tunnel-only connection's tunnel up. It returns once
// the start is queued; progress and failure arrive through tunnels_changed
// and TunnelsList, since a connect can wait on a host-key or login prompt for
// as long as the user takes. Starting a tunnel that is already up or on its
// way up is a no-op; starting a failed one tries again.
func (a *App) TunnelStart(connectionID string) error {
	return a.tunnelStart(connectionID, false)
}

// tunnelStart: patient=true is the launch start, which keeps retrying
// network failures (the app can start before Wi-Fi or a VPN is up); a start
// by hand tries once and reports.
func (a *App) tunnelStart(connectionID string, patient bool) error {
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return err
	}
	if !settings.TunnelOnly {
		return fmt.Errorf("not a tunnel connection - turn on \"Tunnel only\" in its settings")
	}
	a.tunnelsMu.Lock()
	if old, ok := a.tunnels[connectionID]; ok {
		if old.state != tunnelError {
			a.tunnelsMu.Unlock()
			return nil
		}
		// A failed entry has no loop and no session left; replace it.
		close(old.stop)
	}
	a.tunnelGen++
	e := &tunnelEntry{state: tunnelConnecting, since: time.Now().Unix(), stop: make(chan struct{}), gen: a.tunnelGen, byHand: !patient}
	a.tunnels[connectionID] = e
	a.tunnelsMu.Unlock()
	a.emitTunnelsChanged()
	go a.tunnelConnectLoop(connectionID, e, "", patient, false)
	return nil
}

// TunnelStop takes a tunnel down: cancels a pending connect or reconnect,
// then disconnects the session. A no-op for a stopped tunnel.
func (a *App) TunnelStop(connectionID string) error {
	a.tunnelsMu.Lock()
	e, ok := a.tunnels[connectionID]
	sessionID := ""
	if ok {
		delete(a.tunnels, connectionID)
		close(e.stop)
		sessionID = e.sessionID
	}
	a.tunnelsMu.Unlock()
	if !ok {
		return nil
	}
	a.SshCancelConnect(tunnelCancelKey(connectionID))
	if sessionID != "" {
		_ = a.SshDisconnect(sessionID)
	}
	a.emitTunnelsChanged()
	return nil
}

// TunnelsAutostart starts every tunnel marked "start on app launch". The
// frontend calls it each time the vault gate is passed. The first call
// starts them all; later calls (a UI reload, an unlock after "Skip") only
// retry the ones that failed on a locked vault, so a tunnel the user
// stopped by hand stays stopped. Returns how many it started.
func (a *App) TunnelsAutostart() int {
	a.tunnelsMu.Lock()
	first := !a.tunnelsAutostarted
	a.tunnelsAutostarted = true
	a.tunnelsMu.Unlock()

	list, err := a.TunnelsList()
	if err != nil {
		log.Printf("tunnels: autostart list: %v", err)
		return 0
	}
	n := 0
	for _, t := range list {
		if !t.Autostart {
			continue
		}
		if first {
			if t.State != tunnelStopped {
				continue
			}
		} else if !a.tunnelFailedOnVault(t.ConnectionID) {
			continue
		}
		if err := a.tunnelStart(t.ConnectionID, true); err != nil {
			log.Printf("tunnels: autostart %s: %v", t.Name, err)
			continue
		}
		n++
	}
	if n > 0 {
		log.Printf("tunnels: started %d on launch", n)
	}
	return n
}

func (a *App) tunnelFailedOnVault(connectionID string) bool {
	a.tunnelsMu.Lock()
	defer a.tunnelsMu.Unlock()
	e, ok := a.tunnels[connectionID]
	return ok && e.state == tunnelError && e.vaultLocked
}

// tunnelCurrent reports whether e is still the live entry for the
// connection (TunnelStop removes it; a later start replaces it).
func (a *App) tunnelCurrent(connectionID string, e *tunnelEntry) bool {
	return a.tunnels[connectionID] == e
}

// tunnelRetryable says whether a failed connect is worth trying again: the
// network or the server being away. Anything else - a rejected host key, a
// failed or cancelled login, a locked vault, a deleted connection - would
// fail the same way and, for a login, put the same prompt up every minute.
func tunnelRetryable(err error) bool {
	if err == nil || errors.Is(err, store.ErrNotFound) || sshlayer.ContainsVaultLocked(err) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection refused", "no route to host", "network is unreachable",
		"host is down", "i/o timeout", "connection reset", "connection timed out",
		"no such host", "temporary failure in name resolution", "broken pipe",
		"handshake: eof", "handshake failed: eof", "context deadline exceeded",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// tunnelConnectLoop connects the tunnel. patient=false is a start by hand:
// one try, and a failure leaves the tunnel in "error". patient=true keeps
// trying while the failure is retryable (tunnelRetryable), backing off 1, 2,
// 4 ... up to tunnelRetryMaxDelay, until it connects or the tunnel is
// stopped; a failure that is not retryable ends in "error" either way.
// waitFirst delays the first try (a reconnect right after a drop).
// oldSessionID is the dropped session, whose running forwards the new one
// restores.
func (a *App) tunnelConnectLoop(connectionID string, e *tunnelEntry, oldSessionID string, patient, waitFirst bool) {
	defer a.forgetReconnectForwards(oldSessionID)
	delay := time.Second
	for try := 0; ; try++ {
		if try > 0 || waitFirst {
			a.tunnelsMu.Lock()
			if !a.tunnelCurrent(connectionID, e) {
				a.tunnelsMu.Unlock()
				return
			}
			e.attempt++
			e.nextRetry = time.Now().Add(delay).Unix()
			a.tunnelsMu.Unlock()
			a.emitTunnelsChanged()
			select {
			case <-e.stop:
				return
			case <-time.After(delay):
			}
			delay *= 2
			if delay > tunnelRetryMaxDelay {
				delay = tunnelRetryMaxDelay
			}
		}

		res, err := a.sshConnectWith(connectionID, "", "", "", true)

		a.tunnelsMu.Lock()
		if !a.tunnelCurrent(connectionID, e) {
			// Stopped while connecting: drop what we just opened.
			a.tunnelsMu.Unlock()
			if err == nil {
				_ = a.SshDisconnect(res.SessionID)
			}
			return
		}
		now := time.Now().Unix()
		if err == nil {
			// The session can drop between the connect returning and this
			// line; its close hook then found no session id to match and
			// ignored it. The pool tells: a closed session is already out.
			// Checked under tunnelsMu, so a close after this point finds the
			// id set and reconnects through tunnelSessionClosed.
			if _, live := a.pool.Get(res.SessionID); !live {
				e.state = tunnelReconnecting
				e.err = "connection lost"
				a.tunnelsMu.Unlock()
				a.emitTunnelsChanged()
				continue
			}
			// The session id now, so TunnelStop and the close hook see this
			// session while its forwards come up; the state stays connecting
			// until they have, or a list taken in between would show a
			// connected tunnel with nothing listening (degraded, and a
			// misleading "only 0/N" toast for a start by hand).
			e.sessionID = res.SessionID
			skip := make(map[string]bool, len(e.handStopped))
			for k := range e.handStopped {
				skip[k] = true
			}
			a.tunnelsMu.Unlock()
			// Every saved forward comes up (the connection exists for them,
			// auto-start flag or not), on a start and on a reconnect alike -
			// a forward that failed last time gets another try (a remote
			// forward the server still held after the drop). Only forwards
			// the user stopped in this tunnel stay stopped.
			failed := a.forwardsAutoStartFor(connectionID, res.SessionID, true, skip)
			a.tunnelsMu.Lock()
			// Stopped meanwhile (TunnelStop disconnected the session), or the
			// session dropped and tunnelSessionClosed took over: not ours to
			// mark connected any more. The session's close hook stopped its
			// forwards before we started ours, so stop what we started.
			if !a.tunnelCurrent(connectionID, e) || e.sessionID != res.SessionID {
				a.tunnelsMu.Unlock()
				a.forwards.StopAllForSession(res.SessionID)
				return
			}
			e.state = tunnelConnected
			e.err = ""
			e.vaultLocked = false
			e.attempt = 0
			e.nextRetry = 0
			e.since = time.Now().Unix()
			e.fwdErr = map[string]string{}
			for id, ferr := range failed {
				e.fwdErr[id] = ferr.Error()
			}
			a.tunnelsMu.Unlock()
			a.emitTunnelsChanged()
			return
		}
		e.err = err.Error()
		e.nextRetry = 0
		e.vaultLocked = sshlayer.ContainsVaultLocked(err)
		if !patient || !tunnelRetryable(err) {
			e.state = tunnelError
			e.since = now
			a.tunnelsMu.Unlock()
			log.Printf("tunnels: %s failed: %v", connectionID, err)
			a.emitTunnelsChanged()
			return
		}
		e.state = tunnelReconnecting
		attempt := e.attempt
		a.tunnelsMu.Unlock()
		log.Printf("tunnels: %s attempt %d failed: %v", connectionID, attempt, err)
	}
}

// forgetReconnectForwards drops the forward list remembered for a dropped
// session that will not be restored.
func (a *App) forgetReconnectForwards(oldSessionID string) {
	if oldSessionID == "" {
		return
	}
	a.reconnectMu.Lock()
	delete(a.reconnectForwards, oldSessionID)
	a.reconnectMu.Unlock()
}

// tunnelSessionClosed is the close hook of a tunnel session. A stop
// (user-initiated disconnect) has already removed the entry; any other end
// is a drop, and the tunnel reconnects.
func (a *App) tunnelSessionClosed(connectionID, sessionID string, userInitiated bool) {
	// The close hook stopped the session's forwards already, but the tunnel
	// loop may have started some on it since (it starts them after the
	// connect returns). A listener on a dead session would hold its port and
	// read as listening; stop again.
	a.forwards.StopAllForSession(sessionID)
	a.tunnelsMu.Lock()
	e, ok := a.tunnels[connectionID]
	if !ok || e.sessionID != sessionID {
		a.tunnelsMu.Unlock()
		a.forgetReconnectForwards(sessionID)
		return
	}
	if userInitiated {
		// Disconnected by something other than TunnelStop (app quit, an
		// MCP or share teardown). Nothing will bring it back; say stopped.
		delete(a.tunnels, connectionID)
		close(e.stop)
		a.tunnelsMu.Unlock()
		a.emitTunnelsChanged()
		return
	}
	e.state = tunnelReconnecting
	e.sessionID = ""
	e.byHand = false // a reconnect is nobody's start by hand
	e.err = "connection lost"
	e.since = time.Now().Unix()
	a.tunnelsMu.Unlock()
	log.Printf("tunnels: %s dropped, reconnecting", connectionID)
	a.emitTunnelsChanged()
	go a.tunnelConnectLoop(connectionID, e, sessionID, true, true)
}

// tunnelForwardHandStopped records a forward started or stopped by the user
// while the connection's tunnel runs (see tunnelEntry.handStopped). A no-op
// when the connection has no tunnel.
func (a *App) tunnelForwardHandStopped(connectionID, forwardID string, stopped bool) {
	a.tunnelsMu.Lock()
	e, ok := a.tunnels[connectionID]
	if ok {
		if stopped {
			if e.handStopped == nil {
				e.handStopped = map[string]bool{}
			}
			e.handStopped[forwardID] = true
		} else {
			delete(e.handStopped, forwardID)
			delete(e.fwdErr, forwardID)
		}
	}
	a.tunnelsMu.Unlock()
	if ok {
		a.emitTunnelsChanged()
	}
}

// tunnelForwardFailed records why a forward started by hand in a running
// tunnel did not come up, for the panel.
func (a *App) tunnelForwardFailed(connectionID, forwardID string, err error) {
	a.tunnelsMu.Lock()
	e, ok := a.tunnels[connectionID]
	if ok {
		if e.fwdErr == nil {
			e.fwdErr = map[string]string{}
		}
		e.fwdErr[forwardID] = err.Error()
	}
	a.tunnelsMu.Unlock()
	if ok {
		a.emitTunnelsChanged()
	}
}
