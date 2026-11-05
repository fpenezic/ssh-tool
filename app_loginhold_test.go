package main

import (
	"errors"
	"fmt"
	"testing"

	"ssh-tool/internal/tunnelhelper"
)

// A login that did not finish holds proxy starts of the profile; any other
// failure (gateway down, bad certificate) does not, and the tunnel coming
// up lifts the hold.
func TestLoginHold(t *testing.T) {
	a := &App{}
	const id = "p-hold"
	t.Cleanup(func() { clearLoginHold(id) })

	a.holdAfterLogin(id, "lab", fmt.Errorf("profile lab: %w", errors.New("gateway does not answer")))
	if a.loginHeld(id, "lab") {
		t.Fatal("held after a failure that was not a login")
	}
	a.holdAfterLogin(id, "lab", fmt.Errorf("profile lab: %w", &tunnelhelper.LoginError{Err: errors.New("login cancelled")}))
	if !a.loginHeld(id, "lab") || !a.loginHeld(id, "lab") {
		t.Fatal("not held after an unfinished login")
	}
	clearLoginHold(id)
	if a.loginHeld(id, "lab") {
		t.Fatal("still held after the tunnel came up")
	}
}
