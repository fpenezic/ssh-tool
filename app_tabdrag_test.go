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

// While a transfer is being rebuilt, a second drag of the same tab (same
// sessions) is refused; another tab of the window still moves.
func TestTabDragRefusesSecondTransferOfSameTab(t *testing.T) {
	a := &App{detachedSessions: map[string][]string{"detached-k": {"s1", "s2"}}}
	a.WindowStartTabDrag("k", "s1", "", "d1")
	if _, err := a.WindowAcceptTabDrag(); err != nil {
		t.Fatal(err)
	}
	a.WindowStartTabDrag("k", "s1", "", "d2")
	if _, err := a.WindowAcceptTabDrag(); err == nil {
		t.Fatal("second transfer of s1 accepted while the first is in flight")
	}
	a.WindowStartTabDrag("k", "s2", "", "d3")
	if _, err := a.WindowAcceptTabDrag(); err != nil {
		t.Fatalf("another tab refused: %v", err)
	}
}

// A tab dragged from the main window onto an open detached window: the
// detached window claims it, its own drag stays its own, and on done the
// sessions join the receiver's set (so closing that window ends them).
func TestTabDragMainToDetached(t *testing.T) {
	a := &App{detachedSessions: map[string][]string{"detached-x": {"s9"}}}

	a.WindowStartTabDragFrom("main", "t1", "s1,s2", "", "m1")
	if _, err := a.WindowAcceptTabDragAs("main"); err == nil {
		t.Fatal("the main window claimed its own drag")
	}
	p, err := a.WindowAcceptTabDragAs("detached-x")
	if err != nil || p.DragID != "m1" {
		t.Fatalf("accept into the detached window: %v %+v", err, p)
	}
	a.WindowTabDragDone("m1")
	if got := a.detachedSessions["detached-x"]; !slices.Equal(got, []string{"s9", "s1", "s2"}) {
		t.Fatalf("receiver's sessions: %v", got)
	}

	// A failed rebuild leaves them with the main window (no set to return to).
	a.WindowStartTabDragFrom("main", "t2", "s5", "", "m2")
	if _, err := a.WindowAcceptTabDragAs("detached-x"); err != nil {
		t.Fatal(err)
	}
	a.WindowTabDragFailed("m2")
	if got := a.detachedSessions["detached-x"]; slices.Contains(got, "s5") {
		t.Fatalf("failed rebuild gave s5 to the receiver: %v", got)
	}
	if _, ok := a.detachedSessions["main"]; ok {
		t.Fatal("the main window got a detached set")
	}
}
