package main

import (
	"database/sql"
	"fmt"
	"sort"
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
	Icon             string // built-in icon name, or ""
	IconColor        string // palette colour name, or ""
	IconImage        string // uploaded image id, or ""
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

// planEditConn is an edit to an EXISTING connection. Every field is
// optional: nil means "leave this alone", which is what lets an LLM rename a
// connection without disturbing the credential or jump host beside it.
//
// ClearSettings names inheritable fields to remove so they fall back to
// folder inheritance - the "make this inherit the folder's credential"
// operation, which is not expressible as a value.
type planEditConn struct {
	ConnID        string
	Name          *string
	Host          *string
	Folder        *planRef // move; a set-but-empty ref means root
	SetSettings   store.InheritableSettings
	ClearSettings []string
	// Icon is separate from SetSettings because icons are their own columns,
	// outside the inheritable-settings blob. A set-but-empty value clears it.
	Icon      *string
	IconColor *string
	IconImage *string
}

// planEditFolder renames an existing folder. Folder settings already have
// their own plan entry (planFolderSettings), so this is name-only.
type planEditFolder struct {
	FolderID string
	Name     string
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
	editConns      []planEditConn   // edits to EXISTING connections
	editFolders    []planEditFolder // renames of EXISTING folders
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
	Icon             string
	IconColor        string
	IconImage        string
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
	icon := strings.TrimSpace(in.Icon)
	iconColor := strings.TrimSpace(in.IconColor)
	// Reject an unknown icon rather than storing it: nothing downstream
	// validates, so a bad name would be written and then render as the
	// generic fallback glyph, looking like the icon was simply ignored.
	if icon != "" && !validIconName(icon) {
		return "", fmt.Errorf("unknown icon %q", icon)
	}
	if !validIconColor(iconColor) {
		return "", fmt.Errorf("unknown icon colour %q", iconColor)
	}
	if icon == "" && iconColor != "" {
		return "", fmt.Errorf("icon_color needs an icon")
	}
	iconImage := strings.TrimSpace(in.IconImage)
	if iconImage != "" {
		if icon != "" {
			return "", fmt.Errorf("set icon or icon_image, not both")
		}
		if !a.db.ImageExists(iconImage) {
			return "", fmt.Errorf("no uploaded icon with id %q", iconImage)
		}
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
		Icon:             icon,
		IconColor:        iconColor,
		IconImage:        iconImage,
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

// editConnInput is what the edit_connection tool passes down. Strings use a
// pointer so "not mentioned" is distinguishable from "set to empty".
type editConnInput struct {
	ConnID           string
	Name             *string
	Host             *string
	User             *string
	Port             *uint16
	AuthRef          *string
	NetworkProfileID *string
	InitialCommand   *string
	Folder           *string
	Icon             *string
	IconColor        *string
	IconImage        *string
	Clear            []string
}

// planEditConnection stages an edit to an existing connection.
func (a *App) planEditConnection(in editConnInput) error {
	if !a.mcpManageAllowed() {
		return errManageOff
	}
	if strings.TrimSpace(in.ConnID) == "" {
		return fmt.Errorf("connection id required")
	}
	// Fail fast on a bad id: the LLM is likely working from a stale listing,
	// and reporting that now beats a confusing failure at commit time.
	if _, err := a.db.GetConnection(in.ConnID); err != nil {
		return fmt.Errorf("no connection with id %q", in.ConnID)
	}

	e := planEditConn{ConnID: in.ConnID}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return fmt.Errorf("name cannot be empty")
		}
		e.Name = &n
	}
	if in.Host != nil {
		h := strings.TrimSpace(*in.Host)
		if h == "" {
			return fmt.Errorf("host cannot be empty")
		}
		e.Host = &h
	}
	if in.Folder != nil {
		ref := parsePlanRef(*in.Folder)
		e.Folder = &ref
	}
	if in.User != nil {
		e.SetSettings.Username = in.User
	}
	if in.Port != nil {
		e.SetSettings.Port = in.Port
	}
	if in.AuthRef != nil {
		e.SetSettings.AuthRef = in.AuthRef
	}
	if in.NetworkProfileID != nil {
		e.SetSettings.NetworkProfileID = in.NetworkProfileID
	}
	if in.InitialCommand != nil {
		e.SetSettings.InitialCommand = in.InitialCommand
	}
	if in.Icon != nil {
		icon := strings.TrimSpace(*in.Icon)
		// Empty is allowed here (unlike create): it is how an edit takes an
		// icon back off a connection.
		if icon != "" && !validIconName(icon) {
			return fmt.Errorf("unknown icon %q", icon)
		}
		e.Icon = &icon
	}
	if in.IconColor != nil {
		col := strings.TrimSpace(*in.IconColor)
		if !validIconColor(col) {
			return fmt.Errorf("unknown icon colour %q", col)
		}
		e.IconColor = &col
	}
	if in.IconImage != nil {
		img := strings.TrimSpace(*in.IconImage)
		if img != "" {
			if in.Icon != nil && strings.TrimSpace(*in.Icon) != "" {
				return fmt.Errorf("set icon or icon_image, not both")
			}
			if !a.db.ImageExists(img) {
				return fmt.Errorf("no uploaded icon with id %q", img)
			}
		}
		e.IconImage = &img
	}
	for _, c := range in.Clear {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		e.ClearSettings = append(e.ClearSettings, c)
	}
	if e.Name == nil && e.Host == nil && e.Folder == nil && e.Icon == nil && e.IconColor == nil && e.IconImage == nil &&
		len(e.ClearSettings) == 0 && settingsEmpty(e.SetSettings) {
		return fmt.Errorf("nothing to change")
	}

	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	// Merge into an existing staged edit for the same connection so two calls
	// (rename, then clear the credential) do not fight each other.
	for i := range p.editConns {
		if p.editConns[i].ConnID == e.ConnID {
			mergeConnEdit(&p.editConns[i], e)
			return nil
		}
	}
	p.editConns = append(p.editConns, e)
	return nil
}

// mergeConnEdit folds b into a, with b winning on any field it sets.
func mergeConnEdit(a *planEditConn, b planEditConn) {
	if b.Name != nil {
		a.Name = b.Name
	}
	if b.Host != nil {
		a.Host = b.Host
	}
	if b.Folder != nil {
		a.Folder = b.Folder
	}
	if b.Icon != nil {
		a.Icon = b.Icon
	}
	if b.IconColor != nil {
		a.IconColor = b.IconColor
	}
	if b.IconImage != nil {
		a.IconImage = b.IconImage
	}
	if b.SetSettings.Username != nil {
		a.SetSettings.Username = b.SetSettings.Username
	}
	if b.SetSettings.Port != nil {
		a.SetSettings.Port = b.SetSettings.Port
	}
	if b.SetSettings.AuthRef != nil {
		a.SetSettings.AuthRef = b.SetSettings.AuthRef
	}
	if b.SetSettings.NetworkProfileID != nil {
		a.SetSettings.NetworkProfileID = b.SetSettings.NetworkProfileID
	}
	if b.SetSettings.InitialCommand != nil {
		a.SetSettings.InitialCommand = b.SetSettings.InitialCommand
	}
	a.ClearSettings = append(a.ClearSettings, b.ClearSettings...)
}

// settingsEmpty reports whether an edit set no inheritable value at all.
func settingsEmpty(s store.InheritableSettings) bool {
	return s.Username == nil && s.Port == nil && s.AuthRef == nil &&
		s.NetworkProfileID == nil && s.InitialCommand == nil
}

// planRenameFolder stages a rename of an existing folder.
func (a *App) planRenameFolder(folderID, name string) error {
	if !a.mcpManageAllowed() {
		return errManageOff
	}
	folderID = strings.TrimSpace(folderID)
	name = strings.TrimSpace(name)
	if folderID == "" {
		return fmt.Errorf("folder id required")
	}
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if _, err := a.db.GetFolder(folderID); err != nil {
		return fmt.Errorf("no folder with id %q", folderID)
	}
	a.mcp.planMu.Lock()
	defer a.mcp.planMu.Unlock()
	p := a.getOrInitPlan()
	for i := range p.editFolders {
		if p.editFolders[i].FolderID == folderID {
			p.editFolders[i].Name = name
			return nil
		}
	}
	p.editFolders = append(p.editFolders, planEditFolder{FolderID: folderID, Name: name})
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
	Edits       []McpPlanEditPreview   `json:"edits"`
	Warnings    []string               `json:"warnings"`
	Counts      McpPlanCounts          `json:"counts"`
}

// McpPlanEditPreview describes one change to something that ALREADY exists.
// Shown apart from the creations in the approval modal: creating a connection
// is additive and easy to undo by deleting it, whereas an edit overwrites a
// row the user already relies on, so it deserves the more careful look.
type McpPlanEditPreview struct {
	Kind    string   `json:"kind"`    // "connection" | "folder"
	Target  string   `json:"target"`  // current name + path, for recognition
	Changes []string `json:"changes"` // human-readable "field: old -> new"
}

type McpPlanCounts struct {
	Folders     int `json:"folders"`
	Connections int `json:"connections"`
	Forwards    int `json:"forwards"`
	Bookmarks   int `json:"bookmarks"`
	Edits       int `json:"edits"`
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

	// Edits to existing rows. Rendered as "field: old -> new" so the approval
	// modal shows what is being overwritten, not just what it will become -
	// the old value is the part the user needs to weigh.
	for _, ef := range p.editFolders {
		cur, err := a.db.GetFolder(ef.FolderID)
		if err != nil {
			warn("rename targets a folder that no longer exists (" + ef.FolderID + ")")
			continue
		}
		if cur.Name == ef.Name {
			continue // no-op, nothing to show
		}
		pv.Edits = append(pv.Edits, McpPlanEditPreview{
			Kind:    "folder",
			Target:  folderPaths[ef.FolderID],
			Changes: []string{fmt.Sprintf("name: %s -> %s", cur.Name, ef.Name)},
		})
	}
	for _, e := range p.editConns {
		cur, err := a.db.GetConnection(e.ConnID)
		if err != nil {
			warn("edit targets a connection that no longer exists (" + e.ConnID + ")")
			continue
		}
		var ch []string
		if e.Name != nil && *e.Name != cur.Name {
			ch = append(ch, fmt.Sprintf("name: %s -> %s", cur.Name, *e.Name))
		}
		if e.Host != nil && *e.Host != cur.Hostname {
			ch = append(ch, fmt.Sprintf("host: %s -> %s", cur.Hostname, *e.Host))
		}
		if e.Folder != nil {
			from := "(root)"
			if cur.FolderID != nil {
				from = folderPaths[*cur.FolderID]
			}
			ch = append(ch, fmt.Sprintf("folder: %s -> %s", from, folderLabel(*e.Folder)))
		}
		if e.SetSettings.Username != nil {
			ch = append(ch, fmt.Sprintf("user: %s -> %s", strDeref(cur.Overrides.Username, "(inherited)"), *e.SetSettings.Username))
		}
		if e.SetSettings.Port != nil {
			ch = append(ch, fmt.Sprintf("port: %s -> %d", portLabel(cur.Overrides.Port), *e.SetSettings.Port))
		}
		if e.SetSettings.AuthRef != nil {
			ch = append(ch, fmt.Sprintf("credential: %s -> %s",
				credOrInherited(credNames, cur.Overrides.AuthRef),
				credLabel(*e.SetSettings.AuthRef, "edit of "+cur.Name)))
		}
		if e.SetSettings.NetworkProfileID != nil {
			ch = append(ch, fmt.Sprintf("network profile: %s -> %s",
				nameOrInherited(profNames, cur.Overrides.NetworkProfileID), *e.SetSettings.NetworkProfileID))
		}
		if e.SetSettings.InitialCommand != nil {
			ch = append(ch, fmt.Sprintf("initial command: %q -> %q",
				strDeref(cur.Overrides.InitialCommand, ""), *e.SetSettings.InitialCommand))
		}
		// Icons read as a change of appearance, so describe them the way the
		// user would see them rather than by id.
		if e.Icon != nil {
			from := iconLabel(cur.IconName, cur.IconImageID)
			to := "(none)"
			if *e.Icon != "" {
				to = *e.Icon
			}
			ch = append(ch, fmt.Sprintf("icon: %s -> %s", from, to))
		}
		if e.IconImage != nil {
			from := iconLabel(cur.IconName, cur.IconImageID)
			to := "(none)"
			if *e.IconImage != "" {
				to = "uploaded icon"
			}
			ch = append(ch, fmt.Sprintf("icon: %s -> %s", from, to))
		}
		if e.IconColor != nil && e.Icon == nil && e.IconImage == nil {
			ch = append(ch, fmt.Sprintf("icon colour: %s -> %s",
				strDeref(cur.IconColor, "(default)"), orNone(*e.IconColor)))
		}
		// Clearing is the "inherit from the folder again" operation, so say
		// that rather than showing an empty new value.
		for _, f := range e.ClearSettings {
			ch = append(ch, fmt.Sprintf("%s: now inherited from folder", f))
		}
		if len(ch) == 0 {
			continue
		}
		pv.Edits = append(pv.Edits, McpPlanEditPreview{
			Kind: "connection", Target: a.connectionLabel(e.ConnID), Changes: ch,
		})
	}
	pv.Counts.Edits = len(pv.Edits)
	a.warnRepeatedSettings(p, &pv, credNames, profNames, tempFolderName)
	return pv
}

// iconLabel describes the icon a row currently carries, for the approval
// modal. Uploaded images have no name, so they are described by kind.
func iconLabel(name, imageID *string) string {
	if imageID != nil && *imageID != "" {
		return "uploaded icon"
	}
	if name != nil && *name != "" {
		return *name
	}
	return "(none)"
}

func orNone(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}

// warnRepeatedSettings flags the case where several connections landing in the
// same folder each carry the same credential or network profile.
//
// Inheritance is the whole point of the folder tree: put the credential on the
// folder once and the connections pick it up, so a rotation or a swap is one
// edit instead of N. An LLM reaches for the shortest path instead - passing
// auth_ref to each create_connection - and the result looks identical in the
// tree while being materially worse to live with.
//
// This does not rewrite the plan. Hoisting the setting automatically would
// change what the user approved after they read it, and the folder may
// legitimately hold connections that must NOT share the credential. Saying so
// in the approval modal leaves the decision where it belongs.
func (a *App) warnRepeatedSettings(p *mcpPlan, pv *McpPlanPreview,
	credNames, profNames, tempFolderName map[string]string) {

	// Group the staged connections by the folder they land in. Only new
	// connections are considered: an edit that sets a credential is usually a
	// deliberate per-connection override.
	folderName := func(ref planRef) string {
		switch {
		case ref.Temp != "":
			if n, ok := tempFolderName[ref.Temp]; ok {
				return n
			}
			return "the new folder"
		case ref.Existing != "":
			if paths := a.folderPathIndex(); paths[ref.Existing] != "" {
				return paths[ref.Existing]
			}
			return "that folder"
		}
		return "the tree root"
	}

	for _, r := range repeatedSettings(p.conns) {
		names, kind := credNames, "credential"
		if r.Kind == "profile" {
			names, kind = profNames, "network profile"
		}
		name := names[r.ID]
		if name == "" {
			name = r.ID
		}
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(
			"all %d connections in %s use %s %q. Consider putting it on the folder with "+
				"set_folder_settings and letting them inherit it, so changing it later is one edit.",
			r.Count, folderName(r.Folder), kind, name))
	}
}

// repeatedSetting is one "every connection here carries the same value" find.
type repeatedSetting struct {
	Folder planRef
	Kind   string // "credential" | "profile"
	ID     string
	Count  int
}

// repeatedSettings reports settings that every staged connection in a folder
// carries identically, and so belong on the folder instead.
//
// Only a unanimous folder is reported. Where three of five connections share a
// credential the odd ones out are the point, and a warning there would be
// noise the user learns to skip past - which costs the warnings that matter.
func repeatedSettings(conns []planConn) []repeatedSetting {
	type bucket struct {
		creds map[string]int
		profs map[string]int
		total int
		ref   planRef
	}
	byFolder := map[string]*bucket{}
	for _, c := range conns {
		key := c.Folder.Temp + "\x00" + c.Folder.Existing
		b := byFolder[key]
		if b == nil {
			b = &bucket{creds: map[string]int{}, profs: map[string]int{}, ref: c.Folder}
			byFolder[key] = b
		}
		b.total++
		if c.AuthRef != "" {
			b.creds[c.AuthRef]++
		}
		if c.NetworkProfileID != "" {
			b.profs[c.NetworkProfileID]++
		}
	}

	// Sort every level: warnings are user-facing text, and map iteration would
	// reorder them between two identical plans.
	keys := make([]string, 0, len(byFolder))
	for k := range byFolder {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []repeatedSetting
	for _, k := range keys {
		b := byFolder[k]
		if b.total < 2 {
			continue
		}
		add := func(kind string, m map[string]int) {
			ids := make([]string, 0, len(m))
			for id, n := range m {
				if n == b.total {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			for _, id := range ids {
				out = append(out, repeatedSetting{Folder: b.ref, Kind: kind, ID: id, Count: b.total})
			}
		}
		add("credential", b.creds)
		add("profile", b.profs)
	}
	return out
}

// strDeref renders an optional string, falling back to a placeholder.
func strDeref(p *string, dflt string) string {
	if p == nil || *p == "" {
		return dflt
	}
	return *p
}

func portLabel(p *uint16) string {
	if p == nil || *p == 0 {
		return "(inherited)"
	}
	return fmt.Sprintf("%d", *p)
}

// credOrInherited names the credential currently on a row, or says the row
// inherits one, so an edit that swaps a credential shows what it replaces.
func credOrInherited(names map[string]string, id *string) string {
	if id == nil || *id == "" {
		return "(inherited)"
	}
	if n, ok := names[*id]; ok {
		return n
	}
	return "UNKNOWN (" + *id + ")"
}

func nameOrInherited(names map[string]string, id *string) string {
	if id == nil || *id == "" {
		return "(inherited)"
	}
	if n, ok := names[*id]; ok {
		return n
	}
	return *id
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
	if p == nil || (len(p.folders) == 0 && len(p.folderSettings) == 0 && len(p.conns) == 0 &&
		len(p.forwards) == 0 && len(p.bookmarks) == 0 && len(p.editConns) == 0 && len(p.editFolders) == 0) {
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
	for _, e := range p.editConns {
		if _, err := a.db.GetConnection(e.ConnID); err != nil {
			return fmt.Errorf("edit targets unknown connection id %q", e.ConnID)
		}
		if e.SetSettings.AuthRef != nil && *e.SetSettings.AuthRef != "" && !credOK[*e.SetSettings.AuthRef] {
			return fmt.Errorf("edit of %q references unknown credential id %q", e.ConnID, *e.SetSettings.AuthRef)
		}
		if e.SetSettings.NetworkProfileID != nil && *e.SetSettings.NetworkProfileID != "" && !profOK[*e.SetSettings.NetworkProfileID] {
			return fmt.Errorf("edit of %q references unknown network profile id %q", e.ConnID, *e.SetSettings.NetworkProfileID)
		}
		if e.Folder != nil {
			if e.Folder.Temp != "" && !tempFolders[e.Folder.Temp] {
				return fmt.Errorf("edit of %q moves into unknown plan folder %q", e.ConnID, e.Folder.Temp)
			}
			if e.Folder.Existing != "" {
				if _, err := a.db.GetFolder(e.Folder.Existing); err != nil {
					return fmt.Errorf("edit of %q moves into unknown folder id %q", e.ConnID, e.Folder.Existing)
				}
			}
		}
		for _, f := range e.ClearSettings {
			if !store.ValidOverrideField(f) {
				return fmt.Errorf("edit of %q clears unknown settings field %q", e.ConnID, f)
			}
		}
	}
	for _, ef := range p.editFolders {
		if _, err := a.db.GetFolder(ef.FolderID); err != nil {
			return fmt.Errorf("rename targets unknown folder id %q", ef.FolderID)
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
			switch {
			case c.IconImage != "":
				if err := a.db.SetConnectionIconTx(tx, id, c.IconImage); err != nil {
					return err
				}
			case c.Icon != "":
				if err := a.db.SetConnectionNamedIconTx(tx, id, c.Icon, c.IconColor); err != nil {
					return err
				}
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

		// Edits run last: a move can target a folder created earlier in this
		// same plan, so folderIDs has to be populated first.
		for _, ef := range p.editFolders {
			if err := a.db.RenameFolderTx(tx, ef.FolderID, ef.Name); err != nil {
				return fmt.Errorf("rename folder %q: %w", ef.FolderID, err)
			}
		}
		for _, e := range p.editConns {
			if e.Name != nil {
				if err := a.db.RenameConnectionTx(tx, e.ConnID, *e.Name); err != nil {
					return fmt.Errorf("rename connection %q: %w", e.ConnID, err)
				}
			}
			if e.Host != nil {
				if err := a.db.SetConnectionHostnameTx(tx, e.ConnID, *e.Host); err != nil {
					return fmt.Errorf("set hostname on %q: %w", e.ConnID, err)
				}
			}
			if e.Folder != nil {
				var fid *string
				switch {
				case e.Folder.Temp != "":
					rid, ok := folderIDs[e.Folder.Temp]
					if !ok {
						return fmt.Errorf("edit of %q: folder ref %q not created", e.ConnID, e.Folder.Temp)
					}
					fid = &rid
				case e.Folder.Existing != "":
					x := e.Folder.Existing
					fid = &x
				}
				// fid stays nil for an empty ref, which moves to the root.
				if err := a.db.MoveConnectionTx(tx, e.ConnID, fid); err != nil {
					return fmt.Errorf("move connection %q: %w", e.ConnID, err)
				}
			}
			if !settingsEmpty(e.SetSettings) || len(e.ClearSettings) > 0 {
				if err := a.db.PatchConnectionOverridesTx(tx, e.ConnID, e.SetSettings, e.ClearSettings); err != nil {
					return fmt.Errorf("update settings on %q: %w", e.ConnID, err)
				}
			}
			if e.IconImage != nil {
				if err := a.db.SetConnectionIconTx(tx, e.ConnID, *e.IconImage); err != nil {
					return fmt.Errorf("set icon image on %q: %w", e.ConnID, err)
				}
			} else if e.Icon != nil || e.IconColor != nil {
				// The setter writes both columns at once, so an edit touching
				// only one of them has to carry the other's current value
				// through - otherwise changing just the colour would silently
				// drop the icon it was meant to colour.
				name, color, hasImage := "", "", false
				if cur, err := a.db.GetConnection(e.ConnID); err == nil {
					name = strDeref(cur.IconName, "")
					color = strDeref(cur.IconColor, "")
					hasImage = cur.IconImageID != nil && *cur.IconImageID != ""
				}
				if e.Icon != nil {
					name = *e.Icon
				}
				if e.IconColor != nil {
					color = *e.IconColor
				}
				// A colour-only edit against a connection carrying an uploaded
				// image would resolve to an empty name here, and writing that
				// clears the image (the two icon kinds are mutually exclusive).
				// Recolouring an image is not a thing, so skip instead.
				switch {
				case e.Icon != nil:
					if err := a.db.SetConnectionNamedIconTx(tx, e.ConnID, name, color); err != nil {
						return fmt.Errorf("set icon on %q: %w", e.ConnID, err)
					}
				case hasImage:
					// colour-only edit, custom image: nothing to recolour.
				case name != "":
					if err := a.db.SetConnectionNamedIconTx(tx, e.ConnID, name, color); err != nil {
						return fmt.Errorf("set icon colour on %q: %w", e.ConnID, err)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	parts := []string{}
	if len(folderIDs) > 0 || len(connIDs) > 0 || len(forwardIDs) > 0 {
		parts = append(parts, fmt.Sprintf("created %d folder(s), %d connection(s), %d forward(s)",
			len(folderIDs), len(connIDs), len(forwardIDs)))
	}
	if len(p.editConns) > 0 || len(p.editFolders) > 0 {
		parts = append(parts, fmt.Sprintf("edited %d connection(s), %d folder(s)",
			len(p.editConns), len(p.editFolders)))
	}
	return strings.Join(parts, "; "), nil
}

// planSummary is a one-line description for the activity log.
func (a *App) planSummary(p *mcpPlan) string {
	return fmt.Sprintf("provision plan: %d folders, %d connections, %d forwards, %d bookmark sets, %d edits",
		len(p.folders), len(p.conns), len(p.forwards), len(p.bookmarks),
		len(p.editConns)+len(p.editFolders))
}
