package main

// App-side wiring for browser session sharing (internal/share). The share
// server itself is cross-platform Go, but nothing here starts it on mobile -
// see app_share_desktop.go / app_share_mobile.go for the listener lifecycle.
//
// This file holds the IPC surface the frontend calls, the approval gate (a
// clone of the MCP command-approval pattern), the session resolver that lets
// the share package reach both pools without importing them, and the audit
// wiring.

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	shareserver "ssh-tool/internal/share"
	"ssh-tool/internal/store"
)

const shareApprovalTimeout = 2 * time.Minute

func shareCertPath() string { return filepath.Join(store.DataDir(), "share-cert.pem") }
func shareKeyPath() string  { return filepath.Join(store.DataDir(), "share-key.pem") }

// hostDisplayName is what a guest sees as "who is sharing" - user@host, best
// effort. Never a secret.
func hostDisplayName() string {
	host, _ := os.Hostname()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	switch {
	case name != "" && host != "":
		return name + "@" + host
	case host != "":
		return host
	case name != "":
		return name
	default:
		return "ssh-tool"
	}
}

// guestFS returns the embedded dist subtree that serves guest.html + /assets.
// The share server serves these over real HTTP.
func guestFS() (fs.FS, error) {
	return fs.Sub(assets, "frontend/dist")
}

// shareResolver looks a real session id up in the SSH pool first, then the
// local pool, returning it as a share.Sourced. Both *ssh.Session and
// *local.Session satisfy the interface verbatim.
func (a *App) shareResolver(realID string) (shareserver.Sourced, bool) {
	if s, ok := a.pool.Get(realID); ok {
		return s, true
	}
	if s, ok := a.localPool.Get(realID); ok {
		return s, true
	}
	return nil, false
}

// buildShareServer constructs (but does not start) the share server with all
// hooks wired. Called by the desktop listener lifecycle.
func (a *App) buildShareServer() (*shareserver.Server, error) {
	gfs, err := guestFS()
	if err != nil {
		return nil, err
	}
	return shareserver.NewServer(shareserver.Config{
		GuestFS:  gfs,
		HostName: hostDisplayName(),
		CertPath: shareCertPath(),
		KeyPath:  shareKeyPath(),
		Resolve:  a.shareResolver,
		Approve:  a.requestGuestApproval,
		Audit:    a.shareAuditHooks(),
		OnChange: func() { EventsEmit("share_changed", a.shareActiveList()) },
	}), nil
}

// shareSessionClosed is the teardown hook wired into every session's
// SetOnClose. Nil-safe (share may be off).
func (a *App) shareSessionClosed(sessionID string) {
	if a.share != nil {
		a.share.SessionClosed(sessionID)
	}
}

// ----- Approval gate (clone of requestApproval in app_mcp.go) -----

// ShareApprovalRequest is the event payload the frontend renders as a modal.
type ShareApprovalRequest struct {
	ApprovalID  string   `json:"approval_id"`
	ShareID     string   `json:"share_id"`
	RemoteIP    string   `json:"remote_ip"`
	Fingerprint string   `json:"fingerprint"` // the words to compare
	Level       string   `json:"level"`
	Tabs        []string `json:"tabs"`
}

// requestGuestApproval blocks until the host allows/denies the guest, the app
// shuts down, or the 2-minute timeout fires (both default to deny). Supplied to
// the share server as its ApprovalFunc.
func (a *App) requestGuestApproval(shareID, remoteIP, fpWords string) bool {
	id := uuid.NewString()
	ch := make(chan bool, 1) // cap-1: the responder never blocks
	a.shareApprMu.Lock()
	if a.shareApprovals == nil {
		a.shareApprovals = map[string]chan bool{}
	}
	a.shareApprovals[id] = ch
	a.shareApprMu.Unlock()
	defer func() {
		a.shareApprMu.Lock()
		delete(a.shareApprovals, id)
		a.shareApprMu.Unlock()
	}()

	level, tabs := a.shareApprovalContext(shareID)
	EventsEmit("share_approval_request", ShareApprovalRequest{
		ApprovalID:  id,
		ShareID:     shareID,
		RemoteIP:    remoteIP,
		Fingerprint: fpWords,
		Level:       level,
		Tabs:        tabs,
	})
	a.RequestAttention()
	a.SendPromptNotification("Someone wants to join your shared session",
		remoteIP+" is waiting - check the fingerprint before allowing")

	select {
	case ok := <-ch:
		return ok
	case <-a.ctx.Done():
		return false
	case <-time.After(shareApprovalTimeout):
		return false
	}
}

// shareApprovalContext fetches the level + tab titles for a share so the modal
// can show what is being joined.
func (a *App) shareApprovalContext(shareID string) (level string, tabs []string) {
	for _, sh := range a.shareActiveList() {
		if sh.ShareID == shareID {
			return sh.Level, nil // tab titles are in the manifest; keep the modal simple
		}
	}
	return "", nil
}

// ShareApprovalRespond is called by the frontend modal. Anything that isn't
// exactly "allow" denies.
func (a *App) ShareApprovalRespond(approvalID, decision string) error {
	a.shareApprMu.Lock()
	ch, ok := a.shareApprovals[approvalID]
	a.shareApprMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending share approval %s", approvalID)
	}
	ch <- decision == "allow"
	return nil
}

// ----- IPC surface -----

// ShareStartInput is the frontend's request to start a share.
type ShareStartInput struct {
	BindIP     string `json:"bind_ip"`
	Port       uint16 `json:"port"`
	Level      string `json:"level"`      // "read" | "control"
	Scrollback bool   `json:"scrollback"` // include existing history
	// TabsBlob is the frontend-projected {tabs:[...]} JSON (pane trees with
	// sessionIds already rewritten to guest slots). Opaque to the backend.
	TabsBlob string `json:"tabs_blob"`
	// Sessions pairs each guest slot with the real session id + metadata, in
	// the SAME order the slots appear in TabsBlob.
	Sessions []ShareSessionInput `json:"sessions"`
}

type ShareSessionInput struct {
	RealID string `json:"real_id"`
	Name   string `json:"name"`
}

// ShareStart mints a share and returns the guest URL + fingerprint. Errors if
// sharing isn't enabled.
func (a *App) ShareStart(in ShareStartInput) (*shareserver.StartResult, error) {
	if a.share == nil {
		return nil, fmt.Errorf("session sharing is disabled (enable it in Settings -> Sharing)")
	}
	level := shareserver.LevelRead
	if in.Level == string(shareserver.LevelControl) {
		level = shareserver.LevelControl
	}
	sessions := make([]shareserver.SharedSession, 0, len(in.Sessions))
	for _, s := range in.Sessions {
		cols, rows := a.termSize(s.RealID)
		state := "connected"
		if _, ok := a.shareResolver(s.RealID); !ok {
			state = "disconnected"
		}
		sessions = append(sessions, shareserver.SharedSession{
			RealID: s.RealID,
			Name:   s.Name,
			Cols:   cols,
			Rows:   rows,
			State:  state,
		})
	}
	res, err := a.share.Start(uuid.NewString(), shareserver.StartParams{
		BindIP:     in.BindIP,
		Port:       in.Port,
		Level:      level,
		Scrollback: in.Scrollback,
		TabsBlob:   []byte(in.TabsBlob),
		Sessions:   sessions,
	})
	if err != nil {
		return nil, err
	}
	a.recordAudit("share.start", res.ShareID, map[string]string{
		"level": in.Level, "bind": res.Bind,
		"scrollback": boolStr(in.Scrollback), "sessions": fmt.Sprintf("%d", len(sessions)),
	})
	return res, nil
}

// ShareStop ends one share.
func (a *App) ShareStop(shareID string) error {
	if a.share == nil {
		return nil
	}
	a.share.Stop(shareID)
	a.recordAudit("share.stop", shareID, nil)
	return nil
}

// ShareKick disconnects one guest (by remote IP) from a share.
func (a *App) ShareKick(shareID, remoteIP string) error {
	if a.share == nil {
		return nil
	}
	a.share.Kick(shareID, remoteIP)
	a.recordAudit("share.kick", shareID, map[string]string{"remote_ip": remoteIP})
	return nil
}

// ShareActive returns the UI snapshot of active shares + attached guests.
func (a *App) ShareActive() []shareserver.ShareStatus {
	return a.shareActiveList()
}

func (a *App) shareActiveList() []shareserver.ShareStatus {
	if a.share == nil {
		return []shareserver.ShareStatus{}
	}
	return a.share.ActiveShares()
}

// ShareInterfaces returns the bindable network interfaces for the picker.
func (a *App) ShareInterfaces() []shareserver.Interface {
	return shareserver.Interfaces()
}

// ShareFingerprint returns the current cert fingerprint (Settings display).
func (a *App) ShareFingerprint() (shareserver.Fingerprint, error) {
	srv := a.share
	if srv == nil {
		// Build a throwaway to read/create the cert without starting a listener.
		s, err := a.buildShareServer()
		if err != nil {
			return shareserver.Fingerprint{}, err
		}
		return s.Fingerprint()
	}
	return srv.Fingerprint()
}

// ShareRegenerateCert forces a fresh cert. Invalidates every saved fingerprint.
func (a *App) ShareRegenerateCert() (shareserver.Fingerprint, error) {
	srv := a.share
	if srv == nil {
		s, err := a.buildShareServer()
		if err != nil {
			return shareserver.Fingerprint{}, err
		}
		fp, err := s.RegenerateCert()
		if err == nil {
			a.recordAudit("share.cert.regenerate", "", nil)
		}
		return fp, err
	}
	fp, err := srv.RegenerateCert()
	if err == nil {
		a.recordAudit("share.cert.regenerate", "", nil)
	}
	return fp, err
}

// ----- audit wiring -----

// shareAuditOutputEnabled gates persisting guest keystroke CONTENT. Default
// off: audit.db is unsealed plaintext and guest input includes typed passwords.
func (a *App) shareAuditOutputEnabled() bool {
	if a.db == nil {
		return false
	}
	v, _, err := a.db.GetSetting("share_audit_output")
	if err != nil {
		return false
	}
	return v == "1" || v == "true"
}

func (a *App) shareAuditHooks() shareserver.AuditHooks {
	return shareserver.AuditHooks{
		Attach: func(sh shareserver.ShareInfo, remoteIP string) {
			a.recordAudit("share.attach", sh.ID, map[string]string{"remote_ip": remoteIP})
		},
		Detach: func(sh shareserver.ShareInfo, remoteIP string, dur time.Duration) {
			a.recordAudit("share.detach", sh.ID, map[string]string{
				"remote_ip": remoteIP, "duration": dur.Round(time.Second).String(),
			})
		},
		Approve: func(shareID, remoteIP, fpWords string) {
			a.recordAudit("share.approve", shareID, map[string]string{"remote_ip": remoteIP})
		},
		Deny: func(shareID, remoteIP string) {
			a.recordAudit("share.deny", shareID, map[string]string{"remote_ip": remoteIP})
		},
		Input: func(sh shareserver.ShareInfo, remoteIP, realID string, data []byte) {
			meta := map[string]string{"remote_ip": remoteIP, "session_id": realID}
			if a.shareAuditOutputEnabled() {
				meta["content"] = string(data)
			}
			a.recordAudit("share.input", sh.ID, meta)
		},
		Violation: func(sh shareserver.ShareInfo, remoteIP, reason string) {
			a.recordAudit("share.violation", sh.ID, map[string]string{
				"remote_ip": remoteIP, "reason": reason,
			})
		},
	}
}
