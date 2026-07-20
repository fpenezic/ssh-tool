package share

// Server is the share subsystem's front door: one TLS http.Server bound to a
// user-chosen interface, serving the guest HTML and upgrading /ws to a
// websocket. It owns the registry of active shares (by id and by token), the
// hub, the approval hook, and teardown.
//
// Modelled on internal/ssh/vnc.go's VncBridge, but with the three loopback-only
// shortcuts removed: it binds off-box, so it serves TLS, sets a real origin
// check, and gates every guest behind host approval.

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// tokenTTL bounds how long an UNUSED share token stays valid. A share link is
// sent over chat and may be opened much later than the VNC bridge's 2 minutes.
// A USED token lives until the share stops (a guest can reconnect after a blip).
const tokenTTL = 30 * time.Minute

// ApprovalFunc is called when a guest connects, before anything is streamed. It
// blocks until the host allows/denies (or a timeout denies). remoteIP and the
// fingerprint words are shown to the host. Supplied by the App.
type ApprovalFunc func(shareID, remoteIP, fpWords string) bool

// ShareInfo is the public identity of a share handed to audit hooks (the
// internal shareSession stays private).
type ShareInfo struct {
	ID    string
	Level Level
	Bind  string
}

// AuditHooks hooks the app's audit log. All may be nil in tests.
type AuditHooks struct {
	Attach    func(share ShareInfo, remoteIP string)
	Detach    func(share ShareInfo, remoteIP string, dur time.Duration)
	Approve   func(shareID, remoteIP, fpWords string)
	Deny      func(shareID, remoteIP string)
	Input     func(share ShareInfo, remoteIP, realID string, data []byte)
	Violation func(share ShareInfo, remoteIP, reason string)
	Start     func(share ShareInfo)
	Stop      func(shareID string)
}

// info returns the public snapshot of a shareSession.
func (s *shareSession) info() ShareInfo {
	return ShareInfo{ID: s.id, Level: s.level, Bind: s.bind}
}

// Server owns the HTTPS listener and the share registry.
type Server struct {
	guestFS    fs.FS // serves guest.html + assets (the embedded dist subtree)
	hostName   string
	certPath   string
	keyPath    string
	cert       *Cert
	hub        *Hub
	approve    ApprovalFunc
	audit      AuditHooks
	onChange   func() // emitted after any share mutation (share_changed)
	onGuestTab func(shareID, remoteIP string, index int)

	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	bind    string // "10.0.4.7:8443" once listening
	byID    map[string]*shareSession
	byToken map[string]*shareSession
	conns   map[string][]*guestConn // shareID -> attached conns

	// approval rate limiting (network-facing).
	pendingByIP  map[string]int // remoteIP -> in-flight approvals
	pendingTotal int
	pendingMu    sync.Mutex
}

const (
	maxPendingPerIP = 1
	maxPendingTotal = 3
)

// Config bundles the App-supplied wiring for a Server.
type Config struct {
	GuestFS  fs.FS // the embedded dist subtree serving guest.html + assets
	HostName string
	CertPath string
	KeyPath  string
	Resolve  SessionResolver
	Approve  ApprovalFunc // nil => auto-approve (dev/test)
	Audit    AuditHooks
	OnChange func() // called after any share mutation; nil ok
	// OnGuestTab fires when a guest switches tabs (informational; host UI shows
	// where the guest is looking). nil ok.
	OnGuestTab func(shareID, remoteIP string, index int)
}

// NewServer builds a server from a Config. Nothing binds until Start.
func NewServer(cfg Config) *Server {
	onChange := cfg.OnChange
	if onChange == nil {
		onChange = func() {}
	}
	return &Server{
		guestFS:     cfg.GuestFS,
		hostName:    cfg.HostName,
		certPath:    cfg.CertPath,
		keyPath:     cfg.KeyPath,
		hub:         newHub(cfg.Resolve),
		approve:     cfg.Approve,
		audit:       cfg.Audit,
		onChange:    onChange,
		onGuestTab:  cfg.OnGuestTab,
		byID:        make(map[string]*shareSession),
		byToken:     make(map[string]*shareSession),
		conns:       make(map[string][]*guestConn),
		pendingByIP: make(map[string]int),
	}
}

// StartParams is one ShareStart request from the App.
type StartParams struct {
	BindIP     string          // "10.0.4.7"
	Port       uint16          // 8443
	Level      Level           // read | control
	Scrollback bool            // include history on join
	ActiveTab  int             // index of the host's active tab at share time
	TabsBlob   []byte          // projected {tabs:[...]} JSON from the frontend
	Sessions   []SharedSession // the real sessions behind this share
}

// StartResult is returned to the App / UI.
type StartResult struct {
	ShareID     string      `json:"share_id"`
	URL         string      `json:"url"`
	Bind        string      `json:"bind"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Regenerated bool        `json:"regenerated"` // cert fingerprint changed
}

// Start mints a share, ensures the cert covers the bind IP, brings the listener
// up on the chosen interface, and returns the guest URL. The share id is a
// fresh token-independent uuid handed to the App for later Stop/Kick.
func (s *Server) Start(shareID string, p StartParams) (*StartResult, error) {
	ip := net.ParseIP(p.BindIP)
	if ip == nil {
		return nil, fmt.Errorf("share: invalid bind ip %q", p.BindIP)
	}
	port := p.Port
	if port == 0 {
		port = 8443
	}
	cert, regenerated, err := EnsureFor(s.certPath, s.keyPath, ip)
	if err != nil {
		return nil, err
	}
	// Initial bind guess; register overwrites share.bind with the real one
	// after the (possibly-fallback) listen.
	share := newShareSession(shareID, net.JoinHostPort(p.BindIP, itoa(int(port))),
		s.hostName, p.Level, p.Scrollback, p.ActiveTab, p.TabsBlob, p.Sessions)
	url, err := s.register(share, p.BindIP, port, cert)
	if err != nil {
		return nil, err
	}
	return &StartResult{
		ShareID:     shareID,
		URL:         url,
		Bind:        share.bind, // the actual bind (port may have fallen back)
		Fingerprint: cert.Fingerprint,
		Regenerated: regenerated,
	}, nil
}

// ActiveShares returns the UI snapshot.
func (s *Server) ActiveShares() []ShareStatus { return s.listActive() }

// Publish fans a PTY output chunk to every guest of realID. Called from the
// app's output sink, on the hot path - never blocks. Nil-safe delegation to the
// hub.
func (s *Server) Publish(realID string, data []byte, cum uint64) {
	s.hub.Publish(realID, data, cum)
}

// PublishSize tells guests the host PTY resized.
func (s *Server) PublishSize(realID string, cols, rows uint16) {
	s.hub.PublishSize(realID, cols, rows)
}

// UpdateShare changes a live share's tab layout: a new pane tree (an on-the-fly
// split) and/or new sessions (add-tab). Existing sessions keep their slot so
// their stream isn't interrupted; new sessions get fresh slots, are attached to
// the hub, and get their snapshot. Every attached guest receives a fresh
// manifest (and a snap per new session), then re-renders.
func (s *Server) UpdateShare(shareID string, activeTab int, tabsBlob []byte, sessions []SharedSession) error {
	s.mu.Lock()
	sh := s.byID[shareID]
	conns := append([]*guestConn(nil), s.conns[shareID]...)
	s.mu.Unlock()
	if sh == nil {
		return fmt.Errorf("share: unknown share %s", shareID)
	}

	newSlots := sh.updateTabs(tabsBlob, sessions, activeTab)

	// Attach every guest to the new sessions' hub entries so live output flows.
	for _, slot := range newSlots {
		realID, ok := sh.realForSlot(slot)
		if !ok {
			continue
		}
		for _, c := range conns {
			s.hub.attach(realID, c)
		}
	}

	// Re-manifest every guest, then snapshot EVERY slot (not just new ones):
	// the guest re-renders its tab tree from the fresh manifest, which
	// re-mounts its terminals, so they all need a fresh snapshot to repaint.
	sh.mapMu.RLock()
	allPairs := make([][2]string, 0, len(sh.realBySlot))
	for slot, realID := range sh.realBySlot {
		allPairs = append(allPairs, [2]string{slot, realID})
	}
	sh.mapMu.RUnlock()
	_ = newSlots
	for _, c := range conns {
		s.sendManifest(c, sh)
		for _, p := range allPairs {
			s.sendSnap(c, sh, p[0], p[1])
		}
	}
	go s.onChange()
	return nil
}

// SetActiveTab records the host's active tab index and tells every attached
// guest, so a passive viewer follows along. The index is into the share's own
// projected tab list (the frontend maps its tab id to that index).
func (s *Server) SetActiveTab(shareID string, index int) {
	s.mu.Lock()
	sh := s.byID[shareID]
	conns := append([]*guestConn(nil), s.conns[shareID]...)
	s.mu.Unlock()
	if sh == nil {
		return
	}
	sh.setActiveTab(index)
	for _, c := range conns {
		c.sendJSON(Frame{T: TActiveTab, ActiveTab: &ActiveTab{Index: index}})
	}
}

// Fingerprint returns the current cert's fingerprint, loading/creating it if
// needed (for Settings display before any share is started).
func (s *Server) Fingerprint() (Fingerprint, error) {
	if s.cert != nil {
		return s.cert.Fingerprint, nil
	}
	c, err := LoadOrCreate(s.certPath, s.keyPath)
	if err != nil {
		return Fingerprint{}, err
	}
	s.mu.Lock()
	s.cert = c
	s.mu.Unlock()
	return c.Fingerprint, nil
}

// RegenerateCert forces a fresh cert (Settings -> Regenerate). Invalidates every
// saved fingerprint.
func (s *Server) RegenerateCert() (Fingerprint, error) {
	c, err := Regenerate(s.certPath, s.keyPath)
	if err != nil {
		return Fingerprint{}, err
	}
	s.mu.Lock()
	s.cert = c
	s.mu.Unlock()
	return c.Fingerprint, nil
}

// ensureListening starts the TLS server on bindAddr if it isn't already up on
// that address. Caller holds s.mu. A bind change (new interface) tears the old
// listener down first.
// ensureListening binds on ip, preferring preferredPort. If that port is taken
// (another share, a second app, a leftover) it tries the next few, then falls
// back to an OS-chosen ephemeral port - so a busy 8443 never blocks a share.
// Already listening on this ip returns the existing bind (a share adds to the
// running server; it doesn't move it).
func (s *Server) ensureListening(ip string, preferredPort uint16, cert *Cert) error {
	if s.srv != nil {
		// Already up. If the caller wants the SAME interface, reuse the
		// listener (a second share adds to the running server). If they picked
		// a DIFFERENT interface, we must move: the guest URL is built from the
		// actual bind, so reusing the old listener would hand out a URL on the
		// wrong IP (the bug where switching off the Hyper-V/WSL default did
		// nothing). We can only move when no share is currently bound here -
		// tearing the listener down would kill live guests otherwise.
		if curIP, _, err := net.SplitHostPort(s.bind); err == nil && curIP == ip {
			s.cert = cert
			return nil
		}
		if len(s.byID) > 0 {
			return fmt.Errorf("share: already listening on %s; stop active shares before binding a different interface", s.bind)
		}
		// No active shares: safe to tear the old listener down and rebind.
		_ = s.ln.Close()
		_ = s.srv.Close()
		s.srv = nil
		s.ln = nil
		s.bind = ""
	}
	ln, err := listenPreferred(ip, preferredPort)
	if err != nil {
		return err
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert.TLS},
		MinVersion:   tls.VersionTLS12,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/s/", s.handleGuest)
	// Guest bundle assets (vite emits absolute /assets/*.js|css). Only static
	// files - no token, no session state - so serving them openly is fine; the
	// bundle does nothing until it opens an approved, token-gated websocket.
	mux.Handle("/assets/", http.FileServerFS(s.guestFS))
	srv := &http.Server{Handler: mux, TLSConfig: tlsCfg}
	s.srv = srv
	s.ln = ln
	s.bind = ln.Addr().String() // the ACTUAL bind, in case we fell back
	s.cert = cert
	go srv.ServeTLS(ln, "", "") // cert is in TLSConfig
	return nil
}

// listenPreferred tries ip:preferredPort, then the next few ports, then an
// ephemeral one. Returns the listener bound to whatever it got.
func listenPreferred(ip string, preferredPort uint16) (net.Listener, error) {
	tryPort := func(p uint16) (net.Listener, bool) {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, itoa(int(p))))
		if err != nil {
			return nil, false
		}
		return ln, true
	}
	if preferredPort != 0 {
		for i := 0; i < 10; i++ {
			if ln, ok := tryPort(preferredPort + uint16(i)); ok {
				return ln, nil
			}
		}
	}
	// Ephemeral fallback.
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return nil, fmt.Errorf("share listen %s: %w", ip, err)
	}
	return ln, nil
}

// register adds a share and starts the listener if needed. Returns the guest
// URL. cert must already cover ip. The actual bind (which may differ from
// preferredPort if it was taken) is read back from the listener.
func (s *Server) register(share *shareSession, ip string, preferredPort uint16, cert *Cert) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureListening(ip, preferredPort, cert); err != nil {
		return "", err
	}
	// Reflect the real bind (post-fallback) onto the share so its status shows
	// the port a guest actually connects to.
	share.bind = s.bind
	s.gcLocked()
	s.byID[share.id] = share
	s.byToken[share.token] = share
	if s.audit.Start != nil {
		s.audit.Start(share.info())
	}
	go s.onChange()
	return fmt.Sprintf("https://%s/s/%s", s.bind, share.token), nil
}

// gcLocked drops unused expired tokens. Caller holds s.mu.
func (s *Server) gcLocked() {
	now := time.Now()
	for id, sh := range s.byID {
		if !sh.isUsed() && now.Sub(sh.created) > tokenTTL {
			delete(s.byID, id)
			delete(s.byToken, sh.token)
		}
	}
}

// handleGuest serves GET /s/<token> (the HTML) and /s/<token>/ws (the socket).
func (s *Server) handleGuest(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	token, tail, _ := strings.Cut(rest, "/")

	s.mu.Lock()
	share := s.byToken[token]
	bind := s.bind
	cert := s.cert
	s.mu.Unlock()
	if share == nil {
		http.NotFound(w, r)
		return
	}

	if tail != "ws" {
		s.serveGuestHTML(w, r)
		return
	}
	s.handleWS(w, r, share, bind, cert)
}

// serveGuestHTML returns the guest bundle's HTML. Assets referenced by the HTML
// are served from the same origin by handleAsset.
func (s *Server) serveGuestHTML(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.guestFS, "guest.html")
	if err != nil {
		http.Error(w, "guest page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request, share *shareSession, bind string, cert *Cert) {
	// Origin check: only our own bind host may open the socket. Off-box means
	// we cannot use the VNC bridge's InsecureSkipVerify.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{bind},
	})
	if err != nil {
		return
	}

	remoteIP := clientIP(r)
	gc := newGuestConn(s.hub, share, c, remoteIP)
	gc.auditInput = s.audit.Input
	gc.auditViolation = s.audit.Violation
	gc.onGuestTab = s.onGuestTab

	// Tell the guest we're waiting, with the fingerprint words to compare.
	gc.sendJSON(Frame{T: TPending, Pending: &Pending{
		Host:    s.hostName,
		FpHex:   cert.Fingerprint.Hex,
		FpShort: cert.Fingerprint.Short,
		FpWords: cert.Fingerprint.Words,
	}})
	// The write pump must run so that pending frame actually goes out while we
	// block on approval.
	go gc.writePump()

	// Rate-limit before prompting.
	if !s.acquirePending(remoteIP) {
		_ = c.Close(websocket.StatusTryAgainLater, "too many pending requests")
		gc.cancel()
		return
	}
	allowed := s.runApproval(share, remoteIP, cert.Fingerprint.Words)
	s.releasePending(remoteIP)

	if !allowed {
		if s.audit.Deny != nil {
			s.audit.Deny(share.id, remoteIP)
		}
		gc.kill(ByeDenied)
		return
	}
	if s.audit.Approve != nil {
		s.audit.Approve(share.id, remoteIP, cert.Fingerprint.Words)
	}

	share.markUsed()
	s.attachConn(share, gc)
	if s.audit.Attach != nil {
		s.audit.Attach(share.info(), remoteIP)
	}

	// Send the manifest + per-session snapshots, then start streaming.
	s.sendManifest(gc, share)
	share.mapMu.RLock()
	slotPairs := make([][2]string, 0, len(share.realBySlot))
	for slot, realID := range share.realBySlot {
		slotPairs = append(slotPairs, [2]string{slot, realID})
	}
	share.mapMu.RUnlock()
	for _, p := range slotPairs {
		s.hub.attach(p[1], gc)
		s.sendSnap(gc, share, p[0], p[1])
	}

	go s.onChange()
	gc.readPump() // blocks until the conn ends

	// Teardown.
	s.detachConn(share, gc)
	if s.audit.Detach != nil {
		s.audit.Detach(share.info(), remoteIP, time.Since(gc.joinedAt))
	}
	go s.onChange()
}

func (s *Server) runApproval(share *shareSession, remoteIP, fpWords string) bool {
	if s.approve == nil {
		return true // dev / test: auto-approve
	}
	return s.approve(share.id, remoteIP, fpWords)
}

func (s *Server) sendManifest(gc *guestConn, share *shareSession) {
	// meta / tabsBlob are mutable (add-tab, on-the-fly split), so read them
	// under their locks.
	share.mapMu.RLock()
	sessions := make([]ManifestSession, 0, len(share.meta))
	for slot, m := range share.meta {
		sessions = append(sessions, ManifestSession{
			ID: slot, Name: m.Name, Cols: m.Cols, Rows: m.Rows, State: m.State,
		})
	}
	share.mapMu.RUnlock()

	share.tabsBlobMu.Lock()
	blob := share.tabsBlob
	share.tabsBlobMu.Unlock()

	var tabs []ManifestTab
	_ = unmarshalTabs(blob, &tabs)
	gc.sendJSON(Frame{T: TManifest, Manifest: &Manifest{
		ShareID:   share.id,
		Level:     share.level,
		HostName:  s.hostName,
		ActiveTab: share.getActiveTab(),
		Tabs:      tabs,
		Sessions:  sessions,
	}})
}

func (s *Server) sendSnap(gc *guestConn, share *shareSession, slot, realID string) {
	sess, ok := s.hub.resolve(realID)
	if !ok {
		gc.sendJSON(Frame{T: TSnap, Snap: &Snap{Sid: slot, B64: "", Cum: 0}})
		return
	}
	buf, cum := sess.Scrollback()
	b64 := ""
	if share.scrollback {
		b64 = encodeBase64(buf)
	}
	// When scrollback is off, b64 stays empty but cum carries the watermark so
	// the guest can drop live chunks that predate its join.
	gc.sendJSON(Frame{T: TSnap, Snap: &Snap{Sid: slot, B64: b64, Cum: cum}})
}

// SessionClosed is called from the App's SetOnClose for every session. It tells
// guests the session ended and, if it was a share's last live session, ends the
// whole share.
func (s *Server) SessionClosed(realID string) {
	s.hub.PublishState(realID, "disconnected", "session closed")
	s.mu.Lock()
	var toStop []*shareSession
	for _, sh := range s.byID {
		if sh.slotFor(realID) == "" {
			continue
		}
		if s.allSessionsGoneLocked(sh) {
			toStop = append(toStop, sh)
		}
	}
	s.mu.Unlock()
	for _, sh := range toStop {
		s.stopShare(sh.id, ByeShareStopped)
	}
}

// allSessionsGoneLocked reports whether none of a share's sessions still
// resolve. Caller holds s.mu.
func (s *Server) allSessionsGoneLocked(sh *shareSession) bool {
	sh.mapMu.RLock()
	reals := make([]string, 0, len(sh.slotByReal))
	for realID := range sh.slotByReal {
		reals = append(reals, realID)
	}
	sh.mapMu.RUnlock()
	for _, realID := range reals {
		if _, ok := s.hub.resolve(realID); ok {
			return false
		}
	}
	return true
}

// Stop ends one share and disconnects its guests.
func (s *Server) Stop(shareID string) {
	s.stopShare(shareID, ByeShareStopped)
}

func (s *Server) stopShare(shareID, reason string) {
	s.mu.Lock()
	sh := s.byID[shareID]
	if sh == nil {
		s.mu.Unlock()
		return
	}
	delete(s.byID, shareID)
	delete(s.byToken, sh.token)
	conns := s.conns[shareID]
	delete(s.conns, shareID)
	s.mu.Unlock()

	for _, c := range conns {
		c.kill(reason)
	}
	if s.audit.Stop != nil {
		s.audit.Stop(shareID)
	}
	go s.onChange()
}

// Kick disconnects one guest from a share without stopping the share.
func (s *Server) Kick(shareID, remoteIP string) {
	s.mu.Lock()
	conns := s.conns[shareID]
	s.mu.Unlock()
	for _, c := range conns {
		if c.remoteIP == remoteIP {
			c.kill(ByeRevoked)
		}
	}
	go s.onChange()
}

// StopAll ends every share and closes the listener (app shutdown, or the master
// toggle turned off).
func (s *Server) StopAll() {
	s.mu.Lock()
	shares := make([]string, 0, len(s.byID))
	for id := range s.byID {
		shares = append(shares, id)
	}
	srv := s.srv
	s.srv = nil
	s.bind = ""
	s.mu.Unlock()

	for _, id := range shares {
		s.stopShare(id, ByeAppClosing)
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
}

func (s *Server) attachConn(share *shareSession, c *guestConn) {
	s.mu.Lock()
	s.conns[share.id] = append(s.conns[share.id], c)
	s.mu.Unlock()
}

func (s *Server) detachConn(share *shareSession, c *guestConn) {
	s.mu.Lock()
	cur := s.conns[share.id]
	next := make([]*guestConn, 0, len(cur))
	for _, x := range cur {
		if x != c {
			next = append(next, x)
		}
	}
	if len(next) == 0 {
		delete(s.conns, share.id)
	} else {
		s.conns[share.id] = next
	}
	s.mu.Unlock()
}

func (s *Server) acquirePending(remoteIP string) bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pendingByIP[remoteIP] >= maxPendingPerIP || s.pendingTotal >= maxPendingTotal {
		return false
	}
	s.pendingByIP[remoteIP]++
	s.pendingTotal++
	return true
}

func (s *Server) releasePending(remoteIP string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pendingByIP[remoteIP] > 0 {
		s.pendingByIP[remoteIP]--
		if s.pendingByIP[remoteIP] == 0 {
			delete(s.pendingByIP, remoteIP)
		}
	}
	if s.pendingTotal > 0 {
		s.pendingTotal--
	}
}

// listActive returns a snapshot of active shares + their attached guests for
// the UI. Never exposes the internal live state.
func (s *Server) listActive() []ShareStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ShareStatus, 0, len(s.byID))
	for id, sh := range s.byID {
		guests := make([]GuestStatus, 0)
		for _, c := range s.conns[id] {
			guests = append(guests, GuestStatus{
				RemoteIP: c.remoteIP,
				JoinedAt: c.joinedAt.Unix(),
				Level:    string(sh.level),
			})
		}
		// Real session ids this share covers, so any window (main or detached)
		// can attribute the tab badge without holding the share's local state.
		sh.mapMu.RLock()
		sessionIDs := make([]string, 0, len(sh.slotByReal))
		for realID := range sh.slotByReal {
			sessionIDs = append(sessionIDs, realID)
		}
		sh.mapMu.RUnlock()
		out = append(out, ShareStatus{
			ShareID:    id,
			Level:      string(sh.level),
			Bind:       sh.bind,
			SessionIDs: sessionIDs,
			Guests:     guests,
		})
	}
	return out
}

// ShareStatus / GuestStatus are IPC-friendly snapshots.
type ShareStatus struct {
	ShareID    string        `json:"share_id"`
	Level      string        `json:"level"`
	Bind       string        `json:"bind"`
	SessionIDs []string      `json:"session_ids"`
	Guests     []GuestStatus `json:"guests"`
}

type GuestStatus struct {
	RemoteIP string `json:"remote_ip"`
	JoinedAt int64  `json:"joined_at"`
	Level    string `json:"level"`
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
