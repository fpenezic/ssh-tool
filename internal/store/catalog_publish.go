package store

import (
	"database/sql"
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
}

// RecordPublishPending records a publish before it is sent.
func (d *DB) RecordPublishPending(catalogID string, rows []PublishPending) error {
	return d.WithTx(func(tx *sql.Tx) error {
		ts := now()
		for _, r := range rows {
			if _, ok := catalogKindTable[r.Kind]; !ok {
				return fmt.Errorf("publish: unknown kind %q", r.Kind)
			}
			if _, err := tx.Exec(`INSERT INTO catalog_publish_pending (catalog_id, kind, remote_uuid, local_id, created_at)
				VALUES (?, ?, ?, ?, ?)`, catalogID, r.Kind, r.RemoteUUID, r.LocalID, ts); err != nil {
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
