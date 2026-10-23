package main

import (
	"path/filepath"
	"testing"
)

func TestDetachedSessionSaveDropTake(t *testing.T) {
	saved := localStateStore
	localStateStore = &localState{path: filepath.Join(t.TempDir(), "local-state.json")}
	t.Cleanup(func() { localStateStore = saved })
	a := &App{}

	a.DetachedSessionSave("detached-a", `[{"title":"a"}]`)
	a.DetachedSessionSave("detached-b", `[{"title":"b"}]`)
	a.DetachedSessionSave("main", `[{"title":"not a detached window"}]`)
	// An emptied window forgets itself, like a redock or a close by hand.
	a.DetachedSessionSave("detached-c", `[{"title":"c"}]`)
	a.DetachedSessionSave("detached-c", "[]")
	a.detachedSessionDrop("detached-b")

	// A save arriving after the window closed for good is ignored.
	a.DetachedSessionSave("detached-b", `[{"title":"b again"}]`)

	got := a.DetachedSessionTake()
	if len(got) != 1 || got[0] != `[{"title":"a"}]` {
		t.Fatalf("take = %q", got)
	}
	// Taken means gone: the restored windows save themselves anew.
	if again := a.DetachedSessionTake(); len(again) != 0 {
		t.Fatalf("second take = %q", again)
	}
	if _, ok := localStateStore.get(detachedSessionKey); ok {
		t.Fatal("snapshot key left behind after the take")
	}

	// On the way out a window's reports are its tabs being torn down:
	// the snapshot from before the quit stands.
	a.DetachedSessionSave("detached-d", `[{"title":"d"}]`)
	a.appClosing.Store(true)
	a.DetachedSessionSave("detached-d", "[]")
	if got := a.DetachedSessionTake(); len(got) != 1 || got[0] != `[{"title":"d"}]` {
		t.Fatalf("after quit = %q", got)
	}
}
