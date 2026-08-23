package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"

	"ssh-tool/internal/store"
)

// newPersistTestApp builds an App with a real store and the same initial
// broadcast state Startup sets up.
func newPersistTestApp(t *testing.T, dbPath string) *App {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{db: db}
	a.broadcastGroups = map[string]map[string]bool{"": make(map[string]bool)}
	return a
}

func groupNames(a *App) []string {
	a.broadcastMu.Lock()
	defer a.broadcastMu.Unlock()
	var out []string
	for g := range a.broadcastGroups {
		if g != "" {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

func TestBroadcastGroupsSurviveRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-1")
	a.BroadcastAddTo("db-tier", "session-2")

	// Relaunch: a fresh App over the same store.
	b := newPersistTestApp(t, dbPath)
	b.restoreBroadcastGroups()

	got := groupNames(b)
	if len(got) != 2 || got[0] != "db-tier" || got[1] != "web-tier" {
		t.Fatalf("restored groups = %v, want [db-tier web-tier]", got)
	}
}

// The whole point of restoring names only. Session IDs from the previous run
// are dead; a group pre-populated with them shows a member badge for sessions
// that do not exist - the ghost-membership bug fixed in v0.86.0.
func TestRestoredGroupsAreEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-1")
	a.BroadcastAddTo("web-tier", "session-2")

	b := newPersistTestApp(t, dbPath)
	b.restoreBroadcastGroups()

	b.broadcastMu.Lock()
	n := len(b.broadcastGroups["web-tier"])
	b.broadcastMu.Unlock()
	if n != 0 {
		t.Errorf("restored group has %d members; membership must not survive a restart", n)
	}
}

// The stored blob must hold names, never session IDs - a direct check that no
// future refactor starts persisting membership.
func TestPersistedBlobHoldsNoSessionIDs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-abc")

	raw, ok, err := a.db.GetSetting(broadcastGroupsKey)
	if err != nil || !ok {
		t.Fatalf("setting not written: ok=%v err=%v", ok, err)
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		t.Fatalf("stored blob is not a name list: %v (%s)", err, raw)
	}
	for _, n := range names {
		if n == "session-abc" {
			t.Fatal("a session ID was persisted; only group names may be stored")
		}
	}
}

// The default group is created at startup and cannot be deleted, so storing
// it would be noise that grows the blob for nothing.
func TestDefaultGroupIsNotPersisted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("", "session-1")

	raw, _, _ := a.db.GetSetting(broadcastGroupsKey)
	var names []string
	_ = json.Unmarshal([]byte(raw), &names)
	for _, n := range names {
		if n == "" {
			t.Error("the implicit default group must not be persisted")
		}
	}
}

func TestDeletedGroupDoesNotComeBack(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-1")
	a.BroadcastAddTo("db-tier", "session-2")
	a.BroadcastGroupDelete("web-tier")

	b := newPersistTestApp(t, dbPath)
	b.restoreBroadcastGroups()

	for _, g := range groupNames(b) {
		if g == "web-tier" {
			t.Fatal("a deleted group was restored")
		}
	}
}

// Clearing empties a group but keeps it defined, so the user can re-add
// without re-creating it. That distinction has to survive a restart too.
func TestClearedGroupStillPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")

	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-1")
	a.BroadcastClearGroup("web-tier")

	b := newPersistTestApp(t, dbPath)
	b.restoreBroadcastGroups()

	b.broadcastMu.Lock()
	_, ok := b.broadcastGroups["web-tier"]
	b.broadcastMu.Unlock()
	if !ok {
		t.Error("a cleared group should still exist after a restart")
	}
}

func TestRestoreKeepsTheDefaultGroup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-1")

	b := newPersistTestApp(t, dbPath)
	b.restoreBroadcastGroups()

	b.broadcastMu.Lock()
	_, ok := b.broadcastGroups[""]
	b.broadcastMu.Unlock()
	if !ok {
		t.Error("the default group must still exist after a restore")
	}
}

// A restore must never drop groups created before it runs, and must not
// clobber live membership if it is ever called twice.
func TestRestoreIsIdempotentAndNonDestructive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	a := newPersistTestApp(t, dbPath)
	a.BroadcastAddTo("web-tier", "session-1")

	b := newPersistTestApp(t, dbPath)
	b.restoreBroadcastGroups()
	b.BroadcastAddTo("web-tier", "live-session")
	b.restoreBroadcastGroups() // second call

	b.broadcastMu.Lock()
	members := len(b.broadcastGroups["web-tier"])
	b.broadcastMu.Unlock()
	if members != 1 {
		t.Errorf("a second restore wiped live membership: %d members, want 1", members)
	}
}

func TestRestoreToleratesGarbage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	a := newPersistTestApp(t, dbPath)
	if err := a.db.SetSetting(broadcastGroupsKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	// Must not panic, and must leave the default group intact.
	a.restoreBroadcastGroups()
	a.broadcastMu.Lock()
	_, ok := a.broadcastGroups[""]
	a.broadcastMu.Unlock()
	if !ok {
		t.Error("a corrupt setting should be ignored, not destroy the state")
	}
}

// Persistence hangs off emitBroadcastChanged, which every mutation calls. If
// a future mutation path skips the emit, the group list silently stops being
// saved - this pins the full set of entry points.
func TestEveryGroupMutationPersists(t *testing.T) {
	cases := []struct {
		name string
		act  func(a *App)
		want string
	}{
		{"BroadcastAddTo", func(a *App) { a.BroadcastAddTo("g-add", "s1") }, "g-add"},
		{"BroadcastSetAllInGroup", func(a *App) { a.BroadcastSetAllInGroup("g-setall", []string{"s1"}) }, "g-setall"},
		{"BroadcastClearGroup", func(a *App) { a.BroadcastClearGroup("g-clear") }, "g-clear"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newPersistTestApp(t, filepath.Join(t.TempDir(), "store.db"))
			c.act(a)
			raw, ok, _ := a.db.GetSetting(broadcastGroupsKey)
			if !ok {
				t.Fatalf("%s did not persist the group list", c.name)
			}
			var names []string
			_ = json.Unmarshal([]byte(raw), &names)
			found := false
			for _, n := range names {
				if n == c.want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: group %q missing from %v", c.name, c.want, names)
			}
		})
	}
}
