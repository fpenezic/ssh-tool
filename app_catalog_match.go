package main

// "Match my connections": a colleague who already had the hosts a catalog
// now carries pairs each local connection with its catalog record (same
// protocol, host and port; the user name decides between several), and
// moves what was personal about it onto the catalog side:
//
//   - credentials, user, network profile, jump host, database / Kafka
//     logins: an unbound slot is bound (one binding serves the whole
//     folder), anything else goes into the record's local layer (My
//     settings), lifted to the folder's layer when every connection of the
//     folder agrees;
//   - settings the catalog leaves unset (initial command, SSH options,
//     ...) fill the layer; the catalog's own values win otherwise;
//   - notes, tags and icon only where the catalog has none;
//   - stored passwords (copied into the catalog row's own vault keys),
//     favourite, open hidden, last used;
//   - forwards: one the catalog also has keeps the user's local port,
//     bookmarks, browser mode and auto start on the catalog forward and is
//     removed; any other becomes the user's own forward on the catalog
//     connection;
//   - workspaces, snippets and jump host references of other local rows
//     move to the catalog connection.
//
// The local connection is not deleted: it moves into a "Matched to
// <catalog>" folder with its effective settings written onto it, so it
// works as before from there. Everything changed is journalled in a synced
// setting; CatalogMatchUndo reverts the last run (a value changed since is
// left alone and counted). Nothing here talks to the catalog server, and
// MCP has none of it.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// CatalogMatchChange is one thing a pair moves, for the preview.
type CatalogMatchChange struct {
	Field string `json:"field"`
	Value string `json:"value"`
	// Where it lands: "slot" (a binding for the whole slot), "folder" (the
	// catalog folder's local layer), "connection" (the record's local
	// layer), "record" (notes / tags / icon), "local" (a column of the
	// user's own: password, favourite, ...), "forward", "reference".
	Where string `json:"where"`
}

// CatalogMatchConn names one side of a pair.
type CatalogMatchConn struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Hostname string `json:"hostname"`
	Username string `json:"username"`
}

// CatalogMatchPair is a local connection and the catalog record it goes to.
type CatalogMatchPair struct {
	Local   CatalogMatchConn     `json:"local"`
	Catalog CatalogMatchConn     `json:"catalog"`
	Changes []CatalogMatchChange `json:"changes"`
}

// CatalogMatchAmbiguous is a local connection several catalog records fit.
type CatalogMatchAmbiguous struct {
	Local   CatalogMatchConn   `json:"local"`
	Options []CatalogMatchConn `json:"options"`
}

// CatalogMatchDuplicate is a second local connection for a record that is
// already paired.
type CatalogMatchDuplicate struct {
	Local     CatalogMatchConn `json:"local"`
	CatalogID string           `json:"catalog_conn_id"`
}

// CatalogMatchPreview is what Match would do now.
type CatalogMatchPreview struct {
	Pairs      []CatalogMatchPair      `json:"pairs"`
	Ambiguous  []CatalogMatchAmbiguous `json:"ambiguous"`
	Duplicates []CatalogMatchDuplicate `json:"duplicates"`
	// Unmatched: local connections no catalog record fits.
	Unmatched int `json:"unmatched"`
	// LastRun: the run Undo would revert (unix ms, 0 for none) and its size.
	LastRun      int64 `json:"last_run"`
	LastRunPairs int   `json:"last_run_pairs"`
}

// CatalogMatchResult reports an applied match.
type CatalogMatchResult struct {
	Pairs       int    `json:"pairs"`
	Bindings    int    `json:"bindings"`
	Forwards    int    `json:"forwards"`
	Folder      string `json:"folder"`
	Passwords   int    `json:"passwords"`
	PassSkipped int    `json:"passwords_skipped"`
}

// CatalogMatchUndoResult reports an undo.
type CatalogMatchUndoResult struct {
	Pairs int `json:"pairs"`
	// Kept: values changed after the match, left as they are.
	Kept int `json:"kept"`
}

// fields a local value replaces the catalog's in (the user's own way in);
// every other field only fills what the catalog leaves unset.
var matchPersonal = map[string]bool{
	"username": true, "auth_ref": true, "otp_ref": true, "network_profile_id": true,
	"jump_host": true, "kafka_credential_ref": true, "db_credential_ref": true,
	"broadcast_group_id": true, "tunnel_autostart": true,
}

// fields that are the match key, never moved.
var matchSkip = map[string]bool{"port": true}

// slot type per reference field.
var matchSlotType = map[string]string{
	"auth_ref": "cred", "otp_ref": "cred", "kafka_credential_ref": "cred", "db_credential_ref": "cred",
	"network_profile_id": "net",
}

var matchFieldLabel = map[string]string{
	"username": "User", "auth_ref": "Credential", "otp_ref": "One-time code", "network_profile_id": "Network",
	"jump_host": "Jump host", "kafka_credential_ref": "Kafka login", "db_credential_ref": "Database login",
	"broadcast_group_id": "Broadcast group", "tunnel_autostart": "Start tunnels on launch",
	"ssh_options": "SSH options", "env_vars": "Environment", "initial_command": "Initial command",
	"db_targets": "Databases",
}

func matchLabel(k string) string {
	if l, ok := matchFieldLabel[k]; ok {
		return l
	}
	s := strings.ReplaceAll(k, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// matchCtx is one catalog's state for a preview or an apply.
type matchCtx struct {
	cat      *store.Catalog
	folders  []store.Folder
	fByID    map[string]*store.Folder
	conns    []store.Connection
	cByID    map[string]*store.Connection
	eff      map[string]store.InheritableSettings
	layers   map[string]string // remote uuid -> layer json
	slots    map[string]store.CatalogSlot
	hidden   map[string]bool // catalog folder uuids hidden by the user
	excluded map[string]bool // local ids already matched or parked
	fwds     map[string][]store.PortForward
	recKeys  map[string]map[string]bool // catalog conn id -> settings keys its record sets
	// uses: per connection id, how many workspaces, snippets and jump
	// references (local rows) point at it, for the preview.
	uses map[string]*[3]int
}

func (a *App) matchContext(catID string) (*matchCtx, error) {
	cat, err := a.db.GetCatalog(catID)
	if err != nil {
		return nil, err
	}
	m := &matchCtx{cat: cat, fByID: map[string]*store.Folder{}, cByID: map[string]*store.Connection{},
		eff: map[string]store.InheritableSettings{}, layers: map[string]string{}, slots: map[string]store.CatalogSlot{},
		hidden: map[string]bool{}, excluded: map[string]bool{}, fwds: map[string][]store.PortForward{},
		recKeys: map[string]map[string]bool{}, uses: map[string]*[3]int{}}
	if m.folders, err = a.db.ListFolders(); err != nil {
		return nil, err
	}
	for i := range m.folders {
		m.fByID[m.folders[i].ID] = &m.folders[i]
	}
	if m.conns, err = a.db.ListConnections(nil); err != nil {
		return nil, err
	}
	for i := range m.conns {
		c := &m.conns[i]
		m.cByID[c.ID] = c
		m.eff[c.ID] = resolver.EffectiveOverrides(*c, m.folders)
	}
	ovs, err := a.db.ListCatalogOverrides(catID)
	if err != nil {
		return nil, err
	}
	for _, o := range ovs {
		m.layers[o.RemoteUUID] = o.JSON
	}
	slots, err := a.db.ListCatalogSlots(catID)
	if err != nil {
		return nil, err
	}
	for _, s := range slots {
		m.slots[s.UUID] = s
	}
	if raw, ok, _ := a.db.GetSetting("catalog_hidden_folders"); ok && raw != "" {
		var h map[string][]string
		if json.Unmarshal([]byte(raw), &h) == nil {
			for _, u := range h[catID] {
				m.hidden[u] = true
			}
		}
	}
	for _, run := range a.matchJournals(catID) {
		for _, p := range run.Pairs {
			m.excluded[p.Local] = true
		}
	}
	all, err := a.db.ListAllPortForwards()
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		m.fwds[f.ConnectionID] = append(m.fwds[f.ConnectionID], f)
	}
	if err := m.countUses(a.db.Conn()); err != nil {
		return nil, err
	}
	rows, err := a.db.Conn().Query(`SELECT id, catalog_json FROM connections WHERE catalog_id = ?`, catID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var rec struct {
			Settings map[string]json.RawMessage `json:"settings"`
		}
		_ = json.Unmarshal([]byte(raw), &rec)
		keys := map[string]bool{}
		for k := range rec.Settings {
			keys[k] = true
		}
		m.recKeys[id] = keys
	}
	return m, rows.Err()
}

// countUses fills m.uses from workspaces, snippets and the jump hosts of
// local connections and folders.
func (m *matchCtx) countUses(db *sql.DB) error {
	bump := func(id string, i int) {
		if m.uses[id] == nil {
			m.uses[id] = &[3]int{}
		}
		m.uses[id][i]++
	}
	scan := func(q, key string, i int) error {
		rows, err := db.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var doc string
			if err := rows.Scan(&doc); err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, id := range jsonStrings([]byte(doc), key) {
				if !seen[id] {
					seen[id] = true
					bump(id, i)
				}
			}
		}
		return rows.Err()
	}
	if err := scan(`SELECT layout_json FROM workspaces`, "connectionId", 0); err != nil {
		return err
	}
	if err := scan(`SELECT overrides_json FROM connections WHERE catalog_id IS NULL`, "connection_id", 2); err != nil {
		return err
	}
	if err := scan(`SELECT settings_json FROM folders WHERE catalog_id IS NULL`, "connection_id", 2); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT connection_id, COUNT(*) FROM snippets WHERE connection_id IS NOT NULL GROUP BY connection_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		bump(id, 1)
		m.uses[id][1] += n - 1
	}
	return rows.Err()
}

// jsonStrings collects the string values of key anywhere in a document.
func jsonStrings(doc []byte, key string) []string {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return nil
	}
	var out []string
	var walk func(n any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for k, x := range t {
				if s, ok := x.(string); ok && k == key {
					out = append(out, s)
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// inCatalog: the row is this catalog's, live and in a folder the user shows.
func (m *matchCtx) inCatalog(c *store.Connection) bool {
	if c.CatalogID == nil || *c.CatalogID != m.cat.ID || c.CatalogState == "removed" {
		return false
	}
	for id, d := c.FolderID, 0; id != nil && d < 64; d++ {
		f := m.fByID[*id]
		if f == nil {
			break
		}
		if f.RemoteUUID != nil && m.hidden[*f.RemoteUUID] {
			return false
		}
		id = f.ParentID
	}
	return true
}

// hasDeeperConns: a catalog connection of this catalog sits in a subfolder
// (any depth) of folderID.
func (m *matchCtx) hasDeeperConns(folderID string) bool {
	for i := range m.conns {
		c := &m.conns[i]
		if c.CatalogID == nil || *c.CatalogID != m.cat.ID || c.FolderID == nil || *c.FolderID == folderID {
			continue
		}
		own := m.fByID[*c.FolderID]
		if own == nil {
			continue
		}
		for id, d := own.ParentID, 0; id != nil && d < 64; d++ {
			if *id == folderID {
				return true
			}
			f := m.fByID[*id]
			if f == nil {
				break
			}
			id = f.ParentID
		}
	}
	return false
}

// parked: the connection sits in this catalog's "Matched to" folder (a
// run older than the journal keeps, or one moved back by hand).
func (m *matchCtx) parked(c *store.Connection) bool {
	if c.FolderID == nil {
		return false
	}
	f := m.fByID[*c.FolderID]
	return f != nil && f.ParentID == nil && f.CatalogID == nil && f.Name == matchParkName(m.cat)
}

func matchParkName(cat *store.Catalog) string { return "Matched to " + cat.Name }

func (m *matchCtx) key(c *store.Connection) string {
	if c.Protocol == "local" {
		return ""
	}
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Hostname)), ".")
	if h == "" {
		return ""
	}
	port := uint16(22)
	if p := m.eff[c.ID].Port; p != nil {
		port = *p
	}
	proto := c.Protocol
	if proto == "" {
		proto = "ssh"
	}
	return proto + "|" + h + "|" + strconv.Itoa(int(port))
}

func (m *matchCtx) user(id string) string {
	if u := m.eff[id].Username; u != nil {
		return *u
	}
	return ""
}

func (m *matchCtx) path(folderID *string) string {
	var parts []string
	for id, d := folderID, 0; id != nil && d < 64; d++ {
		f := m.fByID[*id]
		if f == nil {
			break
		}
		if !(f.CatalogID != nil && f.ID == m.cat.RootFolderID) {
			parts = append([]string{f.Name}, parts...)
		}
		id = f.ParentID
	}
	return strings.Join(parts, " / ")
}

func (m *matchCtx) describe(c *store.Connection) CatalogMatchConn {
	return CatalogMatchConn{ID: c.ID, Name: c.Name, Path: m.path(c.FolderID), Hostname: c.Hostname, Username: m.user(c.ID)}
}

// pairs finds the pairs, with picks (local id -> catalog conn id) settling
// ambiguous ones and choosing among duplicates.
func (m *matchCtx) pairs(picks map[string]string) (pairs [][2]string, amb []CatalogMatchAmbiguous, dups []CatalogMatchDuplicate, unmatched int) {
	idx := map[string][]*store.Connection{}
	for i := range m.conns {
		c := &m.conns[i]
		if m.inCatalog(c) {
			if k := m.key(c); k != "" {
				idx[k] = append(idx[k], c)
			}
		}
	}
	byCat := map[string][]*store.Connection{}
	for i := range m.conns {
		l := &m.conns[i]
		if l.CatalogID != nil || m.excluded[l.ID] || m.parked(l) {
			continue
		}
		k := m.key(l)
		cands := idx[k]
		if k == "" || len(cands) == 0 {
			if k != "" {
				unmatched++
			}
			continue
		}
		var pick *store.Connection
		if p := picks[l.ID]; p != "" {
			for _, c := range cands {
				if c.ID == p {
					pick = c
				}
			}
		}
		if pick == nil && len(cands) == 1 {
			pick = cands[0]
		}
		if pick == nil {
			var same []*store.Connection
			for _, c := range cands {
				if m.user(c.ID) == m.user(l.ID) {
					same = append(same, c)
				}
			}
			if len(same) == 1 {
				pick = same[0]
			}
		}
		if pick == nil {
			a := CatalogMatchAmbiguous{Local: m.describe(l)}
			for _, c := range cands {
				a.Options = append(a.Options, m.describe(c))
			}
			amb = append(amb, a)
			continue
		}
		byCat[pick.ID] = append(byCat[pick.ID], l)
	}
	catIDs := make([]string, 0, len(byCat))
	for id := range byCat {
		catIDs = append(catIDs, id)
	}
	sort.Strings(catIDs)
	for _, cid := range catIDs {
		ls := byCat[cid]
		// The one picked for it, else the one used last.
		sort.SliceStable(ls, func(i, j int) bool {
			pi, pj := picks[ls[i].ID] == cid, picks[ls[j].ID] == cid
			if pi != pj {
				return pi
			}
			return lastUsed(ls[i]) > lastUsed(ls[j])
		})
		pairs = append(pairs, [2]string{ls[0].ID, cid})
		for _, d := range ls[1:] {
			dups = append(dups, CatalogMatchDuplicate{Local: m.describe(d), CatalogID: cid})
		}
	}
	return pairs, amb, dups, unmatched
}

func lastUsed(c *store.Connection) int64 {
	if c.LastUsedAt == nil {
		return 0
	}
	return *c.LastUsedAt
}

// matchOps is what an apply writes.
type matchOps struct {
	repoint     map[string]string                     // local id -> catalog conn id
	connLayer   map[string]map[string]json.RawMessage // catalog conn id -> new settings keys
	connRecord  map[string]map[string]any             // catalog conn id -> new record keys
	folderLayer map[string]map[string]json.RawMessage // catalog folder id -> new settings keys
	fwdLayer    map[string]uint16                     // catalog forward id -> local port
	bindings    map[string][2]string                  // slot uuid -> kind, target
	fwdMove     map[string]string                     // local forward id -> catalog conn id
	fwdDelete   []string                              // local forwards the catalog has
	catFwdCols  map[string]store.PortForward          // catalog forward -> wanted local columns
	changes     map[string][]CatalogMatchChange       // local id -> preview lines
	pairCat     map[string]string                     // catalog conn id -> local id
}

func (m *matchCtx) plan(pairs [][2]string, a *App) *matchOps {
	o := &matchOps{repoint: map[string]string{}, connLayer: map[string]map[string]json.RawMessage{},
		connRecord: map[string]map[string]any{}, folderLayer: map[string]map[string]json.RawMessage{},
		fwdLayer: map[string]uint16{}, bindings: map[string][2]string{}, fwdMove: map[string]string{},
		catFwdCols: map[string]store.PortForward{}, changes: map[string][]CatalogMatchChange{}, pairCat: map[string]string{}}
	for _, p := range pairs {
		o.repoint[p[0]] = p[1]
		o.pairCat[p[1]] = p[0]
	}
	// slot -> value -> catalog conns wanting it
	type want struct {
		kind  string
		val   string
		conn  string
		key   string // slot uuid
		field string
	}
	var slotWants []want
	for _, p := range pairs {
		l, c := m.cByID[p[0]], m.cByID[p[1]]
		el, ec := m.eff[l.ID], m.eff[c.ID]
		existing := layerKeys(m.layers[uuidOf(c)])
		diff := map[string]json.RawMessage{}
		lv, cv := reflect.ValueOf(el), reflect.ValueOf(ec)
		t := lv.Type()
		for i := 0; i < t.NumField(); i++ {
			k := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
			if k == "" || k == "-" || matchSkip[k] || existing[k] {
				continue
			}
			f, g := lv.Field(i), cv.Field(i)
			var val any
			switch f.Kind() {
			case reflect.Ptr:
				if f.IsNil() {
					continue
				}
				if s, ok := f.Interface().(*string); ok && store.IsCatalogRef(*s) {
					continue
				}
				if k == "jump_host" {
					jl := repointJump(f.Interface().(*store.JumpHostOverride), o.repoint)
					if jumpEqual(jl, ec.JumpHost) {
						continue
					}
					// An unbound jump slot and a single hop through a
					// connection: bind the slot.
					if ec.JumpHost != nil && ec.JumpHost.Chain != nil && ec.JumpHost.Chain.Via == nil && ec.JumpHost.Chain.ConnectionID != nil &&
						jl != nil && jl.Kind == "chain" && jl.Chain != nil && jl.Chain.Via == nil && jl.Chain.ConnectionID != nil {
						if e, ok := store.ParseCatalogRef(*ec.JumpHost.Chain.ConnectionID, ""); ok && e.Unbound && e.CatalogID == m.cat.ID {
							if s, ok := m.slots[e.UUID]; ok && s.Type == "jump" {
								slotWants = append(slotWants, want{"jump", *jl.Chain.ConnectionID, c.ID, e.UUID, k})
							}
						}
					}
					val = jl
					break
				}
				if !g.IsNil() && !matchPersonal[k] {
					continue
				}
				if !g.IsNil() && reflect.DeepEqual(f.Interface(), g.Interface()) {
					continue
				}
				if typ := matchSlotType[k]; typ != "" && !g.IsNil() {
					if e, ok := store.ParseCatalogRef(*g.Interface().(*string), ""); ok && e.Unbound && e.CatalogID == m.cat.ID {
						if s, ok := m.slots[e.UUID]; ok && s.Type == typ {
							slotWants = append(slotWants, want{typ, *f.Interface().(*string), c.ID, e.UUID, k})
						}
					}
				}
				val = f.Interface()
			case reflect.Map:
				if f.Len() == 0 {
					continue
				}
				add := reflect.MakeMap(f.Type())
				for _, mk := range f.MapKeys() {
					if g.IsNil() || !g.MapIndex(mk).IsValid() {
						add.SetMapIndex(mk, f.MapIndex(mk))
					}
				}
				if add.Len() == 0 {
					continue
				}
				val = add.Interface()
			case reflect.Slice:
				if f.Len() == 0 {
					continue
				}
				if g.Len() > 0 {
					// The catalog has its own database targets: keep them and
					// carry over only the user's login for the same database.
					if k != "db_targets" {
						continue
					}
					ov := dbTargetLogins(el.DbTargets, ec.DbTargets)
					if len(ov) == 0 {
						continue
					}
					val = ov
					break
				}
				val = f.Interface()
			default:
				continue
			}
			b, err := json.Marshal(val)
			if err != nil {
				continue
			}
			diff[k] = b
		}
		o.connLayer[c.ID] = diff

		// Record part: only what the catalog leaves empty.
		rec := map[string]any{}
		ex := recordKeys(m.layers[uuidOf(c)])
		if c.Notes == "" && l.Notes != "" && !ex["notes"] {
			rec["notes"] = l.Notes
		}
		if len(c.Tags) == 0 && len(l.Tags) > 0 && !ex["tags"] {
			rec["tags"] = l.Tags
		}
		if c.IconName == nil && c.IconImageID == nil && !ex["icon_name"] && !ex["icon_image_id"] {
			if l.IconImageID != nil {
				rec["icon_image_id"] = *l.IconImageID
			} else if l.IconName != nil {
				rec["icon_name"] = *l.IconName
				if l.IconColor != nil {
					rec["icon_color"] = *l.IconColor
				}
			}
		}
		if len(rec) > 0 {
			o.connRecord[c.ID] = rec
		}

		// Forwards.
		used := map[string]bool{}
		for _, lf := range m.fwds[l.ID] {
			if lf.RemoteUUID != nil {
				continue
			}
			var hit *store.PortForward
			for i, cf := range m.fwds[c.ID] {
				if cf.RemoteUUID == nil || used[cf.ID] || cf.Kind != lf.Kind {
					continue
				}
				if lf.Kind == "dynamic" || (strings.EqualFold(deref(cf.RemoteHost), deref(lf.RemoteHost)) && u16(cf.RemotePort) == u16(lf.RemotePort)) {
					hit = &m.fwds[c.ID][i]
					break
				}
			}
			if hit == nil {
				o.fwdMove[lf.ID] = c.ID
				o.changes[l.ID] = append(o.changes[l.ID], CatalogMatchChange{Field: "Forward", Value: fwdLabel(lf), Where: "forward"})
				continue
			}
			used[hit.ID] = true
			o.fwdDelete = append(o.fwdDelete, lf.ID)
			if lf.LocalPort != nil && u16(hit.LocalPort) != *lf.LocalPort && !forwardLayerHasPort(m.layers[deref(hit.RemoteUUID)]) {
				o.fwdLayer[hit.ID] = *lf.LocalPort
			}
			cols := *hit
			cols.AutoStart = lf.AutoStart
			if len(cols.Bookmarks) == 0 {
				cols.Bookmarks = lf.Bookmarks
			}
			if cols.BrowserMode == "" {
				cols.BrowserMode = lf.BrowserMode
			}
			o.catFwdCols[hit.ID] = cols
			o.changes[l.ID] = append(o.changes[l.ID], CatalogMatchChange{Field: "Forward", Value: fwdLabel(lf) + " (the catalog has it; your local port and settings kept)", Where: "forward"})
		}
	}

	// Bindings: per slot the value most pairs want, unless the slot is
	// bound already (then the marker would not be there).
	bySlot := map[string]map[string]int{}
	order := []string{}
	for _, w := range slotWants {
		if bySlot[w.key] == nil {
			bySlot[w.key] = map[string]int{}
			order = append(order, w.key)
		}
		bySlot[w.key][w.val]++
	}
	for _, s := range order {
		best, n := "", 0
		for v, c := range bySlot[s] {
			if c > n || (c == n && v < best) {
				best, n = v, c
			}
		}
		o.bindings[s] = [2]string{m.slots[s].Type, best}
	}
	for _, w := range slotWants {
		if b := o.bindings[w.key]; b[1] == w.val {
			delete(o.connLayer[w.conn], w.field)
		}
	}

	// Lift a value every connection of a catalog folder gets to the
	// folder's layer.
	byFolder := map[string][]string{}
	for i := range m.conns {
		c := &m.conns[i]
		if c.CatalogID != nil && *c.CatalogID == m.cat.ID && c.CatalogState != "removed" && c.FolderID != nil {
			byFolder[*c.FolderID] = append(byFolder[*c.FolderID], c.ID)
		}
	}
	for fid, ids := range byFolder {
		f := m.fByID[fid]
		if f == nil || f.ID == m.cat.RootFolderID || f.CatalogSkeleton || len(ids) < 2 {
			continue
		}
		// A folder layer reaches subfolders too: lift only when no catalog
		// connection sits deeper than the folder's own.
		if m.hasDeeperConns(fid) {
			continue
		}
		all := true
		for _, id := range ids {
			if _, ok := o.pairCat[id]; !ok {
				all = false
			}
		}
		if !all {
			continue
		}
		fex := layerKeys(m.layers[deref(f.RemoteUUID)])
		for k, v := range o.connLayer[ids[0]] {
			if fex[k] {
				continue
			}
			same := true
			for _, id := range ids {
				if w, ok := o.connLayer[id][k]; !ok || string(w) != string(v) || m.recKeys[id][k] {
					same = false
					break
				}
			}
			if !same {
				continue
			}
			if o.folderLayer[fid] == nil {
				o.folderLayer[fid] = map[string]json.RawMessage{}
			}
			o.folderLayer[fid][k] = v
			for _, id := range ids {
				delete(o.connLayer[id], k)
			}
		}
	}

	// Preview lines.
	for _, p := range pairs {
		l, c := m.cByID[p[0]], m.cByID[p[1]]
		var lines []CatalogMatchChange
		for s, b := range o.bindings {
			for _, w := range slotWants {
				if w.key == s && w.conn == c.ID && w.val == b[1] {
					lines = append(lines, CatalogMatchChange{Field: "Slot " + strconv.Quote(m.slots[s].Name), Value: a.matchValue(b[0], json.RawMessage(strconv.Quote(b[1])), o.repoint), Where: "slot"})
					break
				}
			}
		}
		if c.FolderID != nil {
			for k, v := range o.folderLayer[*c.FolderID] {
				lines = append(lines, CatalogMatchChange{Field: matchLabel(k), Value: a.matchValue(k, v, o.repoint), Where: "folder"})
			}
		}
		for k, v := range o.connLayer[c.ID] {
			lines = append(lines, CatalogMatchChange{Field: matchLabel(k), Value: a.matchValue(k, v, o.repoint), Where: "connection"})
		}
		rec := o.connRecord[c.ID]
		if _, ok := rec["notes"]; ok {
			lines = append(lines, CatalogMatchChange{Field: "Notes", Where: "record"})
		}
		if _, ok := rec["tags"]; ok {
			lines = append(lines, CatalogMatchChange{Field: "Tags", Value: strings.Join(l.Tags, ", "), Where: "record"})
		}
		if _, ok := rec["icon_name"]; ok {
			lines = append(lines, CatalogMatchChange{Field: "Icon", Where: "record"})
		} else if _, ok := rec["icon_image_id"]; ok {
			lines = append(lines, CatalogMatchChange{Field: "Icon", Where: "record"})
		}
		if l.PasswordVaultKey != nil && c.PasswordVaultKey == nil {
			lines = append(lines, CatalogMatchChange{Field: "Saved password", Where: "local"})
		}
		if l.VncPasswordVaultKey != nil && c.VncPasswordVaultKey == nil {
			lines = append(lines, CatalogMatchChange{Field: "VNC password", Where: "local"})
		}
		if l.Favorite && !c.Favorite {
			lines = append(lines, CatalogMatchChange{Field: "Favourite", Where: "local"})
		}
		if u := m.uses[l.ID]; u != nil {
			var parts []string
			if u[0] > 0 {
				parts = append(parts, countOf(u[0], "workspace", "workspaces"))
			}
			if u[1] > 0 {
				parts = append(parts, countOf(u[1], "snippet", "snippets"))
			}
			if u[2] > 0 {
				parts = append(parts, countOf(u[2], "jump host reference", "jump host references"))
			}
			if len(parts) > 0 {
				lines = append(lines, CatalogMatchChange{Field: "Used in", Value: strings.Join(parts, ", "), Where: "reference"})
			}
		}
		rank := map[string]int{"slot": 0, "folder": 1, "connection": 2, "record": 3, "local": 4, "reference": 5}
		sort.SliceStable(lines, func(i, j int) bool {
			if rank[lines[i].Where] != rank[lines[j].Where] {
				return rank[lines[i].Where] < rank[lines[j].Where]
			}
			return lines[i].Field < lines[j].Field
		})
		o.changes[l.ID] = append(lines, o.changes[l.ID]...)
	}
	return o
}

// matchValue renders a moved value for the preview: names for references,
// keys only for maps (environment values can be secrets).
func (a *App) matchValue(k string, v json.RawMessage, repoint map[string]string) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		switch k {
		case "auth_ref", "otp_ref", "kafka_credential_ref", "db_credential_ref", "cred":
			if c, err := a.db.GetCredential(s); err == nil {
				return c.Name
			}
			return "a credential"
		case "network_profile_id", "net":
			if s == "" {
				return "direct"
			}
			if p, err := a.db.GetNetworkProfile(s); err == nil {
				return p.Name
			}
			return "a network profile"
		case "jump":
			if c, err := a.db.GetConnection(s); err == nil {
				return c.Name
			}
			return s
		}
		if len(s) > 60 {
			s = s[:57] + "..."
		}
		return s
	}
	var mp map[string]any
	if json.Unmarshal(v, &mp) == nil && k != "jump_host" {
		keys := make([]string, 0, len(mp))
		for mk := range mp {
			keys = append(keys, mk)
		}
		sort.Strings(keys)
		return strings.Join(keys, ", ")
	}
	if k == "jump_host" {
		var j store.JumpHostOverride
		if json.Unmarshal(v, &j) == nil {
			if j.Kind != "chain" || j.Chain == nil {
				return "none"
			}
			var hops []string
			for h, d := j.Chain, 0; h != nil && d < 16; h, d = h.Via, d+1 {
				name := h.Hostname
				if h.ConnectionID != nil {
					if c, err := a.db.GetConnection(*h.ConnectionID); err == nil {
						name = c.Name
					}
				}
				hops = append([]string{name}, hops...)
			}
			return strings.Join(hops, " > ")
		}
	}
	var arr []any
	if json.Unmarshal(v, &arr) == nil {
		return strconv.Itoa(len(arr))
	}
	out := string(v)
	if len(out) > 60 {
		out = out[:57] + "..."
	}
	return out
}

// dbTargetLogins: for each catalog database target, the user's login from
// their own target on the same database (same address and database name,
// else the same name), as overrides of the catalog's entries.
func dbTargetLogins(local, cat []store.DbTarget) []store.DbTarget {
	norm := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(*p))
	}
	var out []store.DbTarget
	for _, ct := range cat {
		for _, lt := range local {
			same := norm(lt.Addr) != "" && norm(lt.Addr) == norm(ct.Addr) && norm(lt.Database) == norm(ct.Database)
			if !same && (strings.TrimSpace(lt.Name) == "" || !strings.EqualFold(strings.TrimSpace(lt.Name), strings.TrimSpace(ct.Name))) {
				continue
			}
			if lt.CredentialRef == nil || store.IsCatalogRef(*lt.CredentialRef) {
				break
			}
			if ct.CredentialRef != nil && *ct.CredentialRef == *lt.CredentialRef {
				break
			}
			ref := *lt.CredentialRef
			out = append(out, store.DbTarget{ID: ct.ID, CredentialRef: &ref, Override: true})
			break
		}
	}
	return out
}

func fwdLabel(f store.PortForward) string {
	switch f.Kind {
	case "dynamic":
		return "SOCKS :" + strconv.Itoa(int(u16(f.LocalPort)))
	case "remote":
		return "remote :" + strconv.Itoa(int(u16(f.LocalPort))) + " <- " + deref(f.RemoteHost) + ":" + strconv.Itoa(int(u16(f.RemotePort)))
	}
	return ":" + strconv.Itoa(int(u16(f.LocalPort))) + " -> " + deref(f.RemoteHost) + ":" + strconv.Itoa(int(u16(f.RemotePort)))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func u16(p *uint16) uint16 {
	if p == nil {
		return 0
	}
	return *p
}

func uuidOf(c *store.Connection) string { return deref(c.RemoteUUID) }

func layerKeys(layer string) map[string]bool {
	out := map[string]bool{}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(layer), &m) == nil {
		for k := range m {
			out[k] = true
		}
	}
	return out
}

func recordKeys(layer string) map[string]bool {
	out := map[string]bool{}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(layer), &m) != nil {
		return out
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(m[catalog.RecordLayerKey], &r) == nil {
		for k := range r {
			out[k] = true
		}
	}
	return out
}

func forwardLayerHasPort(layer string) bool { return layerKeys(layer)["local_port"] }

// repointJump copies a jump override with matched connections replaced by
// their catalog records.
func repointJump(j *store.JumpHostOverride, repoint map[string]string) *store.JumpHostOverride {
	if j == nil {
		return nil
	}
	b, _ := json.Marshal(j)
	var out store.JumpHostOverride
	_ = json.Unmarshal(repointJSON(b, "connection_id", repoint), &out)
	for h, d := out.Chain, 0; h != nil && d < 16; h, d = h.Via, d+1 {
		h.Name = ""
	}
	return &out
}

func jumpEqual(a, b *store.JumpHostOverride) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// repointJSON replaces string values of key found in ids anywhere in a
// JSON document. The input comes back unchanged when nothing matched.
func repointJSON(doc []byte, key string, ids map[string]string) []byte {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return doc
	}
	changed := false
	var walk func(n any) any
	walk = func(n any) any {
		switch t := n.(type) {
		case map[string]any:
			for k, x := range t {
				if s, ok := x.(string); ok && k == key {
					if to, ok := ids[s]; ok {
						t[k] = to
						changed = true
					}
					continue
				}
				t[k] = walk(x)
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		}
		return n
	}
	v = walk(v)
	if !changed {
		return doc
	}
	b, err := json.Marshal(v)
	if err != nil {
		return doc
	}
	return b
}

// CatalogMatchPreview pairs this user's local connections with the
// catalog's records and says what a match would move. picks settles a
// local connection several records fit (local id -> catalog conn id).
func (a *App) CatalogMatchPreview(catalogID string, picks map[string]string) (*CatalogMatchPreview, error) {
	m, err := a.matchContext(catalogID)
	if err != nil {
		return nil, err
	}
	pairs, amb, dups, unmatched := m.pairs(picks)
	o := m.plan(pairs, a)
	out := &CatalogMatchPreview{Ambiguous: amb, Duplicates: dups, Unmatched: unmatched}
	for _, p := range pairs {
		out.Pairs = append(out.Pairs, CatalogMatchPair{Local: m.describe(m.cByID[p[0]]), Catalog: m.describe(m.cByID[p[1]]), Changes: o.changes[p[0]]})
	}
	sort.SliceStable(out.Pairs, func(i, j int) bool {
		if out.Pairs[i].Catalog.Path != out.Pairs[j].Catalog.Path {
			return out.Pairs[i].Catalog.Path < out.Pairs[j].Catalog.Path
		}
		return out.Pairs[i].Catalog.Name < out.Pairs[j].Catalog.Name
	})
	if runs := a.matchJournals(catalogID); len(runs) > 0 {
		out.LastRun, out.LastRunPairs = runs[len(runs)-1].At, len(runs[len(runs)-1].Pairs)
	}
	return out, nil
}

// Journal -----------------------------------------------------------------

type matchPairJ struct {
	Local   string `json:"local"`
	Catalog string `json:"catalog"`
}

type matchTextJ struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type matchParkedJ struct {
	ID        string  `json:"id"`
	FolderID  *string `json:"folder_id"`
	SortOrder int64   `json:"sort_order"`
	Before    string  `json:"before"`
	After     string  `json:"after"`
	// LastUsed: cleared on the parked copy so Recent and Quick access
	// show the catalog connection; Undo puts it back unless the copy was
	// used since.
	LastUsed *int64 `json:"last_used_at"`
}

// matchLocalColsJ: the user's own columns on a catalog row, before and
// after. Undo restores each only while it still has the after value.
type matchLocalColsJ struct {
	ID            string  `json:"id"`
	Favorite      bool    `json:"favorite"`
	OpenHidden    bool    `json:"open_hidden"`
	LastUsed      *int64  `json:"last_used_at"`
	PassKey       *string `json:"password_vault_key"`
	VncKey        *string `json:"vnc_password_vault_key"`
	AfterFavorite bool    `json:"after_favorite"`
	AfterHidden   bool    `json:"after_open_hidden"`
	AfterLastUsed *int64  `json:"after_last_used_at"`
}

type matchLayerJ struct {
	UUID   string `json:"uuid"`
	Kind   string `json:"kind"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type matchBindJ struct {
	Slot   string `json:"slot"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// matchFwdColsJ: a catalog forward's local columns before and after the
// match; Undo restores each only while it still has the after value.
type matchFwdColsJ struct {
	ID               string `json:"id"`
	AutoStart        bool   `json:"auto_start"`
	Bookmarks        string `json:"bookmarks"`
	BrowserMode      string `json:"browser_mode"`
	AfterAutoStart   bool   `json:"after_auto_start"`
	AfterBookmarks   string `json:"after_bookmarks"`
	AfterBrowserMode string `json:"after_browser_mode"`
}

type matchMoveJ struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

type matchJournal struct {
	At          int64             `json:"at"`
	Pairs       []matchPairJ      `json:"pairs"`
	ParkFolder  string            `json:"park_folder"`
	ParkCreated bool              `json:"park_created"`
	Parked      []matchParkedJ    `json:"parked"`
	LocalCols   []matchLocalColsJ `json:"local_cols"`
	Layers      []matchLayerJ     `json:"layers"`
	Bindings    []matchBindJ      `json:"bindings"`
	FwdMoved    []matchMoveJ      `json:"fwd_moved"`
	FwdDeleted  []json.RawMessage `json:"fwd_deleted"`
	FwdCols     []matchFwdColsJ   `json:"fwd_cols"`
	Snippets    []matchMoveJ      `json:"snippets"`
	Workspaces  []matchTextJ      `json:"workspaces"`
	ConnRefs    []matchTextJ      `json:"conn_refs"`
	FolderRefs  []matchTextJ      `json:"folder_refs"`
	VaultCopies []matchVaultJ     `json:"vault_copies"`
}

// matchVaultJ: a stored password copied from the local connection's vault
// key to the catalog row's own. Undo removes the copy only while it still
// holds the same secret as its source (a password saved since stays).
type matchVaultJ struct {
	Key  string `json:"key"`
	From string `json:"from"`
}

// matchJournalMax: runs kept per catalog (Undo reverts the last).
const matchJournalMax = 10

func matchJournalKey(catID string) string { return "catalog_match_journal:" + catID }

func (a *App) matchJournals(catID string) []matchJournal {
	raw, ok, err := a.db.GetSetting(matchJournalKey(catID))
	if err != nil || !ok || raw == "" {
		return nil
	}
	var runs []matchJournal
	if json.Unmarshal([]byte(raw), &runs) != nil {
		return nil
	}
	return runs
}

func saveMatchJournalsTx(tx *sql.Tx, catID string, runs []matchJournal) error {
	if len(runs) > matchJournalMax {
		runs = runs[len(runs)-matchJournalMax:]
	}
	if len(runs) == 0 {
		_, err := tx.Exec(`DELETE FROM app_settings WHERE key = ?`, matchJournalKey(catID))
		return err
	}
	b, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, matchJournalKey(catID), string(b))
	return err
}

// Apply -------------------------------------------------------------------

// CatalogMatchApply matches the given pairs (local id -> catalog conn id,
// as the preview offered them). The plan is computed again here; a pair
// the preview would not make now is refused.
func (a *App) CatalogMatchApply(catalogID string, pairs map[string]string) (*CatalogMatchResult, error) {
	if len(pairs) == 0 {
		return nil, fmt.Errorf("no connections were picked")
	}
	// One lock for the whole run, secrets and their journal entry
	// included, so an Undo or another match never sees half of it.
	rt := a.catRT()
	rt.apply.Lock()
	defer rt.apply.Unlock()
	m, err := a.matchContext(catalogID)
	if err != nil {
		return nil, err
	}
	found, _, _, _ := m.pairs(pairs)
	var chosen [][2]string
	for _, p := range found {
		if pairs[p[0]] == p[1] {
			chosen = append(chosen, p)
		}
	}
	if len(chosen) != len(pairs) {
		return nil, fmt.Errorf("%d of the picked connections no longer match their catalog record; the list was refreshed", len(pairs)-len(chosen))
	}
	o := m.plan(chosen, a)
	j := matchJournal{At: time.Now().UnixMilli()}
	res := &CatalogMatchResult{Pairs: len(chosen), Bindings: len(o.bindings), Forwards: len(o.fwdMove) + len(o.fwdDelete)}
	// Read before the transaction: the store has one connection, a read
	// on it inside the transaction would wait for itself.
	prior := a.matchJournals(catalogID)
	err = a.db.WithTx(func(tx *sql.Tx) error { return a.applyMatchTx(tx, m, o, chosen, prior, &j, res) })
	if err == nil {
		ext, _ := catalog.PrepareExternal(a.db, m.cat, nil)
		if merr := catalog.Materialise(a.db, catalogID, ext); merr != nil {
			log.Printf("catalog %s: materialise after match: %v", m.cat.Name, merr)
		}
	}
	if err != nil {
		return nil, err
	}
	// Stored passwords: the catalog row gets its own vault keys, so
	// deleting the parked copy later does not take them.
	for _, p := range chosen {
		l, c := m.cByID[p[0]], m.cByID[p[1]]
		res.Passwords, res.PassSkipped = a.matchCopySecrets(l, c, &j, res.Passwords, res.PassSkipped)
	}
	if len(j.VaultCopies) > 0 {
		if runs := a.matchJournals(catalogID); len(runs) > 0 && runs[len(runs)-1].At == j.At {
			runs[len(runs)-1].VaultCopies = j.VaultCopies
			if err := a.db.WithTx(func(tx *sql.Tx) error { return saveMatchJournalsTx(tx, catalogID, runs) }); err != nil {
				log.Printf("match journal: %v", err)
			}
		}
	}
	a.recordAudit("catalog.match", catalogID, map[string]string{"pairs": strconv.Itoa(res.Pairs), "bindings": strconv.Itoa(res.Bindings)})
	EventsEmit("catalog_changed", map[string]any{"catalog_id": catalogID})
	return res, nil
}

func (a *App) matchCopySecrets(l, c *store.Connection, j *matchJournal, n, skipped int) (int, int) {
	one := func(from, to *string, key string, set func(string) error) {
		if from == nil || to != nil {
			return
		}
		if a.vault == nil {
			skipped++
			return
		}
		v, ok, err := a.vault.Get(*from)
		if err != nil || !ok {
			skipped++
			return
		}
		if err := a.vault.Put(key, v); err != nil {
			skipped++
			return
		}
		if err := set(key); err != nil {
			log.Printf("match: password key: %v", err)
			skipped++
			return
		}
		j.VaultCopies = append(j.VaultCopies, matchVaultJ{Key: key, From: *from})
		n++
	}
	one(l.PasswordVaultKey, c.PasswordVaultKey, "conn_pass:"+c.ID, func(k string) error { return a.db.SetConnectionPasswordKey(c.ID, k) })
	one(l.VncPasswordVaultKey, c.VncPasswordVaultKey, "conn_vnc_pass:"+c.ID, func(k string) error { return a.db.SetConnectionVncPasswordKey(c.ID, k) })
	return n, skipped
}

func mergeLayer(before string, settings map[string]json.RawMessage, record map[string]any) (string, error) {
	m := map[string]json.RawMessage{}
	if strings.TrimSpace(before) != "" {
		if err := json.Unmarshal([]byte(before), &m); err != nil {
			return "", fmt.Errorf("stored layer: %w", err)
		}
	}
	for k, v := range settings {
		m[k] = v
	}
	if len(record) > 0 {
		r := map[string]any{}
		if raw, ok := m[catalog.RecordLayerKey]; ok {
			_ = json.Unmarshal(raw, &r)
		}
		for k, v := range record {
			r[k] = v
		}
		b, err := json.Marshal(r)
		if err != nil {
			return "", err
		}
		m[catalog.RecordLayerKey] = b
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func setLayerTx(tx *sql.Tx, catID, uuid, kind, j string) error {
	t := strings.TrimSpace(j)
	if t == "" || t == "{}" || t == "null" {
		_, err := tx.Exec(`DELETE FROM catalog_overrides WHERE catalog_id = ? AND remote_uuid = ?`, catID, uuid)
		return err
	}
	_, err := tx.Exec(`INSERT INTO catalog_overrides (catalog_id, remote_uuid, kind, json, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (catalog_id, remote_uuid) DO UPDATE SET kind = excluded.kind, json = excluded.json, updated_at = excluded.updated_at`,
		catID, uuid, kind, t, time.Now().UnixMilli())
	return err
}

func (a *App) applyMatchTx(tx *sql.Tx, m *matchCtx, o *matchOps, pairs [][2]string, prior []matchJournal, j *matchJournal, res *CatalogMatchResult) error {
	catID := m.cat.ID
	for _, p := range pairs {
		j.Pairs = append(j.Pairs, matchPairJ{Local: p[0], Catalog: p[1]})
	}

	// Layers.
	layer := func(uuid, kind string, settings map[string]json.RawMessage, record map[string]any) error {
		if uuid == "" || (len(settings) == 0 && len(record) == 0) {
			return nil
		}
		before := m.layers[uuid]
		after, err := mergeLayer(before, settings, record)
		if err != nil {
			return err
		}
		if err := setLayerTx(tx, catID, uuid, kind, after); err != nil {
			return err
		}
		j.Layers = append(j.Layers, matchLayerJ{UUID: uuid, Kind: kind, Before: before, After: after})
		return nil
	}
	for fid, s := range o.folderLayer {
		if err := layer(deref(m.fByID[fid].RemoteUUID), "folders", s, nil); err != nil {
			return err
		}
	}
	for _, p := range pairs {
		c := m.cByID[p[1]]
		if err := layer(uuidOf(c), "connections", o.connLayer[c.ID], o.connRecord[c.ID]); err != nil {
			return err
		}
	}
	for fid, port := range o.fwdLayer {
		var uuid string
		for _, fs := range m.fwds {
			for _, f := range fs {
				if f.ID == fid {
					uuid = deref(f.RemoteUUID)
				}
			}
		}
		b, _ := json.Marshal(port)
		if err := layer(uuid, "forwards", map[string]json.RawMessage{"local_port": b}, nil); err != nil {
			return err
		}
	}

	// Bindings.
	for slot, b := range o.bindings {
		if _, err := tx.Exec(`INSERT INTO catalog_bindings (catalog_id, slot_uuid, kind, target_id, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (catalog_id, slot_uuid) DO UPDATE SET kind = excluded.kind, target_id = excluded.target_id, updated_at = excluded.updated_at`,
			catID, slot, b[0], b[1], time.Now().UnixMilli()); err != nil {
			return err
		}
		j.Bindings = append(j.Bindings, matchBindJ{Slot: slot, Kind: b[0], Target: b[1]})
	}

	// The user's own columns on the catalog rows.
	for _, p := range pairs {
		l, c := m.cByID[p[0]], m.cByID[p[1]]
		lu := c.LastUsedAt
		if lastUsed(l) > lastUsed(c) {
			lu = l.LastUsedAt
		}
		j.LocalCols = append(j.LocalCols, matchLocalColsJ{ID: c.ID, Favorite: c.Favorite, OpenHidden: c.OpenHidden, LastUsed: c.LastUsedAt,
			PassKey: c.PasswordVaultKey, VncKey: c.VncPasswordVaultKey,
			AfterFavorite: c.Favorite || l.Favorite, AfterHidden: c.OpenHidden || l.OpenHidden, AfterLastUsed: lu})
		if _, err := tx.Exec(`UPDATE connections SET favorite = ?, open_hidden = ?, last_used_at = ? WHERE id = ?`,
			c.Favorite || l.Favorite, c.OpenHidden || l.OpenHidden, lu, c.ID); err != nil {
			return err
		}
	}

	// Forwards.
	for id, to := range o.fwdMove {
		var from string
		if err := tx.QueryRow(`SELECT connection_id FROM port_forwards WHERE id = ?`, id).Scan(&from); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE port_forwards SET connection_id = ? WHERE id = ?`, to, id); err != nil {
			return err
		}
		j.FwdMoved = append(j.FwdMoved, matchMoveJ{ID: id, From: from, To: to})
	}
	for id, want := range o.catFwdCols {
		var before matchFwdColsJ
		before.ID = id
		if err := tx.QueryRow(`SELECT auto_start, bookmarks, browser_mode FROM port_forwards WHERE id = ?`, id).Scan(&before.AutoStart, &before.Bookmarks, &before.BrowserMode); err != nil {
			return err
		}
		bm, _ := json.Marshal(want.Bookmarks)
		if want.Bookmarks == nil {
			bm = []byte(before.Bookmarks)
		}
		if _, err := tx.Exec(`UPDATE port_forwards SET auto_start = ?, bookmarks = ?, browser_mode = ? WHERE id = ?`, want.AutoStart, string(bm), want.BrowserMode, id); err != nil {
			return err
		}
		before.AfterAutoStart, before.AfterBookmarks, before.AfterBrowserMode = want.AutoStart, string(bm), want.BrowserMode
		j.FwdCols = append(j.FwdCols, before)
	}
	for _, id := range o.fwdDelete {
		row, err := rowJSON(tx, "port_forwards", id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM port_forwards WHERE id = ?`, id); err != nil {
			return err
		}
		j.FwdDeleted = append(j.FwdDeleted, row)
	}

	// References.
	for local, to := range o.repoint {
		rows, err := tx.Query(`SELECT id FROM snippets WHERE connection_id = ?`, local)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if _, err := tx.Exec(`UPDATE snippets SET connection_id = ? WHERE id = ?`, to, id); err != nil {
				return err
			}
			j.Snippets = append(j.Snippets, matchMoveJ{ID: id, From: local, To: to})
		}
	}
	rewrite := func(q, upd, key string, out *[]matchTextJ, skip map[string]bool) error {
		rows, err := tx.Query(q)
		if err != nil {
			return err
		}
		type rv struct{ id, v string }
		var all []rv
		for rows.Next() {
			var r rv
			if err := rows.Scan(&r.id, &r.v); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		rows.Close()
		for _, r := range all {
			if skip[r.id] {
				continue
			}
			nv := string(repointJSON([]byte(r.v), key, o.repoint))
			if nv == r.v {
				continue
			}
			if _, err := tx.Exec(upd, nv, r.id); err != nil {
				return err
			}
			*out = append(*out, matchTextJ{ID: r.id, Before: r.v, After: nv})
		}
		return nil
	}
	if err := rewrite(`SELECT id, layout_json FROM workspaces`, `UPDATE workspaces SET layout_json = ? WHERE id = ?`, "connectionId", &j.Workspaces, nil); err != nil {
		return err
	}
	parked := map[string]bool{}
	for local := range o.repoint {
		parked[local] = true
	}
	if err := rewrite(`SELECT id, overrides_json FROM connections WHERE catalog_id IS NULL`, `UPDATE connections SET overrides_json = ? WHERE id = ?`, "connection_id", &j.ConnRefs, parked); err != nil {
		return err
	}
	if err := rewrite(`SELECT id, settings_json FROM folders WHERE catalog_id IS NULL`, `UPDATE folders SET settings_json = ? WHERE id = ?`, "connection_id", &j.FolderRefs, nil); err != nil {
		return err
	}

	// Park the local connections, with their effective settings on them.
	name := matchParkName(m.cat)
	var park string
	err := tx.QueryRow(`SELECT id FROM folders WHERE parent_id IS NULL AND catalog_id IS NULL AND name = ? LIMIT 1`, name).Scan(&park)
	if errors.Is(err, sql.ErrNoRows) {
		if park, err = a.db.CreateFolderTx(tx, store.NewFolder{Name: name, SortOrder: 1 << 30}); err != nil {
			return err
		}
		j.ParkCreated = true
	} else if err != nil {
		return err
	}
	j.ParkFolder = park
	res.Folder = name
	for _, p := range pairs {
		l := m.cByID[p[0]]
		var before string
		if err := tx.QueryRow(`SELECT overrides_json FROM connections WHERE id = ?`, l.ID).Scan(&before); err != nil {
			return err
		}
		b, err := json.Marshal(m.eff[l.ID])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE connections SET folder_id = ?, overrides_json = ?, last_used_at = NULL WHERE id = ?`, park, string(b), l.ID); err != nil {
			return err
		}
		j.Parked = append(j.Parked, matchParkedJ{ID: l.ID, FolderID: l.FolderID, SortOrder: l.SortOrder, Before: before, After: string(b), LastUsed: l.LastUsedAt})
	}

	return saveMatchJournalsTx(tx, catID, append(prior, *j))
}

// rowJSON reads one row as a column -> value object (for the journal).
func rowJSON(tx *sql.Tx, table, id string) (json.RawMessage, error) {
	rows, err := tx.Query(`SELECT * FROM `+table+` WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	m := map[string]any{}
	for i, c := range cols {
		if b, ok := vals[i].([]byte); ok {
			vals[i] = string(b)
		}
		m[c] = vals[i]
	}
	b, err := json.Marshal(m)
	return b, err
}

// Undo --------------------------------------------------------------------

// CatalogMatchUndo reverts the last match of a catalog. What was changed
// again since (a layer edited, a workspace saved, a connection moved on)
// keeps its current value and is counted in Kept.
func (a *App) CatalogMatchUndo(catalogID string) (*CatalogMatchUndoResult, error) {
	// The journal is read under the same lock as Apply, so the run undone
	// is the last one and a run applied meanwhile keeps its entry.
	rt := a.catRT()
	rt.apply.Lock()
	defer rt.apply.Unlock()
	runs := a.matchJournals(catalogID)
	if len(runs) == 0 {
		return nil, fmt.Errorf("there is no match to undo for this catalog")
	}
	j := runs[len(runs)-1]
	res := &CatalogMatchUndoResult{Pairs: len(j.Pairs)}
	var delKeys []string
	err := a.db.WithTx(func(tx *sql.Tx) error {
		var err error
		delKeys, err = a.undoMatchTx(tx, catalogID, j, res)
		if err != nil {
			return err
		}
		return saveMatchJournalsTx(tx, catalogID, runs[:len(runs)-1])
	})
	if err == nil {
		if cat, gerr := a.db.GetCatalog(catalogID); gerr == nil {
			ext, _ := catalog.PrepareExternal(a.db, cat, nil)
			if merr := catalog.Materialise(a.db, catalogID, ext); merr != nil {
				log.Printf("catalog %s: materialise after undo: %v", cat.Name, merr)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if a.vault != nil {
		for _, k := range delKeys {
			_ = a.vault.Delete(k)
		}
	}
	a.recordAudit("catalog.match_undo", catalogID, map[string]string{"pairs": strconv.Itoa(res.Pairs), "kept": strconv.Itoa(res.Kept)})
	EventsEmit("catalog_changed", map[string]any{"catalog_id": catalogID})
	return res, nil
}

func (a *App) undoMatchTx(tx *sql.Tx, catID string, j matchJournal, res *CatalogMatchUndoResult) ([]string, error) {
	text := func(q, upd string, items []matchTextJ) error {
		for i := len(items) - 1; i >= 0; i-- {
			it := items[i]
			var cur string
			err := tx.QueryRow(q, it.ID).Scan(&cur)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if cur != it.After {
				res.Kept++
				continue
			}
			if _, err := tx.Exec(upd, it.Before, it.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := text(`SELECT layout_json FROM workspaces WHERE id = ?`, `UPDATE workspaces SET layout_json = ? WHERE id = ?`, j.Workspaces); err != nil {
		return nil, err
	}
	if err := text(`SELECT overrides_json FROM connections WHERE id = ?`, `UPDATE connections SET overrides_json = ? WHERE id = ?`, j.ConnRefs); err != nil {
		return nil, err
	}
	if err := text(`SELECT settings_json FROM folders WHERE id = ?`, `UPDATE folders SET settings_json = ? WHERE id = ?`, j.FolderRefs); err != nil {
		return nil, err
	}
	for _, s := range j.Snippets {
		r, err := tx.Exec(`UPDATE snippets SET connection_id = ? WHERE id = ? AND connection_id = ?`, s.From, s.ID, s.To)
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			res.Kept++
		}
	}

	// Parked connections back where they were.
	for _, p := range j.Parked {
		var folder sql.NullString
		var cur string
		err := tx.QueryRow(`SELECT folder_id, overrides_json FROM connections WHERE id = ?`, p.ID).Scan(&folder, &cur)
		if errors.Is(err, sql.ErrNoRows) {
			res.Kept++
			continue
		}
		if err != nil {
			return nil, err
		}
		if !folder.Valid || folder.String != j.ParkFolder {
			res.Kept++
			continue
		}
		dest := p.FolderID
		if dest != nil {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM folders WHERE id = ?`, *dest).Scan(&n); err != nil {
				return nil, err
			}
			if n == 0 {
				dest = nil
			}
		}
		ov := cur
		if cur == p.After {
			ov = p.Before
		} else {
			res.Kept++
		}
		if _, err := tx.Exec(`UPDATE connections SET folder_id = ?, sort_order = ?, overrides_json = ?, last_used_at = COALESCE(last_used_at, ?) WHERE id = ?`, dest, p.SortOrder, ov, p.LastUsed, p.ID); err != nil {
			return nil, err
		}
	}

	// Forwards.
	for _, f := range j.FwdMoved {
		r, err := tx.Exec(`UPDATE port_forwards SET connection_id = ? WHERE id = ? AND connection_id = ?`, f.From, f.ID, f.To)
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			res.Kept++
		}
	}
	for _, raw := range j.FwdDeleted {
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		var cols []string
		var args []any
		for k, v := range row {
			cols = append(cols, k)
			args = append(args, v)
		}
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
		if _, err := tx.Exec(`INSERT OR IGNORE INTO port_forwards (`+strings.Join(cols, ", ")+`) VALUES (`+ph+`)`, args...); err != nil {
			return nil, err
		}
	}
	for _, f := range j.FwdCols {
		var auto bool
		var bm, mode string
		err := tx.QueryRow(`SELECT auto_start, bookmarks, browser_mode FROM port_forwards WHERE id = ?`, f.ID).Scan(&auto, &bm, &mode)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		kept := false
		if auto == f.AfterAutoStart {
			auto = f.AutoStart
		} else {
			kept = true
		}
		if bm == f.AfterBookmarks {
			bm = f.Bookmarks
		} else {
			kept = true
		}
		if mode == f.AfterBrowserMode {
			mode = f.BrowserMode
		} else {
			kept = true
		}
		if kept {
			res.Kept++
		}
		if _, err := tx.Exec(`UPDATE port_forwards SET auto_start = ?, bookmarks = ?, browser_mode = ? WHERE id = ?`, auto, bm, mode, f.ID); err != nil {
			return nil, err
		}
	}

	// Catalog side.
	for i := len(j.Layers) - 1; i >= 0; i-- {
		l := j.Layers[i]
		var cur string
		err := tx.QueryRow(`SELECT json FROM catalog_overrides WHERE catalog_id = ? AND remote_uuid = ?`, catID, l.UUID).Scan(&cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if canonicalJSONText(cur) != canonicalJSONText(l.After) {
			res.Kept++
			continue
		}
		if err := setLayerTx(tx, catID, l.UUID, l.Kind, l.Before); err != nil {
			return nil, err
		}
	}
	for _, b := range j.Bindings {
		r, err := tx.Exec(`DELETE FROM catalog_bindings WHERE catalog_id = ? AND slot_uuid = ? AND target_id = ?`, catID, b.Slot, b.Target)
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			res.Kept++
		}
	}
	var delKeys []string
	// A copied password goes only while it still equals its source; one
	// the user saved since stays, with its reference.
	created := map[string]bool{}
	for _, v := range j.VaultCopies {
		if a.vault == nil {
			continue
		}
		cur, ok1, err1 := a.vault.Get(v.Key)
		src, ok2, err2 := a.vault.Get(v.From)
		if err1 == nil && err2 == nil && ok1 && ok2 && cur == src {
			created[v.Key] = true
		} else {
			res.Kept++
		}
	}
	for _, c := range j.LocalCols {
		var pass, vnc sql.NullString
		var fav, hidden bool
		var used sql.NullInt64
		err := tx.QueryRow(`SELECT password_vault_key, vnc_password_vault_key, favorite, open_hidden, last_used_at FROM connections WHERE id = ?`, c.ID).Scan(&pass, &vnc, &fav, &hidden, &used)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pk, vk := c.PassKey, c.VncKey
		if pass.Valid && created[pass.String] {
			delKeys = append(delKeys, pass.String)
		} else if pass.Valid {
			pk = &pass.String
		}
		if vnc.Valid && created[vnc.String] {
			delKeys = append(delKeys, vnc.String)
		} else if vnc.Valid {
			vk = &vnc.String
		}
		// Changed since (starred again, used since): the current value stays.
		if fav == c.AfterFavorite {
			fav = c.Favorite
		}
		if hidden == c.AfterHidden {
			hidden = c.OpenHidden
		}
		lu := nullInt64Ptr(used)
		if (lu == nil && c.AfterLastUsed == nil) || (lu != nil && c.AfterLastUsed != nil && *lu == *c.AfterLastUsed) {
			lu = c.LastUsed
		}
		if _, err := tx.Exec(`UPDATE connections SET favorite = ?, open_hidden = ?, last_used_at = ?, password_vault_key = ?, vnc_password_vault_key = ? WHERE id = ?`,
			fav, hidden, lu, pk, vk, c.ID); err != nil {
			return nil, err
		}
	}
	if j.ParkCreated {
		var n int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM connections WHERE folder_id = ?) + (SELECT COUNT(*) FROM folders WHERE parent_id = ?)`, j.ParkFolder, j.ParkFolder).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			if _, err := tx.Exec(`DELETE FROM folders WHERE id = ?`, j.ParkFolder); err != nil {
				return nil, err
			}
		}
	}
	return delKeys, nil
}

func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func canonicalJSONText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}
