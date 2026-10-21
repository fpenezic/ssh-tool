package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"strings"
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

// noDeadlineConn behaves like a jump hop's SSH channel: no deadlines.
type noDeadlineConn struct{ net.Conn }

func (noDeadlineConn) SetDeadline(time.Time) error      { return errors.New("deadline not supported") }
func (noDeadlineConn) SetReadDeadline(time.Time) error  { return errors.New("deadline not supported") }
func (noDeadlineConn) SetWriteDeadline(time.Time) error { return errors.New("deadline not supported") }

// stallServer sends its version line and then nothing: what a key exchange
// looks like when the large packets are lost on the path (MTU).
func stallServer(t *testing.T) string {
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
			_, _ = c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
			go func() { _, _ = io.Copy(io.Discard, c) }()
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().String()
}

func TestClientHandshakeStallLooksLikeMTU(t *testing.T) {
	cfg := &ssh.ClientConfig{User: "u", HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	dial := func(addr string, wrap bool) net.Conn {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if wrap {
			return noDeadlineConn{conn}
		}
		return conn
	}

	// The server answered, then went quiet: a stall, also on a conn that
	// has no deadlines (a jump hop's channel).
	addr := stallServer(t)
	for _, wrap := range []bool{false, true} {
		start := time.Now()
		_, _, _, err := clientHandshake(context.Background(), dial(addr, wrap), addr, cfg, 300*time.Millisecond)
		var se *HandshakeStallError
		if !errors.As(err, &se) || se.Received == 0 || time.Since(start) > 3*time.Second {
			t.Fatalf("wrap=%v: err %v after %s, want a stall", wrap, err, time.Since(start))
		}
		if !strings.Contains(err.Error(), "MTU") {
			t.Fatalf("stall error does not name MTU: %v", err)
		}
	}

	// The server never spoke: a plain timeout, not an MTU suspicion.
	silent := silentServer(t)
	_, _, _, err := clientHandshake(context.Background(), dial(silent, true), silent, cfg, 300*time.Millisecond)
	var se *HandshakeStallError
	if errors.As(err, &se) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("silent server: err %v, want a timeout that is no stall", err)
	}
}

// Slow authentication after the host key (an AuthorizedKeysCommand on a
// slow resolver) gets the afterKey budget, not the connect timeout.
func TestBoundedHandshakeSlowAuth(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srvCfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		time.Sleep(600 * time.Millisecond)
		return nil, nil
	}}
	srvCfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(c, srvCfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				go func() {
					for ch := range chans {
						_ = ch.Reject(ssh.Prohibited, "")
					}
				}()
				_ = sc.Wait()
			}()
		}
	}()
	cfg := &ssh.ClientConfig{User: "u", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	run := func(afterKey time.Duration) error {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		sc, _, _, err := boundedHandshake(context.Background(), conn, ln.Addr().String(), cfg, 300*time.Millisecond, afterKey)
		if err == nil {
			sc.Close()
		}
		return err
	}
	if err := run(5 * time.Second); err != nil {
		t.Fatalf("slow auth within the budget: %v", err)
	}
	if err := run(0); err != nil {
		t.Fatalf("slow auth, interactive (no budget): %v", err)
	}
	if err := run(200 * time.Millisecond); err == nil || !strings.Contains(err.Error(), "authentication took longer") {
		t.Fatalf("auth over the budget: %v", err)
	}
}
