package catalog

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	// ConnID publishes this one connection (with its forwards) instead of
	// a folder, straight into TargetUUID (protocol revision 3). RootID is
	// ignored then.
	ConnID string
	// TargetUUID: the catalog folder the subtree goes under; "" publishes
	// a new space (admin only, checked by the server).
	TargetUUID string
	// SlotNames: names for new slots, by PlanSlot.Key.
	SlotNames map[string]string
	// Admin: the publisher is a catalog admin (whoami). With Revision 12
	// it enables catalog-level slots: SlotsAtCatalog puts new slots there
	// instead of in the published root, and a slot of another space bound
	// to the same thing is moved there and reused, unless its key is in
	// NoSlotMove.
	Admin          bool
	SlotsAtCatalog bool
	NoSlotMove     map[string]bool
	// AccountName goes into the stored records as their author until the
	// feed brings the server's own fields.
	AccountName string
	// Unsent: the "What to send" categories the user unticked
	// (PublishCategories). What is not sent stays in the publisher's
	// local layer, so their own tree does not change.
	Unsent map[string]bool
	// Images: the catalog's image store limits; nil when the server has
	// none (revision 1), and uploaded icons stay in the publisher's layer.
	Images *ImageLimits
	// A credential from the publisher's own vault and a network (VPN)
	// profile are never sent: they stay in the publisher's layer, and
	// readers set their own under My settings. Only shared sources (a
	// Bitwarden / Vaultwarden item, opkssh) go, as slots.
	//
	// NoSharedCreds: shared sources (Bitwarden / Vaultwarden items,
	// opkssh) stay local too. SkipCreds: credentials (local ids) the user
	// chose not to send, whatever their kind.
	NoSharedCreds bool
	SkipCreds     map[string]bool
	// A jump host outside the published folder is sent as a bastion record
	// inside the published root: a saved connection as a copy of it, an
	// inventory host ("dyn:<entry>") as a record made from what ResolveDyn
	// says it is now (address, port, user, route). The publisher keeps
	// their own jump. JumpSkip names the ones (connection or dyn id) not to
	// send: they stay in the publisher's layer. A jump host typed into the
	// chain (hostname, no connection) goes as it is, no record is made.
	// ResolveDyn nil: inventory jumps stay local.
	ResolveDyn func(id string) (store.Connection, bool) `json:"-"`
	JumpSkip   map[string]bool
	// Revision: the server's protocol revision (from a fresh meta). A
	// forward's bookmarks go only from revision 6 and only when the
	// "bookmarks" category is ticked (it starts unticked).
	Revision int
}

// PublishCategories are the optional parts of a publish, in dialog
// order. Settings categories name their settings keys; notes, tags,
// icons and forwards are record fields. Everything else (name, address,
// protocol, port, credential, one-time code, jump host,
// network, ssh options, tunnel settings, sensitive) is always sent: the
// connection does not work, or is not safe, without it.
var PublishCategories = []struct {
	ID   string
	Keys []string
}{
	{"username", []string{"username"}},
	{"notes", nil},
	{"tags", nil},
	{"icons", nil},
	{"forwards", nil},
	{"bookmarks", nil},
	{"database", []string{"db_enabled", "db_engine", "db_addr", "db_name", "db_use_tunnel", "db_default", "db_read_only",
		"db_query_timeout", "db_tls", "db_credential_ref", "db_label", "db_in_tree", "db_default_target", "db_targets"}},
	{"kafka", []string{"kafka_enabled", "kafka_bootstrap", "kafka_use_tunnel", "kafka_default", "kafka_read_only", "kafka_tls",
		"kafka_tls_skip_verify", "kafka_sasl", "kafka_credential_ref"}},
	{"vnc", []string{"vnc_enabled", "vnc_port", "vnc_use_tunnel", "vnc_default"}},
	{"startup", []string{"initial_command", "initial_command_line_delay_ms", "env_vars"}},
	{"terminal", []string{"color_tag", "terminal_type", "keepalive_interval", "auto_reconnect", "verbose", "probe_liveness"}},
}

// unsentKeys: the settings keys of the unticked categories.
func (o PublishOptions) unsentKeys() map[string]bool {
	out := map[string]bool{}
	for _, c := range PublishCategories {
		if o.Unsent[c.ID] {
			for _, k := range c.Keys {
				out[k] = true
			}
		}
	}
	return out
}

// RecordLayerKey is the key of a folder's or connection's local layer
// that holds record fields kept for this user (notes, tags, icons) rather
// than settings: what a publish did not send, or an uploaded icon image
// the catalog cannot carry. A present field replaces the record's.
const RecordLayerKey = "_record"

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
	IconImage  *string         `json:"icon_image,omitempty"`
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
	IconImage  *string         `json:"icon_image,omitempty"`
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
	// Bookmarks: shared ones (revision 6), only when the user chose to.
	Bookmarks []Bookmark `json:"bookmarks,omitempty"`
}

type PublishSlot struct {
	UUID string `json:"uuid"`
	// FolderUUID "" is a catalog-level slot, sent as null (revision 12).
	FolderUUID  string          `json:"folder_uuid"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Kind        *string         `json:"kind"`
	Backend     *string         `json:"backend"`
	ExternalRef json.RawMessage `json:"external_ref"`
	DocMD       string          `json:"doc_md"`
}

// MarshalJSON sends a catalog-level slot's folder_uuid as null.
func (s PublishSlot) MarshalJSON() ([]byte, error) {
	type plain PublishSlot
	b, err := json.Marshal(plain(s))
	if err != nil || s.FolderUUID != "" {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["folder_uuid"] = json.RawMessage("null")
	return json.Marshal(m)
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
	// Reused: where the existing slot lives (its folder's name; "the
	// whole catalog" for a catalog-level slot).
	ReusedFrom string `json:"reused_from,omitempty"`
	UsedBy     int    `json:"used_by"`
	// MoveToCatalog: an existing slot of another space, bound to the same
	// thing here, that the publish moves to the catalog level first so
	// this subtree can use it (admin, revision 12). Rev is its revision
	// for that move. CatalogLevel: a new slot made at the catalog level.
	MoveToCatalog bool  `json:"move_to_catalog,omitempty"`
	Rev           int64 `json:"rev,omitempty"`
	CatalogLevel  bool  `json:"catalog_level,omitempty"`
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
	// Dynamic: an inventory host; Include sends a bastion record made
	// from what it resolves to now.
	Dynamic bool `json:"dynamic,omitempty"`
}

// PublishPlan is everything the dialog shows and the publish sends.
type PublishPlan struct {
	Request PublishRequest `json:"-"`
	Records []PlanRecord   `json:"records"`
	Slots   []PlanSlot     `json:"slots"`
	// CatalogSlots: catalog-level slots are on offer (admin, revision 12):
	// new slots can go to the whole catalog, other spaces' slots move there.
	CatalogSlots bool          `json:"catalog_slots,omitempty"`
	FreeText     []FreeText    `json:"free_text"`
	Refusals     []Refusal     `json:"refusals"`
	OutsideJumps []OutsideJump `json:"outside_jumps"`
	// ReviewHash fingerprints what the user reviews (free text, references
	// kept local, slots): a publish whose rebuilt plan differs from the
	// previewed one is refused, so text changed after the review is never
	// sent unseen.
	ReviewHash string `json:"review_hash"`
	// TargetInherits: settings the target folder (or one above it) sets
	// that the published folder leaves unset today: after the publish
	// the folder inherits them, for the publisher too ("port: 2222").
	TargetInherits []string `json:"target_inherits"`
	// KeptLocal: references that stay the publisher's own (not sent, no
	// slot), with why. Readers get these records without them.
	KeptLocal []KeptRef `json:"kept_local"`
	// Images: the uploaded icons of the subtree, one per distinct image.
	Images []PlanImage `json:"images"`
	// Layers: remote uuid -> what stays local for the publisher (a
	// tunnel autostart, a broadcast group), as a local layer.
	Layers map[string]PlanLayer `json:"-"`
	// TargetLocalID: the local folder the published root goes under.
	TargetLocalID string `json:"-"`
	RootUUID      string `json:"root_uuid"`
	catalogID     string
}

func reviewHash(p *PublishPlan) string {
	// Without the uuids: every build of the plan makes new ones.
	type ft struct {
		Kind, Name, Field, Value, From string
		Folder                         bool
	}
	var f []ft
	for _, x := range p.FreeText {
		f = append(f, ft{x.Kind, x.Name, x.Field, x.Value, x.InheritedFrom, x.FolderLevel})
	}
	var r []string
	for _, x := range p.Records {
		r = append(r, x.Kind+"\x00"+x.Path)
	}
	// Slot moves and catalog-level slots reach other spaces: a plan that
	// gains one since the preview is a different plan.
	var sl []string
	for _, x := range p.Slots {
		if x.MoveToCatalog || x.CatalogLevel {
			sl = append(sl, fmt.Sprintf("%s\x00%v\x00%v", x.Key, x.MoveToCatalog, x.CatalogLevel))
		}
	}
	sort.Strings(sl)
	b, _ := json.Marshal(struct {
		F []ft
		K []KeptRef
		R []string
		S []string `json:",omitempty"`
	}{f, p.KeptLocal, r, sl})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// targetInherits lists what the target's chain sets that the published
// root, as it resolves today, leaves unset.
func (c *pubCtx) targetInherits() {
	if c.plan.TargetLocalID == "" {
		return
	}
	all := c.allFolders()
	target := toMap(resolver.EffectiveFolderSettings(c.plan.TargetLocalID, all))
	root := toMap(resolver.EffectiveFolderSettings(c.opts.RootID, all))
	if conn, ok := c.conns[c.opts.ConnID]; ok && c.opts.ConnID != "" {
		root = toMap(resolver.EffectiveOverrides(conn, all))
	}
	keys := make([]string, 0, len(target))
	for k := range target {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, set := root[k]; set || isEmpty(target[k]) {
			continue
		}
		if store.IsCatalogRef(fmt.Sprint(target[k])) {
			c.plan.TargetInherits = append(c.plan.TargetInherits, k+": a catalog slot")
			continue
		}
		c.plan.TargetInherits = append(c.plan.TargetInherits, k+": "+display(target[k]))
	}
}

// KeptRef is a reference a publish keeps local.
type KeptRef struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
	// Cred: the credential (local id) when the user can choose to send it
	// after all (one they unticked).
	Cred string `json:"cred,omitempty"`
}

// keepRef is what ref returns for a reference that stays local.
const keepRef = "\x00keep"

func (c *pubCtx) kept(kind, name, field, reason string) {
	c.plan.KeptLocal = append(c.plan.KeptLocal, KeptRef{Kind: kind, Name: name, Field: field, Reason: reason})
}

// sharedCred: a credential every reader can use the same way.
func sharedCred(cr store.CredentialRef) bool {
	return cr.Kind == store.CredOpkssh || store.ParseBitwardenRef(cr.Config) != nil
}

// PlanImage is one distinct uploaded icon of the publish.
type PlanImage struct {
	SHA256  string `json:"sha256"`
	ImageID string `json:"image_id"` // the local image row
	// Names of the records wearing it.
	Records []string `json:"records"`
	// Kept: the image is not sent and stays in the publisher's own layer
	// for these records, for Reason (the catalog takes no images, the
	// check failed, Icons unticked, the upload limit).
	Kept   bool   `json:"kept"`
	Reason string `json:"reason,omitempty"`
	// Later: kept now, uploaded and set in the background after the
	// publish (the catalog's upload limit).
	Later bool `json:"later,omitempty"`
	uses  []imageUse
}

type imageUse struct{ kind, uuid string }

// PlanLayer is a converted record's local layer.
type PlanLayer struct {
	Kind string
	JSON string
}

// Pending is the plan's rows as recorded before the request goes out.
// Each row carries its local layer, and the first one the bindings of the
// plan's slots, so a publish whose answer was lost is finished by the
// next sync with the publisher's side as CompletePublish would write it.
func (p *PublishPlan) Pending() []store.PublishPending {
	var out []store.PublishPending
	var bindings []store.CatalogBinding
	for _, s := range p.Slots {
		bindings = append(bindings, store.CatalogBinding{SlotUUID: s.UUID, Kind: s.Type, TargetID: s.BindTarget})
	}
	for _, r := range p.Records {
		if r.LocalID == "" {
			continue
		}
		var x store.PublishPendingExtra
		if l, ok := p.Layers[r.UUID]; ok {
			x.Layer = &store.CatalogOverride{RemoteUUID: r.UUID, Kind: l.Kind, JSON: l.JSON}
		}
		if len(out) == 0 {
			x.Bindings = bindings
		}
		extra := ""
		if x.Layer != nil || len(x.Bindings) > 0 {
			b, _ := json.Marshal(x)
			extra = string(b)
		}
		out = append(out, store.PublishPending{Kind: r.Kind, RemoteUUID: r.UUID, LocalID: r.LocalID, Extra: extra})
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
	db *store.DB
	// strict: a record-mode edit, where the writer chose each value; a
	// reference that no longer exists is refused instead of kept local.
	strict bool
	opts   PublishOptions
	plan   *PublishPlan

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
	allSlots []store.CatalogSlot        // every slot of the catalog
	bindings map[string]store.CatalogBinding
	outside  map[string]*OutsideJump
	rootUUID string
	// reusedNames: folder uuid -> name along the target's chain.
	reusedNames map[string]string
	// passthrough (record edits): local values that already stand for a
	// catalog reference - a marker, a credential made for an external
	// slot - and the reference they stand for.
	passthrough map[string]passRef
	inScope     map[string]bool // slot uuid -> usable from the record's folder
}

// passRef is a catalog reference a local value stands for.
type passRef struct {
	UUID string
	Slot bool
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
	if opts.ConnID != "" {
		return c.buildConnection(cat)
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
	if err := c.bastionCopies(); err != nil {
		return nil, err
	}
	c.finish()
	return c.plan, nil
}

// buildConnection plans the publish of one connection (opts.ConnID)
// straight into the target folder: no folder record, the connection's
// slots and bastion copies go into the target too. What it inherits from
// its local folders is folded onto it, as for a bastion copy, so it
// dials the same way for a reader.
func (c *pubCtx) buildConnection(cat *store.Catalog) (*PublishPlan, error) {
	conn, ok := c.conns[c.opts.ConnID]
	if !ok {
		return nil, fmt.Errorf("publish: connection not found")
	}
	if c.opts.TargetUUID == "" {
		return nil, fmt.Errorf("publish: a single connection goes into an existing catalog folder")
	}
	if conn.CatalogID != nil {
		c.refuse("connections", conn.ID, conn.Name, "already belongs to a catalog")
		c.finish()
		return c.plan, nil
	}
	if err := c.target(cat); err != nil {
		return nil, err
	}
	c.rootUUID = c.opts.TargetUUID
	c.plan.RootUUID = c.rootUUID
	c.inside[conn.ID] = uuid.NewString()
	ids := []string{conn.ID}
	if strings.EqualFold(strings.TrimSpace(conn.Protocol), "local") {
		c.refuse("connections", conn.ID, conn.Name, "a local shell connection cannot be published (it would run on colleagues' machines)")
	}
	if pin, err := c.db.GetPinForConnection(conn.ID); err != nil {
		return nil, err
	} else if pin != nil {
		c.refuse("connections", conn.ID, conn.Name, "pinned from a dynamic inventory folder; move it out first")
	}
	for _, p := range c.fwds[conn.ID] {
		ids = append(ids, p.ID)
	}
	if busy, err := c.db.PendingPublishForLocal(ids); err != nil {
		return nil, err
	} else if busy {
		c.refuse("connections", conn.ID, conn.Name, "an earlier publish of it has not settled yet; it settles on the next sync")
	}
	if err := c.connection(conn, c.rootUUID, ""); err != nil {
		return nil, err
	}
	if err := c.bastionCopies(); err != nil {
		return nil, err
	}
	c.finish()
	return c.plan, nil
}

// bastionCopies adds the outside bastions that are sent as copies (their
// own references may add further copies).
func (c *pubCtx) bastionCopies() error {
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
				return err
			}
		}
	}
	return nil
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
		// A new space: only catalog-level slots are in scope.
		c.plan.TargetLocalID = cat.RootFolderID
		slots, err := c.db.ListCatalogSlots(c.opts.CatalogID)
		if err != nil {
			return err
		}
		c.allSlots = slots
		for _, s := range slots {
			if s.FolderUUID == "" {
				c.scope = append(c.scope, s)
			}
		}
		c.reusedNames = map[string]string{"": "the whole catalog"}
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
	c.targetInherits()
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
	c.allSlots = slots
	for _, s := range slots {
		// A catalog-level slot ("" folder) is in scope everywhere.
		if _, ok := chain[s.FolderUUID]; ok || s.FolderUUID == "" {
			c.scope = append(c.scope, s)
		}
	}
	chain[""] = "the whole catalog"
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
	pf := PublishFolder{UUID: u, ParentUUID: parent, Name: f.Name, SortOrder: f.SortOrder, Settings: raw,
		IconName: f.IconName, IconColor: f.IconColor}
	rec := map[string]any{}
	c.icons(KindFolders, u, f.Name, &pf.IconName, &pf.IconColor, &pf.IconImage, f.IconImageID, rec)
	if len(rec) > 0 {
		layer[RecordLayerKey] = rec
	}
	c.plan.Request.Folders = append(c.plan.Request.Folders, pf)
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
	unsent := c.opts.unsentKeys()
	for k := range eff {
		if !reflect.DeepEqual(own[k], eff[k]) && !unsent[k] {
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

// connInherited lists, for review, the values a single published
// connection takes from its local folders (folded onto it).
func (c *pubCtx) connInherited(conn store.Connection, effective store.InheritableSettings, u string) {
	own := toMap(conn.Overrides)
	eff := toMap(effective)
	unsent := c.opts.unsentKeys()
	var keys []string
	for k := range eff {
		if !reflect.DeepEqual(own[k], eff[k]) && !unsent[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var from []string
		for id, depth := conn.FolderID, 0; id != nil && depth < 64; depth++ {
			f, ok := c.folders[*id]
			if !ok {
				break
			}
			if _, has := toMap(f.Settings)[k]; has {
				from = append(from, f.Name)
			}
			id = f.ParentID
		}
		c.plan.FreeText = append(c.plan.FreeText, FreeText{Kind: KindConnections, UUID: u, Name: conn.Name, Field: k,
			Value: display(eff[k]), InheritedFrom: strings.Join(from, ", ")})
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
	if c.opts.ConnID != "" && copyOf == "" {
		// A single connection leaves its folders behind and is converted
		// in place into the target folder.
		parentLocal = c.plan.TargetLocalID
		path = conn.Name
		overrides = resolver.EffectiveOverrides(conn, c.allFolders())
		c.connInherited(conn, overrides, u)
	}
	if copyOf != "" {
		u = c.inside["copy:"+copyOf]
		localID, parentLocal = "", c.opts.RootID
		path = c.folders[c.opts.RootID].Name + "/" + conn.Name
		if c.opts.ConnID != "" {
			parentLocal, path = c.plan.TargetLocalID, conn.Name
		}
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
	pc := PublishConnection{UUID: u, FolderUUID: folderUUID, Name: conn.Name, Hostname: conn.Hostname, Protocol: proto,
		SortOrder: conn.SortOrder, Settings: raw, Tags: tags, Notes: conn.Notes,
		IconName: conn.IconName, IconColor: conn.IconColor}
	// Record fields not sent stay in the publisher's layer.
	rec := map[string]any{}
	if c.opts.Unsent["notes"] {
		if conn.Notes != "" {
			rec["notes"] = conn.Notes
		}
		pc.Notes = ""
	}
	if c.opts.Unsent["tags"] {
		if len(conn.Tags) > 0 {
			rec["tags"] = conn.Tags
		}
		pc.Tags = []string{}
	}
	c.icons(KindConnections, u, conn.Name, &pc.IconName, &pc.IconColor, &pc.IconImage, conn.IconImageID, rec)
	if len(rec) > 0 {
		layer[RecordLayerKey] = rec
	}
	c.plan.Request.Connections = append(c.plan.Request.Connections, pc)
	c.plan.Records = append(c.plan.Records, PlanRecord{Kind: KindConnections, UUID: u, LocalID: localID, Name: conn.Name,
		Path: path, ParentLocalID: parentLocal, CopyOf: copyOf})
	if copyOf == "" {
		c.layer("connections", u, layer)
	}
	if pc.Notes != "" {
		c.text(KindConnections, u, conn.Name, "notes", pc.Notes, false)
	}
	if len(pc.Tags) > 0 {
		c.text(KindConnections, u, conn.Name, "tags", strings.Join(pc.Tags, ", "), false)
	}
	if copyOf != "" || c.opts.Unsent["forwards"] {
		// A bastion copy goes without the bastion's forwards; unsent
		// forwards stay the publisher's own, on the catalog connection.
		return nil
	}
	for _, p := range c.fwds[conn.ID] {
		fu := uuid.NewString()
		pf := PublishForward{
			UUID: fu, ConnectionUUID: u, Kind: p.Kind, LocalAddr: p.LocalAddr, LocalPort: p.LocalPort,
			RemoteHost: p.RemoteHost, RemotePort: p.RemotePort, AutoStart: p.AutoStart, Description: p.Description,
		}
		if c.opts.Revision >= BookmarksRevision && !c.opts.Unsent["bookmarks"] {
			// in_tree and the like stay local: only name and URL go.
			pf.Bookmarks = SharedBookmarks(p.Bookmarks, c.opts.Revision)
		}
		c.plan.Request.Forwards = append(c.plan.Request.Forwards, pf)
		name := p.Description
		if name == "" {
			name = p.Kind + " forward"
		}
		for _, b := range pf.Bookmarks {
			// A URL can carry a token; it is reviewed like any free text.
			c.text(KindForwards, fu, name, "bookmark "+b.Name, b.URL, false)
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

// icons handles a record's icon for publishing: a built-in icon goes
// unless "Icons" is unticked; an uploaded image goes when the catalog has
// an image store and the image passes its check, else it stays in the
// publisher's layer (as does an unsent built-in icon). Every distinct
// image is listed in the plan with what happens to it.
func (c *pubCtx) icons(kind, u, recName string, name, color, iconImage **string, imageID *string, rec map[string]any) {
	if imageID != nil && *imageID != "" {
		pi := c.planImage(*imageID)
		if pi != nil {
			pi.Records = append(pi.Records, recName)
			pi.uses = append(pi.uses, imageUse{kind, u})
		}
		if c.opts.Unsent["icons"] || pi == nil || pi.Kept {
			rec["icon_image_id"] = *imageID
		} else {
			ref := "sha256:" + pi.SHA256
			*iconImage, *name, *color = &ref, nil, nil
		}
	}
	if c.opts.Unsent["icons"] {
		if *name != nil {
			rec["icon_name"] = **name
		}
		if *color != nil {
			rec["icon_color"] = **color
		}
		*name, *color = nil, nil
	}
}

// planImage returns the plan's entry for a local image, checking it the
// first time (nil when the row is gone).
func (c *pubCtx) planImage(id string) *PlanImage {
	for i := range c.plan.Images {
		if c.plan.Images[i].ImageID == id {
			return &c.plan.Images[i]
		}
	}
	_, data, ok, err := c.db.GetImage(id)
	if err != nil || !ok {
		return nil
	}
	pi := PlanImage{SHA256: ImageSHA256(data), ImageID: id}
	switch {
	case c.opts.Unsent["icons"]:
		pi.Kept, pi.Reason = true, "icons are not sent"
	case c.opts.Images == nil:
		pi.Kept, pi.Reason = true, "this catalog does not take icon images yet"
	default:
		if _, err := CheckImage(data, *c.opts.Images); err != nil {
			pi.Kept, pi.Reason = true, err.Error()
		}
	}
	c.plan.Images = append(c.plan.Images, pi)
	return &c.plan.Images[len(c.plan.Images)-1]
}

// KeepImage takes an image out of the request (refused by the server,
// or held back by the upload limit): the records go without it, and the
// publisher's converted rows keep it in their layer. later marks it for
// the background upload after the publish.
func (p *PublishPlan) KeepImage(hash, reason string, later bool) {
	for i := range p.Images {
		pi := &p.Images[i]
		if pi.SHA256 != hash || pi.Kept {
			continue
		}
		pi.Kept, pi.Reason, pi.Later = true, reason, later
		ref := "sha256:" + hash
		for j := range p.Request.Folders {
			if f := &p.Request.Folders[j]; f.IconImage != nil && *f.IconImage == ref {
				f.IconImage = nil
			}
		}
		for j := range p.Request.Connections {
			if c := &p.Request.Connections[j]; c.IconImage != nil && *c.IconImage == ref {
				c.IconImage = nil
			}
		}
		for _, use := range pi.uses {
			if !p.hasLocalRow(use.uuid) {
				continue // a bastion copy: no row of the publisher's
			}
			layer := map[string]any{}
			if l, ok := p.Layers[use.uuid]; ok {
				_ = json.Unmarshal([]byte(l.JSON), &layer)
			}
			rec, _ := layer[RecordLayerKey].(map[string]any)
			if rec == nil {
				rec = map[string]any{}
			}
			rec["icon_image_id"] = pi.ImageID
			if later {
				rec[IconPendingKey] = true
			}
			layer[RecordLayerKey] = rec
			b, _ := json.Marshal(layer)
			p.Layers[use.uuid] = PlanLayer{Kind: use.kind, JSON: string(b)}
		}
	}
}

func (p *PublishPlan) hasLocalRow(u string) bool {
	for _, r := range p.Records {
		if r.UUID == u {
			return r.LocalID != ""
		}
	}
	return false
}

// SentImages are the images the request carries.
func (p *PublishPlan) SentImages() []PlanImage {
	var out []PlanImage
	for _, pi := range p.Images {
		if !pi.Kept {
			out = append(out, pi)
		}
	}
	return out
}

// SetPlanImageUses records which records wear a plan image (for tests
// that build a plan by hand; BuildPublish fills it itself).
func SetPlanImageUses(p *PublishPlan, hash, kind, uuid string) {
	for i := range p.Images {
		if p.Images[i].SHA256 == hash {
			p.Images[i].uses = append(p.Images[i].uses, imageUse{kind, uuid})
		}
	}
}

// IconPendingKey marks, in a record's "_record" layer, an icon image that
// still has to reach the catalog (held back by the upload limit at
// publish): the background upload sends it and sets icon_image on the
// record, and the mark goes. Setting another icon drops it.
const IconPendingKey = "icon_pending"

// listed: the record already has this field in the review.
func (c *pubCtx) listed(u, field string) bool {
	for _, f := range c.plan.FreeText {
		if f.UUID == u && f.Field == field {
			return true
		}
	}
	return false
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
	// Unticked in "What to send": kept for the publisher only. Removed
	// before the review and the slot translation, so an unsent login
	// lists nothing and creates no slot.
	for k := range c.opts.unsentKeys() {
		if v, ok := m[k]; ok {
			layer[k] = v
			delete(m, k)
		}
	}
	for _, k := range freeKeys {
		if c.listed(u, k) {
			continue // already listed as folded onto the root
		}
		if v, ok := m[k]; ok && !isEmpty(v) {
			c.text(kind, u, name, k, display(v), folderLevel)
		}
	}
	for _, k := range credFields {
		if v, ok := m[k].(string); ok {
			if r := c.ref(v, slotCred, kind, u, name, k); r == keepRef {
				// Sent as an explicit empty value, so readers do not
				// inherit a login from a folder above instead.
				layer[k] = v
				m[k] = ""
			} else {
				m[k] = r
			}
		}
	}
	if v, ok := m["network_profile_id"].(string); ok {
		if r := c.ref(v, slotNet, kind, u, name, "network_profile_id"); r == keepRef {
			layer["network_profile_id"] = v
			delete(m, "network_profile_id")
		} else {
			m["network_profile_id"] = r
		}
	}
	if targets, ok := m["db_targets"].([]any); ok {
		for i, x := range targets {
			tg, ok := x.(map[string]any)
			if !ok {
				continue
			}
			if v, ok := tg["credential_ref"].(string); ok {
				if r := c.ref(v, slotCred, kind, u, name, fmt.Sprintf("db_targets.%d.credential_ref", i)); r == keepRef {
					// The publisher keeps the login on this target.
					own := map[string]any{"id": tg["id"], "credential_ref": v, "override": true}
					list, _ := layer["db_targets"].([]any)
					layer["db_targets"] = append(list, own)
					// Explicitly empty: a shared connection-level login
					// must not stand in for the withheld one.
					tg["credential_ref"] = ""
				} else {
					tg["credential_ref"] = r
				}
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
			orig := deepCopy(jh)
			keep, own := c.hop(hop, kind, u, name, folderLevel)
			switch {
			case keep:
				// A hop readers cannot have: the whole jump host stays the
				// publisher's own.
				layer["jump_host"] = orig
				delete(m, "jump_host")
			case own:
				layer["jump_host"] = orig
			}
		}
	}
	b, err := json.Marshal(m)
	return b, layer, err
}

// hop translates a jump chain; true when it holds something that stays
// local (the caller keeps the whole jump host then).
// own: the chain is sent, but the publisher keeps their original (a jump
// through an inventory host sent as a bastion record).
func (c *pubCtx) hop(h map[string]any, kind, u, name string, folderLevel bool) (keep, own bool) {
	for depth := 0; h != nil && depth < 32; depth++ {
		delete(h, "name")
		if id, ok := h["connection_id"].(string); ok && id != "" {
			if p, ok := c.passthrough[id]; ok {
				if p.Slot {
					if !c.inScope[p.UUID] {
						c.refuse(kind, c.localOf(kind, u), name, "its jump host is a catalog slot that is not available in this folder")
					}
					h["jump_slot"] = p.UUID
					delete(h, "connection_id")
				} else {
					h["connection_id"] = p.UUID
				}
				goto auth
			}
			switch {
			case strings.HasPrefix(id, resolver.DynRefPrefix) && c.strict:
				c.refuse(kind, c.localOf(kind, u), name, "its jump host is one of your inventory hosts; pick a bastion from this catalog, or set your own under My settings")
			case strings.HasPrefix(id, resolver.DynRefPrefix):
				// An inventory host means nothing to others: a bastion
				// record made from what it resolves to now goes instead,
				// and the publisher keeps their own jump.
				if c.opts.ResolveDyn != nil && !c.opts.JumpSkip[id] {
					if b, ok := c.opts.ResolveDyn(id); ok {
						if b.Name == "" {
							b.Name = strings.TrimPrefix(id, resolver.DynRefPrefix)
						}
						b.ID = id
						c.conns[id] = b
						oj := c.outside[id]
						if oj == nil {
							oj = &OutsideJump{ConnectionID: id, Name: b.Name, Include: true, Dynamic: true}
							c.outside[id] = oj
						}
						oj.UsedBy = appendOnce(oj.UsedBy, name)
						cu := c.inside["copy:"+id]
						if cu == "" {
							cu = uuid.NewString()
							c.inside["copy:"+id] = cu
						}
						h["connection_id"] = cu
						own = true
						goto auth
					}
				}
				if c.opts.ResolveDyn != nil && c.opts.JumpSkip[id] && c.outside[id] == nil {
					c.outside[id] = &OutsideJump{ConnectionID: id, Name: strings.TrimPrefix(id, resolver.DynRefPrefix), Dynamic: true}
				}
				if oj := c.outside[id]; oj != nil {
					oj.UsedBy = appendOnce(oj.UsedBy, name)
				}
				c.kept(kind, name, "Jump host", "a dynamic inventory host; colleagues set their own")
				return true, false
			case c.inside[id] != "":
				h["connection_id"] = c.inside[id]
			case isMarker(id):
				c.refuse(kind, c.localOf(kind, u), name, "its jump host is an unbound or missing catalog value; choose one first")
			default:
				b, ok := c.conns[id]
				if !ok {
					if c.strict {
						c.refuse(kind, c.localOf(kind, u), name, "its jump host no longer exists; choose another first")
						break
					}
					c.kept(kind, name, "Jump host", "points at a connection that no longer exists; not sent")
					return true, false
				}
				if c.strict {
					c.refuse(kind, c.localOf(kind, u), name, "its jump host ("+b.Name+") is not in this catalog; pick a bastion from the catalog, or set your own under My settings")
					break
				}
				oj := c.outside[id]
				if oj == nil {
					oj = &OutsideJump{ConnectionID: id, Name: b.Name, Include: !c.opts.JumpSkip[id]}
					c.outside[id] = oj
				}
				oj.UsedBy = appendOnce(oj.UsedBy, name)
				if !oj.Include {
					c.kept(kind, name, "Jump host", b.Name+": you chose not to send it; colleagues set their own")
					return true, false
				}
				cu := c.inside["copy:"+id]
				if cu == "" {
					// Assigned now so references resolve; the copy
					// record is built after the subtree.
					cu = uuid.NewString()
					c.inside["copy:"+id] = cu
				}
				h["connection_id"] = cu
				// The publisher keeps jumping through their own bastion.
				own = true
			}
		} else if hn, ok := h["hostname"].(string); ok && hn != "" {
			c.text(kind, u, name, "jump_host.hostname", hn, folderLevel)
		}
	auth:
		if v, ok := h["auth_ref"].(string); ok {
			r := c.ref(v, slotCred, kind, u, name, "jump_host.auth_ref")
			if r == keepRef {
				// Sent without it; the publisher keeps their own chain.
				delete(h, "auth_ref")
				own = true
			} else {
				h["auth_ref"] = r
			}
		}
		next, _ := h["via"].(map[string]any)
		h = next
	}
	return false, own
}

func deepCopy(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
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
	if p, ok := c.passthrough[v]; ok && p.Slot {
		if !c.inScope[p.UUID] {
			c.refuse(kind, c.localOf(kind, u), name, field+" names a catalog slot that is not available in this folder")
			return ""
		}
		return p.UUID
	}
	if isMarker(v) {
		c.refuse(kind, c.localOf(kind, u), name, field+" holds an unbound or missing catalog value; choose one first")
		return ""
	}
	label := fieldLabel(field)
	switch typ {
	case slotCred:
		cr, ok := c.creds[v]
		if !ok {
			if c.strict {
				c.refuse(kind, c.localOf(kind, u), name, field+" names a credential that no longer exists")
				return ""
			}
			c.kept(kind, name, label, "names a credential that no longer exists; not sent")
			return keepRef
		}
		if c.opts.SkipCreds[v] {
			c.plan.KeptLocal = append(c.plan.KeptLocal, KeptRef{Kind: kind, Name: name, Field: label,
				Reason: "you chose not to send " + cr.Name, Cred: v})
			return keepRef
		}
		if sharedCred(cr) && c.opts.NoSharedCreds {
			c.kept(kind, name, label, "a shared-source credential ("+cr.Name+"); shared sources are not sent")
			return keepRef
		}
		if !sharedCred(cr) {
			// Never a new slot; one already in scope and bound to it here
			// (a colleague's, say) is used as it is.
			if c.boundInScope(slotCred, v) {
				return c.slot("cred:"+v, slotCred, v, kind, u, name, field)
			}
			if c.strict {
				c.refuse(kind, c.localOf(kind, u), name, label+": "+cr.Name+" is in your own vault and is not shared; set it under My settings, or pick a Vaultwarden / Bitwarden item")
				return ""
			}
			c.kept(kind, name, label, "a credential from your own vault ("+cr.Name+"); colleagues use their own")
			return keepRef
		}
		return c.slot("cred:"+v, slotCred, v, kind, u, name, field)
	case slotNet:
		p, ok := c.profiles[v]
		if !ok {
			if c.strict {
				c.refuse(kind, c.localOf(kind, u), name, field+" names a network profile that no longer exists")
				return ""
			}
			c.kept(kind, name, label, "names a network profile that no longer exists; not sent")
			return keepRef
		}
		if c.boundInScope(slotNet, v) {
			return c.slot("net:"+v, slotNet, v, kind, u, name, field)
		}
		if c.strict {
			c.refuse(kind, c.localOf(kind, u), name, "network profiles are personal; set "+p.Name+" under My settings")
			return ""
		}
		c.kept(kind, name, label, "your network profile ("+p.Name+"); colleagues use their own")
		return keepRef
	}
	return ""
}

func fieldLabel(f string) string {
	switch {
	case f == "auth_ref", f == "jump_host.auth_ref":
		return "Credential"
	case f == "otp_ref":
		return "One-time code"
	case f == "network_profile_id":
		return "Network"
	case f == "db_credential_ref", strings.HasPrefix(f, "db_targets."):
		return "Database login"
	case f == "kafka_credential_ref":
		return "Kafka login"
	}
	return f
}

// boundInScope: a slot of this type in scope is already bound to target.
func (c *pubCtx) boundInScope(typ, target string) bool {
	for _, s := range c.scope {
		if b, ok := c.bindings[s.UUID]; ok && s.Type == typ && b.Kind == typ && b.TargetID == target {
			return true
		}
	}
	return false
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
			// Keep the binding the slot already has here (the same item
			// through another credential): rebinding it would change every
			// record that uses it.
			if bound {
				ps.BindTarget = b.TargetID
			}
			ps.UUID, ps.Name, ps.New = s.UUID, s.Name, false
			ps.Kind, ps.Backend = s.Kind, s.Backend
			ps.ReusedFrom = c.reusedNames[s.FolderUUID]
			c.slots[key] = ps
			return ps.UUID
		}
	}
	// Admin on a revision-12 server: a slot of another space that already
	// stands for the same thing moves to the catalog level and is reused,
	// rather than a second slot everyone binds again.
	if c.catalogSlots() && !c.opts.NoSlotMove[key] {
		for _, s := range c.allSlots {
			if s.Type != typ || s.FolderUUID == "" {
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
			if !same {
				continue
			}
			if bound {
				ps.BindTarget = b.TargetID
			}
			ps.UUID, ps.Name, ps.New, ps.Rev = s.UUID, s.Name, false, s.Rev
			ps.Kind, ps.Backend = s.Kind, s.Backend
			ps.MoveToCatalog, ps.ReusedFrom = true, c.slotFolderName(s.FolderUUID)
			c.slots[key] = ps
			return ps.UUID
		}
	}
	ps.UUID, ps.New = uuid.NewString(), true
	ps.Name = ps.BindName
	if typ == slotJump {
		// The publisher's own name for their bastion is theirs (often a
		// person's or a host's); colleagues see what it is for.
		ps.Name = "bastion for " + c.folders[c.opts.RootID].Name
	}
	if n := strings.TrimSpace(c.opts.SlotNames[key]); n != "" {
		ps.Name = n
	}
	if ps.Name == "" {
		ps.Name = typ + " slot"
	}
	folder := c.rootUUID
	if c.catalogSlots() && c.opts.SlotsAtCatalog {
		folder, ps.CatalogLevel = "", true
	}
	rec := PublishSlot{UUID: ps.UUID, FolderUUID: folder, Type: typ, Name: ps.Name, ExternalRef: json.RawMessage("null")}
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

// catalogSlots: catalog-level slots can be made and moved to (an admin
// publishing to a revision-12 server).
func (c *pubCtx) catalogSlots() bool {
	return c.opts.Admin && c.opts.Revision >= CatalogSlotsRevision
}

// slotFolderName names where a slot lives, for the review.
func (c *pubCtx) slotFolderName(folderUUID string) string {
	if folderUUID == "" {
		return "the whole catalog"
	}
	if n, ok := c.reusedNames[folderUUID]; ok {
		return n
	}
	if id, err := c.db.CatalogLocalID(c.opts.CatalogID, KindFolders, folderUUID); err == nil && id != "" {
		if f, err := c.db.GetFolder(id); err == nil {
			return f.Name
		}
	}
	return "another space"
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
	c.plan.Images = nonNil(c.plan.Images)
	c.plan.KeptLocal = nonNil(c.plan.KeptLocal)
	c.plan.TargetInherits = nonNil(c.plan.TargetInherits)
	defer func() { c.plan.ReviewHash = reviewHash(c.plan) }()
	c.plan.CatalogSlots = c.catalogSlots()
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
