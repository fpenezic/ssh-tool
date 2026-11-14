package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Catalog rows as the engine writes them. Everything here runs inside a
// WithCatalogWriter transaction (the guard triggers refuse it otherwise).
// Rows are found by (catalog_id, remote_uuid); local ids are generated
// once and never change, so references to them survive resyncs.

// CatalogFolderRow is a folder record to upsert. ParentLocalID is the
// resolved local parent (the catalog root for a space or a parent not
// known yet).
type CatalogFolderRow struct {
	RemoteUUID    string
	Rev           int64
	ParentLocalID string
	Name          string
	SortOrder     int64
	IconName      *string
	IconColor     *string
	RawJSON       string
}

// CatalogConnectionRow is a connection record to upsert.
type CatalogConnectionRow struct {
	RemoteUUID    string
	Rev           int64
	FolderLocalID string
	Name          string
	Hostname      string
	Protocol      string
	SortOrder     int64
	Tags          []string
	Notes         string
	IconName      *string
	IconColor     *string
	RawJSON       string
}

// CatalogForwardRow is a forward record to upsert. AutoStart is not here:
// the column holds the user's own decision, never the catalog suggestion.
type CatalogForwardRow struct {
	RemoteUUID        string
	Rev               int64
	ConnectionLocalID string
	Kind              string
	LocalAddr         *string
	LocalPort         *uint16
	RemoteHost        *string
	RemotePort        *uint16
	Description       string
	RawJSON           string
}

// CatalogHeld is what this store holds of one catalog: remote uuid -> local
// id and rev, per kind (folders, connections, forwards).
type CatalogHeld struct {
	IDs   map[string]map[string]string
	Revs  map[string]map[string]int64
	State map[string]map[string]string
}

var catalogKindTable = map[string]string{"folders": "folders", "connections": "connections", "forwards": "port_forwards"}

// CatalogHeldTx loads what this store holds of a catalog.
func (d *DB) CatalogHeldTx(tx *sql.Tx, catalogID string) (*CatalogHeld, error) {
	h := &CatalogHeld{IDs: map[string]map[string]string{}, Revs: map[string]map[string]int64{}, State: map[string]map[string]string{}}
	for kind, table := range catalogKindTable {
		h.IDs[kind], h.Revs[kind], h.State[kind] = map[string]string{}, map[string]int64{}, map[string]string{}
		rows, err := tx.Query(`SELECT id, remote_uuid, remote_rev, catalog_state FROM `+table+` WHERE catalog_id = ? AND remote_uuid IS NOT NULL`, catalogID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, uuid, state string
			var rev int64
			if err := rows.Scan(&id, &uuid, &rev, &state); err != nil {
				rows.Close()
				return nil, err
			}
			h.IDs[kind][uuid], h.Revs[kind][uuid], h.State[kind][uuid] = id, rev, state
		}
		rows.Close()
	}
	return h, nil
}

func jsonTags(tags []string) string {
	if tags == nil {
		tags = []string{}
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// UpsertCatalogFolderTx inserts or updates a catalog folder, reviving it
// from "removed". Returns its local id.
func (d *DB) UpsertCatalogFolderTx(tx *sql.Tx, catalogID string, r CatalogFolderRow, localID string) (string, error) {
	ts := now()
	if localID == "" {
		localID = newID()
		_, err := tx.Exec(`INSERT INTO folders (id, parent_id, name, sort_order, settings_json, icon_name, icon_color,
			created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json, catalog_state)
			VALUES (?, ?, ?, ?, '{}', ?, ?, ?, ?, ?, ?, ?, ?, '')`,
			localID, r.ParentLocalID, r.Name, r.SortOrder, r.IconName, r.IconColor, ts, ts, catalogID, r.RemoteUUID, r.Rev, r.RawJSON)
		return localID, err
	}
	_, err := tx.Exec(`UPDATE folders SET parent_id = ?, name = ?, sort_order = ?, icon_name = ?, icon_color = ?, icon_image_id = NULL,
		remote_rev = ?, catalog_json = ?, catalog_state = '', updated_at = ? WHERE id = ?`,
		r.ParentLocalID, r.Name, r.SortOrder, r.IconName, r.IconColor, r.Rev, r.RawJSON, ts, localID)
	return localID, err
}

// UpsertCatalogConnectionTx inserts or updates a catalog connection.
// overrides_json is left to materialisation (empty on insert).
func (d *DB) UpsertCatalogConnectionTx(tx *sql.Tx, catalogID string, r CatalogConnectionRow, localID string) (string, error) {
	ts := now()
	if localID == "" {
		localID = newID()
		_, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, sort_order, overrides_json, tags_json, notes,
			favorite, sensitive, open_hidden, protocol, icon_name, icon_color, created_at, updated_at,
			catalog_id, remote_uuid, remote_rev, catalog_json, catalog_state)
			VALUES (?, ?, ?, ?, ?, '{}', ?, ?, 0, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
			localID, r.FolderLocalID, r.Name, strings.TrimSpace(r.Hostname), r.SortOrder, jsonTags(r.Tags), r.Notes,
			r.Protocol, r.IconName, r.IconColor, ts, ts, catalogID, r.RemoteUUID, r.Rev, r.RawJSON)
		return localID, err
	}
	_, err := tx.Exec(`UPDATE connections SET folder_id = ?, name = ?, hostname = ?, sort_order = ?, tags_json = ?, notes = ?,
		protocol = ?, icon_name = ?, icon_color = ?, icon_image_id = NULL, remote_rev = ?, catalog_json = ?, catalog_state = '',
		updated_at = ? WHERE id = ?`,
		r.FolderLocalID, r.Name, strings.TrimSpace(r.Hostname), r.SortOrder, jsonTags(r.Tags), r.Notes,
		r.Protocol, r.IconName, r.IconColor, r.Rev, r.RawJSON, ts, localID)
	return localID, err
}

// UpsertCatalogForwardTx inserts or updates a catalog forward. The local
// columns (auto start, disabled, bookmarks, browser mode) are never
// touched; local_port is written here and adjusted by the user's layer at
// materialisation.
func (d *DB) UpsertCatalogForwardTx(tx *sql.Tx, catalogID string, r CatalogForwardRow, localID string) (string, error) {
	if r.Kind != "local" && r.Kind != "remote" && r.Kind != "dynamic" {
		return "", fmt.Errorf("catalog forward %s: unknown kind %q", r.RemoteUUID, r.Kind)
	}
	if localID == "" {
		localID = newID()
		_, err := tx.Exec(`INSERT INTO port_forwards (id, connection_id, kind, local_addr, local_port, remote_host, remote_port,
			auto_start, description, catalog_id, remote_uuid, remote_rev, catalog_json, catalog_state)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, '')`,
			localID, r.ConnectionLocalID, r.Kind, nullableStr(r.LocalAddr), nullableU16(r.LocalPort),
			nullableStr(r.RemoteHost), nullableU16(r.RemotePort), r.Description, catalogID, r.RemoteUUID, r.Rev, r.RawJSON)
		return localID, err
	}
	_, err := tx.Exec(`UPDATE port_forwards SET connection_id = ?, kind = ?, local_addr = ?, local_port = ?, remote_host = ?,
		remote_port = ?, description = ?, remote_rev = ?, catalog_json = ?, catalog_state = '' WHERE id = ?`,
		r.ConnectionLocalID, r.Kind, nullableStr(r.LocalAddr), nullableU16(r.LocalPort),
		nullableStr(r.RemoteHost), nullableU16(r.RemotePort), r.Description, r.Rev, r.RawJSON, localID)
	return localID, err
}

func hasOverride(tx *sql.Tx, catalogID, uuid string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM catalog_overrides WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, uuid).Scan(&n)
	return n > 0, err
}

// forwardHasLocalData: the user adjusted this catalog forward.
func forwardHasLocalData(tx *sql.Tx, catalogID, id, uuid string) (bool, error) {
	if ok, err := hasOverride(tx, catalogID, uuid); ok || err != nil {
		return ok, err
	}
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM port_forwards WHERE id = ?
		AND (auto_start != 0 OR disabled != 0 OR bookmarks NOT IN ('', '[]') OR browser_mode != '')`, id).Scan(&n)
	return n > 0, err
}

// connectionHasLocalData: an override, a forward of the user's own, a
// stored password, or a catalog forward the user adjusted. Favourite and
// recency do not count (decision of the catalog design).
func connectionHasLocalData(tx *sql.Tx, catalogID, id, uuid string) (bool, error) {
	if ok, err := hasOverride(tx, catalogID, uuid); ok || err != nil {
		return ok, err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = ?
		AND (password_vault_key IS NOT NULL OR vnc_password_vault_key IS NOT NULL)`, id).Scan(&n); err != nil || n > 0 {
		return n > 0, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM port_forwards WHERE connection_id = ? AND remote_uuid IS NULL`, id).Scan(&n); err != nil || n > 0 {
		return n > 0, err
	}
	rows, err := tx.Query(`SELECT id, remote_uuid FROM port_forwards WHERE connection_id = ? AND remote_uuid IS NOT NULL`, id)
	if err != nil {
		return false, err
	}
	type fw struct{ id, uuid string }
	var fws []fw
	for rows.Next() {
		var f fw
		if err := rows.Scan(&f.id, &f.uuid); err != nil {
			rows.Close()
			return false, err
		}
		fws = append(fws, f)
	}
	rows.Close()
	for _, f := range fws {
		if ok, err := forwardHasLocalData(tx, catalogID, f.id, f.uuid); ok || err != nil {
			return ok, err
		}
	}
	return false, nil
}

// RemoveCatalogRecordTx handles a record that left the catalog (tombstone,
// or absent from a full snapshot). It is deleted unless it carries local
// data; then it stays, flagged "removed", for the user to keep as a local
// row or delete. A folder stays while anything is still below it (the
// rows below are removed first by the caller, children before parents),
// since its settings are what the kept rows inherit. Returns whether the
// record was kept.
func (d *DB) RemoveCatalogRecordTx(tx *sql.Tx, catalogID, kind, uuid string) (bool, error) {
	table, ok := catalogKindTable[kind]
	if !ok {
		return false, fmt.Errorf("catalog: unknown kind %q", kind)
	}
	var id string
	err := tx.QueryRow(`SELECT id FROM `+table+` WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, uuid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // never held (tombstones include those)
	}
	if err != nil {
		return false, err
	}
	keep := false
	switch kind {
	case "forwards":
		keep, err = forwardHasLocalData(tx, catalogID, id, uuid)
	case "connections":
		keep, err = connectionHasLocalData(tx, catalogID, id, uuid)
	case "folders":
		if keep, err = hasOverride(tx, catalogID, uuid); err == nil && !keep {
			var n int
			err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM folders WHERE parent_id = ?) + (SELECT COUNT(*) FROM connections WHERE folder_id = ?)`, id, id).Scan(&n)
			keep = n > 0
		}
	}
	if err != nil {
		return false, err
	}
	if keep {
		_, err = tx.Exec(`UPDATE `+table+` SET catalog_state = 'removed' WHERE id = ?`, id)
		return true, err
	}
	_, err = tx.Exec(`DELETE FROM `+table+` WHERE id = ?`, id)
	return false, err
}

// CatalogParentsTx re-links every catalog folder and connection to the
// local id of the parent its catalog record names, and every forward to
// its connection. Records whose parent is not held hang under the root
// (a fallback the protocol rules out: every record arrives with its parent
// chain). Returns how many fell back.
func (d *DB) CatalogParentsTx(tx *sql.Tx, catalogID, rootID string) (int, error) {
	fallback := 0
	// Folders: parent_uuid null = a space, under the root.
	res, err := tx.Exec(`UPDATE folders SET parent_id = COALESCE(
			(SELECT p.id FROM folders p WHERE p.catalog_id = folders.catalog_id
			   AND p.remote_uuid = json_extract(folders.catalog_json, '$.parent_uuid')), ?)
		WHERE catalog_id = ? AND remote_uuid IS NOT NULL AND json_valid(catalog_json)`, rootID, catalogID)
	if err != nil {
		return 0, err
	}
	_ = res
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM folders f WHERE f.catalog_id = ? AND f.remote_uuid IS NOT NULL
		AND json_extract(f.catalog_json, '$.parent_uuid') IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM folders p WHERE p.catalog_id = f.catalog_id AND p.remote_uuid = json_extract(f.catalog_json, '$.parent_uuid'))`,
		catalogID).Scan(&n); err != nil {
		return 0, err
	}
	fallback += n
	if _, err := tx.Exec(`UPDATE connections SET folder_id = COALESCE(
			(SELECT p.id FROM folders p WHERE p.catalog_id = connections.catalog_id
			   AND p.remote_uuid = json_extract(connections.catalog_json, '$.folder_uuid')), ?)
		WHERE catalog_id = ? AND remote_uuid IS NOT NULL AND json_valid(catalog_json)`, rootID, catalogID); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM connections c WHERE c.catalog_id = ? AND c.remote_uuid IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM folders p WHERE p.catalog_id = c.catalog_id AND p.remote_uuid = json_extract(c.catalog_json, '$.folder_uuid'))`,
		catalogID).Scan(&n); err != nil {
		return 0, err
	}
	fallback += n
	return fallback, nil
}

// DeleteCatalogTx removes a catalog's rows and tables (detach). Rows that
// carry local data are kept and turned into local rows first by the
// caller if wanted; here everything of the catalog goes, root included.
func (d *DB) DeleteCatalogTx(tx *sql.Tx, catalogID string) error {
	for _, q := range []string{
		`DELETE FROM port_forwards WHERE catalog_id = ?`,
		`DELETE FROM connections WHERE catalog_id = ?`,
		`DELETE FROM folders WHERE catalog_id = ?`,
		`DELETE FROM catalog_slots WHERE catalog_id = ?`,
		`DELETE FROM catalog_bindings WHERE catalog_id = ?`,
		`DELETE FROM catalog_overrides WHERE catalog_id = ?`,
		`DELETE FROM catalogs WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, catalogID); err != nil {
			return err
		}
	}
	return nil
}

// SetCatalogCursorTx stores the cursor with the rows it describes.
func (d *DB) SetCatalogCursorTx(tx *sql.Tx, catalogID, cursor string, syncedAt int64) error {
	_, err := tx.Exec(`UPDATE catalogs SET cursor = ?, last_sync_at = ?, last_error = '' WHERE id = ?`, cursor, syncedAt, catalogID)
	return err
}

// CatalogRowStateTx returns (catalog_id, remote_uuid, catalog_state) of a
// local row, for the "removed" actions.
func (d *DB) catalogRowState(table, id string) (catalogID, uuid, state string, err error) {
	var c, u sql.NullString
	err = d.conn.QueryRow(`SELECT catalog_id, remote_uuid, catalog_state FROM `+table+` WHERE id = ?`, id).Scan(&c, &u, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	return c.String, u.String, state, err
}

// ErrNotRemoved: the "removed from catalog" actions apply only to rows the
// catalog no longer holds.
var ErrNotRemoved = errors.New("this record is still in its catalog")

// DeleteRemovedCatalogRow deletes a connection or folder that left its
// catalog (the user's "Delete"), with everything below it, then any
// removed ancestors left empty.
func (d *DB) DeleteRemovedCatalogRow(kind, id string) error {
	table, ok := catalogKindTable[kind]
	if !ok || kind == "forwards" {
		return fmt.Errorf("catalog: cannot delete a %s row this way", kind)
	}
	catID, _, state, err := d.catalogRowState(table, id)
	if err != nil {
		return err
	}
	if catID == "" || state != "removed" {
		return ErrNotRemoved
	}
	var parent sql.NullString
	col := "parent_id"
	if kind == "connections" {
		col = "folder_id"
	}
	if err := d.conn.QueryRow(`SELECT `+col+` FROM `+table+` WHERE id = ?`, id).Scan(&parent); err != nil {
		return err
	}
	return d.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE id = ?`, id); err != nil {
			return err
		}
		return pruneRemovedAncestors(tx, parent.String)
	})
}

// pruneRemovedAncestors deletes removed folders that have nothing below
// them any more, walking up from id.
func pruneRemovedAncestors(tx *sql.Tx, id string) error {
	for i := 0; id != "" && i < 1000; i++ {
		var state string
		var parent sql.NullString
		var override int
		err := tx.QueryRow(`SELECT f.catalog_state, f.parent_id,
			(SELECT COUNT(*) FROM catalog_overrides o WHERE o.catalog_id = f.catalog_id AND o.remote_uuid = f.remote_uuid)
			FROM folders f WHERE f.id = ?`, id).Scan(&state, &parent, &override)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state != "removed" || override > 0 {
			return nil
		}
		var n int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM folders WHERE parent_id = ?) + (SELECT COUNT(*) FROM connections WHERE folder_id = ?)`, id, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if _, err := tx.Exec(`DELETE FROM folders WHERE id = ?`, id); err != nil {
			return err
		}
		id = parent.String
	}
	return nil
}

// KeepRemovedConnectionLocal turns a connection that left its catalog into
// an ordinary local connection at the top level of the tree. settings is
// what it resolved to through its (removed) catalog folders, folded into
// its own overrides by the caller, so it keeps working the same way.
// Its forwards become local forwards; its local layer is dropped (it is
// part of settings now). Removed ancestors left empty are pruned.
func (d *DB) KeepRemovedConnectionLocal(id string, settings InheritableSettings) error {
	catID, uuid, state, err := d.catalogRowState("connections", id)
	if err != nil {
		return err
	}
	if catID == "" || state != "removed" {
		return ErrNotRemoved
	}
	var folder sql.NullString
	if err := d.conn.QueryRow(`SELECT folder_id FROM connections WHERE id = ?`, id).Scan(&folder); err != nil {
		return err
	}
	ov, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return d.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE connections SET folder_id = NULL, overrides_json = ?, catalog_id = NULL, remote_uuid = NULL,
			remote_rev = 0, catalog_json = '', catalog_state = '', updated_at = ? WHERE id = ?`, string(ov), now(), id); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT remote_uuid FROM port_forwards WHERE connection_id = ? AND remote_uuid IS NOT NULL`, id)
		if err != nil {
			return err
		}
		var fwUUIDs []string
		for rows.Next() {
			var u string
			_ = rows.Scan(&u)
			fwUUIDs = append(fwUUIDs, u)
		}
		rows.Close()
		if _, err := tx.Exec(`UPDATE port_forwards SET catalog_id = NULL, remote_uuid = NULL, remote_rev = 0, catalog_json = '',
			catalog_state = '' WHERE connection_id = ?`, id); err != nil {
			return err
		}
		for _, u := range append(fwUUIDs, uuid) {
			if _, err := tx.Exec(`DELETE FROM catalog_overrides WHERE catalog_id = ? AND remote_uuid = ?`, catID, u); err != nil {
				return err
			}
		}
		return pruneRemovedAncestors(tx, folder.String)
	})
}

// CatalogSlotRevsTx returns uuid -> rev of a catalog's slots.
func (d *DB) CatalogSlotRevsTx(tx *sql.Tx, catalogID string) (map[string]int64, error) {
	rows, err := tx.Query(`SELECT uuid, rev FROM catalog_slots WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var u string
		var r int64
		if err := rows.Scan(&u, &r); err != nil {
			return nil, err
		}
		out[u] = r
	}
	return out, rows.Err()
}

// CatalogFolderDepthTx is the number of ancestors of a folder.
func (d *DB) CatalogFolderDepthTx(tx *sql.Tx, id string) (int, error) {
	depth := 0
	for i := 0; i < 1000; i++ {
		var p sql.NullString
		if err := tx.QueryRow(`SELECT parent_id FROM folders WHERE id = ?`, id).Scan(&p); err != nil || !p.Valid {
			return depth, nil
		}
		depth++
		id = p.String
	}
	return depth, nil
}

// CatalogRowRaw is one catalog row as materialisation reads it: the stored
// server record and the current materialised value (settings_json for a
// folder, overrides_json for a connection, local_port for a forward).
type CatalogRowRaw struct {
	Kind       string
	ID         string
	RemoteUUID string
	RawJSON    string
	Current    string
	LocalPort  *int64
}

// CatalogRowsRawTx lists every catalog row of a catalog (removed ones
// included: they keep resolving until the user decides).
func (d *DB) CatalogRowsRawTx(tx *sql.Tx, catalogID string) ([]CatalogRowRaw, error) {
	var out []CatalogRowRaw
	for _, q := range []struct{ kind, sql string }{
		{"folders", `SELECT id, remote_uuid, catalog_json, settings_json, NULL FROM folders WHERE catalog_id = ? AND remote_uuid IS NOT NULL`},
		{"connections", `SELECT id, remote_uuid, catalog_json, overrides_json, NULL FROM connections WHERE catalog_id = ? AND remote_uuid IS NOT NULL`},
		{"forwards", `SELECT id, remote_uuid, catalog_json, '', local_port FROM port_forwards WHERE catalog_id = ? AND remote_uuid IS NOT NULL`},
	} {
		rows, err := tx.Query(q.sql, catalogID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := CatalogRowRaw{Kind: q.kind}
			var lp sql.NullInt64
			if err := rows.Scan(&r.ID, &r.RemoteUUID, &r.RawJSON, &r.Current, &lp); err != nil {
				rows.Close()
				return nil, err
			}
			if lp.Valid {
				v := lp.Int64
				r.LocalPort = &v
			}
			out = append(out, r)
		}
		rows.Close()
	}
	return out, nil
}

// SetCatalogMaterialTx writes a materialised value (no updated_at bump:
// nothing the user did changed).
func (d *DB) SetCatalogMaterialTx(tx *sql.Tx, kind, id, value string, localPort *uint16) error {
	var err error
	switch kind {
	case "folders":
		_, err = tx.Exec(`UPDATE folders SET settings_json = ? WHERE id = ?`, value, id)
	case "connections":
		_, err = tx.Exec(`UPDATE connections SET overrides_json = ? WHERE id = ?`, value, id)
	case "forwards":
		_, err = tx.Exec(`UPDATE port_forwards SET local_port = ? WHERE id = ?`, nullableU16(localPort), id)
	default:
		err = fmt.Errorf("catalog: unknown kind %q", kind)
	}
	return err
}

// CatalogInputsTx is what materialisation translates with.
type CatalogInputsTx struct {
	Slots       map[string]CatalogSlot
	Bindings    map[string]CatalogBinding
	Overrides   map[string]CatalogOverride
	ConnIDs     map[string]string // remote uuid -> local id
	Credentials map[string]bool
	Profiles    map[string]bool
	Connections map[string]bool
}

// CatalogInputs loads the inputs of one catalog inside tx.
func (d *DB) CatalogInputs(tx *sql.Tx, catalogID string) (*CatalogInputsTx, error) {
	in := &CatalogInputsTx{Slots: map[string]CatalogSlot{}, Bindings: map[string]CatalogBinding{},
		Overrides: map[string]CatalogOverride{}, ConnIDs: map[string]string{}}
	rows, err := tx.Query(`SELECT `+slotCols+` FROM catalog_slots WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s CatalogSlot
		if err := rows.Scan(&s.CatalogID, &s.UUID, &s.Rev, &s.FolderUUID, &s.Type, &s.Name, &s.Kind,
			&s.Backend, &s.ExternalRefJSON, &s.DocMD); err != nil {
			rows.Close()
			return nil, err
		}
		in.Slots[s.UUID] = s
	}
	rows.Close()
	rows, err = tx.Query(`SELECT catalog_id, slot_uuid, kind, target_id, updated_at FROM catalog_bindings WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b CatalogBinding
		if err := rows.Scan(&b.CatalogID, &b.SlotUUID, &b.Kind, &b.TargetID, &b.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		in.Bindings[b.SlotUUID] = b
	}
	rows.Close()
	rows, err = tx.Query(`SELECT catalog_id, remote_uuid, kind, json, updated_at FROM catalog_overrides WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o CatalogOverride
		if err := rows.Scan(&o.CatalogID, &o.RemoteUUID, &o.Kind, &o.JSON, &o.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		in.Overrides[o.RemoteUUID] = o
	}
	rows.Close()
	rows, err = tx.Query(`SELECT remote_uuid, id FROM connections WHERE catalog_id = ? AND remote_uuid IS NOT NULL AND catalog_state = ''`, catalogID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u, id string
		if err := rows.Scan(&u, &id); err != nil {
			rows.Close()
			return nil, err
		}
		in.ConnIDs[u] = id
	}
	rows.Close()
	if in.Credentials, err = idSet(tx, `SELECT id FROM credential_refs`); err != nil {
		return nil, err
	}
	if in.Profiles, err = idSet(tx, `SELECT id FROM network_profiles`); err != nil {
		return nil, err
	}
	if in.Connections, err = idSet(tx, `SELECT id FROM connections`); err != nil {
		return nil, err
	}
	return in, nil
}
