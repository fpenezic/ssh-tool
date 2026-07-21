package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"ssh-tool/internal/store"
)

// LLM bulk provisioning: plan-then-commit.
//
// The create_* MCP tools do NOT write. They append typed entries to an
// in-memory pending plan (mcpPlan) with LLM-facing temp ids, so a later entry
// can reference an earlier one (a forward references a connection created
// earlier in the same plan). commit_plan renders the whole plan to a rich
// app-side approval modal; on approve it writes everything in ONE transaction
// (all-or-nothing). Nothing here ever sets a secret: a connection or its
// inline jump host can only reference an EXISTING vault credential by id
// (auth_ref), never carry a password.

// planRef points at either a plan-local staged entry (Temp) or an already-
// existing store row (Existing). Exactly one is non-empty.
type planRef struct {
	Temp     string
	Existing string
}

func (r planRef) empty() bool { return r.Temp == "" && r.Existing == "" }

// parsePlanRef splits a ref string the LLM passed. A "tmp:" prefix marks a
// plan-local temp id; anything else is treated as an existing store id.
func parsePlanRef(s string) planRef {
	s = strings.TrimSpace(s)
	if s == "" {
		return planRef{}
	}
	if rest, ok := strings.CutPrefix(s, "tmp:"); ok {
		return planRef{Temp: rest}
	}
	return planRef{Existing: s}
}

// folderSettingsInput carries the inheritable defaults set_folder_settings puts
// on a folder so its connections inherit them (jump host, credential, network
// profile, port, user, initial command). All optional; only non-zero fields are
// applied. NEVER a secret - authRef/jumpAuthRef are existing credential ids.
type folderSettingsInput struct {
	User             string
	Port             uint16
	AuthRef          string
	NetworkProfileID string
	InitialCommand   string
	JumpHost         string
	JumpUser         string
	JumpPort         uint16
	JumpAuthRef      string
}

func (f folderSettingsInput) empty() bool {
	return f.User == "" && f.Port == 0 && f.AuthRef == "" && f.NetworkProfileID == "" &&
		f.InitialCommand == "" && f.JumpHost == "" && f.JumpUser == "" && f.JumpPort == 0 && f.JumpAuthRef == ""
}

type planFolder struct {
	TempID   string
	Name     string
	Parent   planRef // temp id, existing folder id, or empty (root)
	Settings *folderSettingsInput
}

// planFolderSettings sets inheritable defaults on an EXISTING folder (by id).
type planFolderSettings struct {
	FolderID string
	Settings folderSettingsInput
}

type planJump struct {
	Host    string
	User    string
	Port    uint16 // 0 = default
	AuthRef string // existing vault credential id, or ""
}

type planConn struct {
	TempID           string
	Name             string
	Host             string
	Port             uint16 // 0 = default
	User             string
	Folder           planRef // temp id, existing folder id, or empty (root)
	AuthRef          string  // existing vault credential id, or ""
	NetworkProfileID string  // existing network profile id, or ""
	Jump             *planJump
	InitialCommand   string
	Tags             []string
}

type planForward struct {
	TempID     string
	Conn       planRef // temp id or existing connection id
	Kind       string  // local | remote | dynamic
	LocalAddr  string
	LocalPort  uint16
	RemoteHost string
	RemotePort uint16
	AutoStart  bool
	Desc       string
}

type planBookmarks struct {
	Forward   planRef // temp id or existing forward id (dynamic only)
	Bookmarks []store.ProxyBookmark
}

// mcpPlan is the ordered staging buffer built by the create_* tools.
type mcpPlan struct {
	folders        []planFolder
	folderSettings []planFolderSettings // settings on EXISTING folders
	conns          []planConn
	forwards       []planForward
	bookmarks      []planBookmarks
}

// getOrInitPlan returns the current plan, creating an empty one if none is in
// progress. Caller holds planMu.
func (a *App) getOrInitPlan() *mcpPlan {
	if a.mcp.plan == nil {
		a.mcp.plan = &mcpPlan{}
	}
	return a.mcp.plan
}

func newTempID() string { return uuid.NewString()[:8] }

// ----- Plan-building (called by the MCP tools) -----

func (a *App) planAddFolder(name, parent string) (string, error) {
	if !a.mcpManageAllowed() {
		return "", errManageOff
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("folder name required")
	}
	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	id := newTempID()
	p.folders = append(p.folders, planFolder{
		TempID: id, Name: name, Parent: parsePlanRef(parent),
	})
	return id, nil
}

// planSetFolderSettings stages inheritable defaults on a folder. folder is a
// tmp: temp id (a folder staged earlier in this plan) or an existing folder id.
func (a *App) planSetFolderSettings(folder string, s folderSettingsInput) error {
	if !a.mcpManageAllowed() {
		return errManageOff
	}
	ref := parsePlanRef(folder)
	if ref.empty() {
		return fmt.Errorf("folder ref required")
	}
	if s.empty() {
		return fmt.Errorf("no settings given")
	}
	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	if ref.Temp != "" {
		// Attach to the staged folder.
		for i := range p.folders {
			if p.folders[i].TempID == ref.Temp {
				sc := s
				p.folders[i].Settings = &sc
				return nil
			}
		}
		return fmt.Errorf("no staged folder with ref %q", ref.Temp)
	}
	// Existing folder: overwrite any prior staged settings for the same id.
	for i := range p.folderSettings {
		if p.folderSettings[i].FolderID == ref.Existing {
			p.folderSettings[i].Settings = s
			return nil
		}
	}
	p.folderSettings = append(p.folderSettings, planFolderSettings{FolderID: ref.Existing, Settings: s})
	return nil
}

// toInheritable builds an InheritableSettings from folder-settings input. Only
// non-zero fields are set (nil = inherit further up / unset).
func (f folderSettingsInput) toInheritable() store.InheritableSettings {
	ov := store.InheritableSettings{}
	if f.User != "" {
		u := f.User
		ov.Username = &u
	}
	if f.Port != 0 {
		pt := f.Port
		ov.Port = &pt
	}
	if f.AuthRef != "" {
		ar := f.AuthRef
		ov.AuthRef = &ar
	}
	if f.NetworkProfileID != "" {
		npid := f.NetworkProfileID
		ov.NetworkProfileID = &npid
	}
	if strings.TrimSpace(f.InitialCommand) != "" {
		ic := f.InitialCommand
		ov.InitialCommand = &ic
	}
	if f.JumpHost != "" {
		spec := store.JumpHostSpec{Hostname: f.JumpHost}
		if f.JumpUser != "" {
			u := f.JumpUser
			spec.Username = &u
		}
		if f.JumpPort != 0 {
			jp := f.JumpPort
			spec.Port = &jp
		}
		if f.JumpAuthRef != "" {
			ar := f.JumpAuthRef
			spec.AuthRef = &ar
		}
		ov.JumpHost = &store.JumpHostOverride{Kind: "chain", Chain: &spec}
	}
	return ov
}

// planConnInput is the flat input the create_connection tool passes in.
type planConnInput struct {
	Name             string
	Host             string
	Port             uint16
	User             string
	Folder           string
	AuthRef          string
	NetworkProfileID string
	JumpHost         string
	JumpUser         string
	JumpPort         uint16
	JumpAuthRef      string
	InitialCommand   string
	Tags             []string
}

func (a *App) planAddConnection(in planConnInput) (string, error) {
	if !a.mcpManageAllowed() {
		return "", errManageOff
	}
	name := strings.TrimSpace(in.Name)
	host := strings.TrimSpace(in.Host)
	if name == "" {
		return "", fmt.Errorf("connection name required")
	}
	if host == "" {
		return "", fmt.Errorf("connection host required")
	}
	c := planConn{
		TempID:           newTempID(),
		Name:             name,
		Host:             host,
		Port:             in.Port,
		User:             strings.TrimSpace(in.User),
		Folder:           parsePlanRef(in.Folder),
		AuthRef:          strings.TrimSpace(in.AuthRef),
		NetworkProfileID: strings.TrimSpace(in.NetworkProfileID),
		InitialCommand:   in.InitialCommand,
		Tags:             in.Tags,
	}
	if jh := strings.TrimSpace(in.JumpHost); jh != "" {
		c.Jump = &planJump{
			Host: jh, User: strings.TrimSpace(in.JumpUser),
			Port: in.JumpPort, AuthRef: strings.TrimSpace(in.JumpAuthRef),
		}
	}
	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	p.conns = append(p.conns, c)
	return c.TempID, nil
}

func (a *App) planAddForward(connection, kind, localAddr string, localPort uint16, remoteHost string, remotePort uint16, autoStart bool, desc string) (string, error) {
	if !a.mcpManageAllowed() {
		return "", errManageOff
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind != "local" && kind != "remote" && kind != "dynamic" {
		return "", fmt.Errorf("kind must be local, remote or dynamic")
	}
	ref := parsePlanRef(connection)
	if ref.empty() {
		return "", fmt.Errorf("connection ref required")
	}
	if kind != "dynamic" && (strings.TrimSpace(remoteHost) == "" || remotePort == 0) {
		return "", fmt.Errorf("%s forward needs remote_host and remote_port", kind)
	}
	// A dynamic (SOCKS5) forward always binds an OS-assigned random local port
	// (persisted as 0 = auto; the SSH layer reads the real port at start). No
	// point pinning a fixed port for a proxy the user reaches via bookmarks, so
	// ignore any local_port the LLM sent for dynamic forwards.
	if kind == "dynamic" {
		localPort = 0
	}
	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	id := newTempID()
	p.forwards = append(p.forwards, planForward{
		TempID: id, Conn: ref, Kind: kind,
		LocalAddr: strings.TrimSpace(localAddr), LocalPort: localPort,
		RemoteHost: strings.TrimSpace(remoteHost), RemotePort: remotePort,
		AutoStart: autoStart, Desc: desc,
	})
	return id, nil
}

func (a *App) planSetBookmarks(forward string, bookmarks []store.ProxyBookmark) error {
	if !a.mcpManageAllowed() {
		return errManageOff
	}
	ref := parsePlanRef(forward)
	if ref.empty() {
		return fmt.Errorf("forward ref required")
	}
	for _, b := range bookmarks {
		if strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.URL) == "" {
			return fmt.Errorf("each bookmark needs a name and a url")
		}
	}
	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	p.bookmarks = append(p.bookmarks, planBookmarks{Forward: ref, Bookmarks: bookmarks})
	return nil
}

// planDiscard drops the pending plan without writing.
func (a *App) planDiscard() {
	a.mcp.planMu.Lock()
	a.mcp.plan = nil
	a.mcp.planMu.Unlock()
}

var errManageOff = fmt.Errorf("the manage grant is off; the user must enable \"Allow manage\" in the LLM Share popover before you can create connections")

// ----- Preview (for the approval modal) -----

// McpPlanPreview is the rich, human-readable render of a pending plan the
// approval modal shows. Credentials and network profiles are resolved to
// NAMES here (never secrets) so the user sees what they are approving.
type McpPlanPreview struct {
	ApprovalID  string                 `json:"approval_id"`
	Folders     []McpPlanFolderPreview `json:"folders"`
	Connections []McpPlanConnPreview   `json:"connections"`
	Warnings    []string               `json:"warnings"`
	Counts      McpPlanCounts          `json:"counts"`
}

type McpPlanCounts struct {
	Folders     int `json:"folders"`
	Connections int `json:"connections"`
	Forwards    int `json:"forwards"`
	Bookmarks   int `json:"bookmarks"`
}

type McpPlanFolderPreview struct {
	Name     string   `json:"name"`
	Parent   string   `json:"parent"`             // resolved path or "(root)" or "(new: <name>)"
	Defaults []string `json:"defaults,omitempty"` // inherited settings lines (cred by NAME)
}

type McpPlanConnPreview struct {
	Name           string                  `json:"name"`
	Target         string                  `json:"target"` // user@host:port
	Folder         string                  `json:"folder"`
	Credential     string                  `json:"credential"`      // name or ""
	Via            string                  `json:"via"`             // bastion "user@host" or ""
	NetworkProfile string                  `json:"network_profile"` // name or ""
	InitialCommand string                  `json:"initial_command"`
	Forwards       []McpPlanForwardPreview `json:"forwards"`
}

type McpPlanForwardPreview struct {
	Kind      string   `json:"kind"`
	Detail    string   `json:"detail"`
	Bookmarks []string `json:"bookmarks"` // "name -> url"
}

// buildPlanPreview renders the pending plan into a preview and collects any
// validation warnings (unknown credential/profile ids, dangling refs). It does
// NOT hold planMu across resolution (it reads store), so callers snapshot the
// plan first.
func (a *App) buildPlanPreview(p *mcpPlan) McpPlanPreview {
	pv := McpPlanPreview{
		Counts: McpPlanCounts{
			Folders: len(p.folders), Connections: len(p.conns),
			Forwards: len(p.forwards),
		},
	}
	warn := func(s string) { pv.Warnings = append(pv.Warnings, s) }

	// Resolve credential + network-profile names once.
	credNames := map[string]string{}
	if creds, err := a.CredentialsList(); err == nil {
		for _, c := range creds {
			credNames[c.ID] = c.Name
		}
	}
	profNames := map[string]string{}
	if profs, err := a.db.ListNetworkProfiles(); err == nil {
		for _, pr := range profs {
			profNames[pr.ID] = pr.Name
		}
	}
	folderPaths := a.folderPathIndex()

	credLabel := func(id, ctx string) string {
		if id == "" {
			return ""
		}
		if n, ok := credNames[id]; ok {
			return n
		}
		warn(fmt.Sprintf("%s references unknown credential id %q", ctx, id))
		return "UNKNOWN (" + id + ")"
	}

	// Temp-folder name index for parent labels.
	tempFolderName := map[string]string{}
	for _, f := range p.folders {
		tempFolderName[f.TempID] = f.Name
	}
	folderLabel := func(ref planRef) string {
		if ref.empty() {
			return "(root)"
		}
		if ref.Temp != "" {
			if n, ok := tempFolderName[ref.Temp]; ok {
				return "(new) " + n
			}
			warn("references unknown plan folder ref " + ref.Temp)
			return "(new) ?"
		}
		if pth, ok := folderPaths[ref.Existing]; ok && pth != "" {
			return pth
		}
		warn("references unknown existing folder id " + ref.Existing)
		return "UNKNOWN folder"
	}

	// settingsLines renders a folder-settings block into human-readable lines,
	// resolving credentials + network profiles to NAMES (never secrets).
	settingsLines := func(s folderSettingsInput, ctx string) []string {
		var out []string
		if s.User != "" {
			out = append(out, "user: "+s.User)
		}
		if s.Port != 0 {
			out = append(out, fmt.Sprintf("port: %d", s.Port))
		}
		if s.JumpHost != "" {
			via := s.JumpHost
			if s.JumpUser != "" {
				via = s.JumpUser + "@" + via
			}
			if s.JumpAuthRef != "" {
				via += " (cred: " + credLabel(s.JumpAuthRef, ctx+" jump") + ")"
			}
			out = append(out, "via "+via)
		}
		if s.AuthRef != "" {
			out = append(out, "cred: "+credLabel(s.AuthRef, ctx))
		}
		if s.NetworkProfileID != "" {
			if n, ok := profNames[s.NetworkProfileID]; ok {
				out = append(out, "net: "+n)
			} else {
				warn(ctx + " references unknown network profile id " + s.NetworkProfileID)
				out = append(out, "net: UNKNOWN ("+s.NetworkProfileID+")")
			}
		}
		if strings.TrimSpace(s.InitialCommand) != "" {
			out = append(out, "init: "+s.InitialCommand)
		}
		return out
	}

	for _, f := range p.folders {
		fp := McpPlanFolderPreview{Name: f.Name, Parent: folderLabel(f.Parent)}
		if f.Settings != nil {
			fp.Defaults = settingsLines(*f.Settings, "folder "+f.Name)
		}
		pv.Folders = append(pv.Folders, fp)
	}
	// Settings applied to existing folders show as folder rows too.
	for _, fs := range p.folderSettings {
		name := folderPaths[fs.FolderID]
		if name == "" {
			name = fs.FolderID
		}
		pv.Folders = append(pv.Folders, McpPlanFolderPreview{
			Name:     name,
			Parent:   "(existing - settings updated)",
			Defaults: settingsLines(fs.Settings, "folder "+name),
		})
	}

	// Group forwards + bookmarks by their connection ref for the tree.
	// A forward can reference an existing connection too; those still show
	// under a synthetic "(existing connection)" node.
	fwdByConnTemp := map[string][]planForward{}
	fwdByConnExisting := map[string][]planForward{}
	tempForwardIdx := map[string]int{} // tempID -> index into a flat list for bookmark lookup
	flatForwards := []planForward{}
	for _, fw := range p.forwards {
		tempForwardIdx[fw.TempID] = len(flatForwards)
		flatForwards = append(flatForwards, fw)
		if fw.Conn.Temp != "" {
			fwdByConnTemp[fw.Conn.Temp] = append(fwdByConnTemp[fw.Conn.Temp], fw)
		} else {
			fwdByConnExisting[fw.Conn.Existing] = append(fwdByConnExisting[fw.Conn.Existing], fw)
		}
	}
	// Bookmarks by forward ref.
	bmByForwardTemp := map[string][]store.ProxyBookmark{}
	for _, bm := range p.bookmarks {
		pv.Counts.Bookmarks += len(bm.Bookmarks)
		if bm.Forward.Temp != "" {
			bmByForwardTemp[bm.Forward.Temp] = append(bmByForwardTemp[bm.Forward.Temp], bm.Bookmarks...)
		}
	}

	fwdDetail := func(fw planForward) string {
		switch fw.Kind {
		case "dynamic":
			la := fw.LocalAddr
			if la == "" {
				la = "127.0.0.1"
			}
			if fw.LocalPort == 0 {
				return fmt.Sprintf("SOCKS5 on %s (auto port)", la)
			}
			return fmt.Sprintf("SOCKS5 on %s:%d", la, fw.LocalPort)
		case "local":
			la := fw.LocalAddr
			if la == "" {
				la = "127.0.0.1"
			}
			return fmt.Sprintf("%s:%d -> %s:%d", la, fw.LocalPort, fw.RemoteHost, fw.RemotePort)
		default: // remote
			return fmt.Sprintf("remote :%d -> %s:%d", fw.RemotePort, fw.RemoteHost, fw.RemotePort)
		}
	}
	renderForwards := func(list []planForward) []McpPlanForwardPreview {
		out := []McpPlanForwardPreview{}
		for _, fw := range list {
			fp := McpPlanForwardPreview{Kind: fw.Kind, Detail: fwdDetail(fw)}
			for _, bm := range bmByForwardTemp[fw.TempID] {
				fp.Bookmarks = append(fp.Bookmarks, bm.Name+" -> "+bm.URL)
			}
			if fw.Kind != "dynamic" && len(fp.Bookmarks) > 0 {
				warn("bookmarks set on a non-dynamic forward are ignored")
			}
			out = append(out, fp)
		}
		return out
	}

	for _, c := range p.conns {
		target := c.Host
		if c.Port != 0 {
			target = fmt.Sprintf("%s:%d", c.Host, c.Port)
		}
		if c.User != "" {
			target = c.User + "@" + target
		}
		via := ""
		if c.Jump != nil {
			via = c.Jump.Host
			if c.Jump.User != "" {
				via = c.Jump.User + "@" + via
			}
			if c.Jump.AuthRef != "" {
				via += " (cred: " + credLabel(c.Jump.AuthRef, "jump host") + ")"
			}
		}
		np := ""
		if c.NetworkProfileID != "" {
			if n, ok := profNames[c.NetworkProfileID]; ok {
				np = n
			} else {
				warn("connection " + c.Name + " references unknown network profile id " + c.NetworkProfileID)
				np = "UNKNOWN (" + c.NetworkProfileID + ")"
			}
		}
		pv.Connections = append(pv.Connections, McpPlanConnPreview{
			Name:           c.Name,
			Target:         target,
			Folder:         folderLabel(c.Folder),
			Credential:     credLabel(c.AuthRef, "connection "+c.Name),
			Via:            via,
			NetworkProfile: np,
			InitialCommand: c.InitialCommand,
			Forwards:       renderForwards(fwdByConnTemp[c.TempID]),
		})
	}
	// Forwards attached to existing connections (rare) get a synthetic node.
	for connID, list := range fwdByConnExisting {
		label := "(existing connection " + connID + ")"
		if paths := a.connectionLabel(connID); paths != "" {
			label = paths
		}
		pv.Connections = append(pv.Connections, McpPlanConnPreview{
			Name: label, Forwards: renderForwards(list),
		})
	}
	return pv
}

// connectionLabel returns "folder/name" for an existing connection id, or ""
// if not found. Used only for preview labels.
func (a *App) connectionLabel(id string) string {
	conn, err := a.db.GetConnection(id)
	if err != nil || conn == nil {
		return ""
	}
	if conn.FolderID != nil {
		if pth := a.folderPathIndex()[*conn.FolderID]; pth != "" {
			return pth + "/" + conn.Name
		}
	}
	return conn.Name
}

// ----- Commit (atomic write) -----

// planCommit renders the plan to the approval modal, blocks on the user's
// decision, and on approval writes everything in one transaction. Returns a
// summary string. Clears the plan either way.
func (a *App) planCommit() (string, error) {
	if !a.mcpManageAllowed() {
		return "", errManageOff
	}
	a.mcp.planMu.Lock()
	p := a.mcp.plan
	a.mcp.planMu.Unlock()
	if p == nil || (len(p.folders) == 0 && len(p.folderSettings) == 0 && len(p.conns) == 0 && len(p.forwards) == 0 && len(p.bookmarks) == 0) {
		return "", fmt.Errorf("no plan to commit; stage folders/connections/forwards first")
	}

	// Validate auth_refs up front so a bad ref is reported before the modal.
	if err := a.validatePlanRefs(p); err != nil {
		a.planDiscard()
		return "", err
	}

	preview := a.buildPlanPreview(p)

	// Approval (reuses the approvals channel plumbing).
	decision := a.requestPlanApproval(preview)
	if decision != mcpDecisionRun {
		a.planDiscard()
		a.recordActivity(McpActivity{
			Kind: "provision", Session: "plan", Command: a.planSummary(p), Gate: "denied",
		})
		return "", fmt.Errorf("plan rejected by user")
	}

	created, err := a.writePlan(p)
	// Clear the plan regardless of outcome; a failed commit rolled back, and a
	// half-applied plan must not be re-committed.
	a.planDiscard()
	if err != nil {
		a.recordActivity(McpActivity{
			Kind: "provision", Session: "plan", Command: a.planSummary(p),
			Gate: "approved", Exit: "error", Output: err.Error(),
		})
		return "", fmt.Errorf("commit failed (nothing written): %w", err)
	}
	a.recordActivity(McpActivity{
		Kind: "provision", Session: "plan", Command: a.planSummary(p),
		Gate: "approved", Exit: "ok",
	})
	// Refresh the tree UI (same event a live sync-pull uses to reload the
	// stores that read from the DB).
	EventsEmit("profile_reloaded", nil)
	return created, nil
}

// validatePlanRefs checks every auth_ref (connection + jump) and network
// profile id resolves to an existing row, and that forward/bookmark refs point
// at a known connection/forward (plan-temp or existing).
func (a *App) validatePlanRefs(p *mcpPlan) error {
	credOK := map[string]bool{}
	if creds, err := a.CredentialsList(); err == nil {
		for _, c := range creds {
			credOK[c.ID] = true
		}
	}
	profOK := map[string]bool{}
	if profs, err := a.db.ListNetworkProfiles(); err == nil {
		for _, pr := range profs {
			profOK[pr.ID] = true
		}
	}
	tempFolders := map[string]bool{}
	for _, f := range p.folders {
		tempFolders[f.TempID] = true
	}
	tempConns := map[string]bool{}
	for _, c := range p.conns {
		tempConns[c.TempID] = true
	}
	tempForwards := map[string]bool{}
	for _, fw := range p.forwards {
		tempForwards[fw.TempID] = true
	}

	// checkFolderSettings validates the credential / network-profile refs a
	// folder-settings block carries (never a secret - only ids).
	checkFolderSettings := func(what string, s folderSettingsInput) error {
		if s.AuthRef != "" && !credOK[s.AuthRef] {
			return fmt.Errorf("%s references unknown credential id %q", what, s.AuthRef)
		}
		if s.JumpAuthRef != "" && !credOK[s.JumpAuthRef] {
			return fmt.Errorf("%s jump host references unknown credential id %q", what, s.JumpAuthRef)
		}
		if s.NetworkProfileID != "" && !profOK[s.NetworkProfileID] {
			return fmt.Errorf("%s references unknown network profile id %q", what, s.NetworkProfileID)
		}
		return nil
	}

	for _, f := range p.folders {
		if f.Parent.Temp != "" && !tempFolders[f.Parent.Temp] {
			return fmt.Errorf("folder %q references unknown plan folder %q", f.Name, f.Parent.Temp)
		}
		if f.Parent.Existing != "" {
			if _, err := a.db.GetFolder(f.Parent.Existing); err != nil {
				return fmt.Errorf("folder %q references unknown folder id %q", f.Name, f.Parent.Existing)
			}
		}
		if f.Settings != nil {
			if err := checkFolderSettings("folder "+f.Name, *f.Settings); err != nil {
				return err
			}
		}
	}
	for _, fs := range p.folderSettings {
		if _, err := a.db.GetFolder(fs.FolderID); err != nil {
			return fmt.Errorf("settings target unknown folder id %q", fs.FolderID)
		}
		if err := checkFolderSettings("folder "+fs.FolderID, fs.Settings); err != nil {
			return err
		}
	}
	for _, c := range p.conns {
		if c.AuthRef != "" && !credOK[c.AuthRef] {
			return fmt.Errorf("connection %q references unknown credential id %q", c.Name, c.AuthRef)
		}
		if c.Jump != nil && c.Jump.AuthRef != "" && !credOK[c.Jump.AuthRef] {
			return fmt.Errorf("connection %q jump host references unknown credential id %q", c.Name, c.Jump.AuthRef)
		}
		if c.NetworkProfileID != "" && !profOK[c.NetworkProfileID] {
			return fmt.Errorf("connection %q references unknown network profile id %q", c.Name, c.NetworkProfileID)
		}
		if c.Folder.Temp != "" && !tempFolders[c.Folder.Temp] {
			return fmt.Errorf("connection %q references unknown plan folder %q", c.Name, c.Folder.Temp)
		}
		if c.Folder.Existing != "" {
			if _, err := a.db.GetFolder(c.Folder.Existing); err != nil {
				return fmt.Errorf("connection %q references unknown folder id %q", c.Name, c.Folder.Existing)
			}
		}
	}
	for _, fw := range p.forwards {
		if fw.Conn.Temp != "" && !tempConns[fw.Conn.Temp] {
			return fmt.Errorf("a %s forward references unknown plan connection %q", fw.Kind, fw.Conn.Temp)
		}
		if fw.Conn.Existing != "" {
			if c, err := a.db.GetConnection(fw.Conn.Existing); err != nil || c == nil {
				return fmt.Errorf("a %s forward references unknown connection id %q", fw.Kind, fw.Conn.Existing)
			}
		}
	}
	for _, bm := range p.bookmarks {
		if bm.Forward.Temp != "" && !tempForwards[bm.Forward.Temp] {
			return fmt.Errorf("bookmarks reference unknown plan forward %q", bm.Forward.Temp)
		}
		if bm.Forward.Existing == "" && bm.Forward.Temp == "" {
			return fmt.Errorf("bookmarks reference an empty forward")
		}
	}
	return nil
}

// writePlan resolves temp ids to real ids in dependency order and inserts
// everything inside a single transaction. Any error rolls the whole thing back.
func (a *App) writePlan(p *mcpPlan) (string, error) {
	folderIDs := map[string]string{} // tempID -> real id
	connIDs := map[string]string{}
	forwardIDs := map[string]string{}

	err := a.db.WithTx(func(tx *sql.Tx) error {
		// Folders may parent other folders in the same plan, so insert in the
		// order given; a parent temp must appear before its child (the LLM
		// creates parents first). Resolve parent refs as we go.
		for _, f := range p.folders {
			var parent *string
			switch {
			case f.Parent.Temp != "":
				rid, ok := folderIDs[f.Parent.Temp]
				if !ok {
					return fmt.Errorf("folder %q: parent %q not yet created (order folders parent-first)", f.Name, f.Parent.Temp)
				}
				parent = &rid
			case f.Parent.Existing != "":
				pid := f.Parent.Existing
				parent = &pid
			}
			nf := store.NewFolder{ParentID: parent, Name: f.Name}
			if f.Settings != nil {
				nf.Settings = f.Settings.toInheritable()
			}
			id, err := a.db.CreateFolderTx(tx, nf)
			if err != nil {
				return err
			}
			folderIDs[f.TempID] = id
		}

		// Settings on existing folders.
		for _, fs := range p.folderSettings {
			if err := a.db.UpdateFolderSettingsTx(tx, fs.FolderID, fs.Settings.toInheritable()); err != nil {
				return fmt.Errorf("set settings on folder %q: %w", fs.FolderID, err)
			}
		}

		for _, c := range p.conns {
			var folder *string
			switch {
			case c.Folder.Temp != "":
				rid, ok := folderIDs[c.Folder.Temp]
				if !ok {
					return fmt.Errorf("connection %q: folder ref %q not created", c.Name, c.Folder.Temp)
				}
				folder = &rid
			case c.Folder.Existing != "":
				fid := c.Folder.Existing
				folder = &fid
			}
			ov := store.InheritableSettings{}
			if c.User != "" {
				u := c.User
				ov.Username = &u
			}
			if c.Port != 0 {
				pt := c.Port
				ov.Port = &pt
			}
			if c.AuthRef != "" {
				ar := c.AuthRef
				ov.AuthRef = &ar
			}
			if c.NetworkProfileID != "" {
				npid := c.NetworkProfileID
				ov.NetworkProfileID = &npid
			}
			if strings.TrimSpace(c.InitialCommand) != "" {
				ic := c.InitialCommand
				ov.InitialCommand = &ic
			}
			if c.Jump != nil {
				spec := store.JumpHostSpec{Hostname: c.Jump.Host}
				if c.Jump.User != "" {
					u := c.Jump.User
					spec.Username = &u
				}
				if c.Jump.Port != 0 {
					jp := c.Jump.Port
					spec.Port = &jp
				}
				if c.Jump.AuthRef != "" {
					ar := c.Jump.AuthRef
					spec.AuthRef = &ar
				}
				ov.JumpHost = &store.JumpHostOverride{Kind: "chain", Chain: &spec}
			}
			id, err := a.db.CreateConnectionTx(tx, store.NewConnection{
				FolderID:  folder,
				Name:      c.Name,
				Hostname:  c.Host,
				Overrides: ov,
				Tags:      c.Tags,
				Protocol:  "ssh",
			})
			if err != nil {
				return err
			}
			connIDs[c.TempID] = id
		}

		for _, fw := range p.forwards {
			connID := fw.Conn.Existing
			if fw.Conn.Temp != "" {
				rid, ok := connIDs[fw.Conn.Temp]
				if !ok {
					return fmt.Errorf("forward: connection ref %q not created", fw.Conn.Temp)
				}
				connID = rid
			}
			nf := store.NewPortForward{
				ConnectionID: connID, Kind: fw.Kind,
				AutoStart: fw.AutoStart, Description: fw.Desc,
			}
			if fw.LocalAddr != "" {
				la := fw.LocalAddr
				nf.LocalAddr = &la
			}
			if fw.LocalPort != 0 {
				lp := fw.LocalPort
				nf.LocalPort = &lp
			}
			if fw.RemoteHost != "" {
				rh := fw.RemoteHost
				nf.RemoteHost = &rh
			}
			if fw.RemotePort != 0 {
				rp := fw.RemotePort
				nf.RemotePort = &rp
			}
			id, err := a.db.CreatePortForwardTx(tx, nf)
			if err != nil {
				return err
			}
			forwardIDs[fw.TempID] = id
		}

		for _, bm := range p.bookmarks {
			fwID := bm.Forward.Existing
			if bm.Forward.Temp != "" {
				rid, ok := forwardIDs[bm.Forward.Temp]
				if !ok {
					return fmt.Errorf("bookmarks: forward ref %q not created", bm.Forward.Temp)
				}
				fwID = rid
			}
			if err := a.db.SetPortForwardBookmarksTx(tx, fwID, bm.Bookmarks); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("created %d folder(s), %d connection(s), %d forward(s)",
		len(folderIDs), len(connIDs), len(forwardIDs)), nil
}

// planSummary is a one-line description for the activity log.
func (a *App) planSummary(p *mcpPlan) string {
	return fmt.Sprintf("provision plan: %d folders, %d connections, %d forwards, %d bookmark sets",
		len(p.folders), len(p.conns), len(p.forwards), len(p.bookmarks))
}
