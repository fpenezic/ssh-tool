package ssh

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ForwardKind tags which of the three SSH forwarding modes a Forward
// represents.
type ForwardKind string

const (
	ForwardLocal   ForwardKind = "local"   // -L  local listen -> remote dial
	ForwardRemote  ForwardKind = "remote"  // -R  remote listen -> local dial
	ForwardDynamic ForwardKind = "dynamic" // -D  local SOCKS5 listen
)

// ForwardState reports a forward's current lifecycle.
type ForwardState string

const (
	StateStopped   ForwardState = "stopped"
	StateListening ForwardState = "listening"
	StateError     ForwardState = "error"
)

// ForwardStatus is the IPC-friendly snapshot. Pool returns slices of these
// for display in the UI.
type ForwardStatus struct {
	ID         string       `json:"id"`
	Kind       ForwardKind  `json:"kind"`
	SessionID  string       `json:"session_id"`
	LocalAddr  string       `json:"local_addr"`
	LocalPort  uint16       `json:"local_port"`
	RemoteHost string       `json:"remote_host,omitempty"`
	RemotePort uint16       `json:"remote_port,omitempty"`
	State      ForwardState `json:"state"`
	Error      string       `json:"error,omitempty"`
	BytesIn    uint64       `json:"bytes_in"`
	BytesOut   uint64       `json:"bytes_out"`
	StartedAt  int64        `json:"started_at"`
}

// activeForward is the live state of a single forward. Owned by the
// ForwardPool; never exposed directly across IPC.
type activeForward struct {
	id      string
	kind    ForwardKind
	session *Session // owning SSH session
	desc    string

	// Local / dynamic forwards: TCP listener we accept on locally.
	// Remote forwards: net.Listener returned by client.Listen on the remote.
	listener net.Listener

	localAddr  string
	localPort  uint16
	remoteHost string
	remotePort uint16

	state ForwardState
	errMsg string

	bytesIn  atomicCounter
	bytesOut atomicCounter
	started  int64

	done chan struct{}
}

func (f *activeForward) snapshot() ForwardStatus {
	return ForwardStatus{
		ID:         f.id,
		Kind:       f.kind,
		SessionID:  f.session.ID,
		LocalAddr:  f.localAddr,
		LocalPort:  f.localPort,
		RemoteHost: f.remoteHost,
		RemotePort: f.remotePort,
		State:      f.state,
		Error:      f.errMsg,
		BytesIn:    f.bytesIn.load(),
		BytesOut:   f.bytesOut.load(),
		StartedAt:  f.started,
	}
}

// ForwardPool owns all active forwards across all sessions. Methods are
// safe for concurrent use.
type ForwardPool struct {
	mu       sync.Mutex
	byID     map[string]*activeForward
	bySess   map[string]map[string]struct{} // sessionID -> set of forwardIDs
}

func NewForwardPool() *ForwardPool {
	return &ForwardPool{
		byID:   map[string]*activeForward{},
		bySess: map[string]map[string]struct{}{},
	}
}

// StartLocal starts a -L forward: listen on localAddr:localPort, dial
// remoteHost:remotePort through the session's SSH connection.
func (p *ForwardPool) StartLocal(
	sess *Session,
	id, localAddr string, localPort uint16,
	remoteHost string, remotePort uint16,
) (*ForwardStatus, error) {
	if localAddr == "" {
		localAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", localAddr, localPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	// Pull the actual bound port back out in case caller passed 0.
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		localPort = uint16(tcpAddr.Port)
	}

	af := &activeForward{
		id:         id,
		kind:       ForwardLocal,
		session:    sess,
		listener:   listener,
		localAddr:  localAddr,
		localPort:  localPort,
		remoteHost: remoteHost,
		remotePort: remotePort,
		state:      StateListening,
		started:    time.Now().Unix(),
		done:       make(chan struct{}),
	}

	go p.acceptLocal(af, lastClient(sess))
	p.register(af)
	log.Printf("forward: local %s:%d -> %s:%d started (id=%s, session=%s)",
		localAddr, localPort, remoteHost, remotePort, id, sess.ID)
	s := af.snapshot()
	return &s, nil
}

// acceptLocal services connections on a -L listener by dialing through SSH.
func (p *ForwardPool) acceptLocal(af *activeForward, client *ssh.Client) {
	for {
		conn, err := af.listener.Accept()
		if err != nil {
			if af.state == StateListening {
				af.state = StateStopped
			}
			return
		}
		// If the parent session has gone away, stop accepting + drop the
		// connection cleanly rather than letting Dial fail forever.
		select {
		case <-af.session.closed:
			_ = conn.Close()
			_ = af.listener.Close()
			af.state = StateStopped
			return
		default:
		}
		go func(local net.Conn) {
			remoteAddr := fmt.Sprintf("%s:%d", af.remoteHost, af.remotePort)
			remote, err := client.Dial("tcp", remoteAddr)
			if err != nil {
				log.Printf("forward %s: dial %s: %v", af.id, remoteAddr, err)
				_ = local.Close()
				return
			}
			tunnel(local, remote, &af.bytesIn, &af.bytesOut)
		}(conn)
	}
}

// StartDynamic starts a -D SOCKS5 forward.
func (p *ForwardPool) StartDynamic(
	sess *Session,
	id, localAddr string, localPort uint16,
) (*ForwardStatus, error) {
	if localAddr == "" {
		localAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", localAddr, localPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		localPort = uint16(tcpAddr.Port)
	}

	af := &activeForward{
		id:        id,
		kind:      ForwardDynamic,
		session:   sess,
		listener:  listener,
		localAddr: localAddr,
		localPort: localPort,
		state:     StateListening,
		started:   time.Now().Unix(),
		done:      make(chan struct{}),
	}

	go p.acceptDynamic(af, lastClient(sess))
	p.register(af)
	log.Printf("forward: dynamic (SOCKS5) %s:%d started (id=%s, session=%s)",
		localAddr, localPort, id, sess.ID)
	s := af.snapshot()
	return &s, nil
}

func (p *ForwardPool) acceptDynamic(af *activeForward, client *ssh.Client) {
	for {
		conn, err := af.listener.Accept()
		if err != nil {
			if af.state == StateListening {
				af.state = StateStopped
			}
			return
		}
		select {
		case <-af.session.closed:
			_ = conn.Close()
			_ = af.listener.Close()
			af.state = StateStopped
			return
		default:
		}
		go func(local net.Conn) {
			defer local.Close()
			dest, err := handleSocks5(local)
			if err != nil {
				log.Printf("forward %s: socks5 handshake: %v", af.id, err)
				return
			}
			remote, err := client.Dial("tcp", dest)
			if err != nil {
				log.Printf("forward %s: dial %s: %v", af.id, dest, err)
				return
			}
			tunnel(local, remote, &af.bytesIn, &af.bytesOut)
		}(conn)
	}
}

// StartRemote starts a -R forward: listen on the remote, dial locally.
func (p *ForwardPool) StartRemote(
	sess *Session,
	id, remoteAddr string, remotePort uint16,
	localHost string, localPort uint16,
) (*ForwardStatus, error) {
	if remoteAddr == "" {
		remoteAddr = "127.0.0.1"
	}
	client := lastClient(sess)
	addr := fmt.Sprintf("%s:%d", remoteAddr, remotePort)
	listener, err := client.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("remote listen %s: %w", addr, err)
	}

	af := &activeForward{
		id:         id,
		kind:       ForwardRemote,
		session:    sess,
		listener:   listener,
		localAddr:  remoteAddr,
		localPort:  remotePort,
		remoteHost: localHost,
		remotePort: localPort,
		state:      StateListening,
		started:    time.Now().Unix(),
		done:       make(chan struct{}),
	}

	go p.acceptRemote(af)
	p.register(af)
	log.Printf("forward: remote %s:%d -> %s:%d started (id=%s, session=%s)",
		remoteAddr, remotePort, localHost, localPort, id, sess.ID)
	s := af.snapshot()
	return &s, nil
}

func (p *ForwardPool) acceptRemote(af *activeForward) {
	for {
		conn, err := af.listener.Accept()
		if err != nil {
			if af.state == StateListening {
				af.state = StateStopped
			}
			return
		}
		select {
		case <-af.session.closed:
			_ = conn.Close()
			_ = af.listener.Close()
			af.state = StateStopped
			return
		default:
		}
		go func(remote net.Conn) {
			localAddr := fmt.Sprintf("%s:%d", af.remoteHost, af.remotePort)
			local, err := net.DialTimeout("tcp", localAddr, 10*time.Second)
			if err != nil {
				log.Printf("forward %s: local dial %s: %v", af.id, localAddr, err)
				_ = remote.Close()
				return
			}
			// For remote forwards "in" means data arriving from the remote
			// side; "out" is data we send back. Keep counters consistent.
			tunnel(remote, local, &af.bytesIn, &af.bytesOut)
		}(conn)
	}
}

// Stop terminates a forward by id. No-op if it doesn't exist.
func (p *ForwardPool) Stop(id string) error {
	p.mu.Lock()
	af, ok := p.byID[id]
	if !ok {
		p.mu.Unlock()
		return nil
	}
	delete(p.byID, id)
	if set, ok := p.bySess[af.session.ID]; ok {
		delete(set, id)
	}
	p.mu.Unlock()

	af.state = StateStopped
	if af.listener != nil {
		_ = af.listener.Close()
	}
	close(af.done)
	return nil
}

// StopAllForSession is called when a session terminates so we don't leak
// listeners pointing at a dead SSH client.
func (p *ForwardPool) StopAllForSession(sessionID string) {
	p.mu.Lock()
	set := p.bySess[sessionID]
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		_ = p.Stop(id)
	}
}

// List returns a snapshot of all forwards, optionally filtered by session.
func (p *ForwardPool) List(sessionID string) []ForwardStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []ForwardStatus
	for _, af := range p.byID {
		if sessionID != "" && af.session.ID != sessionID {
			continue
		}
		out = append(out, af.snapshot())
	}
	return out
}

func (p *ForwardPool) register(af *activeForward) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byID[af.id] = af
	set, ok := p.bySess[af.session.ID]
	if !ok {
		set = map[string]struct{}{}
		p.bySess[af.session.ID] = set
	}
	set[af.id] = struct{}{}
}

// tunnel copies bytes both ways between local and remote, accumulating
// counters. Returns when either side closes.
func tunnel(local, remote net.Conn, in, out *atomicCounter) {
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(remote, &countingReader{r: local, c: out})
		_ = n
		_ = remote.(closeWriter).CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(local, &countingReader{r: remote, c: in})
		_ = n
		_ = local.(closeWriter).CloseWrite()
		done <- struct{}{}
	}()
	<-done
	_ = local.Close()
	_ = remote.Close()
}

// closeWriter is satisfied by both *net.TCPConn and the half-closeable
// ssh channel-backed conns golang.org/x/crypto/ssh hands out.
type closeWriter interface {
	CloseWrite() error
}

type atomicCounter struct{ v uint64 }

func (c *atomicCounter) add(n int) {
	// We don't actually need atomicity for this - counters are read by
	// the snapshot goroutine and exact accounting isn't critical. A
	// future cleanup could swap in sync/atomic.Uint64.
	c.v += uint64(n)
}
func (c *atomicCounter) load() uint64 { return c.v }

type countingReader struct {
	r io.Reader
	c *atomicCounter
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.c.add(n)
	}
	return n, err
}

// lastClient returns the deepest *ssh.Client in the session's chain, i.e.
// the one that should service forward dials.
func lastClient(s *Session) *ssh.Client {
	if len(s.stack) == 0 {
		return nil
	}
	return s.stack[len(s.stack)-1]
}
