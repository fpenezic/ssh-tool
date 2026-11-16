package store

// Turning catalog rows into the user's own local rows.
//
// Two user actions share this: "Remove catalog" with "Keep its connections
// as a local folder" (KeepCatalogLocal: the same rows, same ids, in place)
// and "Copy to my connections" (CopyCatalogRows: new rows under a local
// folder, independent of the catalog). Materialised rows already hold the
// translated settings with the user's own layer overlaid, so what is left
// is "localising" them:
//   - a slot this user bound is already a local id and stays;
//   - an unbound slot, or one not available to the user, becomes empty: a
//     jump host holding one goes as a whole, any other field is removed.
//     Each is reported, and a connection gets a line in its notes;
//   - credentials the catalog client made for external slots (catcred-*)
//     are catalog-owned (shadowing, removal and the credential folder go
//     by the prefix), so they become ordinary credentials under new ids
//     and the references are rewritten.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// EmptiedField is a setting left empty because it named a catalog slot
// this user had not bound.
type EmptiedField struct {
	Kind  string `json:"kind"` // folders | connections
	ID    string `json:"id"`   // the local row (after the action)
	Name  string `json:"name"`
	Field string `json:"field"` // label: "Credential", "Jump host", ...
	Slot  string `json:"slot"`  // the slot's name, "" when unknown
}

// LocaliseResult is what a keep or a copy did.
type LocaliseResult struct {
	Folders     int            `json:"folders"`
	Connections int            `json:"connections"`
	Forwards    int            `json:"forwards"`
	Credentials int            `json:"credentials"` // made ordinary
	Emptied     []EmptiedField `json:"emptied"`
	// RootID: the local folder (keep) or the top copied row (copy).
	RootID string `json:"root_id"`
	// OutsideJumps: copied connections still jumping through a connection
	// outside the copy (the original, which keeps working while it exists).
	OutsideJumps []string `json:"outside_jumps,omitempty"`
}

var slotFieldLabels = map[string]string{
	"auth_ref": "Credential", "otp_ref": "One-time code", "network_profile_id": "Network",
	"jump_host": "Jump host", "db_credential_ref": "Database login", "credential_ref": "Database login",
	"kafka_credential_ref": "Kafka login",
}

func slotFieldLabel(k string) string {
	if l, ok := slotFieldLabels[k]; ok {
		return l
	}
	return k
}

type localiser struct {
	catID    string
	slotName map[string]string // slot uuid -> name
	creds    map[string]string // catcred id -> its ordinary id
	// jumps: connection id -> its new id (copy), for jump hosts inside.
	jumps map[string]string
}

// hasMarker reports whether a value holds a catalog marker anywhere.
func hasMarker(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if e, ok := ParseCatalogRef(t, ""); ok {
			return e.UUID, true
		}
	case map[string]any:
		for _, x := range t {
			if u, ok := hasMarker(x); ok {
				return u, true
			}
		}
	case []any:
		for _, x := range t {
			if u, ok := hasMarker(x); ok {
				return u, true
			}
		}
	}
	return "", false
}

// rewrite replaces credential and jump ids inside a value.
func (l *localiser) rewrite(v any) any {
	switch t := v.(type) {
	case string:
		if n, ok := l.creds[t]; ok {
			return n
		}
		return t
	case map[string]any:
		for k, x := range t {
			if k == "connection_id" {
				if s, ok := x.(string); ok {
					if n, ok := l.jumps[s]; ok {
						t[k] = n
						continue
					}
				}
			}
			t[k] = l.rewrite(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = l.rewrite(x)
		}
		return t
	}
	return v
}

// strip removes markers from a settings object and returns the emptied
// fields (label, slot name).
func (l *localiser) strip(m map[string]any) [][2]string {
	var out [][2]string
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		if k == "db_targets" {
			list, _ := v.([]any)
			for _, it := range list {
				tg, _ := it.(map[string]any)
				for tk, tv := range tg {
					if u, ok := hasMarker(tv); ok {
						delete(tg, tk)
						out = append(out, [2]string{slotFieldLabel(tk), l.slotName[u]})
					}
				}
			}
			continue
		}
		if u, ok := hasMarker(v); ok {
			delete(m, k)
			out = append(out, [2]string{slotFieldLabel(k), l.slotName[u]})
		}
	}
	return out
}

// localise turns one row's settings JSON into local terms.
func (l *localiser) localise(raw string) (string, [][2]string, error) {
	m := map[string]any{}
	if strings.TrimSpace(raw) != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return "", nil, err
		}
	}
	empt := l.strip(m)
	l.rewrite(m)
	b, err := json.Marshal(m)
	if err != nil {
		return "", nil, err
	}
	return string(b), empt, nil
}

func emptiedNote(e [][2]string) string {
	var lines []string
	for _, f := range e {
		if f[1] != "" {
			lines = append(lines, fmt.Sprintf("%s: was the catalog slot %q (not set when it left the catalog)", f[0], f[1]))
		} else {
			lines = append(lines, fmt.Sprintf("%s: was a catalog slot (not set when it left the catalog)", f[0]))
		}
	}
	return strings.Join(lines, "\n")
}

func appendNote(notes, add string) string {
	if add == "" {
		return notes
	}
	if strings.TrimSpace(notes) == "" {
		return add
	}
	return strings.TrimRight(notes, "\n") + "\n\n" + add
}

func newLocaliser(tx *sql.Tx, catID string) (*localiser, error) {
	l := &localiser{catID: catID, slotName: map[string]string{}, creds: map[string]string{}, jumps: map[string]string{}}
	rows, err := tx.Query(`SELECT uuid, name FROM catalog_slots WHERE catalog_id = ?`, catID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u, n string
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		l.slotName[u] = n
	}
	return l, rows.Err()
}

// ordinaryCredentials makes the catalog's catcred-* credentials ordinary:
// move (keep) renames them in place under new ids into a new folder named
// after the catalog; otherwise (copy) they are copied, names made unique.
// Only those the given settings JSONs reference are handled.
func (l *localiser) ordinaryCredentials(tx *sql.Tx, catName string, jsons []string, move bool) (int, error) {
	prefix := catalogCredPrefix + l.catID + "-"
	used := map[string]bool{}
	for _, j := range jsons {
		for i := strings.Index(j, prefix); i >= 0; {
			end := i + len(prefix)
			for end < len(j) && j[end] != '"' {
				end++
			}
			used[j[i:end]] = true
			next := strings.Index(j[end:], prefix)
			if next < 0 {
				break
			}
			i = end + next
		}
	}
	if len(used) == 0 {
		return 0, nil
	}
	ts := now()
	folder := newID()
	if _, err := tx.Exec(`INSERT INTO credential_folders (id, parent_id, name, sort_order, created_at, updated_at) VALUES (?, NULL, ?, 0, ?, ?)`,
		folder, uniqueCredFolderName(tx, catName), ts, ts); err != nil {
		return 0, err
	}
	cols, err := txTableColumns(tx, "credential_refs")
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	n := 0
	for _, id := range ids {
		var name string
		if err := tx.QueryRow(`SELECT name FROM credential_refs WHERE id = ?`, id).Scan(&name); err != nil {
			continue // gone: the reference stays dangling, as it was
		}
		nid := newID()
		if move {
			if _, err := tx.Exec(`UPDATE credential_refs SET id = ?, folder_id = ?, updated_at = ? WHERE id = ?`, nid, folder, ts, id); err != nil {
				return n, err
			}
		} else {
			var sel []string
			for _, c := range cols {
				switch c {
				case "id":
					sel = append(sel, "?")
				case "folder_id":
					sel = append(sel, "?")
				case "name":
					sel = append(sel, "?")
				case "created_at", "updated_at":
					sel = append(sel, fmt.Sprint(ts))
				default:
					sel = append(sel, c)
				}
			}
			q := `INSERT INTO credential_refs (` + strings.Join(cols, ",") + `) SELECT ` + strings.Join(sel, ",") + ` FROM credential_refs WHERE id = ?`
			args := []any{}
			for _, c := range cols {
				switch c {
				case "id":
					args = append(args, nid)
				case "folder_id":
					args = append(args, folder)
				case "name":
					args = append(args, uniqueCredName(tx, name))
				}
			}
			if _, err := tx.Exec(q, append(args, id)...); err != nil {
				return n, err
			}
		}
		l.creds[id] = nid
		n++
	}
	return n, nil
}

func uniqueCredName(tx *sql.Tx, name string) string {
	try := name
	for i := 1; i < 100; i++ {
		var x int
		if tx.QueryRow(`SELECT COUNT(*) FROM credential_refs WHERE name = ?`, try).Scan(&x) == nil && x == 0 {
			return try
		}
		if i == 1 {
			try = name + " (copy)"
		} else {
			try = fmt.Sprintf("%s (copy %d)", name, i)
		}
	}
	return name + " " + newID()[:8]
}

func uniqueCredFolderName(tx *sql.Tx, name string) string {
	try := name
	for i := 2; i < 100; i++ {
		var x int
		if tx.QueryRow(`SELECT COUNT(*) FROM credential_folders WHERE name = ? AND parent_id IS NULL`, try).Scan(&x) == nil && x == 0 {
			return try
		}
		try = fmt.Sprintf("%s %d", name, i)
	}
	return name
}

// KeepCatalogLocal detaches a catalog and keeps its tree as the user's own
// local folder: the same rows and ids, catalog columns cleared, settings
// localised (the user's own layer is already in them), empty skeleton
// folders dropped, then the catalog's slots, bindings, layers, pending
// publishes, made credentials and the catalog itself removed.
func (d *DB) KeepCatalogLocal(catalogID string) (*LocaliseResult, error) {
	cat, err := d.GetCatalog(catalogID)
	if err != nil {
		return nil, err
	}
	res := &LocaliseResult{RootID: cat.RootFolderID, Emptied: []EmptiedField{}}
	err = d.WithCatalogWriter(func(tx *sql.Tx) error {
		l, err := newLocaliser(tx, catalogID)
		if err != nil {
			return err
		}
		// Skeleton folders with nothing below them go (repeat: a chain of
		// outlines empties from the bottom up).
		for {
			r, err := tx.Exec(`DELETE FROM folders WHERE catalog_id = ? AND json_valid(catalog_json) AND json_extract(catalog_json, '$.access') = 'skeleton'
				AND NOT EXISTS (SELECT 1 FROM folders c WHERE c.parent_id = folders.id)
				AND NOT EXISTS (SELECT 1 FROM connections c WHERE c.folder_id = folders.id)`, catalogID)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				break
			}
		}
		type row struct{ id, name, settings, notes string }
		load := func(q string) ([]row, error) {
			rs, err := tx.Query(q, catalogID)
			if err != nil {
				return nil, err
			}
			defer rs.Close()
			var out []row
			for rs.Next() {
				var r row
				if err := rs.Scan(&r.id, &r.name, &r.settings, &r.notes); err != nil {
					return nil, err
				}
				out = append(out, r)
			}
			return out, rs.Err()
		}
		folders, err := load(`SELECT id, name, IFNULL(settings_json, ''), '' FROM folders WHERE catalog_id = ?`)
		if err != nil {
			return err
		}
		conns, err := load(`SELECT id, name, IFNULL(overrides_json, ''), IFNULL(notes, '') FROM connections WHERE catalog_id = ?`)
		if err != nil {
			return err
		}
		var all []string
		for _, r := range append(append([]row{}, folders...), conns...) {
			all = append(all, r.settings)
		}
		if res.Credentials, err = l.ordinaryCredentials(tx, cat.Name, all, true); err != nil {
			return err
		}
		clear := `catalog_id = NULL, remote_uuid = NULL, remote_rev = 0, catalog_json = '', catalog_state = ''`
		for _, r := range folders {
			s, empt, err := l.localise(r.settings)
			if err != nil {
				return fmt.Errorf("folder %s: %w", r.name, err)
			}
			for _, e := range empt {
				res.Emptied = append(res.Emptied, EmptiedField{Kind: "folders", ID: r.id, Name: r.name, Field: e[0], Slot: e[1]})
			}
			if _, err := tx.Exec(`UPDATE folders SET settings_json = ?, `+clear+` WHERE id = ?`, s, r.id); err != nil {
				return err
			}
			if r.id != cat.RootFolderID {
				res.Folders++
			}
		}
		for _, r := range conns {
			s, empt, err := l.localise(r.settings)
			if err != nil {
				return fmt.Errorf("connection %s: %w", r.name, err)
			}
			for _, e := range empt {
				res.Emptied = append(res.Emptied, EmptiedField{Kind: "connections", ID: r.id, Name: r.name, Field: e[0], Slot: e[1]})
			}
			if _, err := tx.Exec(`UPDATE connections SET overrides_json = ?, notes = ?, `+clear+` WHERE id = ?`,
				s, appendNote(r.notes, emptiedNote(empt)), r.id); err != nil {
				return err
			}
			res.Connections++
		}
		fr, err := tx.Exec(`UPDATE port_forwards SET `+clear+` WHERE catalog_id = ?`, catalogID)
		if err != nil {
			return err
		}
		n, _ := fr.RowsAffected()
		res.Forwards = int(n)
		if _, err := tx.Exec(`UPDATE folders SET `+clear+`, updated_at = ? WHERE id = ?`, now(), cat.RootFolderID); err != nil {
			return err
		}
		return d.DeleteCatalogTx(tx, catalogID)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// CopySource is one catalog row to copy with what it inherits from the
// catalog folders above the copy, folded in (the caller resolves it).
type CopySource struct {
	Kind      string // folders | connections
	ID        string
	Inherited string // effective settings JSON of the top row
}

// CopiedConnection maps a copied connection to its copy (the caller
// copies its stored passwords).
type CopiedConnection struct{ From, To string }

// CopyCatalogRows copies a catalog folder (with everything below it) or a
// connection under a local folder (targetID "" for the top level): new
// ids, settings localised, forwards copied, jump hosts inside the copy
// pointed at the copies. Nothing in the catalog changes, and the copy
// does not follow it.
func (d *DB) CopyCatalogRows(src CopySource, targetID string) (*LocaliseResult, []CopiedConnection, error) {
	table := map[string]string{"folders": "folders", "connections": "connections"}[src.Kind]
	if table == "" {
		return nil, nil, fmt.Errorf("copy: unknown kind %q", src.Kind)
	}
	catID, _, _, err := d.catalogRowState(table, src.ID)
	if err != nil {
		return nil, nil, err
	}
	if catID == "" {
		return nil, nil, fmt.Errorf("copy: not a catalog row")
	}
	cat, err := d.GetCatalog(catID)
	if err != nil {
		return nil, nil, err
	}
	if targetID != "" {
		var c sql.NullString
		if err := d.conn.QueryRow(`SELECT catalog_id FROM folders WHERE id = ?`, targetID).Scan(&c); err != nil {
			return nil, nil, fmt.Errorf("copy: target folder: %w", err)
		}
		if c.Valid {
			return nil, nil, fmt.Errorf("copy: the target must be one of your own folders")
		}
	}
	res := &LocaliseResult{Emptied: []EmptiedField{}}
	var copied []CopiedConnection
	err = d.WithCatalogWriter(func(tx *sql.Tx) error {
		l, err := newLocaliser(tx, catID)
		if err != nil {
			return err
		}
		// The rows of the copy.
		folderIDs := []string{}
		if src.Kind == "folders" {
			folderIDs = append(folderIDs, src.ID)
			for i := 0; i < len(folderIDs); i++ {
				rs, err := tx.Query(`SELECT id FROM folders WHERE parent_id = ? ORDER BY sort_order, name`, folderIDs[i])
				if err != nil {
					return err
				}
				for rs.Next() {
					var id string
					_ = rs.Scan(&id)
					folderIDs = append(folderIDs, id)
				}
				rs.Close()
			}
		}
		newIDs := map[string]string{}
		for _, f := range folderIDs {
			newIDs[f] = newID()
		}
		var connIDs []string
		if src.Kind == "connections" {
			connIDs = []string{src.ID}
		} else {
			for _, f := range folderIDs {
				rs, err := tx.Query(`SELECT id FROM connections WHERE folder_id = ? ORDER BY sort_order, name`, f)
				if err != nil {
					return err
				}
				for rs.Next() {
					var id string
					_ = rs.Scan(&id)
					connIDs = append(connIDs, id)
				}
				rs.Close()
			}
		}
		for _, c := range connIDs {
			newIDs[c] = newID()
			l.jumps[c] = newIDs[c]
		}

		settingsOf := func(table, col, id string) (string, error) {
			var s string
			err := tx.QueryRow(`SELECT IFNULL(`+col+`, '') FROM `+table+` WHERE id = ?`, id).Scan(&s)
			return s, err
		}
		var all []string
		for _, f := range folderIDs {
			s, err := settingsOf("folders", "settings_json", f)
			if err != nil {
				return err
			}
			all = append(all, s)
		}
		for _, c := range connIDs {
			s, err := settingsOf("connections", "overrides_json", c)
			if err != nil {
				return err
			}
			all = append(all, s)
		}
		all = append(all, src.Inherited)
		if res.Credentials, err = l.ordinaryCredentials(tx, cat.Name, all, false); err != nil {
			return err
		}

		copyRow := func(table, oldID, newIDv string, sets map[string]any) error {
			cols, err := txTableColumns(tx, table)
			if err != nil {
				return err
			}
			var names, sel []string
			var args []any
			for _, c := range cols {
				names = append(names, c)
				if v, ok := sets[c]; ok {
					sel = append(sel, "?")
					args = append(args, v)
					continue
				}
				switch c {
				case "catalog_id", "remote_uuid", "password_vault_key", "vnc_password_vault_key":
					sel = append(sel, "NULL")
				case "remote_rev":
					sel = append(sel, "0")
				case "catalog_json", "catalog_state":
					sel = append(sel, "''")
				default:
					sel = append(sel, c)
				}
			}
			args = append(args, oldID)
			_, err = tx.Exec(`INSERT INTO `+table+` (`+strings.Join(names, ",")+`) SELECT `+strings.Join(sel, ",")+` FROM `+table+` WHERE id = ?`, args...)
			return err
		}
		ts := now()
		uniqueName := func(table, parentCol string, parent any, name string) string {
			try := name
			for i := 1; i < 100; i++ {
				var x int
				q := `SELECT COUNT(*) FROM ` + table + ` WHERE name = ? AND ` + parentCol + ` IS ?`
				if tx.QueryRow(q, try, parent).Scan(&x) == nil && x == 0 {
					return try
				}
				if i == 1 {
					try = name + " (copy)"
				} else {
					try = fmt.Sprintf("%s (copy %d)", name, i)
				}
			}
			return try
		}
		var target any
		if targetID != "" {
			target = targetID
		}
		for i, f := range folderIDs {
			raw, _ := settingsOf("folders", "settings_json", f)
			if i == 0 && src.Inherited != "" {
				raw = src.Inherited
			}
			s, empt, err := l.localise(raw)
			if err != nil {
				return err
			}
			var name string
			var parent sql.NullString
			_ = tx.QueryRow(`SELECT name, parent_id FROM folders WHERE id = ?`, f).Scan(&name, &parent)
			for _, e := range empt {
				res.Emptied = append(res.Emptied, EmptiedField{Kind: "folders", ID: newIDs[f], Name: name, Field: e[0], Slot: e[1]})
			}
			sets := map[string]any{"id": newIDs[f], "settings_json": s, "created_at": ts, "updated_at": ts}
			if i == 0 {
				sets["parent_id"] = target
				sets["name"] = uniqueName("folders", "parent_id", target, name)
			} else {
				sets["parent_id"] = newIDs[parent.String]
			}
			if err := copyRow("folders", f, newIDs[f], sets); err != nil {
				return fmt.Errorf("copy folder %s: %w", name, err)
			}
			res.Folders++
		}
		for i, c := range connIDs {
			raw, _ := settingsOf("connections", "overrides_json", c)
			if i == 0 && src.Kind == "connections" && src.Inherited != "" {
				raw = src.Inherited
			}
			// A jump host outside the copy: kept, reported.
			if strings.Contains(raw, `"connection_id"`) {
				var m map[string]any
				if json.Unmarshal([]byte(raw), &m) == nil {
					if id := jumpConnID(m["jump_host"]); id != "" && newIDs[id] == "" {
						var name string
						_ = tx.QueryRow(`SELECT name FROM connections WHERE id = ?`, c).Scan(&name)
						res.OutsideJumps = append(res.OutsideJumps, name)
					}
				}
			}
			s, empt, err := l.localise(raw)
			if err != nil {
				return err
			}
			var name, notes string
			var folder sql.NullString
			_ = tx.QueryRow(`SELECT name, IFNULL(notes, ''), folder_id FROM connections WHERE id = ?`, c).Scan(&name, &notes, &folder)
			for _, e := range empt {
				res.Emptied = append(res.Emptied, EmptiedField{Kind: "connections", ID: newIDs[c], Name: name, Field: e[0], Slot: e[1]})
			}
			sets := map[string]any{"id": newIDs[c], "overrides_json": s, "notes": appendNote(notes, emptiedNote(empt)), "created_at": ts, "updated_at": ts}
			if src.Kind == "connections" {
				sets["folder_id"] = target
				sets["name"] = uniqueName("connections", "folder_id", target, name)
			} else {
				sets["folder_id"] = newIDs[folder.String]
			}
			if err := copyRow("connections", c, newIDs[c], sets); err != nil {
				return fmt.Errorf("copy connection %s: %w", name, err)
			}
			copied = append(copied, CopiedConnection{From: c, To: newIDs[c]})
			res.Connections++
			// Its forwards, catalog ones and the user's own.
			rs, err := tx.Query(`SELECT id FROM port_forwards WHERE connection_id = ?`, c)
			if err != nil {
				return err
			}
			var fw []string
			for rs.Next() {
				var id string
				_ = rs.Scan(&id)
				fw = append(fw, id)
			}
			rs.Close()
			for _, p := range fw {
				if err := copyRow("port_forwards", p, "", map[string]any{"id": newID(), "connection_id": newIDs[c]}); err != nil {
					return fmt.Errorf("copy forward: %w", err)
				}
				res.Forwards++
			}
		}
		if src.Kind == "folders" {
			res.RootID = newIDs[src.ID]
		} else {
			res.RootID = newIDs[src.ID]
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return res, copied, nil
}

func jumpConnID(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	if s, ok := m["connection_id"].(string); ok {
		return s
	}
	for _, k := range []string{"chain", "spec"} {
		if id := jumpConnID(m[k]); id != "" {
			return id
		}
	}
	if list, ok := m["chain"].([]any); ok {
		for _, x := range list {
			if id := jumpConnID(x); id != "" {
				return id
			}
		}
	}
	return ""
}
