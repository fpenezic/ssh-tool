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

// restoreBroadcastGroups re-creates the persisted groups, empty. Call once at
// startup, after the store is open and before the frontend asks for the group
// list.
//
// Restoring them EMPTY is the deliberate part: a persisted member list would
// hold session IDs from the previous run, and every one of them is dead. A
// group pre-populated with dead IDs would show a non-zero member badge for
// sessions that do not exist - exactly the ghost-membership bug fixed in
// v0.86.0, reintroduced through the back door.
func (a *App) restoreBroadcastGroups() {
	if a.db == nil {
		return
	}
	raw, ok, err := a.db.GetSetting(broadcastGroupsKey)
	if err != nil || !ok || raw == "" {
		return
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return
	}
	a.broadcastMu.Lock()
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, exists := a.broadcastGroups[n]; !exists {
			a.broadcastGroups[n] = make(map[string]bool)
		}
	}
	a.broadcastMu.Unlock()
}
