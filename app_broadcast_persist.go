package main

// Persistence for the broadcast group LIST. Groups themselves live only in
// backend RAM (app.go, broadcastGroups) because their members are session IDs,
// which are minted per connection and mean nothing after a relaunch. What
// survives a restart is therefore the set of group NAMES, restored empty:
// the user's groups are still in the picker after a relaunch and they re-add
// sessions, instead of re-typing every group name from memory.
//
// A group's ID *is* its user-typed name (BroadcastManager's addGroup prompts
// for it and passes it straight through as the group key), so the name list is
// the whole structure worth keeping.

import (
	"encoding/json"
	"sort"
)

// broadcastGroupsKey is the app_settings key holding the persisted group
// names. Machine-local: broadcast groups describe how THIS machine's windows
// are arranged for a working session, not shared profile data, so the key is
// excluded from profile sync (see store.machineLocalSettings).
const broadcastGroupsKey = "broadcast_groups_v1"

// persistBroadcastGroups writes the current group names. Called after every
// mutation via emitBroadcastChanged, so it must stay cheap and must never
// block a UI action: a failure to persist is logged by the caller's choice to
// ignore it, not surfaced - losing the group list is a nuisance, not a fault
// worth interrupting a broadcast for.
func (a *App) persistBroadcastGroups() {
	if a.db == nil {
		return
	}
	a.broadcastMu.Lock()
	names := make([]string, 0, len(a.broadcastGroups))
	for gid := range a.broadcastGroups {
		// The default group ("") is implicit - it is created at startup and
		// cannot be deleted, so persisting it would be noise.
		if gid == "" {
			continue
		}
		names = append(names, gid)
	}
	a.broadcastMu.Unlock()
	sort.Strings(names)

	blob, err := json.Marshal(names)
	if err != nil {
		return
	}
	_ = a.db.SetSetting(broadcastGroupsKey, string(blob))
}

// savedBroadcastGroupNames returns the group names persisted by the previous
// run, without touching live state. The frontend calls this at startup to
// decide whether to offer a restore.
func (a *App) savedBroadcastGroupNames() []string {
	if a.db == nil {
		return nil
	}
	raw, ok, err := a.db.GetSetting(broadcastGroupsKey)
	if err != nil || !ok || raw == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// BroadcastSavedGroups reports the groups the previous run left behind, so the
// UI can ask before re-creating them. Empty when there is nothing to restore.
func (a *App) BroadcastSavedGroups() []string {
	return a.savedBroadcastGroupNames()
}

// BroadcastRestoreSaved re-creates the persisted groups, EMPTY, and pushes the
// new list to every window. Returns how many were added.
//
// Restoring them empty is the deliberate part: a persisted member list would
// hold session IDs from the previous run, and every one of them is dead. A
// group pre-populated with dead IDs would show a non-zero member badge for
// sessions that do not exist - exactly the ghost-membership bug fixed in
// v0.86.0, reintroduced through the back door.
func (a *App) BroadcastRestoreSaved() int {
	names := a.savedBroadcastGroupNames()
	if len(names) == 0 {
		return 0
	}
	added := 0
	a.broadcastMu.Lock()
	for _, n := range names {
		if _, exists := a.broadcastGroups[n]; !exists {
			a.broadcastGroups[n] = make(map[string]bool)
			added++
		}
	}
	a.broadcastMu.Unlock()
	if added > 0 {
		// Without this the restored groups sit in backend memory that no
		// window ever asked about again - the frontend reads the list once at
		// init, and a restore that happens after that is invisible.
		a.emitBroadcastChanged()
	}
	return added
}

// BroadcastForgetSaved drops the persisted group list. Used when the user
// declines a restore permanently.
func (a *App) BroadcastForgetSaved() {
	if a.db == nil {
		return
	}
	_ = a.db.SetSetting(broadcastGroupsKey, "[]")
}
