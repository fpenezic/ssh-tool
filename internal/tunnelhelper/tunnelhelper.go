// Package tunnelhelper manages sidecar tunnel processes (plugins):
// external binaries that join an overlay network (NetBird today,
// Tailscale later) and expose it on a loopback SOCKS5 proxy. The
// package is provider-agnostic - it only speaks the helper protocol:
//
//	stdout, line JSON:
//	  {"event":"ready","socks":"127.0.0.1:PORT","protocol":N}
//	  {"event":"status","peers":N}
//	  {"event":"error","error":"...","cert_pin":"..."}
//	  {"event":"auth","id":"..","message":"..","fields":[...]}  (optional)
//	  {"event":"info","info":{...}}                              (optional)
//	stdin: JSON lines answering auth events ({"cmd":"auth"|"cancel"});
//	       closed by us -> helper shuts down.
//
// The ready event carries a protocol version the app validates against
// minProtocol/maxProtocol; a mismatch is rejected with an actionable
// "update" error instead of run. See checkProtocol.
//
// Keeping the heavy overlay clients out of the main binary is the
// whole point - see netbird-helper/ for why (module replaces + size).
package tunnelhelper

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// readyTimeout caps how long a helper may take from spawn to its
// ready line. NetBird registration + first management sync on a slow
// link is the worst case.
const readyTimeout = 90 * time.Second

// Proc is one live helper process.
type Proc struct {
	ProfileID string
	Name      string // profile display name, for logs / UI

	cmd       *exec.Cmd
	stdin     io.WriteCloser
	socksAddr string
	dialer    proxy.ContextDialer
	startedAt time.Time

	mu     sync.Mutex
	peers  int
	exited bool
	info   json.RawMessage

	stdinMu sync.Mutex
}

// AuthField is one input of a login form a helper forwards.
type AuthField struct {
	Key     string       `json:"key"`
	Name    string       `json:"name"`
	Label   string       `json:"label"`
	Kind    string       `json:"kind"` // text | password | select
	Value   string       `json:"value,omitempty"`
	Options []AuthOption `json:"options,omitempty"`
}

// AuthOption is one choice of a select field.
type AuthOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AuthChallenge is a login form the helper needs answered (VPN gateways
// ask for user, password, a one-time code... in one or several rounds).
type AuthChallenge struct {
	ID      string      `json:"id"`
	Message string      `json:"message"`
	Banner  string      `json:"banner"`
	Error   string      `json:"error"` // the server rejected the previous answer
	Fields  []AuthField `json:"fields"`
}

// CertUntrustedError is a helper refusing a gateway certificate the system
// does not trust. Pin is what to pass back (after the user agrees) to
// trust exactly this certificate.
type CertUntrustedError struct {
	Msg, Pin, Subject string
	// Changed: the certificate no longer matches the pinned one.
	Changed bool
}

func (e *CertUntrustedError) Error() string { return e.Msg }

// UnreachableError is a helper that found nothing answering at the
// gateway, so another gateway of the profile may be tried.
type UnreachableError struct{ Msg string }

func (e *UnreachableError) Error() string { return e.Msg }

// Options tunes one spawn. Zero value = the plain NetBird / Tailscale
// contract.
type Options struct {
	// OnAuth answers a login challenge by calling p.Answer / p.Cancel.
	// Called on its own goroutine; nil makes any challenge a failure.
	OnAuth func(p *Proc, ch AuthChallenge)
	// OnInfo is called after the tunnel description changes once ready
	// (rekey, reconnect, transport switch), for the UI to refresh.
	OnInfo func(p *Proc)
	// OnSession receives the session cookie a helper reports for a later
	// resume (openconnect, when asked for it). Secret: never log it.
	OnSession func(cookie string)
	// OnOpenURL shows a login page the helper asks for (a web / SSO
	// login the server holds the connect open for).
	OnOpenURL func(url string)
	// ReadyTimeout overrides readyTimeout (a login with a typed one-time
	// code takes longer than a NetBird registration).
	ReadyTimeout time.Duration
}

// Answer submits values (field key -> value) for an auth challenge.
func (p *Proc) Answer(id string, values map[string]string) error {
	return p.send(map[string]any{"cmd": "auth", "id": id, "values": values})
}

// Cancel abandons the login; the helper exits.
func (p *Proc) Cancel(id string) error {
	return p.send(map[string]any{"cmd": "cancel", "id": id})
}

func (p *Proc) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

// Info is the helper's latest tunnel description (addresses, routes, DNS,
// MTU...), nil when it sends none.
func (p *Proc) Info() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// DialContext dials through the helper's SOCKS5 proxy. Hostnames are
// passed to the proxy verbatim and resolve inside the overlay.
func (p *Proc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return p.dialer.DialContext(ctx, network, addr)
}

// Status is the UI snapshot.
type Status struct {
	Running   bool
	StartedAt int64
	Peers     int
	Info      json.RawMessage // helper's tunnel description, nil if none
}

type helperEvent struct {
	Event string `json:"event"`
	Socks string `json:"socks"`
	Peers int    `json:"peers"`
	Error string `json:"error"`
	// error: an untrusted gateway certificate (trust on first use).
	CertPin     string `json:"cert_pin"`
	CertSubject string `json:"cert_subject"`
	CertChanged bool   `json:"cert_changed"`
	// error: the gateway did not answer at all (openconnect).
	Unreachable bool `json:"unreachable"`
	// auth
	ID      string      `json:"id"`
	Message string      `json:"message"`
	Banner  string      `json:"banner"`
	Fields  []AuthField `json:"fields"`
	// info
	Info json.RawMessage `json:"info"`
	// session: a session cookie to resume with (openconnect)
	Cookie string `json:"cookie"`
	// open_url: a login page for the user's browser (openvpn pending auth)
	URL string `json:"url"`
	// Protocol is the helper protocol version, carried on the ready
	// event. Absent (0) means a pre-versioning helper from the app-tag
	// era, which the app treats as too old. See protocol version range
	// below and docs/helper-release-plan.md.
	Protocol int `json:"protocol"`
}

// Protocol version range the app speaks. The helper announces its own
// version in the ready event; a value outside [minProtocol, maxProtocol]
// is rejected with an actionable "update" error rather than run.
//
// Bump maxProtocol only on a BREAKING wire change (new required field,
// changed shutdown semantics). Additive optional fields need no bump.
// minProtocol rises only when the app genuinely drops the ability to
// drive an older helper. A helper that omits the field reports 0, which
// is below minProtocol and so is flagged for a re-download.
const (
	minProtocol = 1
	maxProtocol = 1
)

// MaxProtocol is the highest helper protocol version this app speaks.
// The helper releases share this integer as their tag major
// (helper-v<MaxProtocol>), so the plugin downloader uses it to pick the
// right helper release. See docs/helper-release-plan.md.
func MaxProtocol() int { return maxProtocol }

// Manager owns the running helper processes, keyed by profile id.
// Same lazy contract as wg.Manager.
type Manager struct {
	mu    sync.Mutex
	procs map[string]*Proc
	// onExit, when set, is called (own goroutine) after a helper
	// process dies for any reason, so the app can update UI state.
	onExit func(profileID string)
}

func NewManager(onExit func(profileID string)) *Manager {
	return &Manager{procs: map[string]*Proc{}, onExit: onExit}
}

// Ensure returns the running helper for the profile, spawning it if
// needed. exePath is the helper binary; env entries are appended to
// the child's environment (used for the setup-key secret - never put
// secrets in args).
func (m *Manager) Ensure(profileID, name, exePath string, args, env []string) (*Proc, error) {
	return m.EnsureWith(profileID, name, exePath, args, env, Options{})
}

// EnsureWith is Ensure with spawn options (auth relay, ready timeout).
// The manager lock is held across the spawn, so a second connect through
// the same profile waits for the first login instead of starting another.
func (m *Manager) EnsureWith(profileID, name, exePath string, args, env []string, opts Options) (*Proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.procs[profileID]; ok {
		p.mu.Lock()
		alive := !p.exited
		p.mu.Unlock()
		if alive {
			return p, nil
		}
		delete(m.procs, profileID)
	}
	p, err := spawn(profileID, name, exePath, args, env, opts)
	if err != nil {
		return nil, err
	}
	m.procs[profileID] = p
	go m.reap(p)
	log.Printf("tunnelhelper: %s up (socks %s)", name, p.socksAddr)
	return p, nil
}

// Get returns a running helper or nil.
func (m *Manager) Get(profileID string) *Proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[profileID]
	if p == nil {
		return nil
	}
	p.mu.Lock()
	alive := !p.exited
	p.mu.Unlock()
	if !alive {
		return nil
	}
	return p
}

// Stop shuts one helper down: close stdin (the protocol's shutdown
// signal), give it 10s, then kill.
func (m *Manager) Stop(profileID string) {
	m.mu.Lock()
	p, ok := m.procs[profileID]
	if ok {
		delete(m.procs, profileID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	stopProc(p)
	log.Printf("tunnelhelper: %s stopped", p.Name)
}

// Detach stops one helper with the "detach" command: it exits without
// ending its session at the gateway, so a later start can resume it. A
// helper that does not exit on its own (one that predates the command)
// gets the normal Stop.
func (m *Manager) Detach(profileID string) {
	m.mu.Lock()
	p, ok := m.procs[profileID]
	if ok {
		delete(m.procs, profileID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if p.send(map[string]any{"cmd": "detach"}) == nil {
		for i := 0; i < 30; i++ {
			p.mu.Lock()
			exited := p.exited
			p.mu.Unlock()
			if exited {
				log.Printf("tunnelhelper: %s detached", p.Name)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	stopProc(p)
	log.Printf("tunnelhelper: %s stopped (detach not supported)", p.Name)
}

// StopAll tears down everything (app shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	ps := m.procs
	m.procs = map[string]*Proc{}
	m.mu.Unlock()
	for _, p := range ps {
		stopProc(p)
	}
}

// Status reports the helper state for one profile.
func (m *Manager) Status(profileID string) Status {
	p := m.Get(profileID)
	if p == nil {
		return Status{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Running: true, StartedAt: p.startedAt.Unix(), Peers: p.peers, Info: p.info}
}

// reap waits for process exit, marks the proc dead and drops it from
// the table so the next Ensure respawns.
func (m *Manager) reap(p *Proc) {
	_ = p.cmd.Wait()
	p.mu.Lock()
	p.exited = true
	p.mu.Unlock()
	m.mu.Lock()
	if m.procs[p.ProfileID] == p {
		delete(m.procs, p.ProfileID)
	}
	m.mu.Unlock()
	log.Printf("tunnelhelper: %s exited", p.Name)
	if m.onExit != nil {
		m.onExit(p.ProfileID)
	}
}

func stopProc(p *Proc) {
	_ = p.stdin.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			p.mu.Lock()
			exited := p.exited
			p.mu.Unlock()
			if exited {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if !exited && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// checkProtocol validates the helper's announced protocol version
// against the range the app speaks, returning an actionable error that
// names which side is out of date. nil = compatible.
func checkProtocol(v int) error {
	if v < minProtocol {
		// 0 = a pre-versioning helper (app-tag era) or one older than the
		// app now supports. The fix is on the helper side.
		return fmt.Errorf("tunnel helper is out of date (protocol %d, need %d) - update it from Settings > Network profiles > Plugins", v, minProtocol)
	}
	if v > maxProtocol {
		// The helper is newer than this app can drive; the app is the
		// stale side.
		return fmt.Errorf("tunnel helper is newer than this app supports (protocol %d, app speaks up to %d) - update ssh-tool", v, maxProtocol)
	}
	return nil
}

func spawn(profileID, name, exePath string, args, env []string, opts Options) (*Proc, error) {
	cmd := exec.Command(exePath, args...)
	cmd.Env = append(cmd.Environ(), env...)
	configureSysProcAttr(cmd) // hide console window on Windows
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Helper's own logging goes to stderr; forward it into our log so
	// netbird client output lands in the app log file.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exePath, err)
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Printf("helper[%s]: %s", name, sc.Text())
		}
	}()

	p := &Proc{ProfileID: profileID, Name: name, cmd: cmd, stdin: stdin, startedAt: time.Now()}

	// Read protocol lines; the first must be ready or error.
	readyCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		got := false
		for sc.Scan() {
			var ev helperEvent
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			switch ev.Event {
			case "ready":
				if !got {
					got = true
					if perr := checkProtocol(ev.Protocol); perr != nil {
						readyCh <- perr
						break
					}
					p.socksAddr = ev.Socks
					readyCh <- nil
				}
			case "status":
				p.mu.Lock()
				p.peers = ev.Peers
				p.mu.Unlock()
			case "info":
				p.mu.Lock()
				p.info = append(json.RawMessage(nil), ev.Info...)
				p.mu.Unlock()
				if got && opts.OnInfo != nil {
					go opts.OnInfo(p)
				}
			case "open_url":
				if opts.OnOpenURL != nil && ev.URL != "" {
					go opts.OnOpenURL(ev.URL)
				}
			case "session":
				if opts.OnSession != nil && ev.Cookie != "" {
					opts.OnSession(ev.Cookie)
				}
			case "auth":
				ch := AuthChallenge{ID: ev.ID, Message: ev.Message, Banner: ev.Banner, Error: ev.Error, Fields: ev.Fields}
				if opts.OnAuth == nil {
					_ = p.Cancel(ch.ID)
					continue
				}
				go opts.OnAuth(p, ch)
			case "error":
				if !got {
					got = true
					if ev.CertPin != "" {
						readyCh <- &CertUntrustedError{Msg: ev.Error, Pin: ev.CertPin, Subject: ev.CertSubject, Changed: ev.CertChanged}
						break
					}
					if ev.Unreachable {
						readyCh <- &UnreachableError{Msg: ev.Error}
						break
					}
					readyCh <- fmt.Errorf("%s", ev.Error)
				} else {
					log.Printf("helper[%s]: error: %s", name, ev.Error)
				}
			}
		}
		if !got {
			readyCh <- fmt.Errorf("helper exited before ready")
		}
	}()

	timeout := readyTimeout
	if opts.ReadyTimeout > 0 {
		timeout = opts.ReadyTimeout
	}
	select {
	case err := <-readyCh:
		if err != nil {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			go func() { _ = cmd.Wait() }() // reap the failed child
			return nil, err
		}
	case <-time.After(timeout):
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		go func() { _ = cmd.Wait() }()
		return nil, fmt.Errorf("helper did not become ready within %s", timeout)
	}

	socksDialer, err := proxy.SOCKS5("tcp", p.socksAddr, nil, proxy.Direct)
	if err != nil {
		stopProc(p)
		return nil, fmt.Errorf("socks dialer: %w", err)
	}
	cd, ok := socksDialer.(proxy.ContextDialer)
	if !ok {
		stopProc(p)
		return nil, fmt.Errorf("socks dialer has no DialContext")
	}
	p.dialer = cd
	return p, nil
}
