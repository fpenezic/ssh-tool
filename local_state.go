package main

// Machine-local state, split out of the synced profile.
//
// Some "settings" are not profile at all - they describe THIS
// machine's session: which tabs are open, where the window sits,
// which Settings section was last viewed. Keeping them in store.db
// had two costs once sync landed: every tab switch dirtied the
// profile (auto-push fired for "no changes"), and a pull would
// overwrite this machine's window geometry and open-tab list with
// the other machine's - confusing, not syncing.
//
// They live in <DataDir>/local-state.json instead: not part of the
// sync/backup envelope, and writing them doesn't touch store.db's
// mtime (the auto-sync dirty signal). SettingsGet/Set/Delete route
// these keys here transparently, so no frontend caller changes;
// existing values migrate out of the DB on first write.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ssh-tool/internal/store"
)

// localStateKeys is the routing table: settings keys that are
// machine-local. Everything else stays in the store (and syncs).
var localStateKeys = map[string]bool{
	"last_session_tabs_v1":    true, // open-tab snapshot, written on every tab change
	"window_state_v1":         true, // window geometry, written on move/resize
	"settings_active_section": true, // last viewed Settings section
}

type localState struct {
	mu   sync.Mutex
	path string
	data map[string]string
	read bool
}

var localStateStore = &localState{}

func (l *localState) load() {
	if l.read {
		return
	}
	l.read = true
	if l.path == "" {
		l.path = filepath.Join(filepath.Dir(store.DefaultPath()), "local-state.json")
	}
	l.data = map[string]string{}
	if b, err := os.ReadFile(l.path); err == nil {
		_ = json.Unmarshal(b, &l.data)
	}
}

func (l *localState) get(key string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	v, ok := l.data[key]
	return v, ok
}

func (l *localState) set(key, value string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	l.data[key] = value
	return l.flushLocked()
}

func (l *localState) delete(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	delete(l.data, key)
	return l.flushLocked()
}

func (l *localState) flushLocked() error {
	b, err := json.MarshalIndent(l.data, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, b, 0o600)
}

// localSettingGet reads a routed key: local file first, then a
// one-time fallback to the DB row the value lived in before the
// split (migrated on the next write).
func (a *App) localSettingGet(key string) (string, bool) {
	if v, ok := localStateStore.get(key); ok {
		return v, true
	}
	if a.db != nil {
		if v, ok, _ := a.db.GetSetting(key); ok {
			return v, true
		}
	}
	return "", false
}

// localSettingSet writes the routed key locally and clears the
// legacy DB row so the synced profile stops carrying it.
func (a *App) localSettingSet(key, value string) error {
	if err := localStateStore.set(key, value); err != nil {
		return err
	}
	if a.db != nil {
		_ = a.db.DeleteSetting(key)
	}
	return nil
}

func (a *App) localSettingDelete(key string) error {
	if err := localStateStore.delete(key); err != nil {
		return err
	}
	if a.db != nil {
		_ = a.db.DeleteSetting(key)
	}
	return nil
}

// ----- Recents (machine-local) -----
//
// "Recently connected" used to be connections.last_used_at, touched
// on EVERY connect - one more store.db write the sync dirty signal
// can't distinguish from a real profile change (field log: a push
// fired two minutes after each connect). Recency is also genuinely
// per-machine: what you use at work isn't what you use at home.
// The legacy column stays (read-merged below) but is never written
// again.

const recentsKey = "recents_v1"

func (a *App) touchRecent(connectionID string) {
	raw, _ := localStateStore.get(recentsKey)
	m := map[string]int64{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	m[connectionID] = time.Now().Unix()
	if b, err := json.Marshal(m); err == nil {
		_ = localStateStore.set(recentsKey, string(b))
	}
}

// recentTimes merges local recents with the legacy column values
// (pre-split data), local winning on conflict.
func (a *App) recentTimes() map[string]int64 {
	m := map[string]int64{}
	if conns, err := a.db.ListConnections(nil); err == nil {
		for _, c := range conns {
			if c.LastUsedAt != nil {
				m[c.ID] = *c.LastUsedAt
			}
		}
	}
	if raw, ok := localStateStore.get(recentsKey); ok && raw != "" {
		local := map[string]int64{}
		if json.Unmarshal([]byte(raw), &local) == nil {
			for id, ts := range local {
				m[id] = ts
			}
		}
	}
	return m
}
