package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	sshlayer "ssh-tool/internal/ssh"
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
}

func newMcpState() *mcpState {
	return &mcpState{
		grants:    map[string]mcpGrantLevel{},
		approvals: map[string]chan mcpDecision{},
	}
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
	if !sshlayer.IsReadOnly(command, a.mcpReadOnlyExtra()) {
		name := a.sessionDisplayName(sessionID)
		switch a.requestApproval(sessionID, name, "run", command) {
		case mcpDecisionRun, mcpDecisionType:
			// User approved. (type on a run request is treated as approve-run;
			// the modal only offers run/deny for the run kind, but be lenient.)
		default:
			return "", fmt.Errorf("command denied by user")
		}
	}
	client := sess.TargetClient()
	if client == nil {
		return "", fmt.Errorf("session has no live client")
	}
	out, err := sshlayer.RunCapture(client, command)
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
		return "", fmt.Errorf("typing denied by user")
	}
	if err := sess.Write([]byte(text)); err != nil {
		return "", fmt.Errorf("type: %w", err)
	}
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
