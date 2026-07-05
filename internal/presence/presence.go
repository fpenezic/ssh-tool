// Package presence coordinates network-tunnel ownership across the
// machines that share a synced profile. A WireGuard profile carries a
// single identity (key + overlay IP); if the tunnel is up on two
// machines at once, both peers fight for it and both degrade. This
// package lets a second machine SEE that the tunnel is live elsewhere
// and ASK the owning machine to hand it over, all through the existing
// sync transport - no direct machine-to-machine channel needed.
//
// The data is a small plaintext file (presence.json) alongside the
// snapshot. It carries hostnames + machine ids + timestamps, no
// secrets, so it can be written frequently (heartbeat) without
// re-sealing the vault.
//
// Key invariant that makes this safe: our tunnels are userspace and
// in-process, so a tunnel cannot outlive its app. A FRESH presence
// record therefore implies the owning app is alive and will see a
// kill-request. No zombie-tunnel problem.
package presence

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FileName is the sync object presence lives in, beside the snapshot.
const FileName = "presence.json"

// formatVersion guards against a future incompatible shape.
const formatVersion = 1

// Tunables. Exposed so the app and the UI countdown derive their
// timing from the same source of truth.
const (
	// Heartbeat is how often an owning machine refreshes its record
	// while a tunnel is up.
	Heartbeat = 30 * time.Second
	// StaleAfter is when a record is considered dead (missed beats).
	// 3 missed 30s beats + margin.
	StaleAfter = 100 * time.Second
	// KillTTL is how long a kill-request lingers before GC drops it
	// (covers the target being briefly offline).
	KillTTL = 10 * time.Minute
)

// Record is one machine's claim on a profile's tunnel.
type Record struct {
	MachineID   string `json:"machine_id"`
	MachineName string `json:"machine_name"` // hostname, for the UI
	Kind        string `json:"kind"`         // "wireguard" | "netbird"
	Since       int64  `json:"since"`        // unix, tunnel-up time
	Heartbeat   int64  `json:"heartbeat"`    // unix, last refresh
}

// Kill is a request that the named machine stop a profile's tunnel.
type Kill struct {
	Target    string `json:"target"`     // machine_id that should stop
	ByMachine string `json:"by_machine"` // machine_id that asked
	ByName    string `json:"by_name"`    // hostname, for the owner's UI
	At        int64  `json:"at"`         // unix, request time
	Nonce     string `json:"nonce"`      // so a stale request isn't re-honoured
}

// File is the whole presence document. Both maps are keyed by
// profileID: at most one live owner + at most one pending kill per
// profile (a second take-over request supersedes the first via nonce).
type File struct {
	Format   int               `json:"format"`
	Presence map[string]Record `json:"presence"`
	Kills    map[string]Kill   `json:"kills"`
}

// Transport is the subset of the sync transport presence needs. The
// syncer.Transport satisfies it; kept local so this package doesn't
// import syncer (avoids a cycle and keeps it testable).
type Transport interface {
	Get(name string) ([]byte, error)
	Put(name string, data []byte) error
}

// ErrNotFound is what a Transport.Get returns when the object has
// never been written. Callers pass their transport's sentinel in via
// IsNotFound so this package stays transport-agnostic.
var ErrNotFound = errors.New("presence: not found")

// Load reads and parses the presence file. A missing file (per
// isNotFound) yields an empty document, not an error - the first
// machine to sync simply hasn't written one yet.
func Load(t Transport, isNotFound func(error) bool) (*File, error) {
	raw, err := t.Get(FileName)
	if err != nil {
		if isNotFound != nil && isNotFound(err) {
			return emptyFile(), nil
		}
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		// A corrupt presence file must not wedge tunnels - start fresh.
		return emptyFile(), nil
	}
	if f.Presence == nil {
		f.Presence = map[string]Record{}
	}
	if f.Kills == nil {
		f.Kills = map[string]Kill{}
	}
	return &f, nil
}

// Save writes the document back after GC. Presence is best-effort:
// callers log and continue on error rather than failing a connect.
func Save(t Transport, f *File) error {
	f.Format = formatVersion
	gc(f, time.Now())
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return t.Put(FileName, raw)
}

func emptyFile() *File {
	return &File{Format: formatVersion, Presence: map[string]Record{}, Kills: map[string]Kill{}}
}

// gc drops stale presence records and expired kill-requests. Called
// on every Save so the file can't grow without bound after crashes.
func gc(f *File, now time.Time) {
	for pid, r := range f.Presence {
		if now.Unix()-r.Heartbeat > int64(StaleAfter.Seconds()) {
			delete(f.Presence, pid)
		}
	}
	for pid, k := range f.Kills {
		if now.Unix()-k.At > int64(KillTTL.Seconds()) {
			delete(f.Kills, pid)
		}
	}
}

// LiveOwner returns the presence record for a profile if it is fresh
// and owned by a DIFFERENT machine than selfID. Returns nil when the
// profile is free, stale, or owned by this machine (our own record is
// not a conflict).
func (f *File) LiveOwner(profileID, selfID string) *Record {
	r, ok := f.Presence[profileID]
	if !ok || r.MachineID == selfID {
		return nil
	}
	if time.Now().Unix()-r.Heartbeat > int64(StaleAfter.Seconds()) {
		return nil
	}
	rc := r
	return &rc
}

// PendingKillFor returns a kill-request targeting selfID for the
// profile, if one exists and hasn't been handled (nonce not in
// handled). The owner passes its set of already-honoured nonces so a
// request isn't re-executed after the owner legitimately reconnects.
func (f *File) PendingKillFor(profileID, selfID string, handled map[string]bool) *Kill {
	k, ok := f.Kills[profileID]
	if !ok || k.Target != selfID {
		return nil
	}
	if handled[k.Nonce] {
		return nil
	}
	kc := k
	return &kc
}

// SetOwner records this machine as the live owner of a profile's
// tunnel (or refreshes the heartbeat if already owner).
func (f *File) SetOwner(profileID string, r Record) {
	if existing, ok := f.Presence[profileID]; ok && existing.MachineID == r.MachineID {
		r.Since = existing.Since // preserve original up-time on heartbeat
	}
	f.Presence[profileID] = r
}

// ClearOwner drops this machine's ownership of a profile (clean stop).
// Only removes the record when WE own it, so we don't wipe another
// machine's claim.
func (f *File) ClearOwner(profileID, selfID string) {
	if r, ok := f.Presence[profileID]; ok && r.MachineID == selfID {
		delete(f.Presence, profileID)
	}
}

// RequestKill records a take-over request targeting the profile's
// current owner. No-op (returns "", false) if the profile has no live
// foreign owner. Returns the nonce so the requester can watch for the
// owner's presence to disappear.
func (f *File) RequestKill(profileID, selfID, selfName string, nonce string) (string, bool) {
	r, ok := f.Presence[profileID]
	if !ok || r.MachineID == selfID {
		return "", false
	}
	f.Kills[profileID] = Kill{
		Target:    r.MachineID,
		ByMachine: selfID,
		ByName:    selfName,
		At:        time.Now().Unix(),
		Nonce:     nonce,
	}
	return nonce, true
}

// ClearKill removes a kill-request for a profile (the owner honoured
// it, or the requester is done). Idempotent.
func (f *File) ClearKill(profileID string) {
	delete(f.Kills, profileID)
}

// NewNonce is a small unique token for a kill-request. Not a secret;
// just needs to differ across requests so a stale one isn't reused.
func NewNonce(seed string) string {
	return fmt.Sprintf("%s-%d", seed, time.Now().UnixNano())
}
