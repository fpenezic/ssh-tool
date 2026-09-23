package main

import (
	"testing"

	"ssh-tool/internal/local"
)

// A window that did not open a tab (a detached one, or the main window
// after a reload) rebuilds it from LocalShellList alone. The list has to
// carry the saved connection's identity, or the tab comes back as its
// bare shell kind with no icon.
func TestLocalShellListCarriesConnectionIdentity(t *testing.T) {
	a := &App{localPool: local.NewPool()}
	a.localPool.Add(&local.Session{ID: "s1", Kind: "wsl", Display: "wsl", ConnectionID: "c1", Name: "Build box"})
	a.localPool.Add(&local.Session{ID: "s2", Kind: "bash", Display: "bash"})

	got := map[string]LocalShellInfo{}
	for _, l := range a.LocalShellList() {
		got[l.SessionID] = l
	}
	if l := got["s1"]; l.ConnectionID != "c1" || l.Name != "Build box" {
		t.Errorf("saved connection shell: got %+v, want connection_id c1, name %q", l, "Build box")
	}
	if l := got["s2"]; l.ConnectionID != "" || l.Name != "" {
		t.Errorf("ad-hoc shell: got %+v, want empty connection_id and name", l)
	}
}
