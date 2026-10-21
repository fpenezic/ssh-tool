package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/wg"
)

// An auto-mode profile reports each step of the first-hop dial, so the
// Connect button can say it is checking the direct route or bringing the
// VPN up instead of only "Connecting". (A tunnel already up skips the
// probe; that needs a running plugin and is checked on the rig.)
func TestAutoProfileReportsDialSteps(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	row, err := db.CreateNetworkProfile("corp", `{"kind":"openconnect","mode":"auto"}`)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, wgman: wg.NewManager(), nbman: tunnelhelper.NewManager(func(string) {})}

	var stages []string
	ctx := sshlayer.WithProgress(context.Background(), func(s string) { stages = append(stages, s) })
	dial, err := a.wgDialerForCtx(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Reachable directly: only the probe step.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := dial(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(stages) != 1 || !strings.HasPrefix(stages[0], "Checking if ") {
		t.Fatalf("direct stages: %q", stages)
	}

	// Not reachable: the probe, then the tunnel start (which fails here,
	// the plugin is not installed).
	stages = nil
	ln.Close()
	if _, err := dial(ctx, "tcp", ln.Addr().String()); err == nil {
		t.Fatal("dial to a closed port succeeded")
	}
	if len(stages) != 2 || stages[1] != "Not reachable directly - starting VPN corp" {
		t.Fatalf("tunnel stages: %q", stages)
	}
}

// A stalled key exchange names the MTU setting that applies: the network
// profile's when the first hop went through it, the jump host's network
// when it stalled behind one.
func TestMTUAdvice(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row, err := db.CreateNetworkProfile("corp", `{"mode":"always"}`)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}
	s := &store.ResolvedSettings{NetworkProfileID: &row.ID}
	advice := func(se *sshlayer.HandshakeStallError) string {
		return a.withMTUAdvice(fmt.Errorf("target h: ssh handshake: %w", se), s).Error()
	}
	if got := advice(&sshlayer.HandshakeStallError{Received: 900, Via: "tunnel", FirstHop: true}); !strings.Contains(got, `MTU of network profile "corp"`) {
		t.Fatalf("wireguard advice: %s", got)
	}
	if got := advice(&sshlayer.HandshakeStallError{Received: 900, Via: "tunnel"}); !strings.Contains(got, "behind the jump host") {
		t.Fatalf("jump advice: %s", got)
	}
	if got := advice(&sshlayer.HandshakeStallError{Received: 900, Via: "direct", FirstHop: true}); !strings.Contains(got, "over a VPN") {
		t.Fatalf("direct advice: %s", got)
	}
	plain := errors.New("connection refused")
	if a.withMTUAdvice(plain, s) != plain {
		t.Fatal("advice added to an unrelated error")
	}
}
