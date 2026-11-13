package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"ssh-tool/internal/tunnelhelper"
)

// A login that did not finish holds background starts of the profile; any other
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

// The background mark survives the contexts the dial path derives from it
// (cancel, timeout, values), and a plain context carries none.
func TestBackgroundStartMark(t *testing.T) {
	if isBackgroundStart(context.Background()) {
		t.Fatal("a plain context is not a background start")
	}
	ctx, cancel := context.WithTimeout(withBackgroundStart(context.Background()), time.Minute)
	defer cancel()
	ctx, cancel2 := context.WithCancel(context.WithValue(ctx, struct{}{}, 1))
	defer cancel2()
	if !isBackgroundStart(ctx) {
		t.Fatal("the mark was lost in a derived context")
	}
}

// A tunnel retry that meets a held login keeps retrying (each try fails at
// once, no prompt), wrapped the way the SSH layer wraps a first-hop error.
func TestTunnelRetryableLoginHeld(t *testing.T) {
	err := fmt.Errorf("target h: dial: %w", fmt.Errorf("network profile: %w", errLoginHeld))
	if !tunnelRetryable(err) {
		t.Fatal("a held login must stay retryable")
	}
}
