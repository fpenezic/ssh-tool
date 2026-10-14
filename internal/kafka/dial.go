package kafka

import (
	"context"
	"errors"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Transport states of a LazySSH, as the Kafka tab shows them.
const (
	TransportIdle       = "idle"
	TransportConnecting = "connecting"
	TransportConnected  = "connected"
	TransportError      = "error"
	TransportClosed     = "closed"
)

// ErrTransportClosed is returned by a LazySSH after Close.
var ErrTransportClosed = errors.New("kafka transport closed")

// LazySSH holds the one SSH session a cluster's broker connections ride on.
// It connects on first use, shares that connect between concurrent dials,
// and connects again after the session dies (Invalidate), so a dropped
// tunnel heals on the next broker dial instead of needing a reopened tab.
type LazySSH struct {
	// Connect opens the session and returns the client to open channels on
	// plus a func that closes the session. It runs on a context Close
	// cancels, not on a dial's context: one dial giving up must not abort a
	// handshake other dials are waiting for.
	Connect func(ctx context.Context) (*ssh.Client, func(), error)

	mu       sync.Mutex
	client   *ssh.Client
	closeFn  func()
	inflight chan struct{} // closed when the running connect finishes
	lastErr  error
	closed   bool
	ctx      context.Context
	cancel   context.CancelFunc
}

// Client returns the live client, connecting first if there is none.
func (l *LazySSH) Client(ctx context.Context) (*ssh.Client, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, ErrTransportClosed
		}
		if l.client != nil {
			c := l.client
			l.mu.Unlock()
			return c, nil
		}
		wait := l.inflight
		if wait == nil {
			if l.ctx == nil {
				l.ctx, l.cancel = context.WithCancel(context.Background())
			}
			wait = make(chan struct{})
			l.inflight = wait
			go l.connect(l.ctx, wait)
		}
		l.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		l.mu.Lock()
		c, err := l.client, l.lastErr
		l.mu.Unlock()
		if c != nil {
			return c, nil
		}
		if err != nil {
			return nil, err
		}
		// Invalidated between the connect and this read: go round again.
	}
}

func (l *LazySSH) connect(ctx context.Context, done chan struct{}) {
	c, closeFn, err := l.Connect(ctx)
	l.mu.Lock()
	l.inflight = nil
	switch {
	case err != nil:
		l.lastErr = err
	case l.closed:
		closeFn()
	default:
		l.client, l.closeFn, l.lastErr = c, closeFn, nil
	}
	l.mu.Unlock()
	close(done)
}

// Invalidate drops c if it is the current client (its session died), so the
// next dial connects again. A stale c is ignored.
func (l *LazySSH) Invalidate(c *ssh.Client) {
	l.mu.Lock()
	if c == nil || l.client != c {
		l.mu.Unlock()
		return
	}
	closeFn := l.closeFn
	l.client, l.closeFn = nil, nil
	l.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
}

// State reports the transport state and the last connect error.
func (l *LazySSH) State() (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return TransportClosed, ""
	case l.client != nil:
		return TransportConnected, ""
	case l.inflight != nil:
		return TransportConnecting, ""
	case l.lastErr != nil:
		return TransportError, l.lastErr.Error()
	}
	return TransportIdle, ""
}

// Close tears the session down and cancels a connect in flight.
func (l *LazySSH) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	closeFn := l.closeFn
	l.client, l.closeFn = nil, nil
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if closeFn != nil {
		closeFn()
	}
}

// Dial opens a broker connection as a direct-tcpip channel. The address is
// resolved by the SSH server, so advertised broker names that only exist on
// the far side work. A refused channel (nothing listening, name unknown
// there) is the broker's answer and returned as is; any other failure means
// the session is gone, so it is dropped and the dial tried once more on a
// fresh one.
func (l *LazySSH) Dial(ctx context.Context, _, addr string) (net.Conn, error) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var c *ssh.Client
		if c, err = l.Client(ctx); err != nil {
			return nil, err
		}
		var conn net.Conn
		if conn, err = c.DialContext(ctx, "tcp", addr); err == nil {
			return conn, nil
		}
		var refused *ssh.OpenChannelError
		if errors.As(err, &refused) || ctx.Err() != nil {
			return nil, err
		}
		l.Invalidate(c)
	}
	return nil, err
}
