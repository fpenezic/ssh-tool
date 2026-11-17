package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Publishing a local subtree to a catalog (see internal/catalog/publish.go).
// The client-generated uuids are recorded against the local rows before
// the request goes out (RecordPublishPending); on success the rows are
// converted in place (ConvertPublishedTx). When the outcome is lost, the
// change feed completes it: an upsert of a uuid recorded here converts the
// local row instead of inserting a copy (the applier asks PendingPublishTx).

// PublishPending is one recorded local row.
type PublishPending struct {
	Kind       string // folders | connections | forwards
	RemoteUUID string
	LocalID    string
	// Extra: what the publisher's side gets when the feed converts the
	// row (PublishPendingExtra as JSON); "" for none.
	Extra string
}

// PublishPendingExtra is a pending row's publisher-side state.
type PublishPendingExtra struct {
	Layer    *CatalogOverride `json:"layer,omitempty"`
	Bindings []CatalogBinding `json:"bindings,omitempty"`
}

// RecordPublishPending records a publish before it is sent.
func (d *DB) RecordPublishPending(catalogID string, rows []PublishPending) error {
	return d.WithTx(func(tx *sql.Tx) error {
		ts := now()
		for _, r := range rows {
			if _, ok := catalogKindTable[r.Kind]; !ok {
				return fmt.Errorf("publish: unknown kind %q", r.Kind)
			}
			if _, err := tx.Exec(`INSERT INTO catalog_publish_pending (catalog_id, kind, remote_uuid, local_id, created_at, extra_json)
				VALUES (?, ?, ?, ?, ?, ?)`, catalogID, r.Kind, r.RemoteUUID, r.LocalID, ts, r.Extra); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropPublishPending forgets a publish that did not happen (refused).
func (d *DB) DropPublishPending(catalogID string, uuids []string) error {
	return d.WithTx(func(tx *sql.Tx) error {
		for _, u := range uuids {
			if _, err := tx.Exec(`DELETE FROM catalog_publish_pending WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, u); err != nil {
				return err
			}
		}
		return nil
	})
}

// PendingPublishForLocal reports whether a local row is part of a publish
// whose outcome is not settled yet.
func (d *DB) PendingPublishForLocal(localIDs []string) (bool, error) {
	for _, id := range localIDs {
		var n int
		if err := d.conn.QueryRow(`SELECT COUNT(*) FROM catalog_publish_pending WHERE local_id = ?`, id).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// PendingPublishTx returns remote uuid -> (kind, local id) of a catalog's
// unsettled publishes.
func (d *DB) PendingPublishTx(tx *sql.Tx, catalogID string) (map[string]PublishPending, error) {
	rows, err := tx.Query(`SELECT kind, remote_uuid, local_id FROM catalog_publish_pending WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PublishPending{}
	for rows.Next() {
		var p PublishPending
		if err := rows.Scan(&p.Kind, &p.RemoteUUID, &p.LocalID); err != nil {
			return nil, err
		}
		out[p.RemoteUUID] = p
	}
	return out, rows.Err()
}

// ApplyPublishPendingExtraTx writes a converted pending row's
// publisher-side state (its layer, the bindings it carries), as a
// completed publish would have.
func (d *DB) ApplyPublishPendingExtraTx(tx *sql.Tx, catalogID, uuid string) error {
	var raw string
	err := tx.QueryRow(`SELECT extra_json FROM catalog_publish_pending WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, uuid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return nil
	}
	if err != nil {
		return err
	}
	var x PublishPendingExtra
	if err := json.Unmarshal([]byte(raw), &x); err != nil {
		return nil // unreadable: the row converts without it
	}
	// What does not check out is left out: it must not stop the sync.
	var bindings []CatalogBinding
	for _, b := range x.Bindings {
		if validBindingKind(b.Kind) && b.SlotUUID != "" && b.TargetID != "" {
			bindings = append(bindings, b)
		}
	}
	var layers []CatalogOverride
	if x.Layer != nil && validOverrideKind(x.Layer.Kind) && x.Layer.RemoteUUID != "" {
		layers = append(layers, *x.Layer)
	}
	return writeBindingsAndLayersTx(tx, catalogID, bindings, layers)
}

// SettlePublishPendingTx removes the pending rows of uuids (converted, or
// known never to have reached the server).
func (d *DB) SettlePublishPendingTx(tx *sql.Tx, catalogID string, uuids []string) error {
	for _, u := range uuids {
		if _, err := tx.Exec(`DELETE FROM catalog_publish_pending WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, u); err != nil {
			return err
		}
	}
	return nil
}

// PublishedRow is one local row the server accepted.
type PublishedRow struct {
	Kind       string
	LocalID    string
	RemoteUUID string
	Rev        int64
	// ParentLocalID: the folder (or connection, for a forward) it hangs
	// under in the catalog tree. The published root's parent is the
	// target catalog folder or the catalog root.
	ParentLocalID string
	RawJSON       string
}

// ConvertPublishedTx turns accepted local rows into catalog rows in place:
// same local ids, catalog columns set, re-linked under their catalog
// parents. Columns the catalog does not own (stored passwords, favourite,
// recency, bookmarks, browser mode, a forward's auto start) are untouched.
// Runs inside a catalog writer transaction.
func (d *DB) ConvertPublishedTx(tx *sql.Tx, catalogID string, rows []PublishedRow) error {
	for _, r := range rows {
		var q string
		switch r.Kind {
		case "folders":
			q = `UPDATE folders SET catalog_id = ?, remote_uuid = ?, remote_rev = ?, catalog_json = ?, catalog_state = '', parent_id = ? WHERE id = ?`
		case "connections":
			q = `UPDATE connections SET catalog_id = ?, remote_uuid = ?, remote_rev = ?, catalog_json = ?, catalog_state = '', folder_id = ? WHERE id = ?`
		case "forwards":
			q = `UPDATE port_forwards SET catalog_id = ?, remote_uuid = ?, remote_rev = ?, catalog_json = ?, catalog_state = '', connection_id = ? WHERE id = ?`
		default:
			return fmt.Errorf("publish: unknown kind %q", r.Kind)
		}
		res, err := tx.Exec(q, catalogID, r.RemoteUUID, r.Rev, r.RawJSON, r.ParentLocalID, r.LocalID)
		if err != nil {
			return fmt.Errorf("publish: convert %s %s: %w", r.Kind, r.LocalID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("publish: the local %s %s is gone", r.Kind, r.LocalID)
		}
		if err := d.SettlePublishPendingTx(tx, catalogID, []string{r.RemoteUUID}); err != nil {
			return err
		}
	}
	return nil
}

// CompletePublishTx is the whole local side of an accepted publish in one
// catalog writer transaction: the rows converted in place, the new slots
// stored, the publisher's own bindings on every slot the publish created
// or reused, and the local layers holding what the catalog does not carry
// for them (a tunnel autostart, a broadcast group).
func (d *DB) CompletePublishTx(tx *sql.Tx, catalogID string, rows []PublishedRow, slots []CatalogSlot,
	bindings []CatalogBinding, layers []CatalogOverride) error {
	if err := d.ConvertPublishedTx(tx, catalogID, rows); err != nil {
		return err
	}
	for _, s := range slots {
		s.CatalogID = catalogID
		if err := d.UpsertCatalogSlotTx(tx, s); err != nil {
			return err
		}
	}
	return writeBindingsAndLayersTx(tx, catalogID, bindings, layers)
}

// CatalogLocalID is the local row of a catalog record ("" when not held).
func (d *DB) CatalogLocalID(catalogID, kind, remoteUUID string) (string, error) {
	t, ok := catalogKindTable[kind]
	if !ok {
		return "", fmt.Errorf("unknown kind %q", kind)
	}
	var id string
	err := d.conn.QueryRow(`SELECT id FROM `+t+` WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, remoteUUID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// CatalogFolderOf is the catalog folder a row sits in (a folder: itself;
// a connection: its folder; a forward: its connection's folder), as
// local id, remote uuid and its stored record.
func (d *DB) CatalogFolderOf(kind, id string) (localID, remoteUUID, raw string, err error) {
	fid := id
	switch kind {
	case "connections":
		err = d.conn.QueryRow(`SELECT COALESCE(folder_id, '') FROM connections WHERE id = ?`, id).Scan(&fid)
	case "forwards":
		err = d.conn.QueryRow(`SELECT COALESCE(c.folder_id, '') FROM port_forwards p JOIN connections c ON c.id = p.connection_id WHERE p.id = ?`, id).Scan(&fid)
	}
	if err != nil {
		return "", "", "", err
	}
	var u sql.NullString
	err = d.conn.QueryRow(`SELECT remote_uuid, COALESCE(catalog_json, '') FROM folders WHERE id = ?`, fid).Scan(&u, &raw)
	return fid, u.String, raw, err
}

// writeBindingsAndLayersTx stores a publish's bindings and the
// publisher's local layers.
func writeBindingsAndLayersTx(tx *sql.Tx, catalogID string, bindings []CatalogBinding, layers []CatalogOverride) error {
	ts := now()
	for _, b := range bindings {
		if !validBindingKind(b.Kind) || b.SlotUUID == "" || b.TargetID == "" {
			return fmt.Errorf("publish: invalid binding %+v", b)
		}
		if _, err := tx.Exec(`INSERT INTO catalog_bindings (catalog_id, slot_uuid, kind, target_id, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (catalog_id, slot_uuid) DO UPDATE SET kind = excluded.kind, target_id = excluded.target_id,
			  updated_at = excluded.updated_at`, catalogID, b.SlotUUID, b.Kind, b.TargetID, ts); err != nil {
			return err
		}
	}
	for _, o := range layers {
		if !validOverrideKind(o.Kind) {
			return fmt.Errorf("publish: invalid layer kind %q", o.Kind)
		}
		if _, err := tx.Exec(`INSERT INTO catalog_overrides (catalog_id, remote_uuid, kind, json, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (catalog_id, remote_uuid) DO UPDATE SET kind = excluded.kind, json = excluded.json,
			  updated_at = excluded.updated_at`, catalogID, o.RemoteUUID, o.Kind, o.JSON, ts); err != nil {
			return err
		}
	}
	return nil
}
