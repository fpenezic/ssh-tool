package main

import (
	"encoding/json"
	"log"
	"strings"
)

// Detached windows' share of the last session. The main window saves its
// own tabs (last_session_tabs_v2); each detached window reports its tabs
// here, keyed by window name, so a quit with two windows open comes back
// as two windows. A window closed by hand, redocked or emptied drops out;
// one closed by the quit stays (appClosing).
const detachedSessionKey = "last_session_detached_v1"

// quitting: the app is on its way out. What a window reports from then on
// is its tabs being torn down, not a state to come back to.
func (a *App) quitting() bool {
	return a.appClosing.Load() || a.quitConfirmed.Load() || a.quitDone.Load()
}

// detachedSessionsLocked reads the saved windows. Caller holds detachedMu.
func (a *App) detachedSessionsLocked() map[string]string {
	m := map[string]string{}
	if raw, ok := a.localSettingGet(detachedSessionKey); ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			log.Printf("detached session: unreadable snapshot dropped: %v", err)
			return map[string]string{}
		}
	}
	return m
}

func (a *App) detachedSessionsWriteLocked(m map[string]string) {
	var err error
	if len(m) == 0 {
		err = a.localSettingDelete(detachedSessionKey)
	} else {
		var b []byte
		if b, err = json.Marshal(m); err == nil {
			err = a.localSettingSet(detachedSessionKey, string(b))
		}
	}
	if err != nil {
		log.Printf("detached session: save: %v", err)
	}
}

// DetachedSessionSave records a detached window's open tabs (the same
// JSON tab specs the main window saves); an empty list forgets it.
func (a *App) DetachedSessionSave(window, tabs string) {
	if !strings.HasPrefix(window, "detached-") || a.quitting() {
		return
	}
	a.detachedMu.Lock()
	defer a.detachedMu.Unlock()
	// A last save racing the window's own close must not bring it back.
	if a.detachedGone[window] {
		return
	}
	m := a.detachedSessionsLocked()
	if t := strings.TrimSpace(tabs); t == "" || t == "[]" {
		if _, ok := m[window]; !ok {
			return
		}
		delete(m, window)
	} else {
		if m[window] == tabs {
			return
		}
		m[window] = tabs
	}
	a.detachedSessionsWriteLocked(m)
}

// detachedSessionDrop forgets a window that is going away for good.
func (a *App) detachedSessionDrop(window string) {
	a.detachedMu.Lock()
	defer a.detachedMu.Unlock()
	if a.detachedGone == nil {
		a.detachedGone = map[string]bool{}
	}
	a.detachedGone[window] = true
	m := a.detachedSessionsLocked()
	if _, ok := m[window]; !ok {
		return
	}
	delete(m, window)
	a.detachedSessionsWriteLocked(m)
}

// DetachedSessionTake returns the windows the last session left open, one
// JSON tab-spec list each, and forgets them: the restore opens them under
// new names, and those save themselves again.
func (a *App) DetachedSessionTake() []string {
	a.detachedMu.Lock()
	defer a.detachedMu.Unlock()
	m := a.detachedSessionsLocked()
	out := make([]string, 0, len(m))
	for _, tabs := range m {
		out = append(out, tabs)
	}
	a.detachedSessionsWriteLocked(nil)
	return out
}
