package main

// Tunnel presence + remote-disconnect across synced machines. A synced
// WireGuard profile carries one identity; two machines running it at
// once flap. This wires internal/presence into the tunnel lifecycle:
//
//   - When a tunnel comes up, publish a presence record and heartbeat
//     it while it's live; clear it on stop.
//   - Before bringing a WG tunnel up, check for a live owner on another
//     machine and let the UI offer a take-over.
//   - A take-over writes a kill-request; the owning machine, polling
//     presence while any tunnel is up, honours it by stopping the named
//     tunnel.
//
// Everything rides the existing sync transport (a plaintext
// presence.json beside the snapshot), so no direct machine channel is
// needed. Only meaningful when sync is configured; a no-op otherwise.

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/presence"
	"ssh-tool/internal/syncer"
)

// presencePoll is how often an owning machine re-reads presence while
// a tunnel is up, to spot a kill-request. The UI countdown for a
// take-over derives its estimate from this (2x + margin).
const presencePoll = 25 * time.Second

// presenceState holds the runtime bits: this machine's id, which
// profiles we currently own (for heartbeat), and the set of
// kill-request nonces already honoured (so one isn't re-run after we
// legitimately reconnect).
type presenceState struct {
	mu       sync.Mutex
	machine  string
	owned    map[string]string // profileID -> kind, tunnels we own
	handled  map[string]bool   // honoured kill nonces
	pollStop chan struct{}     // non-nil while the poll loop runs
	// force holds profile ids the user has explicitly authorised to
	// bring up despite a live owner elsewhere (they either took over
	// or chose "connect anyway"). One-shot: consumed on the next
	// ensureWgTunnel so a later connect re-checks presence.
	force map[string]bool
}

// machineID returns this install's presence id: the hardware/OS-derived
// machine identifier (creds.StableMachineID), cached for the process
// lifetime. It MUST NOT be a store-persisted UUID - that rides the sync
// snapshot, so two machines sharing a profile would end up with the same
// id and each would read the other's presence record as its own (the
// bug that made "running on another machine" never show). The stable id
// is derived from /etc/machine-id (Linux), the hardware UUID (macOS) or
// the registry (Windows) and never travels through sync.
func (a *App) machineID() string {
	a.presence.mu.Lock()
	defer a.presence.mu.Unlock()
	if a.presence.machine != "" {
		return a.presence.machine
	}
	id := creds.StableMachineID()
	if id == "" {
		// Impossible in practice (hostname is the last fallback); use a
		// process-random id so presence just treats every record as
		// foreign rather than silently matching.
		id = "unknown-" + uuid.NewString()
	}
	a.presence.machine = id
	return id
}

func machineName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "machine"
}

// presenceTransport builds a sync transport for a presence read/write,
// or (nil, false) when sync isn't configured / vault is locked. The
// caller closes it. Presence is strictly best-effort: no configured
// sync means the feature is simply off.
func (a *App) presenceTransport() (syncer.Transport, bool) {
	if !a.autoSyncReady() {
		return nil, false
	}
	t, _, err := a.syncClient()
	if err != nil {
		return nil, false
	}
	return t, true
}

func syncNotFound(err error) bool { return err == syncer.ErrNotFound }

// presenceLoadSave runs fn against the current presence file and saves
// the result. Serialised through presence.mu-adjacent logic by the
// callers; the sync object itself has no locking, so concurrent
// machines rely on the small window + generation-free last-writer
// semantics (presence is advisory, not authoritative).
func (a *App) presenceLoadSave(fn func(*presence.File)) {
	t, ok := a.presenceTransport()
	if !ok {
		return
	}
	defer t.Close()
	f, err := presence.Load(t, syncNotFound)
	if err != nil {
		log.Printf("presence: load: %v", err)
		return
	}
	fn(f)
	if err := presence.Save(t, f); err != nil {
		log.Printf("presence: save: %v", err)
	}
}

// presencePublish records this machine as owner of a profile's tunnel
// and starts the poll loop if it isn't running. Called when a tunnel
// comes up.
func (a *App) presencePublish(profileID, kind string) {
	a.presence.mu.Lock()
	if a.presence.owned == nil {
		a.presence.owned = map[string]string{}
	}
	a.presence.owned[profileID] = kind
	a.presence.mu.Unlock()

	now := time.Now().Unix()
	a.presenceLoadSave(func(f *presence.File) {
		f.SetOwner(profileID, presence.Record{
			MachineID:   a.machineID(),
			MachineName: machineName(),
			Kind:        kind,
			Since:       now,
			Heartbeat:   now,
		})
	})
	a.ensurePresencePoll()
}

// presenceClear drops this machine's ownership of a profile (clean
// stop). Stops the poll loop when nothing is owned anymore.
func (a *App) presenceClear(profileID string) {
	a.presence.mu.Lock()
	delete(a.presence.owned, profileID)
	remaining := len(a.presence.owned)
	a.presence.mu.Unlock()

	self := a.machineID()
	a.presenceLoadSave(func(f *presence.File) {
		f.ClearOwner(profileID, self)
	})
	if remaining == 0 {
		a.stopPresencePoll()
	}
}

// ensurePresencePoll starts the background poll loop if not running.
func (a *App) ensurePresencePoll() {
	a.presence.mu.Lock()
	if a.presence.pollStop != nil {
		a.presence.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	a.presence.pollStop = stop
	a.presence.mu.Unlock()

	go a.presencePollLoop(stop)
}

func (a *App) stopPresencePoll() {
	a.presence.mu.Lock()
	if a.presence.pollStop != nil {
		close(a.presence.pollStop)
		a.presence.pollStop = nil
	}
	a.presence.mu.Unlock()
}

// presencePollLoop, while any tunnel is owned, refreshes our
// heartbeats and honours any kill-request targeting us.
func (a *App) presencePollLoop(stop chan struct{}) {
	t := time.NewTicker(presencePoll)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a.presenceTick()
		}
	}
}

// presenceTick is one poll: heartbeat owned profiles + act on kills.
func (a *App) presenceTick() {
	a.presence.mu.Lock()
	owned := make(map[string]string, len(a.presence.owned))
	for k, v := range a.presence.owned {
		owned[k] = v
	}
	handled := make(map[string]bool, len(a.presence.handled))
	for k := range a.presence.handled {
		handled[k] = true
	}
	a.presence.mu.Unlock()
	if len(owned) == 0 {
		return
	}

	self := a.machineID()
	now := time.Now().Unix()
	var toKill []string

	a.presenceLoadSave(func(f *presence.File) {
		for pid, kind := range owned {
			// Honour a kill-request targeting us for this profile.
			if k := f.PendingKillFor(pid, self, handled); k != nil {
				toKill = append(toKill, pid)
				a.presence.mu.Lock()
				if a.presence.handled == nil {
					a.presence.handled = map[string]bool{}
				}
				a.presence.handled[k.Nonce] = true
				a.presence.mu.Unlock()
				f.ClearKill(pid)
				f.ClearOwner(pid, self)
				log.Printf("presence: %s asked us to release profile %s; stopping", k.ByName, pid)
				continue
			}
			// Otherwise refresh our heartbeat.
			f.SetOwner(pid, presence.Record{
				MachineID:   self,
				MachineName: machineName(),
				Kind:        kind,
				Heartbeat:   now,
			})
		}
	})

	for _, pid := range toKill {
		a.presence.mu.Lock()
		delete(a.presence.owned, pid)
		a.presence.mu.Unlock()
		// Stop the managers directly - NOT tunnelStop, which would
		// re-enter presence (we already cleared this profile's owner +
		// kill inside the presenceLoadSave above).
		a.wgman.Stop(pid)
		a.nbman.Stop(pid)
		EventsEmit("network_tunnel_changed", pid)
	}
}

// ----- IPC: bring-up check, take-over, status -----

// RemoteOwner is what the UI needs to render the take-over dialog.
type RemoteOwner struct {
	Active      bool   `json:"active"`       // a live foreign owner exists
	MachineName string `json:"machine_name"` // hostname to name in the dialog
	Kind        string `json:"kind"`
	SinceUnix   int64  `json:"since_unix"`
	// EstimateSeconds is the honest upper bound the countdown should
	// start from: two poll cycles (owner reads the kill, requester
	// reads the freed presence) plus a margin.
	EstimateSeconds int `json:"estimate_seconds"`
}

// ErrProfileBusyElsewhere is returned by the connect path when a WG
// profile's tunnel is live on another synced machine and the user
// hasn't authorised a take-over. The frontend recognises this to show
// the take-over dialog instead of a raw connect failure.
const errProfileBusyPrefix = "network profile is active on another machine: "

// presenceBlocksBringUp checks, for a WG profile about to come up,
// whether another machine owns it and the user hasn't forced it. A
// consumed force clears itself. WireGuard only - NetBird gives each
// machine its own peer, so there's no identity conflict to guard.
func (a *App) presenceBlocksBringUp(profileID string) (owner string, blocked bool) {
	a.presence.mu.Lock()
	if a.presence.force[profileID] {
		delete(a.presence.force, profileID)
		a.presence.mu.Unlock()
		return "", false
	}
	a.presence.mu.Unlock()

	ro := a.NetworkProfilePresence(profileID)
	if ro.Active {
		return ro.MachineName, true
	}
	return "", false
}

// markForce authorises one bring-up of a profile despite a live owner
// (set by TakeOver-confirmed or connect-anyway).
func (a *App) markForce(profileID string) {
	a.presence.mu.Lock()
	if a.presence.force == nil {
		a.presence.force = map[string]bool{}
	}
	a.presence.force[profileID] = true
	a.presence.mu.Unlock()
}

// NetworkProfilePresence reports whether a profile's tunnel is live on
// another synced machine right now. The connect flow calls this before
// bringing a WG tunnel up so it can offer a take-over instead of
// flapping. Empty/false when sync is off, the profile is free, or we
// own it ourselves.
func (a *App) NetworkProfilePresence(profileID string) RemoteOwner {
	t, ok := a.presenceTransport()
	if !ok {
		return RemoteOwner{}
	}
	defer t.Close()
	f, err := presence.Load(t, syncNotFound)
	if err != nil {
		return RemoteOwner{}
	}
	r := f.LiveOwner(profileID, a.machineID())
	if r == nil {
		return RemoteOwner{}
	}
	return RemoteOwner{
		Active:          true,
		MachineName:     r.MachineName,
		Kind:            r.Kind,
		SinceUnix:       r.Since,
		EstimateSeconds: int(2*presencePoll.Seconds()) + 15,
	}
}

// NetworkProfileTakeOver writes a kill-request for a profile's current
// owner and returns the estimate the caller's countdown uses. The
// requester then polls NetworkProfilePresence until the owner's record
// disappears (or the estimate elapses, at which point the UI offers a
// forced connect). No-op (ok=false) if there's no live foreign owner.
func (a *App) NetworkProfileTakeOver(profileID string) (int, error) {
	t, ok := a.presenceTransport()
	if !ok {
		return 0, nil
	}
	defer t.Close()
	f, err := presence.Load(t, syncNotFound)
	if err != nil {
		return 0, err
	}
	self := a.machineID()
	if f.LiveOwner(profileID, self) == nil {
		return 0, nil // already free / ours
	}
	nonce := presence.NewNonce(self)
	if _, done := f.RequestKill(profileID, self, machineName(), nonce); !done {
		return 0, nil
	}
	if err := presence.Save(t, f); err != nil {
		return 0, err
	}
	// Authorise the next bring-up: once the owner releases (or the
	// estimate elapses and the user forces it), the retry connect must
	// pass the presence guard.
	a.markForce(profileID)
	log.Printf("presence: requested take-over of profile %s", profileID)
	return int(2*presencePoll.Seconds()) + 15, nil
}

// NetworkProfileConnectAnyway authorises one bring-up of a profile
// despite a live owner, without asking it to stop (the "connect
// anyway" escape when the owner is known-dead or unresponsive). The
// two peers will flap - the UI warns before calling this.
func (a *App) NetworkProfileConnectAnyway(profileID string) {
	a.markForce(profileID)
}
