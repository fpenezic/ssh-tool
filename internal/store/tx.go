package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// execer is the subset of *sql.DB / *sql.Tx the create helpers need, so the
// same INSERT can run either standalone (autocommit) or inside a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// WithTx runs fn inside a single transaction, committing on nil error and
// rolling back on any error (or panic). Used by the LLM plan-commit path so a
// batch of folder/connection/forward inserts is all-or-nothing: any failure
// leaves the store untouched. modernc/sqlite is a single writer
// (SetMaxOpenConns(1)), so this serialises naturally against other writers.
func (d *DB) WithTx(fn func(tx *sql.Tx) error) (err error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// insertFolder inserts a folder row via the given execer and returns its new
// id. Shared by CreateFolder (autocommit) and CreateFolderTx.
func insertFolder(x execer, in NewFolder) (string, error) {
	if in.Name == "" {
		return "", fmt.Errorf("validation: folder name is empty")
	}
	id := newID()
	ts := now()
	settingsJSON, err := json.Marshal(in.Settings)
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	_, err = x.Exec(
		`INSERT INTO folders (id, parent_id, name, sort_order, settings_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.ParentID, in.Name, in.SortOrder, string(settingsJSON), ts, ts,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// insertConnection inserts a connection row via the given execer and returns
// its new id. Shared by CreateConnection (autocommit) and CreateConnectionTx.
func insertConnection(x execer, in NewConnection) (string, error) {
	if in.Name == "" {
		return "", fmt.Errorf("validation: connection name is empty")
	}
	id := newID()
	ts := now()
	overrides, err := json.Marshal(in.Overrides)
	if err != nil {
		return "", err
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return "", err
	}
	protocol := in.Protocol
	if protocol == "" {
		protocol = "ssh"
	}
	_, err = x.Exec(
		`INSERT INTO connections
		 (id, folder_id, name, hostname, sort_order, overrides_json, tags_json, notes, favorite, sensitive, protocol, local_shell_kind, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)`,
		id, in.FolderID, in.Name, in.Hostname, in.SortOrder,
		string(overrides), string(tagsJSON), in.Notes, protocol, in.LocalShellKind, ts, ts,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// insertPortForward inserts a forward row via the given execer and returns its
// new id. Shared by CreatePortForward (autocommit) and CreatePortForwardTx.
func insertPortForward(x execer, in NewPortForward) (string, error) {
	if in.Kind != "local" && in.Kind != "remote" && in.Kind != "dynamic" {
		return "", fmt.Errorf("kind must be local|remote|dynamic, got %q", in.Kind)
	}
	if in.ConnectionID == "" {
		return "", fmt.Errorf("connection_id required")
	}
	if in.Kind != "dynamic" && (in.RemoteHost == nil || in.RemotePort == nil) {
		return "", fmt.Errorf("%s forward needs remote_host + remote_port", in.Kind)
	}
	id := newID()
	_, err := x.Exec(
		`INSERT INTO port_forwards
		 (id, connection_id, kind, local_addr, local_port, remote_host, remote_port, auto_start, description)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.ConnectionID, in.Kind,
		nullableStr(in.LocalAddr), nullableU16(in.LocalPort),
		nullableStr(in.RemoteHost), nullableU16(in.RemotePort),
		boolToInt(in.AutoStart), in.Description,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// setForwardBookmarks writes the bookmarks JSON for a dynamic forward via the
// given execer. Shared by SetPortForwardBookmarks and the Tx variant.
func setForwardBookmarks(x execer, forwardID string, bookmarks []ProxyBookmark) error {
	if bookmarks == nil {
		bookmarks = []ProxyBookmark{}
	}
	b, err := json.Marshal(bookmarks)
	if err != nil {
		return err
	}
	_, err = x.Exec(`UPDATE port_forwards SET bookmarks = ? WHERE id = ?`,
		string(b), forwardID)
	return err
}

// CreateFolderTx creates a folder inside tx, returning the new id.
func (d *DB) CreateFolderTx(tx *sql.Tx, in NewFolder) (string, error) {
	return insertFolder(tx, in)
}

// CreateConnectionTx creates a connection inside tx, returning the new id.
func (d *DB) CreateConnectionTx(tx *sql.Tx, in NewConnection) (string, error) {
	return insertConnection(tx, in)
}

// CreatePortForwardTx creates a port forward inside tx, returning the new id.
func (d *DB) CreatePortForwardTx(tx *sql.Tx, in NewPortForward) (string, error) {
	return insertPortForward(tx, in)
}

// SetPortForwardBookmarksTx sets a forward's bookmarks inside tx.
func (d *DB) SetPortForwardBookmarksTx(tx *sql.Tx, forwardID string, bookmarks []ProxyBookmark) error {
	return setForwardBookmarks(tx, forwardID, bookmarks)
}

// UpdateFolderSettingsTx replaces the inheritable settings JSON of an existing
// folder inside tx, leaving parent/name/sort untouched. Used by the LLM plan
// commit to set folder defaults (jump host, credential, network profile) that
// its connections inherit. Returns ErrNotFound if the folder is gone.
func (d *DB) UpdateFolderSettingsTx(tx *sql.Tx, folderID string, settings InheritableSettings) error {
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	res, err := tx.Exec(
		`UPDATE folders SET settings_json = ?, updated_at = ? WHERE id = ?`,
		string(b), now(), folderID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
