package ssh

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// SessionID identifies an active SSH session in the pool.
type SessionID = string

// ContextDialer matches net.Dialer.DialContext / netstack's
// Net.DialContext - the shape a custom first-hop transport must have.
type ContextDialer func(ctx context.Context, network, addr string) (net.Conn, error)

// FirstHopDialerHook, when non-nil, is consulted for connections whose
// resolved settings carry a NetworkProfileID: it returns the dialer
// that reaches the first hop (e.g. a userspace WireGuard tunnel from
// internal/wg, wired by the host app - same pattern as
// BrowserOpenHook). A returned error ABORTS the connect: a connection
// pinned to a network profile must never silently fall back to a
// direct dial. Hops after the first ride the previous hop's SSH
// channel and never consult this.
var FirstHopDialerHook func(settings *store.ResolvedSettings) (ContextDialer, error)

// DialPathKey is the context key under which firstHopDial passes a
// *string to the custom dialer; the dialer records which transport
// it actually used ("tunnel", or "direct" when an auto/paused policy
// skipped the tunnel). Lets the UI show a truthful VPN indicator.
type DialPathKey struct{}

// firstHopDial dials the first hop of a chain: through the network
// profile's tunnel when one is resolved, plain TCP otherwise. The
// second return is the transport actually used: "" (no profile),
// "tunnel" or "direct".
func firstHopDial(ctx context.Context, settings *store.ResolvedSettings, addr string, timeout time.Duration) (net.Conn, string, error) {
	if settings.NetworkProfileID != nil {
		if FirstHopDialerHook == nil {
			return nil, "", fmt.Errorf("network profile set but no tunnel support wired")
		}
		d, err := FirstHopDialerHook(settings)
		if err != nil {
			return nil, "", fmt.Errorf("network profile: %w", err)
		}
		path := "tunnel" // default when the dialer doesn't report
		dctx, cancel := context.WithTimeout(
			context.WithValue(ctx, DialPathKey{}, &path), timeout)
		defer cancel()
		conn, err := d(dctx, "tcp", addr)
		return conn, path, err
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	return conn, "", err
}

// SessionState is the lifecycle event we emit to the frontend.
type SessionState struct {
	State   string `json:"state"` // connecting | auth_in_progress | connected | disconnected | error
	Hint    string `json:"hint,omitempty"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Clean is true when the session closed because the remote shell
	// exited normally (clean EOF, exit 0). False for non-zero exits,
	// network drops, ssh-side errors. Lets the UI decide whether to
	// auto-close the tab on a clean Ctrl+D / `exit` rather than parse
	// Reason strings.
	Clean bool `json:"clean,omitempty"`
}

// OutputPayload is the base64-encoded data chunk sent to xterm.js. Cum is
// the cumulative byte count after this chunk; the frontend dedupes against
// the snapshot watermark it captured at mount.
type OutputPayload struct {
	B64 string `json:"b64"`
	Cum uint64 `json:"cum"`
}

// EventSink is what the IPC layer plugs in to receive session events.
// runtime.EventsEmit is called via this so the ssh package doesn't depend
// on Wails directly (keeps tests possible).
type EventSink interface {
	EmitState(sessionID string, state SessionState)
	// EmitOutput delivers a raw chunk plus the cumulative byte counter
	// after this chunk (totalEmitted). The frontend uses cum to dedupe
	// against the scrollback snapshot it fetched on mount.
	EmitOutput(sessionID string, data []byte, cum uint64)
	EmitExitStatus(sessionID string, code uint32)
	// EmitDebug is fired only when the resolved settings say verbose=true.
	// Frontend renders these inline in the terminal scrollback. Lines must
	// never contain secrets - fingerprints + truncated paths are OK.
	EmitDebug(sessionID string, line string)
}

const scrollbackCap = 256 * 1024 // 256 KB - enough for ~2000 lines

// scrollbackBuf is a mutex-protected append buffer capped at scrollbackCap.
// Old bytes are dropped from the front when the cap is exceeded.
//
// Tracks total bytes ever written to the session (totalEmitted) so a newly
// mounting frontend can snapshot + subscribe atomically and ignore the
// re-delivery of bytes it already has in the snapshot.
type scrollbackBuf struct {
	mu           sync.Mutex
	buf          []byte
	totalEmitted uint64
}

// appendAndEmit appends bytes, bumps totalEmitted, AND fires the sink event
// all under the SAME lock. Holding the lock across the emit is what keeps
// emit order == cum order: with two pump goroutines (stdout + stderr) the
// old appendAndCount released the lock before the caller emitted, so a
// chunk that got the higher cum could reach the frontend FIRST. The frontend
// concatenates live chunks in arrival order, so an out-of-order pair landed
// content on the wrong rows - the "prompt rendered mid-listing" garble on a
// big `ls -l` / `ll`. Emitting inside the lock serialises the two pumps.
func (b *scrollbackBuf) appendAndEmit(data []byte, sink EventSink, sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, data...)
	if len(b.buf) > scrollbackCap {
		b.buf = b.buf[len(b.buf)-scrollbackCap:]
	}
	b.totalEmitted += uint64(len(data))
	sink.EmitOutput(sessionID, data, b.totalEmitted)
}

// snapshot returns the current buffer plus the cumulative-bytes counter at
// snapshot time, taken under the same lock so they're consistent. The
// frontend uses the counter as a threshold for live events to discard.
func (b *scrollbackBuf) snapshot() ([]byte, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out, b.totalEmitted
}

// Session is one live shell on a remote host (possibly via a chain).
type Session struct {
	ID string
	// NetworkVia is the first hop's transport: "" (plain dial, no
	// network profile), "tunnel" (through the profile's WireGuard
	// device) or "direct" (profile present but auto/paused policy
	// dialed outside the tunnel).
	NetworkVia string
	conn     ssh.Conn      // top-of-chain client connection
	stack    []*ssh.Client // chain of clients (last one is the target)
	channel  ssh.Channel
	requests <-chan *ssh.Request

	stdin  io.WriteCloser
	closed chan struct{}

	mu         sync.Mutex
	closedOnce sync.Once

	// onClose is called from the session task once Wait() returns (whether
	// the user disconnected, the server killed the channel, or the network
	// dropped). Used to tear down forward listeners + remove the pool
	// entry. Set by the IPC layer.
	onClose func(sessionID string)

	// sftp holds a lazily-created SFTP client. nil until first SFTP use.
	// Closed by CloseSFTP from the session-close path.
	sftp *sftpHolder

	// userInitiatedClose is set true by Disconnect so the wait-goroutine
	// can mark the disconnect as expected. Auto-reconnect logic in the
	// app layer uses this to decide whether to retry.
	userInitiatedClose bool

	// scrollback accumulates raw PTY bytes so newly mounted terminals
	// (after detach/redock or UI reload) can replay the session history.
	scrollback scrollbackBuf
}

// Scrollback returns a snapshot of the accumulated PTY output bytes together
// with the cumulative byte counter at snapshot time. The counter is the
// "watermark" - every byte already in the snapshot has cum ≤ watermark,
// so the frontend can discard live pty_output events with cum ≤ watermark
// (or trim the overlapping prefix of a straddling chunk).
func (s *Session) Scrollback() ([]byte, uint64) {
	return s.scrollback.snapshot()
}

// WasUserInitiated reports whether the last (or only) disconnect was
// triggered by an explicit Disconnect() call as opposed to the remote
// dying / network drop. Caller should consult this after the close
// channel is closed.
func (s *Session) WasUserInitiated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userInitiatedClose
}

// SetOnClose registers a callback invoked exactly once when this session
// ends. Safe to set before the wait goroutine runs.
func (s *Session) SetOnClose(fn func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onClose = fn
}

// TargetClient returns the *ssh.Client at the end of the jump chain,
// i.e. the one talking to the actual destination host. SFTP, port
// forwards and any "do something on the target" feature opens new
// channels on this client. Returns nil if the chain is empty (should
// never happen on a live session).
func (s *Session) TargetClient() *ssh.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stack) == 0 {
		return nil
	}
	return s.stack[len(s.stack)-1]
}

// hop describes one step of the connect chain (jump host or target).
type hop struct {
	Label    string
	Hostname string
	Port     uint16
	Username string
	AuthRef  *string
}

func buildHopChain(s *store.ResolvedSettings) []hop {
	var chain []hop
	if s.JumpHost != nil {
		// Walk outermost->innermost.
		cur := s.JumpHost
		var jumps []*store.JumpHostSpec
		for cur != nil {
			jumps = append(jumps, cur)
			cur = cur.Via
		}
		targetUser := ""
		if s.Username != nil {
			targetUser = *s.Username
		}
		for _, j := range jumps {
			port := uint16(22)
			if j.Port != nil {
				port = *j.Port
			}
			user := targetUser
			if j.Username != nil {
				user = *j.Username
			}
			authRef := s.AuthRef
			if j.AuthRef != nil {
				authRef = j.AuthRef
			}
			chain = append(chain, hop{
				Label:    "jump " + j.Hostname,
				Hostname: j.Hostname,
				Port:     port,
				Username: user,
				AuthRef:  authRef,
			})
		}
	}
	user := ""
	if s.Username != nil {
		user = *s.Username
	}
	chain = append(chain, hop{
		Label:    "target " + s.Hostname,
		Hostname: s.Hostname,
		Port:     s.Port,
		Username: user,
		AuthRef:  s.AuthRef,
	})
	return chain
}

// Connect builds the chain and ends with a PTY-backed shell on the target.
//
// The returned Session is registered in the pool by the caller before any
// reads happen so the event sink can find it.
//
// connectTimeout applies to both the TCP dial and the SSH handshake for
// each hop. Pass 0 to use the default (20s).
//
// progress (if non-nil) receives short status strings ("dial bastion1",
// "handshake target", "opening shell") so the caller can render a
// live hint while SshConnect blocks. The events keyed by sessionID
// happen too - but the caller doesn't have the sessionID yet, so this
// is the only channel that can reach DetailPane.
// hostKeyAlgoLookup, when non-nil, returns the algorithm names that
// the server MUST use for a given (host, port). The slice is plugged
// into ssh.ClientConfig.HostKeyAlgorithms so the SSH handshake fails
// at server-key-exchange if the remote presents a different algo -
// closes the host-key-algorithm-downgrade hole where an attacker
// could trigger a fresh "unknown host" prompt by serving a key
// under a different algo than the one the user originally trusted.
// Return nil/empty for hosts with no pinning yet (first connect).
type HostKeyAlgoLookup func(host string, port int) []string

func Connect(
	ctx context.Context,
	db *store.DB,
	vault *creds.Vault,
	settings *store.ResolvedSettings,
	sink EventSink,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	progress func(stage string),
) (*Session, error) {
	if connectTimeout <= 0 {
		connectTimeout = 20 * time.Second
	}
	if progress == nil {
		progress = func(string) {}
	}
	chain := buildHopChain(settings)
	if len(chain) == 0 {
		return nil, fmt.Errorf("empty chain")
	}

	sessionID := uuid.New().String()
	sink.EmitState(sessionID, SessionState{State: "connecting"})

	// Debug helper: no-op unless settings.Verbose is true. Keeps the
	// emit-or-not branch in one place.
	debug := func(format string, args ...any) {
		if !settings.Verbose {
			return
		}
		sink.EmitDebug(sessionID, fmt.Sprintf(format, args...))
	}

	debug("resolved: host=%s port=%d user=%v auth_ref=%v terminal=%s keepalive=%ds",
		settings.Hostname, settings.Port, derefStr(settings.Username),
		derefStr(settings.AuthRef), settings.TerminalType, settings.KeepaliveInterval)
	debug("chain length=%d (hops innermost-first):", len(chain))
	for i, h := range chain {
		debug("  hop %d: %s %s@%s:%d auth_ref=%v",
			i, h.Label, h.Username, h.Hostname, h.Port, derefStr(h.AuthRef))
	}

	var (
		clients []*ssh.Client
		prev    *ssh.Client // for direct-tcpip on subsequent hops
		rawConn ssh.Conn
		// networkVia records the first hop's transport ("", "tunnel",
		// "direct") so the app layer can surface a VPN indicator.
		networkVia string
	)

	for i, h := range chain {
		progress("Connecting to " + h.Label)
		sink.EmitState(sessionID, SessionState{
			State: "auth_in_progress",
			Hint:  "connecting to " + h.Label,
		})

		var methods []ssh.AuthMethod
		if h.AuthRef != nil {
			cred, err := db.GetCredential(*h.AuthRef)
			if err != nil {
				cleanup(clients)
				return nil, fmt.Errorf("%s: get credential %s: %w", h.Label, *h.AuthRef, err)
			}
			// Fall back to the credential's default_username when the hop has no
			// username from the resolved settings (imported credentials carry this).
			if h.Username == "" && cred.DefaultUsername != nil {
				h.Username = *cred.DefaultUsername
			}
			auth, err := ResolveAuth(ctx, cred, vault)
			if err != nil {
				// If we're on the last hop and a per-connection password override
				// is set, that override will cover auth. Don't hard-fail here -
				// the inherited credential may be broken (e.g. RDM-imported
				// password credential with vault_key = NULL) while the override
				// is perfectly valid.
				isLastHop := i == len(chain)-1
				if isLastHop && settings.PasswordOverride != nil {
					log.Printf("ssh: %s credential resolve failed (%v); per-connection password override will be used instead", h.Label, err)
				} else {
					cleanup(clients)
					return nil, fmt.Errorf("%s: %w", h.Label, err)
				}
			} else {
				methods = auth.ToAuthMethods()
				var algos []string
				for _, signer := range auth.Signers {
					algos = append(algos, signer.PublicKey().Type())
				}
				log.Printf("ssh: %s auth attempt user=%s methods=%d signers=%v", h.Label, h.Username, len(methods), algos)
				debug("%s: auth ready user=%s methods=%d signers=%v cred=%s kind=%s",
					h.Label, h.Username, len(methods), algos, cred.Name, cred.Kind)
			}
		}
		// For the target hop, add a per-connection password override as an
		// additional auth method (appended so key auth is tried first).
		if i == len(chain)-1 && settings.PasswordOverride != nil {
			methods = append(methods, ssh.Password(*settings.PasswordOverride))
		}
		if h.Username == "" {
			cleanup(clients)
			return nil, fmt.Errorf("%s: no username", h.Label)
		}
		if len(methods) == 0 {
			cleanup(clients)
			return nil, fmt.Errorf("%s: no credential assigned and no password set", h.Label)
		}

		var pinnedAlgos []string
		if algoLookup != nil {
			pinnedAlgos = algoLookup(h.Hostname, int(h.Port))
		}
		// Buffer the pre-auth banner instead of emitting straight to
		// the terminal: BannerCallback fires BEFORE HostKeyCallback
		// returns, so painting it raw lets an unverified MITM peer
		// drop arbitrary VT100 sequences (cursor move, OSC title set,
		// color reset) into the user's screen. xterm.js scrubs the
		// worst classes but title-set and a handful of OSC variants
		// slip through. We hold the bytes until the SSH handshake
		// (which includes the host key callback) completes - then
		// emit them as one chunk with cum=0 so they still land in
		// the terminal without contributing to the scrollback cum
		// watermark (the session isn't running yet).
		var bannerBuf []byte
		cfg := &ssh.ClientConfig{
			User:              h.Username,
			Auth:              methods,
			HostKeyCallback:   hostKeyCB,
			HostKeyAlgorithms: pinnedAlgos,
			Timeout:           connectTimeout,
			BannerCallback: func(banner string) error {
				if banner != "" {
					bannerBuf = append(bannerBuf, banner...)
				}
				return nil
			},
		}
		if settings.KeepaliveInterval > 0 {
			// ssh.ClientConfig has no built-in keepalive; we send SSH global
			// requests ourselves below.
			_ = settings.KeepaliveInterval
		}

		addr := fmt.Sprintf("%s:%d", h.Hostname, h.Port)
		var client *ssh.Client
		if i == 0 {
			progress("TCP dial " + h.Label)
			if settings.NetworkProfileID != nil {
				debug("%s: TCP dial %s (via network profile %s)", h.Label, addr, *settings.NetworkProfileID)
			} else {
				debug("%s: TCP dial %s (direct)", h.Label, addr)
			}
			t0 := time.Now()
			conn, dialPath, err := firstHopDial(ctx, settings, addr, connectTimeout)
			if err != nil {
				debug("%s: dial failed after %s: %v", h.Label, time.Since(t0).Round(time.Millisecond), err)
				cleanup(clients)
				return nil, fmt.Errorf("%s: dial: %w", h.Label, err)
			}
			networkVia = dialPath
			if dialPath != "" {
				debug("%s: first hop transport: %s", h.Label, dialPath)
			}
			debug("%s: TCP up in %s, starting SSH handshake", h.Label, time.Since(t0).Round(time.Millisecond))
			progress("SSH handshake " + h.Label)
			t0 = time.Now()
			sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
			if err != nil {
				debug("%s: SSH handshake failed after %s: %v", h.Label, time.Since(t0).Round(time.Millisecond), err)
				_ = conn.Close()
				cleanup(clients)
				log.Printf("ssh: %s handshake failed: %v", h.Label, err)
				return nil, fmt.Errorf("%s: ssh handshake: %w", h.Label, err)
			}
			debug("%s: handshake OK in %s server=%q", h.Label, time.Since(t0).Round(time.Millisecond), string(sshConn.ServerVersion()))
			rawConn = sshConn
			client = ssh.NewClient(sshConn, chans, reqs)
			if len(bannerBuf) > 0 {
				sink.EmitOutput(sessionID, bannerBuf, 0)
				bannerBuf = nil
			}
		} else {
			// Open direct-tcpip channel through the previous client.
			progress("Tunnel dial " + h.Label)
			debug("%s: dial %s through previous hop", h.Label, addr)
			t0 := time.Now()
			netConn, err := prev.Dial("tcp", addr)
			if err != nil {
				debug("%s: jump dial failed after %s: %v", h.Label, time.Since(t0).Round(time.Millisecond), err)
				cleanup(clients)
				log.Printf("ssh: %s dial through jump failed: %v", h.Label, err)
				return nil, fmt.Errorf("%s: dial through jump: %w", h.Label, err)
			}
			debug("%s: tunnel up in %s, starting SSH handshake", h.Label, time.Since(t0).Round(time.Millisecond))
			progress("SSH handshake " + h.Label)
			t0 = time.Now()
			sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
			if err != nil {
				debug("%s: SSH handshake failed after %s: %v", h.Label, time.Since(t0).Round(time.Millisecond), err)
				_ = netConn.Close()
				cleanup(clients)
				log.Printf("ssh: %s handshake failed: %v", h.Label, err)
				return nil, fmt.Errorf("%s: ssh handshake: %w", h.Label, err)
			}
			debug("%s: handshake OK in %s server=%q", h.Label, time.Since(t0).Round(time.Millisecond), string(sshConn.ServerVersion()))
			rawConn = sshConn
			client = ssh.NewClient(sshConn, chans, reqs)
			if len(bannerBuf) > 0 {
				sink.EmitOutput(sessionID, bannerBuf, 0)
				bannerBuf = nil
			}
		}
		clients = append(clients, client)
		prev = client
		log.Printf("ssh: hop %d (%s) authenticated", i, h.Label)
		debug("%s: authenticated", h.Label)
	}
	debug("all hops up, opening PTY shell on target")
	progress("Opening shell")

	target := clients[len(clients)-1]

	sess, err := target.NewSession()
	if err != nil {
		cleanup(clients)
		return nil, fmt.Errorf("new session: %w", err)
	}

	// Set env vars (best effort).
	for k, v := range settings.EnvVars {
		_ = sess.Setenv(k, v)
	}

	if err := sess.RequestPty(settings.TerminalType, 24, 80, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		_ = sess.Close()
		cleanup(clients)
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		cleanup(clients)
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		cleanup(clients)
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		cleanup(clients)
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		cleanup(clients)
		return nil, fmt.Errorf("start shell: %w", err)
	}

	s := &Session{
		ID:         sessionID,
		conn:       rawConn,
		stack:      clients,
		stdin:      stdin,
		closed:     make(chan struct{}),
		NetworkVia: networkVia,
	}

	// Output pump. We ALWAYS allocate a PTY (RequestPty above), and a PTY
	// merges the remote's stdout + stderr onto the single channel master -
	// the SSH extended-data (stderr) stream carries nothing for a shell
	// session. Running a SECOND pump on stderr was therefore pure downside:
	// two goroutines emitting pty_output events meant the events could reach
	// the webview out of order (the cum lock orders the Emit CALLS, but
	// Wails' event dispatch hops through its own goroutine), landing a big
	// `ll` burst's content on the wrong rows (prompt mid-listing). One pump
	// = one totally-ordered stream = no reorder to repair. We still drain
	// stderr in the background and log if it ever delivers bytes, so if some
	// server somehow splits streams under a PTY we find out instead of
	// silently dropping output.
	go pumpOutput(s, stdout, sink)
	go drainUnexpectedStderr(s.ID, stderr)

	// Wait + emit final state
	go func() {
		err := sess.Wait()
		var reason string
		var clean bool
		if err == nil {
			reason = "remote closed"
			clean = true
		} else if xe, ok := err.(*ssh.ExitError); ok {
			code := xe.ExitStatus()
			reason = fmt.Sprintf("remote closed (exit %d)", code)
			sink.EmitExitStatus(sessionID, uint32(code))
			// Treat 0 and the two common user-initiated signal exits
			// (130 = SIGINT, 143 = SIGTERM - bash convention 128+N) as
			// clean. Those are "I'm done, abort", not real failures, and
			// users expect the tab to auto-close just like with `exit 0`.
			clean = code == 0 || code == 130 || code == 143
		} else {
			reason = fmt.Sprintf("session ended: %v", err)
		}
		s.closedOnce.Do(func() {
			close(s.closed)
			s.CloseSFTP()
			// Tear down listeners owned by this session so we don't leak
			// local ports when the remote dies on its own.
			if s.onClose != nil {
				s.onClose(sessionID)
			}
		})
		sink.EmitState(sessionID, SessionState{State: "disconnected", Reason: reason, Clean: clean})
		cleanup(clients)
	}()

	// Keepalive always runs, even when the setting is 0 ("off"). Off means
	// "don't hold the link open with artificial traffic" - it cannot mean
	// "don't notice when the link dies", because the probe is the only thing
	// that CAN notice. See runKeepalive for why nothing below the SSH layer
	// sees a dead chain.
	go runKeepalive(s, keepaliveInterval(settings.KeepaliveInterval))

	// Save sess pointer for resize; channel-based interface for full control
	s.channel = sessionAsChannel{sess: sess}
	sink.EmitState(sessionID, SessionState{State: "connected"})
	return s, nil
}

// drainUnexpectedStderr reads the SSH stderr (extended-data) stream, which
// under an allocated PTY should stay empty (the remote merges stderr into
// the PTY master). We drain it so a misbehaving server can't block on a full
// window, and log a one-time warning with a sample if anything ever shows up
// - that would mean output is NOT fully merged and we'd want to reinstate a
// (cum-ordered) stderr path. Bytes are intentionally NOT emitted to the
// terminal here to keep the live stream single-source and strictly ordered.
func drainUnexpectedStderr(sessionID string, r io.Reader) {
	buf := make([]byte, 8192)
	logged := false
	for {
		n, err := r.Read(buf)
		if n > 0 && !logged {
			sample := buf[:n]
			if len(sample) > 64 {
				sample = sample[:64]
			}
			log.Printf("ssh: session %s UNEXPECTED stderr under PTY (%d bytes): %q",
				sessionID, n, sample)
			logged = true
		}
		if err != nil {
			return
		}
	}
}

func pumpOutput(s *Session, r io.Reader, sink EventSink) {
	buf := make([]byte, 8192)
	first := true
	// carry holds bytes from the end of a read that we deliberately did
	// NOT emit yet because they're an incomplete escape sequence or a
	// partial UTF-8 rune. A fixed 8 KiB read boundary otherwise lands
	// mid-sequence on large colorized output (e.g. `ls -l /var/log`),
	// splitting e.g. "ESC[01;31m" across two events. xterm should buffer
	// a torn sequence across writes, but the alpha is unreliable when the
	// halves arrive as separate async events - the tail of one and head
	// of the next render as garbage / desync the cursor. Holding the
	// incomplete tail back and prepending it to the next read guarantees
	// every emitted chunk is sequence-complete.
	var carry []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if first {
				log.Printf("ssh: session %s first output %d bytes", s.ID, n)
				first = false
			}
			data := buf[:n]
			if len(carry) > 0 {
				data = append(carry, data...)
				carry = nil
			}
			emit, hold := splitAtSafeBoundary(data)
			if len(hold) > 0 {
				// Stash the incomplete tail for the next read. Copy it -
				// `data` may alias buf, which the next Read overwrites.
				carry = append(carry[:0:0], hold...)
			}
			if len(emit) > 0 {
				s.scrollback.appendAndEmit(emit, sink, s.ID)
			}
		}
		if err != nil {
			// Flush whatever incomplete tail remains so nothing is lost on
			// close (e.g. a final prompt ending mid-sequence).
			if len(carry) > 0 {
				s.scrollback.appendAndEmit(carry, sink, s.ID)
			}
			return
		}
	}
}

// splitAtSafeBoundary returns (emit, hold): emit is the prefix of data that
// ends on a clean boundary (no dangling ESC sequence, no partial UTF-8
// rune); hold is the incomplete tail to carry into the next read. The hold
// is bounded - a sequence longer than a sane CSI/OSC (or a lone ESC near a
// huge gap) is emitted rather than buffered forever, so a malformed stream
// can't stall output.
func splitAtSafeBoundary(data []byte) (emit, hold []byte) {
	n := len(data)
	if n == 0 {
		return nil, nil
	}

	// 1. Partial UTF-8 rune at the very end: find the start of the last
	//    rune (walk back over continuation bytes 0x80-0xbf to a lead byte)
	//    and check whether all its bytes have arrived.
	if data[n-1] >= 0x80 {
		i := n - 1
		for i > 0 && data[i] >= 0x80 && data[i] < 0xc0 {
			i-- // continuation byte - walk back to the lead
		}
		if data[i] >= 0xc0 { // a multi-byte lead
			need := utf8RuneLen(data[i])
			if need > 1 && i+need > n {
				return data[:i], data[i:]
			}
		}
	}

	// 2. Dangling escape sequence at the end. Real CSI/SGR/OSC tails we'd
	//    split are short, so scan back a small bound looking for an ESC. If
	//    found and its sequence isn't complete, hold from the ESC. We stop
	//    at the first control byte (< 0x20, e.g. the \n ending the previous
	//    line) - an ESC can't be "open" across a newline - which also caps
	//    the scan cheaply on normal text.
	const maxSeq = 64
	start := n - maxSeq
	if start < 0 {
		start = 0
	}
	for i := n - 1; i >= start; i-- {
		b := data[i]
		if b == 0x1b { // ESC - found the start of the trailing sequence
			if escapeComplete(data[i:]) {
				return data, nil
			}
			return data[:i], data[i:]
		}
		if b < 0x20 {
			// A control byte (CR, LF, BEL, ...) terminates any preceding
			// sequence - nothing is dangling past it.
			break
		}
	}
	return data, nil
}

// escapeComplete reports whether the escape sequence starting at seq[0]
// (== 0x1b) is fully present. Handles the common CSI (ESC [ ... final),
// OSC (ESC ] ... BEL or ST), and simple two-byte ESC forms. Unknown/longer
// forms are treated as complete past a bound so we never hold indefinitely.
func escapeComplete(seq []byte) bool {
	if len(seq) < 2 {
		return false // lone ESC - wait for more
	}
	switch seq[1] {
	case '[': // CSI: final byte in 0x40-0x7e
		for i := 2; i < len(seq); i++ {
			if seq[i] >= 0x40 && seq[i] <= 0x7e {
				return true
			}
		}
		return len(seq) > 256 // give up holding an over-long CSI
	case ']': // OSC: terminated by BEL (0x07) or ST (ESC \)
		for i := 2; i < len(seq); i++ {
			if seq[i] == 0x07 {
				return true
			}
			if seq[i] == 0x1b && i+1 < len(seq) && seq[i+1] == '\\' {
				return true
			}
		}
		return len(seq) > 512 // give up holding an over-long OSC
	default:
		// Two-byte escape (ESC M, ESC =, etc) - complete once the second
		// byte is present, which it is (len >= 2).
		return true
	}
}

// utf8RuneLen returns the total byte length a UTF-8 rune with the given
// lead byte should have (1-4), or 0 if b is not a valid lead byte.
func utf8RuneLen(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b >= 0xc0 && b < 0xe0:
		return 2
	case b >= 0xe0 && b < 0xf0:
		return 3
	case b >= 0xf0 && b < 0xf8:
		return 4
	default:
		return 0
	}
}

// keepaliveIdleProbe is how often we probe a session whose keepalive setting
// is 0 ("off"). Rare enough to be indistinguishable from no traffic for the
// purpose of a NAT timeout - it is not there to keep the link open, only to
// find out that it is gone.
const keepaliveIdleProbe = 60 * time.Second

// keepaliveMinInterval floors the probe tick. The UI steps the field in 5s
// but nothing stops a hand-typed 1, and a 1s SSH keepalive is pointless
// traffic anyway - it also leaves no room for a probe timeout that is both
// meaningful and shorter than the tick.
const keepaliveMinInterval = 5 * time.Second

// keepaliveInterval maps the user's setting (seconds; 0 = off) onto the tick
// the probe loop actually runs at.
func keepaliveInterval(setting uint32) time.Duration {
	if setting == 0 {
		return keepaliveIdleProbe
	}
	d := time.Duration(setting) * time.Second
	if d < keepaliveMinInterval {
		d = keepaliveMinInterval
	}
	return d
}

// keepaliveProbeTimeout bounds a single probe: half the interval, capped at
// 30s. Half - never the whole tick - so a slow probe can never still be in
// flight when the next one starts.
func keepaliveProbeTimeout(every time.Duration) time.Duration {
	t := every / 2
	if t > 30*time.Second {
		t = 30 * time.Second
	}
	return t
}

// runKeepalive probes the far end of the chain on an interval. It serves two
// distinct purposes, and only the second one is unconditional:
//
//   - With a keepalive interval set, the probe traffic KEEPS the path open,
//     stopping a NAT or firewall on the way from evicting an idle flow.
//   - Always, the probe is the only thing that can DETECT that the path has
//     died. Nothing below the SSH layer can: with a jump host, the TCP socket
//     this machine owns goes to the JUMP, and the far hop rides inside it as
//     an SSH channel. When that far hop dies (a firewall or VPN on the jump's
//     far side drops the flow with no RST), the local socket stays perfectly
//     ESTABLISHED - the jump is still up, so the kernel is right. Wait() never
//     returns, the tab stays green, keystrokes disappear into a dead channel,
//     and the only way out is closing the tab by hand. Confirmed in the field:
//     netstat showed the same ESTABLISHED line before and after the break.
//
// s.conn is the LAST hop's connection (rawConn is reassigned on every hop of
// the chain), so the request travels the whole chain and back - which is
// exactly the path whose death we need to observe.
//
// A probe that errors or times out means the chain is gone. Closing the
// transport is all we do: that unblocks sess.Wait(), and the ordinary death
// path takes it from there (emits session_state=disconnected, fires onClose,
// which the app layer turns into auto-reconnect - the close is not
// user-initiated, so WasUserInitiated stays false). No bespoke teardown here;
// the point is to trip the existing one.
func runKeepalive(s *Session, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	timeout := keepaliveProbeTimeout(every)
	for {
		select {
		case <-ticker.C:
			// SendRequest with wantReply blocks until the peer answers. On a
			// dead chain that is forever - no error is ever returned, which is
			// why the old loop could not detect anything even when it ran.
			// Run it aside and impose our own deadline.
			done := make(chan error, 1)
			go func() {
				_, _, err := s.conn.SendRequest("keepalive@ssh-tool", true, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					log.Printf("session %s: keepalive failed (%v) - closing transport", s.ID, err)
					_ = s.conn.Close()
					return
				}
			case <-time.After(timeout):
				log.Printf("session %s: keepalive unanswered for %s - link is dead, closing transport", s.ID, timeout)
				_ = s.conn.Close()
				return
			case <-s.closed:
				return
			}
		case <-s.closed:
			return
		}
	}
}

func cleanup(clients []*ssh.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		_ = clients[i].Close()
	}
}

// Write sends keystrokes to the remote PTY.
func (s *Session) Write(data []byte) error {
	_, err := s.stdin.Write(data)
	return err
}

// Resize sends a window-change request to the remote PTY.
func (s *Session) Resize(cols, rows uint16) error {
	ch, ok := s.channel.(sessionAsChannel)
	if !ok {
		return fmt.Errorf("resize: session unavailable")
	}
	return ch.sess.WindowChange(int(rows), int(cols))
}

// Disconnect closes the channel and tears down the chain. Idempotent;
// safe to call after the wait goroutine already cleaned up.
func (s *Session) Disconnect() {
	s.mu.Lock()
	s.userInitiatedClose = true
	stdin := s.stdin
	stack := s.stack
	ch, _ := s.channel.(sessionAsChannel)
	onClose := s.onClose
	id := s.ID
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if ch.sess != nil {
		_ = ch.sess.Close()
	}
	cleanup(stack)
	s.closedOnce.Do(func() {
		close(s.closed)
		s.CloseSFTP()
		if onClose != nil {
			onClose(id)
		}
	})
}

// sessionAsChannel is a thin adapter so we can keep a single *ssh.Session
// reference for Resize while still satisfying the Session.channel field.
// `ssh.Channel` interface and `*ssh.Session` aren't the same; we don't need
// the channel interface for our use case, so this is just for storage.
type sessionAsChannel struct {
	sess *ssh.Session
}

func (sessionAsChannel) Read(p []byte) (int, error)                     { return 0, io.EOF }
func (sessionAsChannel) Write(p []byte) (int, error)                    { return 0, io.EOF }
func (sessionAsChannel) Close() error                                   { return nil }
func (sessionAsChannel) CloseWrite() error                              { return nil }
func (sessionAsChannel) SendRequest(string, bool, []byte) (bool, error) { return false, nil }
func (sessionAsChannel) Stderr() io.ReadWriter                          { return nil }

// EncodeBase64 is the encoder used by the IPC layer to send PTY bytes to
// the frontend.
func EncodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeBase64 is the decoder for keystrokes coming back from the frontend.
func DecodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	return base64.StdEncoding.DecodeString(s)
}

// ContextDone is unused for now; placeholder if we want to attach context
// cancellation to the connect flow later.
var ContextDone = context.Canceled

// derefStr renders a *string for debug lines: nil prints "(nil)", empty
// prints "(empty)". Keeps logs unambiguous about which one we got.
func derefStr[T ~string](p *T) string {
	if p == nil {
		return "(nil)"
	}
	if *p == "" {
		return "(empty)"
	}
	return string(*p)
}
