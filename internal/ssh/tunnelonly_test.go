package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/store"
)

type nopSink struct{}

func (nopSink) EmitState(string, SessionState)    {}
func (nopSink) EmitOutput(string, []byte, uint64) {}
func (nopSink) EmitExitStatus(string, uint32)     {}
func (nopSink) EmitDebug(string, string)          {}

// tunnelTestServer is a password-only SSH server that records the channel
// types it is asked for and serves direct-tcpip by dialing the target.
type tunnelTestServer struct {
	addr string

	mu       sync.Mutex
	channels []string
	conns    []net.Conn
}

func (s *tunnelTestServer) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.channels...)
}

// dropAll closes every accepted connection, as a server restart or a
// network drop would.
func (s *tunnelTestServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

func startTunnelTestServer(t *testing.T) *tunnelTestServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == "secret" {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &tunnelTestServer{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			srv.mu.Lock()
			srv.conns = append(srv.conns, c)
			srv.mu.Unlock()
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go func() {
					for r := range reqs {
						if r.WantReply {
							_ = r.Reply(true, nil)
						}
					}
				}()
				for nc := range chans {
					srv.mu.Lock()
					srv.channels = append(srv.channels, nc.ChannelType())
					srv.mu.Unlock()
					if nc.ChannelType() != "direct-tcpip" {
						_ = nc.Reject(ssh.Prohibited, "test server: no "+nc.ChannelType())
						continue
					}
					// RFC 4254 7.2: string host, uint32 port, string origin, uint32 port.
					extra := nc.ExtraData()
					hl := binary.BigEndian.Uint32(extra)
					host := string(extra[4 : 4+hl])
					port := binary.BigEndian.Uint32(extra[4+hl:])
					up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
					if err != nil {
						_ = nc.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						_ = up.Close()
						continue
					}
					go ssh.DiscardRequests(creqs)
					go func() { _, _ = io.Copy(ch, up); _ = ch.CloseWrite() }()
					go func() { _, _ = io.Copy(up, ch); _ = up.Close() }()
				}
			}()
		}
	}()
	return srv
}

func startEcho(t *testing.T) string {
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

func connectTunnelOnly(t *testing.T, addr string) *Session {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	user, pw := "tunnel", "secret"
	settings := &store.ResolvedSettings{
		Hostname:         host,
		Port:             uint16(port),
		Username:         &user,
		PasswordOverride: &pw,
		TerminalType:     "xterm-256color",
		TunnelOnly:       true,
	}
	s, err := Connect(context.Background(), nil, nil, settings, nopSink{},
		ssh.InsecureIgnoreHostKey(), nil, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return s
}

// A tunnel-only session must not ask for a shell (a forward-only account
// with a nologin shell would refuse it) yet carry forwards like any other.
func TestTunnelOnlySessionOpensNoShellAndForwards(t *testing.T) {
	srv := startTunnelTestServer(t)
	echo := startEcho(t)
	s := connectTunnelOnly(t, srv.addr)
	defer s.Disconnect()

	if !s.TunnelOnly() {
		t.Fatal("session not marked tunnel-only")
	}
	if err := s.Write([]byte("ls\n")); err == nil {
		t.Fatal("Write on a tunnel-only session should fail")
	}

	c, err := s.TargetClient().Dial("tcp", echo)
	if err != nil {
		t.Fatalf("dial through tunnel: %v", err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo through tunnel: %q %v", buf, err)
	}
	_ = c.Close()

	for _, k := range srv.kinds() {
		if k == "session" {
			t.Fatalf("tunnel-only session opened a %q channel; channels: %v", k, srv.kinds())
		}
	}
}

// The close hook fires once on Disconnect, marked user-initiated.
func TestTunnelOnlyDisconnectRunsOnCloseOnce(t *testing.T) {
	srv := startTunnelTestServer(t)
	s := connectTunnelOnly(t, srv.addr)
	calls := make(chan string, 4)
	s.SetOnClose(func(id string) { calls <- id })
	s.Disconnect()
	select {
	case id := <-calls:
		if id != s.ID {
			t.Fatalf("onClose got %q, want %q", id, s.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onClose not called after Disconnect")
	}
	if !s.WasUserInitiated() {
		t.Fatal("Disconnect not recorded as user-initiated")
	}
	select {
	case <-calls:
		t.Fatal("onClose called twice")
	case <-time.After(300 * time.Millisecond):
	}
}

// A drop on the server side ends the session through the wait goroutine,
// not marked user-initiated - the tunnel manager reconnects on exactly that.
func TestTunnelOnlyServerDropRunsOnClose(t *testing.T) {
	srv := startTunnelTestServer(t)
	s := connectTunnelOnly(t, srv.addr)
	calls := make(chan string, 2)
	s.SetOnClose(func(id string) { calls <- id })
	srv.dropAll()
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("onClose not called after the server dropped the connection")
	}
	if s.WasUserInitiated() {
		t.Fatal("server drop recorded as user-initiated")
	}
}
