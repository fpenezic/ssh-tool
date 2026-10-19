package sshchan_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/sshchan"
	"ssh-tool/internal/sshchan/sshchantest"
)

func ctx10(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// echoServer is a TCP target behind the SSH server.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// A dropped session heals on the next dial.
func TestLazySSHReconnects(t *testing.T) {
	target := echoServer(t)
	srv := sshchantest.Start(t, nil)
	var connects atomic.Int32
	lazy := srv.Lazy(&connects)
	defer lazy.Close()

	conn, err := lazy.Dial(ctx10(t), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	srv.DropAll()
	// The client notices the drop asynchronously; the dial after it either
	// fails over the dead client (and retries) or finds it already gone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err = lazy.Dial(ctx10(t), "tcp", target)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial after drop: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := connects.Load(); n != 2 {
		t.Fatalf("%d connects, want 2", n)
	}
}

// A refused channel is the far side's answer, not a dead session: no
// reconnect.
func TestLazySSHRefusedNoReconnect(t *testing.T) {
	srv := sshchantest.Start(t, nil)
	var connects atomic.Int32
	lazy := srv.Lazy(&connects)
	defer lazy.Close()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	_ = ln.Close()
	_, err := lazy.Dial(ctx10(t), "tcp", closed)
	var oce *ssh.OpenChannelError
	if !errors.As(err, &oce) {
		t.Fatalf("err = %v, want an OpenChannelError", err)
	}
	if n := connects.Load(); n != 1 {
		t.Fatalf("%d connects, want 1", n)
	}
}

// Concurrent first dials share one connect; a failed connect is reported to
// all of them and the next dial tries again.
func TestLazySSHSharedConnect(t *testing.T) {
	var connects atomic.Int32
	release := make(chan struct{})
	fail := true
	lazy := &sshchan.LazySSH{Connect: func(ctx context.Context) (*ssh.Client, func(), error) {
		connects.Add(1)
		<-release
		if fail {
			return nil, nil, errors.New("auth failed")
		}
		return nil, nil, errors.New("unreachable")
	}}
	defer lazy.Close()

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = lazy.Client(ctx10(t))
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if st, _ := lazy.State(); st != sshchan.TransportConnecting {
		t.Fatalf("state %s while connecting", st)
	}
	close(release)
	wg.Wait()
	for i, err := range errs {
		if err == nil || err.Error() != "auth failed" {
			t.Fatalf("dial %d: %v", i, err)
		}
	}
	if n := connects.Load(); n != 1 {
		t.Fatalf("%d connects, want 1", n)
	}
	if st, msg := lazy.State(); st != sshchan.TransportError || msg != "auth failed" {
		t.Fatalf("state %s %q", st, msg)
	}
	fail = false
	if _, err := lazy.Client(ctx10(t)); err == nil || err.Error() != "unreachable" {
		t.Fatalf("second try: %v", err)
	}
	if n := connects.Load(); n != 2 {
		t.Fatalf("%d connects, want 2", n)
	}
}

// A dial whose context ends while the connect is running gives up alone; the
// connect carries on for the others.
func TestLazySSHDialContextAndClose(t *testing.T) {
	release := make(chan struct{})
	lazy := &sshchan.LazySSH{Connect: func(ctx context.Context) (*ssh.Client, func(), error) {
		select {
		case <-release:
			return nil, nil, errors.New("late")
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := lazy.Client(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := lazy.State(); st != sshchan.TransportConnecting {
		t.Fatalf("connect abandoned with the dial: %s", st)
	}
	lazy.Close()
	if _, err := lazy.Client(context.Background()); !errors.Is(err, sshchan.ErrTransportClosed) {
		t.Fatalf("after Close: %v", err)
	}
	close(release)
}
