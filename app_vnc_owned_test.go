package main

import "testing"

// What a console's lazy open creates belongs to that console: not to
// another console of the same connection, and a reconnect closes what the
// previous run opened.
func TestVncOwnedPerConsole(t *testing.T) {
	a := &App{vncSessions: map[string]*vncSessionMeta{
		"vnc:a": {connectionID: "c1"},
		"vnc:b": {connectionID: "c1"},
	}}
	closed := map[string]int{}
	cleanup := func(name string) func() { return func() { closed[name]++ } }

	a.setVncOwned("vnc:b", nil, cleanup("b1"))
	if a.vncSessions["vnc:a"].ownedCleanup != nil {
		t.Fatal("console b's chain was attached to console a")
	}
	a.setVncOwned("vnc:b", nil, cleanup("b2")) // reconnect of b
	if closed["b1"] != 1 {
		t.Fatal("the previous run's chain was not closed")
	}
	a.VncClose("vnc:b")
	if closed["b2"] != 1 || closed["b1"] != 1 {
		t.Fatalf("close: %v", closed)
	}
	a.setVncOwned("vnc:gone", nil, cleanup("late"))
	if closed["late"] != 1 {
		t.Fatal("a chain opened for a closed console leaked")
	}
}
