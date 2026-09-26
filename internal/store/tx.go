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

// SetConnectionNotesTx replaces a connection's notes inside tx.
func (d *DB) SetConnectionNotesTx(tx *sql.Tx, connID, notes string) error {
	res, err := tx.Exec(
		`UPDATE connections SET notes = ?, updated_at = ? WHERE id = ?`,
		notes, now(), connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameConnectionTx changes a connection's name inside tx. Split out from
// UpdateConnection because the plan path needs a single-column write that
// composes with the other statements in the same transaction, and because
// UpdateConnection's Overrides field replaces the whole settings blob -
// which is exactly what an edit that only renames must not do.
func (d *DB) RenameConnectionTx(tx *sql.Tx, connID, name string) error {
	res, err := tx.Exec(
		`UPDATE connections SET name = ?, updated_at = ? WHERE id = ?`,
		name, now(), connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetConnectionHostnameTx changes a connection's hostname inside tx.
func (d *DB) SetConnectionHostnameTx(tx *sql.Tx, connID, hostname string) error {
	res, err := tx.Exec(
		`UPDATE connections SET hostname = ?, updated_at = ? WHERE id = ?`,
		hostname, now(), connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveConnectionTx reparents a connection inside tx. A nil folderID moves it
// to the root.
func (d *DB) MoveConnectionTx(tx *sql.Tx, connID string, folderID *string) error {
	res, err := tx.Exec(
		`UPDATE connections SET folder_id = ?, updated_at = ? WHERE id = ?`,
		folderID, now(), connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameFolderTx changes a folder's name inside tx.
func (d *DB) RenameFolderTx(tx *sql.Tx, folderID, name string) error {
	res, err := tx.Exec(
		`UPDATE folders SET name = ?, updated_at = ? WHERE id = ?`,
		name, now(), folderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PatchConnectionOverridesTx merges a partial settings patch into a
// connection's existing overrides inside tx, rather than replacing the blob.
//
// This is the operation an LLM edit needs and the one the plain
// UpdateConnection cannot express: set is applied field by field, and clear
// lists the fields to remove so they fall back to folder inheritance. A
// rename that also drops a per-connection credential must not silently wipe
// the jump host sitting next to it, which is what writing a fresh
// InheritableSettings would do.
//
// clear accepts the JSON field names of InheritableSettings (username, port,
// auth_ref, jump_host, network_profile_id, initial_command, color_tag,
// keepalive_interval, terminal_type, broadcast_group_id). An unknown name is
// an error rather than a silent no-op - a typo'd field would otherwise look
// like it worked while the setting stayed put.
func (d *DB) PatchConnectionOverridesTx(tx *sql.Tx, connID string, set InheritableSettings, clear []string) error {
	var raw string
	if err := tx.QueryRow(`SELECT overrides_json FROM connections WHERE id = ?`, connID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	// Round-trip through a map so fields this build does not know about
	// survive the edit (a store written by a newer version, or one we simply
	// do not model here).
	cur := map[string]any{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			return fmt.Errorf("connection %s has unreadable overrides_json: %w", connID, err)
		}
	}
	patch := map[string]any{}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &patch); err != nil {
		return err
	}
	for k, v := range patch {
		cur[k] = v
	}
	for _, f := range clear {
		if !ValidOverrideField(f) {
			return fmt.Errorf("unknown settings field %q", f)
		}
		delete(cur, f)
	}
	out, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	res, err := tx.Exec(
		`UPDATE connections SET overrides_json = ?, updated_at = ? WHERE id = ?`,
		string(out), now(), connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ValidOverrideField reports whether name is a settings field an edit may
// clear. Kept as an explicit list so a typo is rejected loudly.
func ValidOverrideField(name string) bool {
	switch name {
	case "username", "port", "auth_ref", "jump_host", "ssh_options", "env_vars",
		"color_tag", "broadcast_group_id", "keepalive_interval", "terminal_type",
		"initial_command", "initial_command_line_delay_ms", "auto_reconnect",
		"verbose", "probe_liveness", "vnc_enabled", "vnc_port", "vnc_use_tunnel",
		"vnc_default", "network_profile_id":
		return true
	}
	return false
}

// SetConnectionNamedIconTx sets a built-in icon + palette colour inside an
// existing transaction. Same shape as SetConnectionNamedIcon, but the MCP plan
// commit writes every row under one transaction: an icon applied outside it
// could survive a rolled-back connection, or vanish while the connection it
// belonged to was kept.
//
// An empty name clears the icon (and the colour with it, since a colour with
// no icon has nothing to paint).
func (d *DB) SetConnectionNamedIconTx(tx *sql.Tx, connID, name, color string) error {
	var n, c interface{}
	if name != "" {
		n = name
		if color != "" {
			c = color
		}
	}
	res, err := tx.Exec(
		`UPDATE connections SET icon_name = ?, icon_color = ?, icon_image_id = NULL, updated_at = ? WHERE id = ?`,
		n, c, now(), connID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// SetConnectionIconTx sets an uploaded image as a connection's icon inside an
// existing transaction, clearing any built-in icon (the two kinds are mutually
// exclusive). Transactional for the same reason as the named variant: the MCP
// plan commit is all-or-nothing.
func (d *DB) SetConnectionIconTx(tx *sql.Tx, connID, imageID string) error {
	var v interface{}
	if imageID != "" {
		v = imageID
	}
	res, err := tx.Exec(
		`UPDATE connections SET icon_image_id = ?, icon_name = NULL, icon_color = NULL, updated_at = ? WHERE id = ?`,
		v, now(), connID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ImageExists reports whether an uploaded image with this id is present.
// Used to reject a stale or invented image id before it reaches a write.
func (d *DB) ImageExists(imageID string) bool {
	if imageID == "" {
		return false
	}
	var one int
	err := d.conn.QueryRow(`SELECT 1 FROM images WHERE id = ?`, imageID).Scan(&one)
	return err == nil
}
