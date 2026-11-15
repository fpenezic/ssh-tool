package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// Publishing a local folder to a catalog (PROTOCOL 8.2).
//
// BuildPublish turns the local subtree into a publish request: every
// local id becomes a catalog reference (a credential, network profile or
// outside bastion becomes a slot, a connection inside the subtree its new
// uuid), the settings inherited from local folders above the subtree are
// folded onto the published root, and everything free-form that could
// carry a secret is listed for review. It also says what the publisher's
// own side needs after the server accepted (bindings to their own
// objects, local layers), so their tree resolves exactly as before.
//
// Nothing here talks to the server or writes the store.

// PublishOptions are the user's choices from the dialog.
type PublishOptions struct {
	CatalogID string
	RootID    string // the local folder to publish
	// TargetUUID: the catalog folder the subtree goes under; "" publishes
	// a new space (admin only, checked by the server).
	TargetUUID string
	// JumpInclude: an outside bastion (local connection id) the subtree
	// jumps through is published as a copy inside the published root
	// instead of becoming a jump slot.
	JumpInclude map[string]bool
	// SlotNames: names for new slots, by PlanSlot.Key.
	SlotNames map[string]string
	// AccountName goes into the stored records as their author until the
	// feed brings the server's own fields.
	AccountName string
}

// PublishFolder, PublishConnection, PublishForward, PublishSlot are the
// request's records (PROTOCOL 3.x, without the read-only fields).
type PublishFolder struct {
	UUID       string          `json:"uuid"`
	ParentUUID *string         `json:"parent_uuid"`
	Name       string          `json:"name"`
	SortOrder  int64           `json:"sort_order"`
	Settings   json.RawMessage `json:"settings"`
	IconName   *string         `json:"icon_name"`
	IconColor  *string         `json:"icon_color"`
}

type PublishConnection struct {
	UUID       string          `json:"uuid"`
	FolderUUID string          `json:"folder_uuid"`
	Name       string          `json:"name"`
	Hostname   string          `json:"hostname"`
	Protocol   string          `json:"protocol"`
	SortOrder  int64           `json:"sort_order"`
	Settings   json.RawMessage `json:"settings"`
	Tags       []string        `json:"tags"`
	Notes      string          `json:"notes"`
	IconName   *string         `json:"icon_name"`
	IconColor  *string         `json:"icon_color"`
}

type PublishForward struct {
	UUID           string  `json:"uuid"`
	ConnectionUUID string  `json:"connection_uuid"`
	Kind           string  `json:"kind"`
	LocalAddr      *string `json:"local_addr"`
	LocalPort      *uint16 `json:"local_port"`
	RemoteHost     *string `json:"remote_host"`
	RemotePort     *uint16 `json:"remote_port"`
	AutoStart      bool    `json:"auto_start"`
	Description    string  `json:"description"`
}

type PublishSlot struct {
	UUID        string          `json:"uuid"`
	FolderUUID  string          `json:"folder_uuid"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Kind        *string         `json:"kind"`
	Backend     *string         `json:"backend"`
	ExternalRef json.RawMessage `json:"external_ref"`
	DocMD       string          `json:"doc_md"`
}

// PublishRequest is the POST /api/v1/publish body.
type PublishRequest struct {
	ParentUUID  *string             `json:"parent_uuid"`
	Folders     []PublishFolder     `json:"folders"`
	Connections []PublishConnection `json:"connections"`
	Forwards    []PublishForward    `json:"forwards"`
	Slots       []PublishSlot       `json:"slots"`
}

// PlanRecord is one record of the publish and the local row it is.
type PlanRecord struct {
	Kind    string `json:"kind"`
	UUID    string `json:"uuid"`
	LocalID string `json:"local_id"` // "" for a bastion copy (a new record)
	Name    string `json:"name"`
	Path    string `json:"path"` // inside the published root, "/"-joined
	// ParentLocalID: where the converted row hangs in the publisher's
	// tree (a folder for folders and connections, a connection for
	// forwards).
	ParentLocalID string `json:"-"`
	CopyOf        string `json:"copy_of,omitempty"` // bastion copy: the local connection
}

// PlanSlot is a slot the publish creates or reuses.
type PlanSlot struct {
	Key     string `json:"key"` // cred:<id> | net:<id> | jump:<id>
	UUID    string `json:"uuid"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	New     bool   `json:"new"`
	Kind    string `json:"kind,omitempty"` // cred: slot | external
	Backend string `json:"backend,omitempty"`
	// What the publisher's binding points at, and its name.
	BindTarget string `json:"bind_target"`
	BindName   string `json:"bind_name"`
	// Reused: where the existing slot lives (its folder's name).
	ReusedFrom string `json:"reused_from,omitempty"`
	UsedBy     int    `json:"used_by"`
}

// FreeText is one free-form value the user reviews before sending.
type FreeText struct {
	Kind  string `json:"kind"`
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	Field string `json:"field"`
	Value string `json:"value"`
	// FolderLevel: set on a folder, it reaches every reader of any folder
	// below it, also through the ancestor outline (PROTOCOL 9.1).
	FolderLevel bool `json:"folder_level"`
	// InheritedFrom: folded onto the published root from these local
	// folders above it.
	InheritedFrom string `json:"inherited_from,omitempty"`
}

// Refusal is a row that blocks the publish until it is moved out or fixed.
type Refusal struct {
	Kind    string `json:"kind"`
	LocalID string `json:"local_id"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// OutsideJump is a bastion outside the subtree that records inside jump
// through.
type OutsideJump struct {
	ConnectionID string   `json:"connection_id"`
	Name         string   `json:"name"`
	UsedBy       []string `json:"used_by"`
	Include      bool     `json:"include"`
}

// PublishPlan is everything the dialog shows and the publish sends.
type PublishPlan struct {
	Request      PublishRequest `json:"-"`
	Records      []PlanRecord   `json:"records"`
	Slots        []PlanSlot     `json:"slots"`
	FreeText     []FreeText     `json:"free_text"`
	Refusals     []Refusal      `json:"refusals"`
	OutsideJumps []OutsideJump  `json:"outside_jumps"`
	// Layers: remote uuid -> what stays local for the publisher (a
	// tunnel autostart, a broadcast group), as a local layer.
	Layers map[string]PlanLayer `json:"-"`
	// TargetLocalID: the local folder the published root goes under.
	TargetLocalID string `json:"-"`
	RootUUID      string `json:"root_uuid"`
	catalogID     string
}

// PlanLayer is a converted record's local layer.
type PlanLayer struct {
	Kind string
	JSON string
}

// Pending is the plan's rows as recorded before the request goes out.
func (p *PublishPlan) Pending() []store.PublishPending {
	var out []store.PublishPending
	for _, r := range p.Records {
		if r.LocalID != "" {
			out = append(out, store.PublishPending{Kind: r.Kind, RemoteUUID: r.UUID, LocalID: r.LocalID})
		}
	}
	return out
}

// UUIDs lists every record uuid of the plan (slots included).
func (p *PublishPlan) UUIDs() []string {
	var out []string
	for _, r := range p.Records {
		out = append(out, r.UUID)
	}
	for _, s := range p.Slots {
		if s.New {
			out = append(out, s.UUID)
		}
	}
	return out
}

type pubCtx struct {
	db   *store.DB
	opts PublishOptions
	plan *PublishPlan

	folders  map[string]store.Folder
	children map[string][]string // folder id -> child folder ids
	conns    map[string]store.Connection
	byFolder map[string][]store.Connection
	fwds     map[string][]store.PortForward
	creds    map[string]store.CredentialRef
	profiles map[string]store.NetworkProfile

	inside   map[string]string // local id -> new uuid (folders, connections, bastion copies)
	slots    map[string]*PlanSlot
	slotUse  map[string]map[string]bool // slot key -> record uuids using it
	scope    []store.CatalogSlot        // existing slots in scope of the target
	bindings map[string]store.CatalogBinding
	outside  map[string]*OutsideJump
	rootUUID string
	// reusedNames: folder uuid -> name along the target's chain.
	reusedNames map[string]string
}

// BuildPublish plans the publish of a local folder. Refusals in the plan
// mean it cannot be sent as it is.
func BuildPublish(db *store.DB, opts PublishOptions) (*PublishPlan, error) {
	cat, err := db.GetCatalog(opts.CatalogID)
	if err != nil {
		return nil, err
	}
	c := &pubCtx{db: db, opts: opts, plan: &PublishPlan{Layers: map[string]PlanLayer{}, catalogID: opts.CatalogID},
		folders: map[string]store.Folder{}, children: map[string][]string{}, conns: map[string]store.Connection{},
		byFolder: map[string][]store.Connection{}, fwds: map[string][]store.PortForward{},
		creds: map[string]store.CredentialRef{}, profiles: map[string]store.NetworkProfile{},
		inside: map[string]string{}, slots: map[string]*PlanSlot{}, slotUse: map[string]map[string]bool{},
		bindings: map[string]store.CatalogBinding{}, outside: map[string]*OutsideJump{}}
	if err := c.load(); err != nil {
		return nil, err
	}
	root, ok := c.folders[opts.RootID]
	if !ok {
		return nil, fmt.Errorf("publish: folder not found")
	}
	if root.CatalogID != nil {
		c.refuse("folders", root.ID, root.Name, "already belongs to a catalog")
		c.finish()
		return c.plan, nil
	}
	if err := c.target(cat); err != nil {
		return nil, err
	}

	// Pass 1: assign uuids to everything inside, check what cannot go.
	var subtree []string
	var walk func(id string)
	walk = func(id string) {
		subtree = append(subtree, id)
		c.inside[id] = uuid.NewString()
		for _, conn := range c.byFolder[id] {
			c.inside[conn.ID] = uuid.NewString()
		}
		for _, ch := range c.children[id] {
			walk(ch)
		}
	}
	walk(root.ID)
	c.rootUUID = c.inside[root.ID]
	c.plan.RootUUID = c.rootUUID
	if err := c.check(subtree); err != nil {
		return nil, err
	}

	// Pass 2: the records.
	for _, fid := range subtree {
		if err := c.folder(fid); err != nil {
			return nil, err
		}
	}
	for _, fid := range subtree {
		for _, conn := range c.byFolder[fid] {
			if err := c.connection(conn, c.inside[fid], ""); err != nil {
				return nil, err
			}
		}
	}
	// Bastion copies (their own references may add further copies).
	for done := map[string]bool{}; ; {
		var next []string
		for id, oj := range c.outside {
			if oj.Include && !done[id] {
				next = append(next, id)
			}
		}
		if len(next) == 0 {
			break
		}
		sort.Strings(next)
		for _, id := range next {
			done[id] = true
			b := c.conns[id]
			if b.Protocol == "local" {
				c.refuse("connections", b.ID, b.Name, "a local shell connection cannot be published (it would run on colleagues' machines)")
				continue
			}
			if err := c.connection(b, c.rootUUID, id); err != nil {
				return nil, err
			}
		}
	}
	c.finish()
	return c.plan, nil
}

func (c *pubCtx) load() error {
	fs, err := c.db.ListFolders()
	if err != nil {
		return err
	}
	for _, f := range fs {
		c.folders[f.ID] = f
		if f.ParentID != nil {
			c.children[*f.ParentID] = append(c.children[*f.ParentID], f.ID)
		}
	}
	for k := range c.children {
		ch := c.children[k]
		sort.SliceStable(ch, func(i, j int) bool { return c.folders[ch[i]].SortOrder < c.folders[ch[j]].SortOrder })
	}
	cs, err := c.db.ListConnections(nil)
	if err != nil {
		return err
	}
	for _, conn := range cs {
		c.conns[conn.ID] = conn
		if conn.FolderID != nil {
			c.byFolder[*conn.FolderID] = append(c.byFolder[*conn.FolderID], conn)
		}
	}
	pf, err := c.db.ListAllPortForwards()
	if err != nil {
		return err
	}
	for _, p := range pf {
		c.fwds[p.ConnectionID] = append(c.fwds[p.ConnectionID], p)
	}
	cr, err := c.db.ListCredentials()
	if err != nil {
		return err
	}
	for _, x := range cr {
		c.creds[x.ID] = x
	}
	np, err := c.db.ListNetworkProfiles()
	if err != nil {
		return err
	}
	for _, x := range np {
		c.profiles[x.ID] = x
	}
	bs, err := c.db.ListCatalogBindings(c.opts.CatalogID)
	if err != nil {
		return err
	}
	for _, b := range bs {
		c.bindings[b.SlotUUID] = b
	}
	return nil
}

// target finds the local folder of the target and the slots in its scope
// (its own and its ancestors', skeleton ones included).
func (c *pubCtx) target(cat *store.Catalog) error {
	if c.opts.TargetUUID == "" {
		c.plan.TargetLocalID = cat.RootFolderID
		return nil
	}
	type folderRec struct {
		ParentUUID *string `json:"parent_uuid"`
		Access     string  `json:"access"`
		Name       string  `json:"name"`
	}
	recs := map[string]folderRec{}
	localOf := map[string]string{}
	err := c.db.WithTx(func(tx *sql.Tx) error {
		rows, err := c.db.CatalogRowsRawTx(tx, c.opts.CatalogID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Kind != KindFolders || r.State != "" {
				continue
			}
			var fr folderRec
			if json.Unmarshal([]byte(r.RawJSON), &fr) == nil {
				recs[r.RemoteUUID] = fr
				localOf[r.RemoteUUID] = r.ID
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	t, ok := recs[c.opts.TargetUUID]
	if !ok {
		return fmt.Errorf("publish: the target folder is not in this catalog")
	}
	if t.Access != "write" && t.Access != "manage" {
		return fmt.Errorf("publish: you cannot add to %q in this catalog", t.Name)
	}
	c.plan.TargetLocalID = localOf[c.opts.TargetUUID]
	chain := map[string]string{} // folder uuid -> name
	for u, depth := c.opts.TargetUUID, 0; u != "" && depth < 64; depth++ {
		r, ok := recs[u]
		if !ok {
			break
		}
		chain[u] = r.Name
		if r.ParentUUID == nil {
			break
		}
		u = *r.ParentUUID
	}
	slots, err := c.db.ListCatalogSlots(c.opts.CatalogID)
	if err != nil {
		return err
	}
	for _, s := range slots {
		if _, ok := chain[s.FolderUUID]; ok {
			c.scope = append(c.scope, s)
		}
	}
	c.reusedNames = chain
	return nil
}

func (c *pubCtx) refuse(kind, id, name, reason string) {
	c.plan.Refusals = append(c.plan.Refusals, Refusal{Kind: kind, LocalID: id, Name: name, Reason: reason})
}

func (c *pubCtx) check(subtree []string) error {
	var ids []string
	hasContent := false
	for _, fid := range subtree {
		f := c.folders[fid]
		ids = append(ids, fid)
		if fid != c.opts.RootID || len(c.byFolder[fid]) > 0 || len(c.children[fid]) > 0 {
			hasContent = true
		}
		if f.CatalogID != nil {
			c.refuse("folders", fid, f.Name, "already belongs to a catalog")
		}
		if dyn, err := c.db.IsDynamicFolder(fid); err != nil {
			return err
		} else if dyn {
			c.refuse("folders", fid, f.Name, "a dynamic inventory folder cannot be published; move it out first")
		}
		for _, conn := range c.byFolder[fid] {
			ids = append(ids, conn.ID)
			if strings.EqualFold(strings.TrimSpace(conn.Protocol), "local") {
				c.refuse("connections", conn.ID, conn.Name, "a local shell connection cannot be published (it would run on colleagues' machines); move it out first")
			}
			if pin, err := c.db.GetPinForConnection(conn.ID); err != nil {
				return err
			} else if pin != nil {
				c.refuse("connections", conn.ID, conn.Name, "pinned from a dynamic inventory folder; move it out first")
			}
			for _, p := range c.fwds[conn.ID] {
				ids = append(ids, p.ID)
			}
		}
	}
	if !hasContent {
		c.refuse("folders", c.opts.RootID, c.folders[c.opts.RootID].Name, "the folder is empty")
	}
	busy, err := c.db.PendingPublishForLocal(ids)
	if err != nil {
		return err
	}
	if busy {
		c.refuse("folders", c.opts.RootID, c.folders[c.opts.RootID].Name,
			"an earlier publish of these rows has not settled yet; it settles on the next sync")
	}
	return nil
}

func (c *pubCtx) pathOf(folderID string) string {
	var parts []string
	for id, depth := folderID, 0; id != "" && depth < 64; depth++ {
		f, ok := c.folders[id]
		if !ok {
			break
		}
		parts = append([]string{f.Name}, parts...)
		if id == c.opts.RootID || f.ParentID == nil {
			break
		}
		id = *f.ParentID
	}
	return strings.Join(parts, "/")
}

func (c *pubCtx) folder(fid string) error {
	f := c.folders[fid]
	u := c.inside[fid]
	settings := f.Settings
	var parent *string
	parentLocal := c.plan.TargetLocalID
	if fid == c.opts.RootID {
		// Fold what the root inherits from local folders above it, so a
		// reader resolves what the publisher resolves.
		settings = resolver.EffectiveFolderSettings(fid, c.allFolders())
		if c.opts.TargetUUID != "" {
			t := c.opts.TargetUUID
			parent = &t
		}
		c.inherited(f, settings, u)
	} else {
		p := c.inside[*f.ParentID]
		parent = &p
		parentLocal = *f.ParentID
	}
	raw, layer, err := c.settings(settings, "folders", u, f.Name, true)
	if err != nil {
		return err
	}
	c.plan.Request.Folders = append(c.plan.Request.Folders, PublishFolder{
		UUID: u, ParentUUID: parent, Name: f.Name, SortOrder: f.SortOrder, Settings: raw,
		IconName: f.IconName, IconColor: f.IconColor,
	})
	c.plan.Records = append(c.plan.Records, PlanRecord{Kind: KindFolders, UUID: u, LocalID: fid, Name: f.Name,
		Path: c.pathOf(fid), ParentLocalID: parentLocal})
	c.layer("folders", u, layer)
	return nil
}

func (c *pubCtx) allFolders() []store.Folder {
	out := make([]store.Folder, 0, len(c.folders))
	for _, f := range c.folders {
		out = append(out, f)
	}
	return out
}

// inherited lists the values folded onto the root, with where they come
// from, for review: every one of them is folder level.
func (c *pubCtx) inherited(root store.Folder, effective store.InheritableSettings, u string) {
	own := toMap(root.Settings)
	eff := toMap(effective)
	var keys []string
	for k := range eff {
		if !reflect.DeepEqual(own[k], eff[k]) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var from []string
		for id, depth := root.ParentID, 0; id != nil && depth < 64; depth++ {
			f, ok := c.folders[*id]
			if !ok {
				break
			}
			if _, has := toMap(f.Settings)[k]; has {
				from = append(from, f.Name)
			}
			id = f.ParentID
		}
		c.plan.FreeText = append(c.plan.FreeText, FreeText{Kind: KindFolders, UUID: u, Name: root.Name, Field: k,
			Value: display(eff[k]), FolderLevel: true, InheritedFrom: strings.Join(from, ", ")})
	}
}

func (c *pubCtx) connection(conn store.Connection, folderUUID, copyOf string) error {
	u := c.inside[conn.ID]
	parentLocal := ""
	if conn.FolderID != nil {
		parentLocal = *conn.FolderID
	}
	localID := conn.ID
	path := c.pathOf(parentLocal) + "/" + conn.Name
	overrides := conn.Overrides
	if copyOf != "" {
		u = c.inside["copy:"+copyOf]
		localID, parentLocal = "", c.opts.RootID
		path = c.folders[c.opts.RootID].Name + "/" + conn.Name
		// The copy leaves its own folders behind: what it inherited there
		// goes onto it, so it dials the same way.
		overrides = resolver.EffectiveOverrides(conn, c.allFolders())
	}
	raw, layer, err := c.settings(overrides, "connections", u, conn.Name, false)
	if err != nil {
		return err
	}
	proto := conn.Protocol
	if proto == "" {
		proto = "ssh"
	}
	tags := conn.Tags
	if tags == nil {
		tags = []string{}
	}
	c.plan.Request.Connections = append(c.plan.Request.Connections, PublishConnection{
		UUID: u, FolderUUID: folderUUID, Name: conn.Name, Hostname: conn.Hostname, Protocol: proto,
		SortOrder: conn.SortOrder, Settings: raw, Tags: tags, Notes: conn.Notes,
		IconName: conn.IconName, IconColor: conn.IconColor,
	})
	c.plan.Records = append(c.plan.Records, PlanRecord{Kind: KindConnections, UUID: u, LocalID: localID, Name: conn.Name,
		Path: path, ParentLocalID: parentLocal, CopyOf: copyOf})
	if copyOf == "" {
		c.layer("connections", u, layer)
	}
	if conn.Notes != "" {
		c.text(KindConnections, u, conn.Name, "notes", conn.Notes, false)
	}
	if len(conn.Tags) > 0 {
		c.text(KindConnections, u, conn.Name, "tags", strings.Join(conn.Tags, ", "), false)
	}
	if copyOf != "" {
		return nil // a bastion copy goes without the bastion's forwards
	}
	for _, p := range c.fwds[conn.ID] {
		fu := uuid.NewString()
		c.plan.Request.Forwards = append(c.plan.Request.Forwards, PublishForward{
			UUID: fu, ConnectionUUID: u, Kind: p.Kind, LocalAddr: p.LocalAddr, LocalPort: p.LocalPort,
			RemoteHost: p.RemoteHost, RemotePort: p.RemotePort, AutoStart: p.AutoStart, Description: p.Description,
		})
		name := p.Description
		if name == "" {
			name = p.Kind + " forward"
		}
		c.plan.Records = append(c.plan.Records, PlanRecord{Kind: KindForwards, UUID: fu, LocalID: p.ID, Name: name,
			Path: path + "/" + name, ParentLocalID: conn.ID})
		if p.Description != "" {
			c.text(KindForwards, fu, name, "description", p.Description, false)
		}
		if p.RemoteHost != nil && *p.RemoteHost != "" {
			c.text(KindForwards, fu, name, "remote_host", *p.RemoteHost, false)
		}
	}
	return nil
}

func (c *pubCtx) text(kind, u, name, field, value string, folderLevel bool) {
	c.plan.FreeText = append(c.plan.FreeText, FreeText{Kind: kind, UUID: u, Name: name, Field: field, Value: value, FolderLevel: folderLevel})
}

func (c *pubCtx) layer(kind, u string, layer map[string]any) {
	if len(layer) == 0 {
		return
	}
	b, _ := json.Marshal(layer)
	c.plan.Layers[u] = PlanLayer{Kind: kind, JSON: string(b)}
}

// freeKeys are the settings shown for review.
var freeKeys = []string{"initial_command", "env_vars", "ssh_options", "kafka_bootstrap", "db_addr", "db_name", "db_label"}

// settings translates one settings object to catalog references. It
// returns the catalog value and what stays local for the publisher.
func (c *pubCtx) settings(s store.InheritableSettings, kind, u, name string, folderLevel bool) (json.RawMessage, map[string]any, error) {
	m := toMap(s)
	layer := map[string]any{}
	// Local concepts: kept for the publisher, not sent.
	if v, ok := m["broadcast_group_id"]; ok {
		layer["broadcast_group_id"] = v
		delete(m, "broadcast_group_id")
	}
	// Sent as the catalog's suggestion; the publisher keeps their own.
	if v, ok := m["tunnel_autostart"]; ok {
		layer["tunnel_autostart"] = v
	}
	for _, k := range freeKeys {
		if v, ok := m[k]; ok && !isEmpty(v) {
			c.text(kind, u, name, k, display(v), folderLevel)
		}
	}
	for _, k := range credFields {
		if v, ok := m[k].(string); ok {
			m[k] = c.ref(v, slotCred, kind, u, name, k)
		}
	}
	if v, ok := m["network_profile_id"].(string); ok {
		m["network_profile_id"] = c.ref(v, slotNet, kind, u, name, "network_profile_id")
	}
	if targets, ok := m["db_targets"].([]any); ok {
		for i, x := range targets {
			tg, ok := x.(map[string]any)
			if !ok {
				continue
			}
			if v, ok := tg["credential_ref"].(string); ok {
				tg["credential_ref"] = c.ref(v, slotCred, kind, u, name, fmt.Sprintf("db_targets.%d.credential_ref", i))
			}
			var parts []string
			for _, f := range []string{"name", "addr", "database"} {
				if v, ok := tg[f].(string); ok && v != "" {
					parts = append(parts, f+"="+v)
				}
			}
			if len(parts) > 0 {
				c.text(kind, u, name, fmt.Sprintf("db_targets.%d", i), strings.Join(parts, " "), folderLevel)
			}
		}
	}
	if jh, ok := m["jump_host"].(map[string]any); ok && jh["kind"] == "chain" {
		if hop, ok := jh["chain"].(map[string]any); ok {
			c.hop(hop, kind, u, name, folderLevel)
		}
	}
	b, err := json.Marshal(m)
	return b, layer, err
}

func (c *pubCtx) hop(h map[string]any, kind, u, name string, folderLevel bool) {
	for depth := 0; h != nil && depth < 32; depth++ {
		delete(h, "name")
		if id, ok := h["connection_id"].(string); ok && id != "" {
			switch {
			case strings.HasPrefix(id, resolver.DynRefPrefix):
				// An inventory host means nothing to others: a jump slot
				// the publisher binds to it.
				h["jump_slot"] = c.slot("jump:"+id, slotJump, id, kind, u, name, "jump_host")
				delete(h, "connection_id")
			case c.inside[id] != "":
				h["connection_id"] = c.inside[id]
			case isMarker(id):
				c.refuse(kind, c.localOf(kind, u), name, "its jump host is an unbound or missing catalog value; choose one first")
			default:
				b, ok := c.conns[id]
				if !ok {
					c.refuse(kind, c.localOf(kind, u), name, "its jump host no longer exists; choose another first")
					break
				}
				oj := c.outside[id]
				if oj == nil {
					oj = &OutsideJump{ConnectionID: id, Name: b.Name, Include: c.opts.JumpInclude[id]}
					c.outside[id] = oj
				}
				oj.UsedBy = appendOnce(oj.UsedBy, name)
				if oj.Include {
					cu := c.inside["copy:"+id]
					if cu == "" {
						// Assigned now so references resolve; the copy
						// record is built after the subtree.
						cu = uuid.NewString()
						c.inside["copy:"+id] = cu
					}
					h["connection_id"] = cu
				} else {
					h["jump_slot"] = c.slot("jump:"+id, slotJump, id, kind, u, name, "jump_host")
					delete(h, "connection_id")
				}
			}
		} else if hn, ok := h["hostname"].(string); ok && hn != "" {
			c.text(kind, u, name, "jump_host.hostname", hn, folderLevel)
		}
		if v, ok := h["auth_ref"].(string); ok {
			h["auth_ref"] = c.ref(v, slotCred, kind, u, name, "jump_host.auth_ref")
		}
		next, _ := h["via"].(map[string]any)
		h = next
	}
}

func (c *pubCtx) localOf(kind, u string) string {
	for id, x := range c.inside {
		if x == u {
			return id
		}
	}
	return ""
}

// ref turns a local credential / profile id into a slot uuid.
func (c *pubCtx) ref(v, typ, kind, u, name, field string) string {
	if v == "" {
		return ""
	}
	if isMarker(v) {
		c.refuse(kind, c.localOf(kind, u), name, field+" holds an unbound or missing catalog value; choose one first")
		return ""
	}
	switch typ {
	case slotCred:
		if _, ok := c.creds[v]; !ok {
			c.refuse(kind, c.localOf(kind, u), name, field+" names a credential that no longer exists")
			return ""
		}
		return c.slot("cred:"+v, slotCred, v, kind, u, name, field)
	case slotNet:
		if _, ok := c.profiles[v]; !ok {
			c.refuse(kind, c.localOf(kind, u), name, field+" names a network profile that no longer exists")
			return ""
		}
		return c.slot("net:"+v, slotNet, v, kind, u, name, field)
	}
	return ""
}

// slot finds or creates the slot for a local object.
func (c *pubCtx) slot(key, typ, target, kind, u, name, field string) string {
	if c.slotUse[key] == nil {
		c.slotUse[key] = map[string]bool{}
	}
	c.slotUse[key][u] = true
	if s, ok := c.slots[key]; ok {
		return s.UUID
	}
	ps := &PlanSlot{Key: key, Type: typ, BindTarget: target}
	var bw *store.BitwardenRef
	switch typ {
	case slotCred:
		cr := c.creds[target]
		ps.BindName = cr.Name
		ps.Kind = "slot"
		if bw = store.ParseBitwardenRef(cr.Config); bw != nil {
			ps.Kind, ps.Backend = "external", "bitwarden"
		}
	case slotNet:
		ps.BindName = c.profiles[target].Name
	case slotJump:
		if b, ok := c.conns[target]; ok {
			ps.BindName = b.Name
		} else {
			ps.BindName = strings.TrimPrefix(target, resolver.DynRefPrefix)
		}
	}
	// Reuse a slot in scope that already stands for the same thing.
	for _, s := range c.scope {
		if s.Type != typ {
			continue
		}
		b, bound := c.bindings[s.UUID]
		same := bound && b.Kind == typ && b.TargetID == target
		if !same && bw != nil && s.Kind == "external" {
			var ref bitwardenRef
			if json.Unmarshal([]byte(s.ExternalRefJSON), &ref) == nil && ref.CipherID == bw.CipherID && fieldOr(ref.Field) == bw.Field {
				same = true
			}
		}
		if same {
			ps.UUID, ps.Name, ps.New = s.UUID, s.Name, false
			ps.Kind, ps.Backend = s.Kind, s.Backend
			ps.ReusedFrom = c.reusedNames[s.FolderUUID]
			c.slots[key] = ps
			return ps.UUID
		}
	}
	ps.UUID, ps.New = uuid.NewString(), true
	ps.Name = ps.BindName
	if n := strings.TrimSpace(c.opts.SlotNames[key]); n != "" {
		ps.Name = n
	}
	if ps.Name == "" {
		ps.Name = typ + " slot"
	}
	rec := PublishSlot{UUID: ps.UUID, FolderUUID: c.rootUUID, Type: typ, Name: ps.Name, ExternalRef: json.RawMessage("null")}
	if typ == slotCred {
		k := ps.Kind
		rec.Kind = &k
		if bw != nil {
			be := "bitwarden"
			rec.Backend = &be
			rec.ExternalRef, _ = json.Marshal(map[string]string{"cipher_id": bw.CipherID, "field": bw.Field})
		}
	}
	c.plan.Request.Slots = append(c.plan.Request.Slots, rec)
	c.slots[key] = ps
	return ps.UUID
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func fieldOr(f string) string {
	if f == "" {
		return "password"
	}
	return f
}

func (c *pubCtx) finish() {
	// Lists, never null, for the dialog.
	c.plan.Slots, c.plan.FreeText, c.plan.Refusals, c.plan.OutsideJumps = []PlanSlot{}, nonNil(c.plan.FreeText), nonNil(c.plan.Refusals), []OutsideJump{}
	c.plan.Request.Slots = nonNil(c.plan.Request.Slots)
	c.plan.Request.Forwards = nonNil(c.plan.Request.Forwards)
	if c.opts.TargetUUID != "" {
		t := c.opts.TargetUUID
		c.plan.Request.ParentUUID = &t
	}
	keys := make([]string, 0, len(c.slots))
	for k := range c.slots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := c.slots[k]
		s.UsedBy = len(c.slotUse[k])
		c.plan.Slots = append(c.plan.Slots, *s)
		if s.New {
			c.text(KindSlots, s.UUID, s.Name, "name", s.Name, true)
		}
	}
	ids := make([]string, 0, len(c.outside))
	for id := range c.outside {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c.plan.OutsideJumps = append(c.plan.OutsideJumps, *c.outside[id])
	}
}

func toMap(s store.InheritableSettings) map[string]any {
	b, _ := json.Marshal(s)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func isMarker(v string) bool { return strings.HasPrefix(v, "cat-") }

func isEmpty(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case map[string]any:
		return len(x) == 0
	}
	return v == nil
}

func display(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, x[k]))
		}
		return strings.Join(parts, "\n")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func appendOnce(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// Accepted is one record of a 201 publish response.
type Accepted struct {
	Kind string `json:"kind"`
	UUID string `json:"uuid"`
	Rev  int64  `json:"rev"`
}

// CompletePublish is the publisher's side of an accepted publish: the
// local rows become the catalog rows they were published as (same ids),
// the new slots are stored, every slot is bound to the publisher's own
// object, and what the catalog does not carry for them stays as their
// local layer. Then the catalog is materialised, which writes back the
// same settings. A bastion copy has no local row; the next sync brings it.
func CompletePublish(db *store.DB, plan *PublishPlan, accepted []Accepted, accountName string, external ExternalResolver) error {
	revs := map[string]int64{}
	for _, a := range accepted {
		revs[a.Kind+"/"+a.UUID] = a.Rev
	}
	rev := func(kind, u string) int64 {
		if r, ok := revs[kind+"/"+u]; ok {
			return r
		}
		return 1
	}
	stamp := func(rec any, kind, u string, extra map[string]any) string {
		b, _ := json.Marshal(rec)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		m["rev"] = rev(kind, u)
		m["created_by"], m["updated_by"] = accountName, accountName
		m["doc_md"] = ""
		for k, v := range extra {
			m[k] = v
		}
		out, _ := json.Marshal(m)
		return string(out)
	}
	raw := map[string]string{}
	for _, f := range plan.Request.Folders {
		raw[KindFolders+"/"+f.UUID] = stamp(f, KindFolders, f.UUID, map[string]any{"access": "write"})
	}
	for _, c := range plan.Request.Connections {
		raw[KindConnections+"/"+c.UUID] = stamp(c, KindConnections, c.UUID, nil)
	}
	for _, p := range plan.Request.Forwards {
		raw[KindForwards+"/"+p.UUID] = stamp(p, KindForwards, p.UUID, nil)
	}
	var rows []store.PublishedRow
	for _, r := range plan.Records {
		if r.LocalID == "" {
			continue
		}
		rows = append(rows, store.PublishedRow{Kind: r.Kind, LocalID: r.LocalID, RemoteUUID: r.UUID,
			Rev: rev(r.Kind, r.UUID), ParentLocalID: r.ParentLocalID, RawJSON: raw[r.Kind+"/"+r.UUID]})
	}
	var slots []store.CatalogSlot
	for _, s := range plan.Request.Slots {
		cs := store.CatalogSlot{UUID: s.UUID, Rev: rev(KindSlots, s.UUID), FolderUUID: s.FolderUUID, Type: s.Type, Name: s.Name}
		if s.Kind != nil {
			cs.Kind = *s.Kind
		}
		if s.Backend != nil {
			cs.Backend = *s.Backend
			cs.ExternalRefJSON = string(s.ExternalRef)
		}
		slots = append(slots, cs)
	}
	var bindings []store.CatalogBinding
	for _, s := range plan.Slots {
		bindings = append(bindings, store.CatalogBinding{SlotUUID: s.UUID, Kind: s.Type, TargetID: s.BindTarget})
	}
	var layers []store.CatalogOverride
	for u, l := range plan.Layers {
		layers = append(layers, store.CatalogOverride{RemoteUUID: u, Kind: l.Kind, JSON: l.JSON})
	}
	catID := plan.catalogID
	return db.WithCatalogWriter(func(tx *sql.Tx) error {
		if err := db.CompletePublishTx(tx, catID, rows, slots, bindings, layers); err != nil {
			return err
		}
		return materialiseTx(db, tx, catID, external)
	})
}
