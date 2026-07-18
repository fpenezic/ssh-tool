package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type NewConnection struct {
	FolderID  *string
	Name      string
	Hostname  string
	SortOrder int64
	Overrides InheritableSettings
	Tags      []string
	Notes     string
	// Protocol: "ssh" (default when empty) or "local". LocalShellKind
	// picks the shell for a local connection (nil = auto).
	Protocol       string
	LocalShellKind *string
}

func (d *DB) CreateConnection(in NewConnection) (*Connection, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("validation: connection name is empty")
	}
	id := newID()
	ts := now()
	overrides, err := json.Marshal(in.Overrides)
	if err != nil {
		return nil, err
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, err
	}
	protocol := in.Protocol
	if protocol == "" {
		protocol = "ssh"
	}
	_, err = d.conn.Exec(
		`INSERT INTO connections
		 (id, folder_id, name, hostname, sort_order, overrides_json, tags_json, notes, favorite, sensitive, protocol, local_shell_kind, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)`,
		id, in.FolderID, in.Name, in.Hostname, in.SortOrder,
		string(overrides), string(tagsJSON), in.Notes, protocol, in.LocalShellKind, ts, ts,
	)
	if err != nil {
		return nil, err
	}
	return d.GetConnection(id)
}

func (d *DB) GetConnection(id string) (*Connection, error) {
	row := d.conn.QueryRow(
		`SELECT id, folder_id, name, hostname, sort_order, overrides_json, tags_json,
		        notes, favorite, sensitive, icon_image_id, icon_name, icon_color, last_used_at, created_at, updated_at,
		        password_vault_key, vnc_password_vault_key, protocol, local_shell_kind
		 FROM connections WHERE id = ?`, id,
	)
	return scanConnection(row)
}

func (d *DB) ListConnections(folderID *string) ([]Connection, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if folderID != nil {
		rows, err = d.conn.Query(
			`SELECT id, folder_id, name, hostname, sort_order, overrides_json, tags_json,
			        notes, favorite, sensitive, icon_image_id, icon_name, icon_color, last_used_at, created_at, updated_at,
			        password_vault_key, vnc_password_vault_key, protocol, local_shell_kind
			 FROM connections WHERE folder_id = ? ORDER BY sort_order, name`,
			*folderID,
		)
	} else {
		rows, err = d.conn.Query(
			`SELECT id, folder_id, name, hostname, sort_order, overrides_json, tags_json,
			        notes, favorite, sensitive, icon_image_id, icon_name, icon_color, last_used_at, created_at, updated_at,
			        password_vault_key, vnc_password_vault_key, protocol, local_shell_kind
			 FROM connections ORDER BY sort_order, name`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

type UpdateConnection struct {
	ID          string
	FolderID    *string
	ClearFolder bool
	Name        *string
	Hostname    *string
	SortOrder   *int64
	Overrides   *InheritableSettings
	Tags        *[]string
	Notes       *string
	Favorite    *bool
	Sensitive   *bool
	// Protocol: nil = leave as-is; else "ssh"/"local". LocalShellKind
	// pointer-to-pointer semantics kept simple: nil = leave as-is, a
	// non-nil value (incl. pointer to "") replaces. ClearLocalShellKind
	// forces it back to NULL (auto).
	Protocol            *string
	LocalShellKind      *string
	ClearLocalShellKind bool
}

func (d *DB) UpdateConnection(in UpdateConnection) (*Connection, error) {
	existing, err := d.GetConnection(in.ID)
	if err != nil {
		return nil, err
	}
	newFolder := existing.FolderID
	if in.ClearFolder {
		newFolder = nil
	} else if in.FolderID != nil {
		newFolder = in.FolderID
	}
	newName := existing.Name
	if in.Name != nil {
		newName = *in.Name
	}
	if newName == "" {
		return nil, fmt.Errorf("validation: connection name is empty")
	}
	newHost := existing.Hostname
	if in.Hostname != nil {
		newHost = *in.Hostname
	}
	newSort := existing.SortOrder
	if in.SortOrder != nil {
		newSort = *in.SortOrder
	}
	newOverrides := existing.Overrides
	if in.Overrides != nil {
		newOverrides = *in.Overrides
	}
	newTags := existing.Tags
	if in.Tags != nil {
		newTags = *in.Tags
	}
	newNotes := existing.Notes
	if in.Notes != nil {
		newNotes = *in.Notes
	}
	newFav := existing.Favorite
	if in.Favorite != nil {
		newFav = *in.Favorite
	}
	newSens := existing.Sensitive
	if in.Sensitive != nil {
		newSens = *in.Sensitive
	}
	newProto := existing.Protocol
	if in.Protocol != nil && *in.Protocol != "" {
		newProto = *in.Protocol
	}
	if newProto == "" {
		newProto = "ssh"
	}
	newLocalKind := existing.LocalShellKind
	if in.ClearLocalShellKind {
		newLocalKind = nil
	} else if in.LocalShellKind != nil {
		newLocalKind = in.LocalShellKind
	}

	overridesJSON, err := json.Marshal(newOverrides)
	if err != nil {
		return nil, err
	}
	if newTags == nil {
		newTags = []string{}
	}
	tagsJSON, err := json.Marshal(newTags)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(
		`UPDATE connections SET
		   folder_id=?, name=?, hostname=?, sort_order=?, overrides_json=?, tags_json=?,
		   notes=?, favorite=?, sensitive=?, protocol=?, local_shell_kind=?, updated_at=?
		 WHERE id=?`,
		newFolder, newName, newHost, newSort, string(overridesJSON), string(tagsJSON),
		newNotes, boolToInt(newFav), boolToInt(newSens), newProto, newLocalKind, now(), in.ID,
	)
	if err != nil {
		return nil, err
	}
	return d.GetConnection(in.ID)
}

// BatchOverridePatch describes a batch edit to apply uniformly to many
// connection overrides_json values. For each field, the rule is:
//   - field appears in ClearFields -> overrides field is set to nil (inherit
//     from folder)
//   - field is set on Patch         -> overrides field is overwritten
//   - otherwise                     -> overrides field is left as-is
//
// JumpHost uses the same rule, but "inherit" means *override = nil, while
// "set to none" means *override = {Kind:"none"} (explicit no-jump).
type BatchOverridePatch struct {
	Patch       InheritableSettings
	ClearFields []string // any of: username, port, auth_ref, jump_host, color_tag, keepalive_interval, terminal_type, broadcast_group_id

	// Tag operations applied per row. AddTags is union-merged into
	// each row's existing tag list; RemoveTags is filtered out. Both
	// are case-sensitive and de-duped on the way in. Set to nil to
	// leave tags alone.
	AddTags    []string
	RemoveTags []string
}

func (d *DB) BatchUpdateConnectionOverrides(ids []string, p BatchOverridePatch) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	clear := make(map[string]bool, len(p.ClearFields))
	for _, f := range p.ClearFields {
		clear[f] = true
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ts := now()
	tagOps := len(p.AddTags) > 0 || len(p.RemoveTags) > 0
	updated := 0
	for _, id := range ids {
		var raw, tagsRaw string
		if err := tx.QueryRow(
			`SELECT overrides_json, tags_json FROM connections WHERE id = ?`, id,
		).Scan(&raw, &tagsRaw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return updated, err
		}
		var ov InheritableSettings
		if err := json.Unmarshal([]byte(raw), &ov); err != nil {
			return updated, fmt.Errorf("unmarshal overrides for %s: %w", id, err)
		}

		applyBatchPatch(&ov, p.Patch, clear)

		nextRaw, err := json.Marshal(ov)
		if err != nil {
			return updated, err
		}
		if tagOps {
			var tags []string
			if tagsRaw != "" {
				_ = json.Unmarshal([]byte(tagsRaw), &tags)
			}
			tags = applyTagOps(tags, p.AddTags, p.RemoveTags)
			nextTags, err := json.Marshal(tags)
			if err != nil {
				return updated, err
			}
			if _, err := tx.Exec(
				`UPDATE connections SET overrides_json = ?, tags_json = ?, updated_at = ? WHERE id = ?`,
				string(nextRaw), string(nextTags), ts, id,
			); err != nil {
				return updated, err
			}
		} else {
			if _, err := tx.Exec(
				`UPDATE connections SET overrides_json = ?, updated_at = ? WHERE id = ?`,
				string(nextRaw), ts, id,
			); err != nil {
				return updated, err
			}
		}
		updated++
	}
	if err := tx.Commit(); err != nil {
		return updated, err
	}
	return updated, nil
}

// applyTagOps merges `add` into existing (union, dedup) then filters
// `remove` out. Order preserved by first-appearance. Returns a fresh
// slice - never mutates the input.
func applyTagOps(existing, add, remove []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(add))
	for _, t := range existing {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range add {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(remove) > 0 {
		drop := map[string]bool{}
		for _, t := range remove {
			drop[t] = true
		}
		filtered := out[:0]
		for _, t := range out {
			if !drop[t] {
				filtered = append(filtered, t)
			}
		}
		out = filtered
	}
	return out
}

func applyBatchPatch(ov *InheritableSettings, patch InheritableSettings, clear map[string]bool) {
	if clear["username"] {
		ov.Username = nil
	} else if patch.Username != nil {
		ov.Username = patch.Username
	}
	if clear["port"] {
		ov.Port = nil
	} else if patch.Port != nil {
		ov.Port = patch.Port
	}
	if clear["auth_ref"] {
		ov.AuthRef = nil
	} else if patch.AuthRef != nil {
		ov.AuthRef = patch.AuthRef
	}
	if clear["jump_host"] {
		ov.JumpHost = nil
	} else if patch.JumpHost != nil {
		ov.JumpHost = patch.JumpHost
	}
	if clear["color_tag"] {
		ov.ColorTag = nil
	} else if patch.ColorTag != nil {
		ov.ColorTag = patch.ColorTag
	}
	if clear["broadcast_group_id"] {
		ov.BroadcastGroupID = nil
	} else if patch.BroadcastGroupID != nil {
		ov.BroadcastGroupID = patch.BroadcastGroupID
	}
	if clear["keepalive_interval"] {
		ov.KeepaliveInterval = nil
	} else if patch.KeepaliveInterval != nil {
		ov.KeepaliveInterval = patch.KeepaliveInterval
	}
	if clear["terminal_type"] {
		ov.TerminalType = nil
	} else if patch.TerminalType != nil {
		ov.TerminalType = patch.TerminalType
	}
	if clear["auto_reconnect"] {
		ov.AutoReconnect = nil
	} else if patch.AutoReconnect != nil {
		ov.AutoReconnect = patch.AutoReconnect
	}
	if clear["verbose"] {
		ov.Verbose = nil
	} else if patch.Verbose != nil {
		ov.Verbose = patch.Verbose
	}
}

func (d *DB) DeleteConnection(id string) error {
	res, err := d.conn.Exec("DELETE FROM connections WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) TouchLastUsed(id string) error {
	_, err := d.conn.Exec("UPDATE connections SET last_used_at=? WHERE id=?", now(), id)
	return err
}

// RecentConnections returns connections sorted by last_used_at desc,
// capped at limit. Entries that were never connected (last_used_at IS
// NULL) are excluded.
func (d *DB) RecentConnections(limit int) ([]Connection, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := d.conn.Query(
		`SELECT id, folder_id, name, hostname, sort_order, overrides_json, tags_json,
		        notes, favorite, sensitive, icon_image_id, icon_name, icon_color, last_used_at, created_at, updated_at,
		        password_vault_key, vnc_password_vault_key, protocol, local_shell_kind
		 FROM connections
		 WHERE last_used_at IS NOT NULL
		 ORDER BY last_used_at DESC
		 LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// FavoriteConnections returns all connections flagged favorite,
// alphabetical by name.
func (d *DB) FavoriteConnections() ([]Connection, error) {
	rows, err := d.conn.Query(
		`SELECT id, folder_id, name, hostname, sort_order, overrides_json, tags_json,
		        notes, favorite, sensitive, icon_image_id, icon_name, icon_color, last_used_at, created_at, updated_at,
		        password_vault_key, vnc_password_vault_key, protocol, local_shell_kind
		 FROM connections
		 WHERE favorite = 1
		 ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ---------- scanning ----------

func scanConnection(s scanner) (*Connection, error) {
	var (
		c            Connection
		folderID     sql.NullString
		overridesRaw string
		tagsRaw      string
		notes        string
		fav, sens    int64
		iconID       sql.NullString
		iconName     sql.NullString
		iconColor    sql.NullString
		lastUsed     sql.NullInt64
		passVaultKey sql.NullString
		vncVaultKey  sql.NullString
		protocol     sql.NullString
		localKind    sql.NullString
	)
	err := s.Scan(
		&c.ID, &folderID, &c.Name, &c.Hostname, &c.SortOrder,
		&overridesRaw, &tagsRaw, &notes, &fav, &sens, &iconID, &iconName, &iconColor, &lastUsed,
		&c.CreatedAt, &c.UpdatedAt, &passVaultKey, &vncVaultKey, &protocol, &localKind,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if folderID.Valid {
		c.FolderID = &folderID.String
	}
	if iconID.Valid {
		c.IconImageID = &iconID.String
	}
	if iconName.Valid {
		c.IconName = &iconName.String
	}
	if iconColor.Valid {
		c.IconColor = &iconColor.String
	}
	if passVaultKey.Valid {
		c.PasswordVaultKey = &passVaultKey.String
	}
	if vncVaultKey.Valid {
		c.VncPasswordVaultKey = &vncVaultKey.String
	}
	if protocol.Valid && protocol.String != "" {
		c.Protocol = protocol.String
	} else {
		c.Protocol = "ssh"
	}
	if localKind.Valid {
		c.LocalShellKind = &localKind.String
	}
	if err := json.Unmarshal([]byte(overridesRaw), &c.Overrides); err != nil {
		return nil, fmt.Errorf("unmarshal overrides: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsRaw), &c.Tags); err != nil {
		return nil, fmt.Errorf("unmarshal tags: %w", err)
	}
	c.Notes = notes
	c.Favorite = fav != 0
	c.Sensitive = sens != 0
	if lastUsed.Valid {
		v := lastUsed.Int64
		c.LastUsedAt = &v
	}
	return &c, nil
}

func (d *DB) SetConnectionPasswordKey(id, vaultKey string) error {
	_, err := d.conn.Exec(
		`UPDATE connections SET password_vault_key = ?, updated_at = ? WHERE id = ?`,
		vaultKey, now(), id,
	)
	return err
}

func (d *DB) ClearConnectionPasswordKey(id string) error {
	_, err := d.conn.Exec(
		`UPDATE connections SET password_vault_key = NULL, updated_at = ? WHERE id = ?`,
		now(), id,
	)
	return err
}

func (d *DB) SetConnectionVncPasswordKey(id, vaultKey string) error {
	_, err := d.conn.Exec(
		`UPDATE connections SET vnc_password_vault_key = ?, updated_at = ? WHERE id = ?`,
		vaultKey, now(), id,
	)
	return err
}

func (d *DB) ClearConnectionVncPasswordKey(id string) error {
	_, err := d.conn.Exec(
		`UPDATE connections SET vnc_password_vault_key = NULL, updated_at = ? WHERE id = ?`,
		now(), id,
	)
	return err
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
