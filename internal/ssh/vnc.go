// VNC websocket bridge.
//
// noVNC (running in the webview) speaks RFB over a websocket. It cannot
// open a raw TCP socket, set a PVEAPIToken header, or skip TLS verify on
// a self-signed Proxmox cert. So the app runs a tiny loopback websocket
// server: the webview connects to ws://127.0.0.1:<port>/vnc/<token> and
// the bridge relays bytes to an "upstream" RFB byte stream.
//
// Three upstream flavours:
//   - generic direct:  net.Dial("tcp", host:port)            -> raw RFB
//   - generic via SSH:  client.Dial("tcp", 127.0.0.1:port)   -> raw RFB
//   - Proxmox:          wss vncwebsocket (token + TLS skip)   -> base64 RFB
//
// The token in the URL carries no secret; it's a single-use handle the
// App mints (VncOpen*), looked up here to build the upstream. Tokens
// expire if unused so a leaked URL can't be replayed.
package ssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	gossh "golang.org/x/crypto/ssh"
)

// tokenTTL bounds how long a minted token stays usable before its first
// (and only) connection. Generous enough for a slow tab open, short
// enough that a leaked ws URL is useless minutes later.
const vncTokenTTL = 2 * time.Minute

// VncUpstream is a single RFB byte stream the bridge relays to/from the
// webview. Implementations wrap a raw TCP conn or a Proxmox websocket.
type VncUpstream interface {
	io.ReadWriteCloser
}

// vncToken is a pending or in-flight upstream factory. open() is called
// once when the webview connects; consumed afterwards.
type vncToken struct {
	open    func(ctx context.Context) (VncUpstream, error)
	created time.Time
	used    bool
}

// VncBridge is the loopback websocket server. One per App, started lazily
// on the first VncBridge.Mint.
type VncBridge struct {
	mu     sync.Mutex
	srv    *http.Server
	addr   string // "127.0.0.1:port" once listening
	tokens map[string]*vncToken
}

func NewVncBridge() *VncBridge {
	return &VncBridge{tokens: map[string]*vncToken{}}
}

// ensureServer starts the loopback HTTP server if it isn't running yet.
// Caller holds b.mu.
func (b *VncBridge) ensureServer() error {
	if b.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("vnc bridge listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/vnc/", b.handle)
	b.srv = &http.Server{Handler: mux}
	b.addr = ln.Addr().String()
	go b.srv.Serve(ln)
	return nil
}

// Mint registers an upstream factory under a fresh token and returns the
// ws:// URL the webview should connect to. The bridge server is started
// on first use.
func (b *VncBridge) Mint(open func(ctx context.Context) (VncUpstream, error)) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureServer(); err != nil {
		return "", err
	}
	b.gcLocked()
	tok := randToken()
	b.tokens[tok] = &vncToken{open: open, created: time.Now()}
	return fmt.Sprintf("ws://%s/vnc/%s", b.addr, tok), nil
}

// gcLocked drops expired unused tokens. Caller holds b.mu.
func (b *VncBridge) gcLocked() {
	now := time.Now()
	for k, t := range b.tokens {
		if !t.used && now.Sub(t.created) > vncTokenTTL {
			delete(b.tokens, k)
		}
	}
}

// peek returns the token's upstream factory without consuming it.
// noVNC reconnects (a dropped console, a detached-window reattach)
// re-open the same ws URL, so the token must survive past the first
// connect. It stays valid until the session is explicitly closed
// (VncClose drops it via the App map) or the app exits; the TTL only
// bounds an UNUSED token so a leaked URL that's never opened expires.
func (b *VncBridge) peek(tok string) *vncToken {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tokens[tok]
	if t == nil {
		return nil
	}
	if !t.used && time.Since(t.created) > vncTokenTTL {
		delete(b.tokens, tok)
		return nil
	}
	t.used = true
	return t
}

func (b *VncBridge) handle(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/vnc/")
	t := b.peek(tok)
	if t == nil {
		http.Error(w, "unknown or expired vnc token", http.StatusForbidden)
		return
	}
	// Accept WITHOUT requiring a subprotocol: noVNC offers none by
	// default, and the RFB stream is plain binary either way. Loopback
	// only, so origin checks (which vary by platform: wails://,
	// http://localhost, file://) are skipped.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("vnc bridge: ws accept: %v", err)
		return
	}
	// Long-lived: a VNC session can sit open for hours.
	ctx := context.Background()
	defer c.Close(websocket.StatusNormalClosure, "")

	log.Printf("vnc bridge: ws accepted, opening upstream for token")
	up, err := t.open(ctx)
	if err != nil {
		log.Printf("vnc bridge: upstream open FAILED: %v", err)
		c.Close(websocket.StatusInternalError, truncErr(err))
		return
	}
	defer up.Close()
	log.Printf("vnc bridge: upstream open OK, starting relay")

	client := websocket.NetConn(ctx, c, websocket.MessageBinary)
	relay(client, up)
	log.Printf("vnc bridge: session ended")
}

// relay copies bytes both directions until either side closes, then
// tears the other down so neither goroutine leaks.
func relay(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
}

// --- upstream builders ---------------------------------------------------

// NewTCPUpstream returns a factory that opens a raw TCP connection to
// addr ("host:port"). Used for direct (non-tunnelled) generic VNC.
func NewTCPUpstream(addr string) func(ctx context.Context) (VncUpstream, error) {
	return func(ctx context.Context) (VncUpstream, error) {
		d := net.Dialer{Timeout: 10 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("vnc dial %s: %w", addr, err)
		}
		return conn, nil
	}
}

// NewSSHClientUpstream returns a factory that dials addr through an
// already-connected SSH client (the RFB port reached on the remote's
// loopback, like a localhost-bound x11vnc). The console owns the SSH
// session; it must stay alive for the console's lifetime.
func NewSSHClientUpstream(client *gossh.Client, addr string) func(ctx context.Context) (VncUpstream, error) {
	return func(ctx context.Context) (VncUpstream, error) {
		conn, err := client.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("vnc dial %s over ssh: %w", addr, err)
		}
		return conn, nil
	}
}

func randToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func truncErr(err error) string {
	s := err.Error()
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
