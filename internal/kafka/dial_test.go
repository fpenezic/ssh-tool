package kafka

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshServer serves direct-tcpip and resolves names from its own table, the
// way a bastion resolves broker names this machine cannot.
type sshServer struct {
	addr  string
	hosts map[string]string // name -> address to dial

	mu    sync.Mutex
	conns []net.Conn
	dials []string
}

func (s *sshServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func (s *sshServer) dialed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dials...)
}

func startSSHServer(t *testing.T, hosts map[string]string) *sshServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &sshServer{addr: ln.Addr().String(), hosts: hosts}
	t.Cleanup(srv.dropAll)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			srv.mu.Lock()
			srv.conns = append(srv.conns, c)
			srv.mu.Unlock()
			go srv.serve(c, cfg)
		}
	}()
	return srv
}

func (s *sshServer) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			_ = nc.Reject(ssh.Prohibited, "no "+nc.ChannelType())
			continue
		}
		extra := nc.ExtraData()
		hl := binary.BigEndian.Uint32(extra)
		host := string(extra[4 : 4+hl])
		port := binary.BigEndian.Uint32(extra[4+hl:])
		target := net.JoinHostPort(host, strconv.Itoa(int(port)))
		if mapped, ok := s.hosts[host]; ok {
			target = net.JoinHostPort(mapped, strconv.Itoa(int(port)))
		}
		s.mu.Lock()
		s.dials = append(s.dials, net.JoinHostPort(host, strconv.Itoa(int(port))))
		s.mu.Unlock()
		up, err := net.Dial("tcp", target)
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
}

// lazyTo returns a LazySSH dialing srv, counting connects.
func lazyTo(srv *sshServer, connects *atomic.Int32) *LazySSH {
	return &LazySSH{Connect: func(ctx context.Context) (*ssh.Client, func(), error) {
		connects.Add(1)
		var d net.Dialer
		raw, err := d.DialContext(ctx, "tcp", srv.addr)
		if err != nil {
			return nil, nil, err
		}
		cc, chans, reqs, err := ssh.NewClientConn(raw, srv.addr, &ssh.ClientConfig{
			User:            "kafka",
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			_ = raw.Close()
			return nil, nil, err
		}
		c := ssh.NewClient(cc, chans, reqs)
		return c, func() { _ = c.Close() }, nil
	}}
}

// The seed broker name only resolves on the SSH server: the cluster must be
// reachable through the tunnel anyway, and every broker connection must ride
// the one session.
func TestClusterThroughSSH(t *testing.T) {
	fc := newFake(t, 2)
	_, port, _ := net.SplitHostPort(fc.ListenAddrs()[0])
	srv := startSSHServer(t, map[string]string{"kafka.internal": "127.0.0.1"})
	var connects atomic.Int32
	lazy := lazyTo(srv, &connects)
	defer lazy.Close()

	c, err := Open(Config{Bootstrap: []string{"kafka.internal:" + port}, Dial: lazy.Dial})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := ctx10(t)
	topics, err := c.Topics(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 || topics[1].Messages != 6 {
		t.Fatalf("topics = %+v", topics)
	}
	k, err := c.Consume(ctx, ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	if msgs, _ := drain(t, k); len(msgs) != 6 {
		t.Fatalf("consumed %d through ssh, want 6", len(msgs))
	}
	if n := connects.Load(); n != 1 {
		t.Fatalf("%d ssh connects, want 1", n)
	}
	if st, _ := lazy.State(); st != TransportConnected {
		t.Fatalf("state %s", st)
	}
	seed := false
	for _, d := range srv.dialed() {
		if d == "kafka.internal:"+port {
			seed = true
		}
	}
	if !seed {
		t.Fatalf("seed not dialed through ssh: %v", srv.dialed())
	}
}

// A dropped session heals on the next dial.
func TestLazySSHReconnects(t *testing.T) {
	fc := newFake(t, 1)
	srv := startSSHServer(t, nil)
	var connects atomic.Int32
	lazy := lazyTo(srv, &connects)
	defer lazy.Close()

	conn, err := lazy.Dial(ctx10(t), "tcp", fc.ListenAddrs()[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	srv.dropAll()
	// The client notices the drop asynchronously; the dial after it either
	// fails over the dead client (and retries) or finds it already gone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err = lazy.Dial(ctx10(t), "tcp", fc.ListenAddrs()[0])
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
	srv := startSSHServer(t, nil)
	var connects atomic.Int32
	lazy := lazyTo(srv, &connects)
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
	lazy := &LazySSH{Connect: func(ctx context.Context) (*ssh.Client, func(), error) {
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
	if st, _ := lazy.State(); st != TransportConnecting {
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
	if st, msg := lazy.State(); st != TransportError || msg != "auth failed" {
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
	lazy := &LazySSH{Connect: func(ctx context.Context) (*ssh.Client, func(), error) {
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
	if st, _ := lazy.State(); st != TransportConnecting {
		t.Fatalf("connect abandoned with the dial: %s", st)
	}
	lazy.Close()
	if _, err := lazy.Client(context.Background()); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("after Close: %v", err)
	}
	close(release)
}
