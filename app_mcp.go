package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// AppExePath returns the absolute path of the running ssh-tool binary, so the
// Settings page can show the exact `claude mcp add ssh-tool -- <path>
// --mcp-bridge` registration command. Empty on error.
func (a *App) AppExePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// AppWslExePath returns the running binary's path translated to the form a WSL
// shell uses to reach it (C:\Users\x\ssh-tool.exe -> /mnt/c/Users/x/ssh-tool.exe),
// so a WSL-hosted LLM client can be pointed at the Windows binary. Empty on any
// platform other than Windows, or on an unrecognisable path - the Settings page
// only shows the WSL block when this is non-empty. Pure string translation; no
// WSL is invoked and none is required.
func (a *App) AppWslExePath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return windowsPathToWSL(p)
}

// windowsPathToWSL maps a `C:\dir\file` path to `/mnt/c/dir/file`. Returns ""
// if the input isn't a drive-letter absolute path.
func windowsPathToWSL(p string) string {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return ""
	}
	drive := strings.ToLower(string(p[0]))
	rest := strings.ReplaceAll(p[2:], "\\", "/")
	return "/mnt/" + drive + rest
}

// MCP bridge: lets an external LLM (Claude Code, etc.) inspect and drive live
// SSH sessions the user has explicitly shared. Off by default; the socket
// listener (app_mcp_desktop.go) only runs when the mcp_bridge_enabled setting
// is on. Reads are safe (scrollback, allowlisted commands); writes are gated
// by an approval modal that mirrors the host-key TOFU flow (see gotcha #9).
//
// Security posture (load-bearing):
//   - No session is reachable until the user shares it (per-session grant).
//   - Terminal scrollback handed to the LLM is untrusted DATA - a tool result,
//     never an instruction channel. The LLM reading "run X" does not run X;
//     only a run/type tool call does, and that is allowlisted-or-gated.
//   - The bridge surface is sessions only: no vault, credentials, or config.

// mcpGrantLevel is the access a shared session grants the LLM.
type mcpGrantLevel string

const (
	mcpGrantNone     mcpGrantLevel = ""
	mcpGrantReadOnly mcpGrantLevel = "read"      // scrollback + allowlisted reads
	mcpGrantReadRun  mcpGrantLevel = "read-run"  // adds gated exec + type
)

// mcpApprovalTimeout bounds how long a gated command waits for the user before
// defaulting to deny. Same value as the host-key challenge.
const mcpApprovalTimeout = 2 * time.Minute

// mcpDecision is the user's response to an approval request.
type mcpDecision string

const (
	mcpDecisionRun  mcpDecision = "run"  // execute via side channel
	mcpDecisionType mcpDecision = "type" // inject into the live PTY, no newline
	mcpDecisionDeny mcpDecision = "deny"
)

// mcpState holds the bridge's in-memory state. Grants are NOT persisted -
// re-share each run; keeps the blast radius small and means a stale grant can
// never outlive the process.
type mcpState struct {
	mu     sync.Mutex
	grants map[string]mcpGrantLevel // sessionID -> level

	approvalsMu sync.Mutex
	approvals   map[string]chan mcpDecision // approvalID -> response channel

	// activity is a bounded ring of what the LLM did, newest last. Feeds the
	// live LLM-activity panel; a copy is also written to audit.db when the
	// mcp_audit_enabled setting is on. In-memory so the panel works even
	// without persistence; capped so a chatty LLM can't grow it unbounded.
	activityMu sync.Mutex
	activity   []McpActivity
	activitySeq int64
}

// mcpActivityCap bounds the in-memory activity ring.
const mcpActivityCap = 500

// mcpActivityOutputCap bounds how much command output is retained per entry
// (both in memory and in audit.db) so a huge journalctl dump can't bloat state.
const mcpActivityOutputCap = 16 * 1024

func newMcpState() *mcpState {
	return &mcpState{
		grants:    map[string]mcpGrantLevel{},
		approvals: map[string]chan mcpDecision{},
	}
}

// McpActivity is one recorded LLM action, surfaced to the activity panel.
type McpActivity struct {
	Seq       int64  `json:"seq"`
	TS        int64  `json:"ts"` // unix seconds
	SessionID string `json:"session_id"`
	Session   string `json:"session"` // friendly name at the time
	Kind      string `json:"kind"`    // run | type | connect | read
	Command   string `json:"command"` // command / typed text / connection label
	Output    string `json:"output,omitempty"`
	Exit      string `json:"exit,omitempty"` // "ok" | "error" | "" (n/a)
	Gate      string `json:"gate"`           // auto | approved | denied | n/a
}

// recordActivity appends an entry to the ring (and audit.db when enabled). It
// never blocks the caller on a persistence error. Output is capped.
func (a *App) recordActivity(e McpActivity) {
	if len(e.Output) > mcpActivityOutputCap {
		e.Output = e.Output[:mcpActivityOutputCap] + "\n...[truncated]"
	}
	e.TS = time.Now().Unix()

	a.mcp.activityMu.Lock()
	a.mcp.activitySeq++
	e.Seq = a.mcp.activitySeq
	a.mcp.activity = append(a.mcp.activity, e)
	if len(a.mcp.activity) > mcpActivityCap {
		a.mcp.activity = a.mcp.activity[len(a.mcp.activity)-mcpActivityCap:]
	}
	a.mcp.activityMu.Unlock()

	// Live panel refresh.
	EventsEmit("mcp_activity", e)

	// Optional durable copy.
	if a.mcpAuditEnabled() {
		a.recordAudit("mcp_"+e.Kind, e.Session, map[string]string{
			"session_id": e.SessionID,
			"command":    e.Command,
			"exit":       e.Exit,
			"gate":       e.Gate,
			"output":     e.Output,
		})
	}
}

// mcpAuditEnabled reports whether LLM activity is persisted to audit.db
// (default true when unset - it's a security record).
func (a *App) mcpAuditEnabled() bool {
	if a.db == nil {
		return false
	}
	v, ok, err := a.db.GetSetting("mcp_audit_enabled")
	if err != nil || !ok || v == "" {
		return true
	}
	return v == "1" || v == "true"
}

// McpActivityList returns the recorded activity (newest first), optionally
// filtered to one session. Used by the activity panel.
func (a *App) McpActivityList(sessionID string) []McpActivity {
	a.mcp.activityMu.Lock()
	defer a.mcp.activityMu.Unlock()
	out := make([]McpActivity, 0, len(a.mcp.activity))
	for i := len(a.mcp.activity) - 1; i >= 0; i-- {
		e := a.mcp.activity[i]
		if sessionID != "" && e.SessionID != sessionID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ----- Grant management (frontend IPC) -----

// McpGrantInfo is the IPC-friendly view of a shared session.
type McpGrantInfo struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	Level     string `json:"level"`
}

// McpShareSession grants the LLM access to a live session at the given level
// ("read" or "read-run"). Sharing a session that isn't in the pool is
// rejected so a grant can't dangle without a session.
func (a *App) McpShareSession(sessionID, level string) error {
	if _, ok := a.pool.Get(sessionID); !ok {
		return fmt.Errorf("session not connected")
	}
	lvl := mcpGrantLevel(level)
	if lvl != mcpGrantReadOnly && lvl != mcpGrantReadRun {
		return fmt.Errorf("level must be read or read-run")
	}
	a.mcp.mu.Lock()
	a.mcp.grants[sessionID] = lvl
	a.mcp.mu.Unlock()
	EventsEmit("mcp_grants_changed", a.McpListGrants())
	return nil
}

// McpUnshareSession revokes a session's grant.
func (a *App) McpUnshareSession(sessionID string) error {
	a.mcp.mu.Lock()
	delete(a.mcp.grants, sessionID)
	a.mcp.mu.Unlock()
	EventsEmit("mcp_grants_changed", a.McpListGrants())
	return nil
}

// McpListGrants returns every currently-shared session with its level.
func (a *App) McpListGrants() []McpGrantInfo {
	a.mcp.mu.Lock()
	ids := make([]string, 0, len(a.mcp.grants))
	levels := map[string]mcpGrantLevel{}
	for id, lvl := range a.mcp.grants {
		ids = append(ids, id)
		levels[id] = lvl
	}
	a.mcp.mu.Unlock()

	out := []McpGrantInfo{}
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	for _, id := range ids {
		info := McpGrantInfo{SessionID: id, Level: string(levels[id])}
		if meta, ok := a.sessionMeta[id]; ok {
			info.Name = meta.name
			info.Hostname = meta.hostname
		}
		out = append(out, info)
	}
	return out
}

// grantLevel returns the current grant for a session (mcpGrantNone if not
// shared). Used by the tool handlers to authorise each call.
func (a *App) grantLevel(sessionID string) mcpGrantLevel {
	a.mcp.mu.Lock()
	defer a.mcp.mu.Unlock()
	return a.mcp.grants[sessionID]
}

// clearMcpGrant drops any grant for a session. Wired into the session-close
// teardown so a grant never outlives its session.
func (a *App) clearMcpGrant(sessionID string) {
	a.mcp.mu.Lock()
	_, had := a.mcp.grants[sessionID]
	delete(a.mcp.grants, sessionID)
	a.mcp.mu.Unlock()
	if had {
		EventsEmit("mcp_grants_changed", a.McpListGrants())
	}
}

// ----- Approval gate (frontend IPC) -----

// McpApprovalRequest is the event payload the frontend renders as a modal.
type McpApprovalRequest struct {
	ApprovalID  string `json:"approval_id"`
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	Kind        string `json:"kind"`    // "run" | "type"
	Command     string `json:"command"` // exact text that will run / be typed
}

// requestApproval emits an approval request and blocks until the user responds,
// the timeout fires, or the app shuts down (both default to deny). Mirrors the
// host-key challenge flow (app.go makeHostKeyCallback).
func (a *App) requestApproval(sessionID, sessionName, kind, command string) mcpDecision {
	id := uuid.NewString()
	ch := make(chan mcpDecision, 1)
	a.mcp.approvalsMu.Lock()
	a.mcp.approvals[id] = ch
	a.mcp.approvalsMu.Unlock()
	defer func() {
		a.mcp.approvalsMu.Lock()
		delete(a.mcp.approvals, id)
		a.mcp.approvalsMu.Unlock()
	}()

	EventsEmit("mcp_approval_request", McpApprovalRequest{
		ApprovalID:  id,
		SessionID:   sessionID,
		SessionName: sessionName,
		Kind:        kind,
		Command:     command,
	})

	select {
	case d := <-ch:
		return d
	case <-a.ctx.Done():
		return mcpDecisionDeny
	case <-time.After(mcpApprovalTimeout):
		return mcpDecisionDeny
	}
}

// McpApprovalRespond is called by the frontend modal. decision is
// "run" | "type" | "deny".
func (a *App) McpApprovalRespond(approvalID, decision string) error {
	a.mcp.approvalsMu.Lock()
	ch, ok := a.mcp.approvals[approvalID]
	a.mcp.approvalsMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending approval %s", approvalID)
	}
	d := mcpDecision(decision)
	if d != mcpDecisionRun && d != mcpDecisionType && d != mcpDecisionDeny {
		d = mcpDecisionDeny
	}
	ch <- d
	return nil
}

// ----- Tool handlers (called by the MCP server per accepted connection) -----

// mcpSessionInfo is what list_sessions returns to the LLM.
type mcpSessionInfo struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	Level     string `json:"level"`
}

// mcpListSessions returns only the sessions the user has shared.
func (a *App) mcpListSessions() []mcpSessionInfo {
	grants := a.McpListGrants()
	out := make([]mcpSessionInfo, 0, len(grants))
	for _, g := range grants {
		out = append(out, mcpSessionInfo{
			SessionID: g.SessionID, Name: g.Name,
			Hostname: g.Hostname, Level: g.Level,
		})
	}
	return out
}

// mcpReadTerminal returns the tail of the session's scrollback, capped. Only
// for shared sessions. The returned string is UNTRUSTED host output - the
// caller frames it as data, not instructions.
func (a *App) mcpReadTerminal(sessionID string, maxBytes int) (string, error) {
	if a.grantLevel(sessionID) == mcpGrantNone {
		return "", fmt.Errorf("session not shared with the LLM")
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not connected")
	}
	data, _ := sess.Scrollback()
	if maxBytes <= 0 || maxBytes > len(data) {
		maxBytes = len(data)
	}
	if maxBytes < len(data) {
		data = data[len(data)-maxBytes:]
	}
	// Record the read (without storing the scrollback itself - it's large and
	// the user can already see it in the terminal; the point is to log that the
	// LLM looked).
	a.recordActivity(McpActivity{
		SessionID: sessionID, Session: a.sessionDisplayName(sessionID),
		Kind: "read", Command: "read terminal scrollback", Gate: "auto",
	})
	return string(data), nil
}

// mcpReadOnlyExtra reads the user's extra read-only allowlist tokens.
func (a *App) mcpReadOnlyExtra() []string {
	raw := a.SettingsGet("mcp_readonly_extra")
	if raw == "" {
		return nil
	}
	return strings.Fields(raw)
}

// mcpRun runs a command on the session's target client via a side channel,
// gated by the allowlist / approval. Requires a read-run grant. Returns the
// combined output.
func (a *App) mcpRun(sessionID, command string) (string, error) {
	if a.grantLevel(sessionID) != mcpGrantReadRun {
		return "", fmt.Errorf("session not shared for running commands")
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not connected")
	}
	name := a.sessionDisplayName(sessionID)
	gate := "auto"
	if !sshlayer.IsReadOnly(command, a.mcpReadOnlyExtra()) {
		switch a.requestApproval(sessionID, name, "run", command) {
		case mcpDecisionRun, mcpDecisionType:
			gate = "approved"
		default:
			a.recordActivity(McpActivity{
				SessionID: sessionID, Session: name, Kind: "run",
				Command: command, Gate: "denied",
			})
			return "", fmt.Errorf("command denied by user")
		}
	}
	client := sess.TargetClient()
	if client == nil {
		return "", fmt.Errorf("session has no live client")
	}
	out, err := sshlayer.RunCapture(client, command)
	exit := "ok"
	if err != nil {
		exit = "error"
	}
	a.recordActivity(McpActivity{
		SessionID: sessionID, Session: name, Kind: "run",
		Command: command, Output: out, Exit: exit, Gate: gate,
	})
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("run: %w", err)
	}
	return out, nil
}

// mcpType asks the user to approve injecting text into the live PTY, then (on
// approve) writes it WITHOUT a trailing newline so it sits at the prompt for
// the user to review and press Enter. Requires a read-run grant.
func (a *App) mcpType(sessionID, text string) (string, error) {
	if a.grantLevel(sessionID) != mcpGrantReadRun {
		return "", fmt.Errorf("session not shared for running commands")
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not connected")
	}
	name := a.sessionDisplayName(sessionID)
	decision := a.requestApproval(sessionID, name, "type", text)
	if decision != mcpDecisionType && decision != mcpDecisionRun {
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: name, Kind: "type",
			Command: text, Gate: "denied",
		})
		return "", fmt.Errorf("typing denied by user")
	}
	if err := sess.Write([]byte(text)); err != nil {
		return "", fmt.Errorf("type: %w", err)
	}
	a.recordActivity(McpActivity{
		SessionID: sessionID, Session: name, Kind: "type",
		Command: text, Gate: "approved",
	})
	return "typed into terminal (no newline sent; user must press Enter)", nil
}

// sessionDisplayName returns a friendly label for a session (name or hostname
// or the id), for approval prompts.
func (a *App) sessionDisplayName(sessionID string) string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if meta, ok := a.sessionMeta[sessionID]; ok {
		if meta.name != "" {
			return meta.name
		}
		if meta.hostname != "" {
			return meta.hostname
		}
	}
	return sessionID
}

// ----- Connection search + connect (find and open a session) -----

// mcpConnectionInfo is what list_connections returns to the LLM. Deliberately
// SHARE-MINIMAL: name + folder path only, no hostname / port / user / tags /
// notes, so searching doesn't leak infrastructure detail before a connect.
type mcpConnectionInfo struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Folder       string `json:"folder"` // "/"-joined folder path, "" at root
}

// mcpListConnections returns saved connections whose name or folder path
// matches query (case-insensitive substring; empty query returns all). Only
// exposed when the bridge is enabled. Connections flagged Sensitive are
// omitted entirely - they never surface to the LLM.
func (a *App) mcpListConnections(query string) ([]mcpConnectionInfo, error) {
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	paths := a.folderPathIndex()
	q := strings.ToLower(strings.TrimSpace(query))
	out := []mcpConnectionInfo{}
	for _, c := range conns {
		if c.Sensitive {
			continue // never expose sensitive connections to the LLM
		}
		folder := ""
		if c.FolderID != nil {
			folder = paths[*c.FolderID]
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(c.Name), q) &&
			!strings.Contains(strings.ToLower(folder), q) {
			continue
		}
		out = append(out, mcpConnectionInfo{
			ConnectionID: c.ID, Name: c.Name, Folder: folder,
		})
	}
	return out, nil
}

// folderPathIndex builds folderID -> "/"-joined path (e.g. "prod/db"). Cheap
// enough to recompute per call; the tree is small.
func (a *App) folderPathIndex() map[string]string {
	folders, err := a.db.ListFolders()
	if err != nil {
		return map[string]string{}
	}
	byID := map[string]store.Folder{}
	for _, f := range folders {
		byID[f.ID] = f
	}
	cache := map[string]string{}
	var path func(id string, depth int) string
	path = func(id string, depth int) string {
		if p, ok := cache[id]; ok {
			return p
		}
		f, ok := byID[id]
		if !ok || depth > 64 { // guard against a cycle in bad data
			return ""
		}
		var p string
		if f.ParentID != nil && *f.ParentID != "" {
			parent := path(*f.ParentID, depth+1)
			if parent != "" {
				p = parent + "/" + f.Name
			} else {
				p = f.Name
			}
		} else {
			p = f.Name
		}
		cache[id] = p
		return p
	}
	for id := range byID {
		cache[id] = path(id, 0)
	}
	return cache
}

// mcpConnect opens a session for connectionID after the user approves, then
// auto-shares that session with the LLM at the given level so the LLM can
// immediately work on it. Requires the bridge enabled; the approval prompt is
// the gate (opening a session spends credentials and may trigger a host-key
// prompt). A Sensitive connection is refused.
func (a *App) mcpConnect(connectionID, level string) (string, error) {
	lvl := mcpGrantLevel(level)
	if lvl != mcpGrantReadOnly && lvl != mcpGrantReadRun {
		lvl = mcpGrantReadRun // default to the more useful level
	}
	conn, err := a.db.GetConnection(connectionID)
	if err != nil || conn == nil {
		return "", fmt.Errorf("connection not found")
	}
	if conn.Sensitive {
		return "", fmt.Errorf("connection is marked sensitive and cannot be opened by the LLM")
	}

	folder := ""
	if conn.FolderID != nil {
		folder = a.folderPathIndex()[*conn.FolderID]
	}
	label := conn.Name
	if folder != "" {
		label = folder + "/" + conn.Name
	}
	if a.requestApproval("", label, "connect", label) != mcpDecisionRun {
		a.recordActivity(McpActivity{
			Kind: "connect", Session: label, Command: label, Gate: "denied",
		})
		return "", fmt.Errorf("connect denied by user")
	}

	res, err := a.SshConnect(connectionID)
	if err != nil {
		a.recordActivity(McpActivity{
			Kind: "connect", Session: label, Command: label,
			Gate: "approved", Exit: "error", Output: err.Error(),
		})
		return "", fmt.Errorf("connect: %w", err)
	}
	// Auto-share the freshly opened session so the LLM can act on it. Ignore a
	// share error (session exists; sharing is best-effort convenience).
	_ = a.McpShareSession(res.SessionID, string(lvl))
	a.recordActivity(McpActivity{
		SessionID: res.SessionID, Session: label, Kind: "connect",
		Command: label, Gate: "approved", Exit: "ok",
	})

	// The frontend normally creates the terminal tab itself right after its own
	// SshConnect call (see DetailPane). A session opened headlessly by the MCP
	// bridge bypasses that, so the app would hold a live session with no tab.
	// Emit an event the frontend listens for to add the tab + switch to it.
	hostname := ""
	if conn.Hostname != "" {
		hostname = conn.Hostname
	}
	EventsEmit("mcp_session_opened", map[string]string{
		"session_id":    res.SessionID,
		"connection_id": connectionID,
		"name":          conn.Name,
		"hostname":      hostname,
	})

	return fmt.Sprintf("connected: session_id=%s (%s), shared at level=%s",
		res.SessionID, label, lvl), nil
}
