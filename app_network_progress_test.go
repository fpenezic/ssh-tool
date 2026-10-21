package main

import (
	"context"
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
// VPN up instead of only "Connecting".
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

