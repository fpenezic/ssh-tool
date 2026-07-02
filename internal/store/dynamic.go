package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// DynamicFolder is the side-table row for a folder driven by an
// external provider. The base row in `folders` carries the inherit
// settings (port, username, jump host, credential); this struct only
// adds the provider plumbing.
type DynamicFolder struct {
	FolderID       string         `json:"folder_id"`
	Provider       string         `json:"provider"`
	Config         map[string]any `json:"config"`
	RefreshSeconds int            `json:"refresh_seconds"`
	LastPulledAt   *int64         `json:"last_pulled_at"`
	LastError      string         `json:"last_error"`
}

// DynamicEntry is a single cached child of a dynamic folder. Created
// by the refresh path, never edited by the user.
type DynamicEntry struct {
	ID         string          `json:"id"`
	FolderID   string          `json:"folder_id"`
	ExternalID string          `json:"external_id"`
	Name       string          `json:"name"`
	Hostname   string          `json:"hostname"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	Tags       []string        `json:"tags"`
	Raw        json.RawMessage `json:"raw"`
	SortOrder  int             `json:"sort_order"`
}

// CreateDynamicFolder inserts a `dynamic_folders` row alongside the
// caller-created `folders` row. Caller is expected to have already
// inserted the base folder.
func (d *DB) CreateDynamicFolder(in DynamicFolder) error {
	cfg, _ := json.Marshal(in.Config)
	_, err := d.conn.Exec(`
		INSERT INTO dynamic_folders (folder_id, provider, config_json, refresh_seconds, last_error)
		VALUES (?, ?, ?, ?, ?)`,
		in.FolderID, in.Provider, string(cfg),
		in.RefreshSeconds, in.LastError,
	)
	return err
}

func (d *DB) UpdateDynamicFolder(in DynamicFolder) error {
	cfg, _ := json.Marshal(in.Config)
	_, err := d.conn.Exec(`
		UPDATE dynamic_folders
		SET provider = ?, config_json = ?, refresh_seconds = ?
		WHERE folder_id = ?`,
		in.Provider, string(cfg), in.RefreshSeconds, in.FolderID,
	)
	return err
}

func (d *DB) GetDynamicFolder(folderID string) (*DynamicFolder, error) {
	row := d.conn.QueryRow(`
		SELECT folder_id, provider, config_json, refresh_seconds, last_pulled_at, last_error
		FROM dynamic_folders WHERE folder_id = ?`, folderID)
	var f DynamicFolder
	var cfg string
	var lastPulled sql.NullInt64
	if err := row.Scan(&f.FolderID, &f.Provider, &cfg, &f.RefreshSeconds, &lastPulled, &f.LastError); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if cfg != "" {
		_ = json.Unmarshal([]byte(cfg), &f.Config)
	}
	if lastPulled.Valid {
		v := lastPulled.Int64
		f.LastPulledAt = &v
	}
	return &f, nil
}

func (d *DB) ListDynamicFolders() ([]DynamicFolder, error) {
	rows, err := d.conn.Query(`
		SELECT folder_id, provider, config_json, refresh_seconds, last_pulled_at, last_error
		FROM dynamic_folders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DynamicFolder{}
	for rows.Next() {
		var f DynamicFolder
		var cfg string
		var lastPulled sql.NullInt64
		if err := rows.Scan(&f.FolderID, &f.Provider, &cfg, &f.RefreshSeconds, &lastPulled, &f.LastError); err != nil {
			return nil, err
		}
		if cfg != "" {
			_ = json.Unmarshal([]byte(cfg), &f.Config)
		}
		if lastPulled.Valid {
			v := lastPulled.Int64
			f.LastPulledAt = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReplaceDynamicEntries swaps the cached children for a folder in one
// transaction: delete-all then insert-all. Caller passes the fresh
// list straight from the provider. last_pulled_at is bumped to now
// and last_error cleared.
func (d *DB) ReplaceDynamicEntries(folderID string, entries []DynamicEntry) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM dynamic_entries WHERE folder_id = ?`, folderID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO dynamic_entries
			(id, folder_id, external_id, name, hostname, kind, status, tags_json, raw_json, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, e := range entries {
		tags, _ := json.Marshal(e.Tags)
		raw := string(e.Raw)
		if raw == "" {
			raw = "{}"
		}
		if _, err := stmt.Exec(e.ID, folderID, e.ExternalID, e.Name, e.Hostname,
			e.Kind, e.Status, string(tags), raw, i); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		UPDATE dynamic_folders
		SET last_pulled_at = ?, last_error = ''
		WHERE folder_id = ?`, time.Now().Unix(), folderID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDynamicFolderError records a failed refresh attempt. Doesn't
// touch entries - last successful refresh stays in place so the user
// keeps a usable list during transient outages.
func (d *DB) SetDynamicFolderError(folderID, errMsg string) error {
	_, err := d.conn.Exec(`UPDATE dynamic_folders SET last_error = ? WHERE folder_id = ?`,
		errMsg, folderID)
	return err
}

// ListDynamicEntries returns every cached entry for a folder ordered
// by sort_order (i.e. provider order).
func (d *DB) ListDynamicEntries(folderID string) ([]DynamicEntry, error) {
	rows, err := d.conn.Query(`
		SELECT id, folder_id, external_id, name, hostname, kind, status, tags_json, raw_json, sort_order
		FROM dynamic_entries WHERE folder_id = ? ORDER BY sort_order`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DynamicEntry{}
	for rows.Next() {
		var e DynamicEntry
		var tags, raw string
		if err := rows.Scan(&e.ID, &e.FolderID, &e.ExternalID, &e.Name, &e.Hostname,
			&e.Kind, &e.Status, &tags, &raw, &e.SortOrder); err != nil {
			return nil, err
		}
		if tags != "" {
			_ = json.Unmarshal([]byte(tags), &e.Tags)
		}
		if raw != "" {
			e.Raw = []byte(raw)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetDynamicEntry resolves a single entry by id.
func (d *DB) GetDynamicEntry(entryID string) (*DynamicEntry, error) {
	row := d.conn.QueryRow(`
		SELECT id, folder_id, external_id, name, hostname, kind, status, tags_json, raw_json, sort_order
		FROM dynamic_entries WHERE id = ?`, entryID)
	var e DynamicEntry
	var tags, raw string
	if err := row.Scan(&e.ID, &e.FolderID, &e.ExternalID, &e.Name, &e.Hostname,
		&e.Kind, &e.Status, &tags, &raw, &e.SortOrder); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if tags != "" {
		_ = json.Unmarshal([]byte(tags), &e.Tags)
	}
	if raw != "" {
		e.Raw = []byte(raw)
	}
	return &e, nil
}

// IsDynamicFolder reports whether the given folder id has a
// `dynamic_folders` row backing it.
func (d *DB) IsDynamicFolder(folderID string) (bool, error) {
	var n int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM dynamic_folders WHERE folder_id = ?`, folderID).Scan(&n); err != nil {
		return false, fmt.Errorf("count dynamic_folders: %w", err)
	}
	return n > 0, nil
}

// DeleteDynamicFolder removes the side-table row (and cached entries via
// FK cascade); leaves the base `folders` row in place so a former
// dynamic folder can survive as a regular folder.
func (d *DB) DeleteDynamicFolder(folderID string) error {
	if _, err := d.conn.Exec(`DELETE FROM dynamic_entries WHERE folder_id = ?`, folderID); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM dynamic_folders WHERE folder_id = ?`, folderID); err != nil {
		return err
	}
	return nil
}

// PinnedDynamicEntry is a row from `pinned_dynamic_entries`. The
// `connection_id` is the real connection that replaced the dynamic
// ghost. `external_id` is the provider-side identifier (Proxmox vmid,
// Hetzner server id, Ansible host name, etc).
type PinnedDynamicEntry struct {
	FolderID     string `json:"folder_id"`
	ExternalID   string `json:"external_id"`
	ConnectionID string `json:"connection_id"`
	PinnedAt     int64  `json:"pinned_at"`
}

// AddPinnedDynamicEntry inserts a pin mapping.
func (d *DB) AddPinnedDynamicEntry(p PinnedDynamicEntry) error {
	if p.PinnedAt == 0 {
		p.PinnedAt = time.Now().Unix()
	}
	_, err := d.conn.Exec(`
		INSERT INTO pinned_dynamic_entries (folder_id, external_id, connection_id, pinned_at)
		VALUES (?, ?, ?, ?)`,
		p.FolderID, p.ExternalID, p.ConnectionID, p.PinnedAt)
	return err
}

// ListPinnedExternalIDs returns the set of pinned external IDs for a folder.
func (d *DB) ListPinnedExternalIDs(folderID string) (map[string]struct{}, error) {
	rows, err := d.conn.Query(`SELECT external_id FROM pinned_dynamic_entries WHERE folder_id = ?`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = struct{}{}
	}
	return out, rows.Err()
}

// GetPinForConnection returns the pin row for a connection if one
// exists, else (nil, nil).
func (d *DB) GetPinForConnection(connID string) (*PinnedDynamicEntry, error) {
	row := d.conn.QueryRow(`
		SELECT folder_id, external_id, connection_id, pinned_at
		FROM pinned_dynamic_entries WHERE connection_id = ?`, connID)
	var p PinnedDynamicEntry
	if err := row.Scan(&p.FolderID, &p.ExternalID, &p.ConnectionID, &p.PinnedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// DeletePinByConnection removes the pin (caller deletes the connection
// row separately, or the FK cascade handles it when the connection is
// deleted).
func (d *DB) DeletePinByConnection(connID string) error {
	_, err := d.conn.Exec(`DELETE FROM pinned_dynamic_entries WHERE connection_id = ?`, connID)
	return err
}

// DeletePinsByFolder drops all pin rows for a folder. Used by the
// "convert dynamic folder to static" path which dismantles the source
// of truth entirely.
func (d *DB) DeletePinsByFolder(folderID string) error {
	_, err := d.conn.Exec(`DELETE FROM pinned_dynamic_entries WHERE folder_id = ?`, folderID)
	return err
}
