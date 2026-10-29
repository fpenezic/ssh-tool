package main

import (
	"slices"
	"testing"
)

// A redock moves the dragged tab's sessions out of the source window's
// set on accept, so a source closed mid-rebuild cannot end them; done
// leaves them with the main window, failed puts them back.
func TestTabDragOwnership(t *testing.T) {
	a := &App{detachedSessions: map[string][]string{"detached-k": {"s1", "s2", "s3"}}}

	a.WindowStartTabDrag("k", "s1,s2", "", "d1")
	if p, err := a.WindowAcceptTabDrag(); err != nil || p.DragID != "d1" {
		t.Fatalf("accept: %v %+v", err, p)
	}
	if got := a.detachedSessions["detached-k"]; !slices.Equal(got, []string{"s3"}) {
		t.Fatalf("after accept the source still owns %v", got)
	}
	a.WindowTabDragFailed("d1")
	if got := a.detachedSessions["detached-k"]; !slices.Equal(got, []string{"s3", "s1", "s2"}) {
		t.Fatalf("failed rebuild did not hand back the sessions: %v", got)
	}
	if len(a.tabDragsInFlight) != 0 {
		t.Fatalf("in-flight record left behind: %v", a.tabDragsInFlight)
	}

	a.WindowStartTabDrag("k", "s1", "", "d2")
	if _, err := a.WindowAcceptTabDrag(); err != nil {
		t.Fatal(err)
	}
	a.WindowTabDragDone("d2")
	if got := a.detachedSessions["detached-k"]; slices.Contains(got, "s1") {
		t.Fatalf("done: the source still owns s1: %v", got)
	}
	a.WindowTabDragFailed("d2") // late or repeated: no effect
	if got := a.detachedSessions["detached-k"]; slices.Contains(got, "s1") {
		t.Fatalf("failed after done handed s1 back: %v", got)
	}
}

// A deferred cancel clears only its own drag and says so.
func TestTabDragCancelOwnGestureOnly(t *testing.T) {
	a := &App{}
	a.WindowStartTabDrag("k", "s1", "", "d1")
	a.WindowStartTabDrag("k", "s1", "", "d2") // next drag of the same tab
	if a.WindowCancelTabDrag("d1") {
		t.Fatal("cancel of d1 cleared the newer drag d2")
	}
	if !a.WindowCancelTabDrag("d2") {
		t.Fatal("cancel of d2 did not clear it")
	}
	if _, err := a.WindowAcceptTabDrag(); err == nil {
		t.Fatal("a cancelled drag was still pending")
	}
}
