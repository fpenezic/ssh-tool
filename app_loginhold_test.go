package main

import (
	"errors"
	"fmt"
	"testing"

	"ssh-tool/internal/tunnelhelper"
)

// A login that did not finish holds proxy starts of the profile; any other
// failure (gateway down, bad certificate) does not, a start that succeeded
// while the failed one ran keeps it from holding, and the tunnel coming up
// lifts the hold.
func TestLoginHold(t *testing.T) {
	a := &App{}
	const id = "p-hold"
	t.Cleanup(func() { tunnelStarted(id) })
	login := fmt.Errorf("profile lab: %w", &tunnelhelper.LoginError{Err: errors.New("login cancelled")})

	a.holdAfterLogin(id, "lab", fmt.Errorf("profile lab: %w", errors.New("gateway does not answer")), loginUps(id))
	if a.loginHeld(id, "lab") {
		t.Fatal("held after a failure that was not a login")
	}

	gen := loginUps(id)
	tunnelStarted(id) // a newer start (Connect) came up meanwhile
	a.holdAfterLogin(id, "lab", login, gen)
	if a.loginHeld(id, "lab") {
		t.Fatal("a stale failure held the profile over a newer start")
	}

	a.holdAfterLogin(id, "lab", login, loginUps(id))
	if !a.loginHeld(id, "lab") || !a.loginHeld(id, "lab") {
		t.Fatal("not held after an unfinished login")
	}
	tunnelStarted(id)
	if a.loginHeld(id, "lab") {
		t.Fatal("still held after the tunnel came up")
	}
}
