package main

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// A tunnel keeps retrying only what can heal on its own; a login or host
// key failure retried every minute would just repeat its prompt.
func TestTunnelRetryable(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	cases := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("target example.com: dial: %w", dial), true},
		{errors.New("target example.com: dial: dial tcp 192.0.2.1:22: connect: no route to host"), true},
		{errors.New("target example.com: ssh handshake: EOF"), true},
		{errors.New("target example.com: ssh: handshake failed: EOF"), true},
		{errors.New("dial tcp: lookup example.com: no such host"), true},
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain"), false},
		{errors.New("ssh: handshake failed: host key rejected by user"), false},
		{fmt.Errorf("resolve: %w", store.ErrNotFound), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := tunnelRetryable(c.err); got != c.want {
			t.Errorf("tunnelRetryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// A connected tunnel with a forward that is not listening is degraded -
// unless the user stopped that forward, which is not a fault.
func TestTunnelsListDegradedAndHandStopped(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{db: db, pool: sshlayer.NewPool(), forwards: sshlayer.NewForwardPool(), tunnels: map[string]*tunnelEntry{}}

	on := true
	f, err := db.CreateFolder(store.NewFolder{Name: "tunnels", Settings: store.InheritableSettings{TunnelOnly: &on}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CreateConnection(store.NewConnection{FolderID: &f.ID, Name: "t1", Hostname: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	addr, port, rhost := "127.0.0.1", uint16(19001), "db.example.com"
	var ids []string
	for _, auto := range []bool{true, false} {
		fw, err := db.CreatePortForward(store.NewPortForward{ConnectionID: c.ID, Kind: "local", LocalAddr: &addr, LocalPort: &port, RemoteHost: &rhost, RemotePort: &port, AutoStart: auto})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, fw.ID)
		port++
	}

	list := func() TunnelInfo {
		t.Helper()
		l, err := a.TunnelsList()
		if err != nil || len(l) != 1 {
			t.Fatalf("TunnelsList = %v, %v", l, err)
		}
		return l[0]
	}
	if got := list(); got.State != tunnelStopped || got.Degraded {
		t.Fatalf("stopped tunnel: state %q degraded %v", got.State, got.Degraded)
	}

	// Connected, nothing listening (no real session: the pool is empty).
	a.tunnels[c.ID] = &tunnelEntry{state: tunnelConnected, sessionID: "s1", stop: make(chan struct{})}
	if got := list(); !got.Degraded {
		t.Fatal("connected tunnel with no forward listening should be degraded")
	}
	a.tunnelForwardHandStopped(c.ID, ids[0], true)
	if got := list(); !got.Degraded {
		t.Fatal("one forward still down: should stay degraded")
	}
	a.tunnelForwardHandStopped(c.ID, ids[1], true)
	got := list()
	if got.Degraded {
		t.Fatal("every forward stopped by hand: not degraded")
	}
	for _, fw := range got.Forwards {
		if !fw.HandStopped {
			t.Fatalf("forward %s not marked hand-stopped", fw.ID)
		}
	}
	// Starting one again by hand clears the mark; when that start fails,
	// the forward is a fault again and says why.
	a.tunnelForwardHandStopped(c.ID, ids[1], false)
	a.tunnelForwardFailed(c.ID, ids[1], errors.New("local port 19002 is in use"))
	got = list()
	if !got.Degraded {
		t.Fatal("a forward started again but not listening: degraded")
	}
	for _, fw := range got.Forwards {
		if fw.ID == ids[1] && (fw.HandStopped || fw.Error == "") {
			t.Fatalf("failed restart: hand_stopped %v error %q", fw.HandStopped, fw.Error)
		}
	}
}
