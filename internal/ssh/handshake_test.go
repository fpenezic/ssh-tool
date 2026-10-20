package ssh

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// silentServer accepts TCP and never speaks SSH.
func silentServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().String()
}

func TestClientHandshakeTimeoutAndCancel(t *testing.T) {
	addr := silentServer(t)
	cfg := &ssh.ClientConfig{User: "u", HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	run := func(ctx context.Context, timeout time.Duration) (error, time.Duration) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, _, _, err = clientHandshake(ctx, conn, addr, cfg, timeout)
		return err, time.Since(start)
	}
	if err, d := run(context.Background(), 300*time.Millisecond); err == nil || d > 3*time.Second {
		t.Fatalf("stalled server: err %v after %s", err, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if err, d := run(ctx, time.Minute); err != context.Canceled || d > 3*time.Second {
		t.Fatalf("cancel: err %v after %s", err, d)
	}
}
