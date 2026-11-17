package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Catalog is one attached connection catalog (migration 29). The access
// token is never stored; the refresh token lives in the vault under a key
// scoped to this machine (creds.CatalogTokenAccount).
type Catalog struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	ServerName       string `json:"server_name"`
	ProtocolRevision int64  `json:"protocol_revision"`
	// Subject / AccountName: who signed in last (whoami). A different
	// subject on the next sign-in forces a full resync.
	Subject     string `json:"subject"`
	AccountName string `json:"account_name"`
	// RootFolderID is the catalog's top-level folder in the tree.
	RootFolderID string `json:"root_folder_id"`
	// Cursor is the change-feed cursor, stored verbatim. It lives here,
	// next to the rows it describes, so a profile-sync pull brings rows
	// and cursor over as a consistent pair.
	Cursor          string `json:"-"`
	LastSyncAt      *int64 `json:"last_sync_at"`
	LastError       string `json:"last_error"`
	SyncIntervalSec int64  `json:"sync_interval_sec"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// CatalogSlot is the local copy of a catalog slot: a named placeholder for
// a credential, a network profile or a jump host that each user binds.
type CatalogSlot struct {
	CatalogID       string `json:"catalog_id"`
	UUID            string `json:"uuid"`
	Rev             int64  `json:"rev"`
	FolderUUID      string `json:"folder_uuid"`
	Type            string `json:"type"` // cred | net | jump
	Name            string `json:"name"`
	Kind            string `json:"kind"`    // cred only: slot | external
	Backend         string `json:"backend"` // external only
	ExternalRefJSON string `json:"external_ref_json"`
	DocMD           string `json:"doc_md"`
}

// CatalogBinding ties a slot to something of the user's own: a credential
// id (cred), a network profile id (net), a connection id or inventory
// reference (jump).
type CatalogBinding struct {
	CatalogID string `json:"catalog_id"`
	SlotUUID  string `json:"slot_uuid"`
	Kind      string `json:"kind"`
	TargetID  string `json:"target_id"`
	UpdatedAt int64  `json:"updated_at"`
}

// CatalogOverride is the user's local layer on one catalog record: for a
// folder or connection a sparse InheritableSettings JSON, for a forward
// its local port. Kind is the record kind (folders, connections, forwards).
type CatalogOverride struct {
	CatalogID  string `json:"catalog_id"`
	RemoteUUID string `json:"remote_uuid"`
	Kind       string `json:"kind"`
	JSON       string `json:"json"`
	UpdatedAt  int64  `json:"updated_at"`
}

// IsCatalogOwned reports whether err is the store's refusal to change a
// catalog-owned row outside the catalog engine (the migration 29 triggers).
func IsCatalogOwned(err error) bool {
	return err != nil && strings.Contains(err.Error(), "catalog_owned:")
}

// ErrCatalogOwned is what callers can wrap or compare against when they
// refuse a catalog-owned row before reaching the store.
var ErrCatalogOwned = errors.New("catalog_owned: this record belongs to a catalog and is read-only")

// WithCatalogWriter runs fn in a transaction that may change catalog-owned
// rows. It inserts the catalog_writer marker first and removes it before
// commit, so the marker is never visible outside this transaction; on any
// error the whole transaction, marker included, rolls back. Only the
// catalog engine and the profile mirror use this.
func (d *DB) WithCatalogWriter(fn func(tx *sql.Tx) error) error {
	return d.WithTx(func(tx *sql.Tx) error {
		if err := beginCatalogWrite(tx); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		return endCatalogWrite(tx)
	})
}

func beginCatalogWrite(tx *sql.Tx) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO catalog_writer (id) VALUES (1)`); err != nil {
		return fmt.Errorf("catalog writer: %w", err)
	}
	return nil
}

func endCatalogWrite(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM catalog_writer`); err != nil {
		return fmt.Errorf("catalog writer: %w", err)
	}
	return nil
}

// clearStaleCatalogWriter removes a marker that should never exist outside
// a transaction. Defensive only: run on open, so a bug that committed it
// cannot leave the guard switched off for good.
func clearStaleCatalogWriter(db *sql.DB) {
	_, _ = db.Exec(`DELETE FROM catalog_writer`)
}

// NewCatalog is the create input. The root folder is created with it.
type NewCatalog struct {
	Name             string
	BaseURL          string
	ServerName       string
	ProtocolRevision int64
}

// CreateCatalog adds a catalog and its root folder in one transaction.
func (d *DB) CreateCatalog(in NewCatalog) (*Catalog, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("validation: catalog name is empty")
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return nil, fmt.Errorf("validation: catalog base URL is empty")
	}
	id := newID()
	rootID := newID()
	ts := now()
	err := d.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO catalogs (id, name, base_url, server_name, protocol_revision, root_folder_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, strings.TrimSpace(in.BaseURL), in.ServerName, in.ProtocolRevision, rootID, ts, ts); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO folders (id, parent_id, name, sort_order, settings_json, created_at, updated_at, catalog_id)
			 VALUES (?, NULL, ?, 0, '{}', ?, ?, ?)`,
			rootID, name, ts, ts, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d.GetCatalog(id)
}

const catalogCols = `id, name, base_url, server_name, protocol_revision, subject, account_name,
	root_folder_id, cursor, last_sync_at, last_error, sync_interval_sec, created_at, updated_at`

func scanCatalog(s scanner) (*Catalog, error) {
	var c Catalog
	var last sql.NullInt64
	err := s.Scan(&c.ID, &c.Name, &c.BaseURL, &c.ServerName, &c.ProtocolRevision, &c.Subject, &c.AccountName,
		&c.RootFolderID, &c.Cursor, &last, &c.LastError, &c.SyncIntervalSec, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if last.Valid {
		v := last.Int64
		c.LastSyncAt = &v
	}
	return &c, nil
}

func (d *DB) GetCatalog(id string) (*Catalog, error) {
	return scanCatalog(d.conn.QueryRow(`SELECT `+catalogCols+` FROM catalogs WHERE id = ?`, id))
}

func (d *DB) ListCatalogs() ([]Catalog, error) {
	rows, err := d.conn.Query(`SELECT ` + catalogCols + ` FROM catalogs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Catalog
	for rows.Next() {
		c, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpdateCatalogConfig changes the user-set fields. The root folder follows
// a rename.
func (d *DB) UpdateCatalogConfig(id, name string, syncIntervalSec int64) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("validation: catalog name is empty")
	}
	if syncIntervalSec < 60 {
		syncIntervalSec = 60
	}
	return d.WithCatalogWriter(func(tx *sql.Tx) error {
		var root string
		if err := tx.QueryRow(`SELECT root_folder_id FROM catalogs WHERE id = ?`, id).Scan(&root); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		ts := now()
		if _, err := tx.Exec(`UPDATE catalogs SET name = ?, sync_interval_sec = ?, updated_at = ? WHERE id = ?`,
			name, syncIntervalSec, ts, id); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE folders SET name = ?, updated_at = ? WHERE id = ?`, name, ts, root)
		return err
	})
}

// UpdateCatalogAccount records the signed-in principal and the server's
// identity from /meta. It does not touch updated_at: this is sign-in
// bookkeeping, not a change to the user's profile.
func (d *DB) UpdateCatalogAccount(id, subject, accountName, serverName string, protocolRevision int64) error {
	_, err := d.conn.Exec(
		`UPDATE catalogs SET subject = ?, account_name = ?, server_name = ?, protocol_revision = ? WHERE id = ?`,
		subject, accountName, serverName, protocolRevision, id)
	return err
}

// SetCatalogSyncState records the outcome of a sync pass. An empty cursor
// keeps the stored one (a failed pass leaves it where it was).
func (d *DB) SetCatalogSyncState(id, cursor string, syncedAt *int64, lastError string) error {
	_, err := d.conn.Exec(
		`UPDATE catalogs SET cursor = CASE WHEN ? = '' THEN cursor ELSE ? END,
		        last_sync_at = COALESCE(?, last_sync_at), last_error = ? WHERE id = ?`,
		cursor, cursor, syncedAt, lastError, id)
	return err
}

// ResetCatalogCursor forgets the cursor, so the next pass is a full
// snapshot (manual full refresh, a new principal).
func (d *DB) ResetCatalogCursor(id string) error {
	_, err := d.conn.Exec(`UPDATE catalogs SET cursor = '' WHERE id = ?`, id)
	return err
}

// Slots ----------------------------------------------------------------

const slotCols = `catalog_id, uuid, rev, folder_uuid, type, name, kind, backend, external_ref_json, doc_md`

func (d *DB) ListCatalogSlots(catalogID string) ([]CatalogSlot, error) {
	rows, err := d.conn.Query(`SELECT `+slotCols+` FROM catalog_slots WHERE catalog_id = ? ORDER BY type, name`, catalogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogSlot
	for rows.Next() {
		var s CatalogSlot
		if err := rows.Scan(&s.CatalogID, &s.UUID, &s.Rev, &s.FolderUUID, &s.Type, &s.Name, &s.Kind,
			&s.Backend, &s.ExternalRefJSON, &s.DocMD); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpsertCatalogSlotTx stores a slot from the feed inside the engine's
// transaction.
func (d *DB) UpsertCatalogSlotTx(tx *sql.Tx, s CatalogSlot) error {
	_, err := tx.Exec(
		`INSERT INTO catalog_slots (`+slotCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (catalog_id, uuid) DO UPDATE SET rev = excluded.rev, folder_uuid = excluded.folder_uuid,
		   type = excluded.type, name = excluded.name, kind = excluded.kind, backend = excluded.backend,
		   external_ref_json = excluded.external_ref_json, doc_md = excluded.doc_md`,
		s.CatalogID, s.UUID, s.Rev, s.FolderUUID, s.Type, s.Name, s.Kind, s.Backend, s.ExternalRefJSON, s.DocMD)
	return err
}

func (d *DB) DeleteCatalogSlotTx(tx *sql.Tx, catalogID, uuid string) error {
	_, err := tx.Exec(`DELETE FROM catalog_slots WHERE catalog_id = ? AND uuid = ?`, catalogID, uuid)
	return err
}

// Bindings -------------------------------------------------------------

func (d *DB) ListCatalogBindings(catalogID string) ([]CatalogBinding, error) {
	rows, err := d.conn.Query(
		`SELECT catalog_id, slot_uuid, kind, target_id, updated_at FROM catalog_bindings WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogBinding
	for rows.Next() {
		var b CatalogBinding
		if err := rows.Scan(&b.CatalogID, &b.SlotUUID, &b.Kind, &b.TargetID, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func validBindingKind(k string) bool { return k == "cred" || k == "net" || k == "jump" }

// SetCatalogBinding binds (or rebinds) a slot.
func (d *DB) SetCatalogBinding(catalogID, slotUUID, kind, targetID string) error {
	if !validBindingKind(kind) {
		return fmt.Errorf("validation: binding kind must be cred, net or jump, got %q", kind)
	}
	if catalogID == "" || slotUUID == "" || strings.TrimSpace(targetID) == "" {
		return fmt.Errorf("validation: binding needs a catalog, a slot and a target")
	}
	_, err := d.conn.Exec(
		`INSERT INTO catalog_bindings (catalog_id, slot_uuid, kind, target_id, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (catalog_id, slot_uuid) DO UPDATE SET kind = excluded.kind, target_id = excluded.target_id,
		   updated_at = excluded.updated_at`,
		catalogID, slotUUID, kind, targetID, now())
	return err
}

func (d *DB) DeleteCatalogBinding(catalogID, slotUUID string) error {
	_, err := d.conn.Exec(`DELETE FROM catalog_bindings WHERE catalog_id = ? AND slot_uuid = ?`, catalogID, slotUUID)
	return err
}

// Overrides ------------------------------------------------------------

func validOverrideKind(k string) bool { return k == "folders" || k == "connections" || k == "forwards" }

func (d *DB) ListCatalogOverrides(catalogID string) ([]CatalogOverride, error) {
	rows, err := d.conn.Query(
		`SELECT catalog_id, remote_uuid, kind, json, updated_at FROM catalog_overrides WHERE catalog_id = ?`, catalogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogOverride
	for rows.Next() {
		var o CatalogOverride
		if err := rows.Scan(&o.CatalogID, &o.RemoteUUID, &o.Kind, &o.JSON, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetCatalogOverride stores the local layer of one record. An empty or
// "{}" json deletes the row: no layer is the same as an empty one, and a
// leftover empty row would count as local data and keep a removed record.
func (d *DB) SetCatalogOverride(catalogID, remoteUUID, kind, jsonText string) error {
	if !validOverrideKind(kind) {
		return fmt.Errorf("validation: override kind must be folders, connections or forwards, got %q", kind)
	}
	t := strings.TrimSpace(jsonText)
	if t == "" || t == "{}" || t == "null" {
		return d.DeleteCatalogOverride(catalogID, remoteUUID)
	}
	_, err := d.conn.Exec(
		`INSERT INTO catalog_overrides (catalog_id, remote_uuid, kind, json, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (catalog_id, remote_uuid) DO UPDATE SET kind = excluded.kind, json = excluded.json,
		   updated_at = excluded.updated_at`,
		catalogID, remoteUUID, kind, t, now())
	return err
}

func (d *DB) DeleteCatalogOverride(catalogID, remoteUUID string) error {
	_, err := d.conn.Exec(`DELETE FROM catalog_overrides WHERE catalog_id = ? AND remote_uuid = ?`, catalogID, remoteUUID)
	return err
}
