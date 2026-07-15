package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

// schema migrations are versioned and applied in order; only those above the
// current `schema_meta.version` value run.
var migrations = []struct {
	version int64
	sql     string
}{
	{
		1,
		`
		CREATE TABLE folders (
		    id            TEXT PRIMARY KEY,
		    parent_id     TEXT REFERENCES folders(id) ON DELETE CASCADE,
		    name          TEXT NOT NULL,
		    sort_order    INTEGER NOT NULL DEFAULT 0,
		    settings_json TEXT NOT NULL DEFAULT '{}',
		    created_at    INTEGER NOT NULL,
		    updated_at    INTEGER NOT NULL
		);
		CREATE INDEX idx_folders_parent ON folders(parent_id);

		CREATE TABLE connections (
		    id             TEXT PRIMARY KEY,
		    folder_id      TEXT REFERENCES folders(id) ON DELETE CASCADE,
		    name           TEXT NOT NULL,
		    hostname       TEXT NOT NULL,
		    sort_order     INTEGER NOT NULL DEFAULT 0,
		    overrides_json TEXT NOT NULL DEFAULT '{}',
		    tags_json      TEXT NOT NULL DEFAULT '[]',
		    notes          TEXT NOT NULL DEFAULT '',
		    favorite       INTEGER NOT NULL DEFAULT 0,
		    sensitive      INTEGER NOT NULL DEFAULT 0,
		    last_used_at   INTEGER,
		    created_at     INTEGER NOT NULL,
		    updated_at     INTEGER NOT NULL
		);
		CREATE INDEX idx_connections_folder ON connections(folder_id);
		CREATE INDEX idx_connections_name ON connections(name);

		CREATE TABLE credential_refs (
		    id                      TEXT PRIMARY KEY,
		    name                    TEXT NOT NULL UNIQUE,
		    kind                    TEXT NOT NULL,
		    storage_mode            TEXT NOT NULL DEFAULT 'managed',
		    hint                    TEXT NOT NULL DEFAULT '',
		    tags_json               TEXT NOT NULL DEFAULT '[]',
		    config_json             TEXT NOT NULL DEFAULT '{}',
		    public_key              TEXT,
		    vault_key               TEXT,
		    default_username        TEXT,
		    last_rotated_at         INTEGER,
		    expires_at              INTEGER,
		    rotation_reminder_days  INTEGER,
		    retain_history          INTEGER NOT NULL DEFAULT 0,
		    created_at              INTEGER NOT NULL,
		    updated_at              INTEGER NOT NULL
		);
		CREATE INDEX idx_credential_refs_kind ON credential_refs(kind);

		CREATE TABLE credential_history (
		    id            TEXT PRIMARY KEY,
		    credential_id TEXT NOT NULL REFERENCES credential_refs(id) ON DELETE CASCADE,
		    changed_at    INTEGER NOT NULL,
		    note          TEXT NOT NULL DEFAULT '',
		    rotated_by    TEXT NOT NULL DEFAULT 'user',
		    has_value     INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_credential_history_cred ON credential_history(credential_id, changed_at DESC);

		CREATE TABLE port_forwards (
		    id            TEXT PRIMARY KEY,
		    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
		    kind          TEXT NOT NULL,
		    local_addr    TEXT,
		    local_port    INTEGER,
		    remote_host   TEXT,
		    remote_port   INTEGER,
		    auto_start    INTEGER NOT NULL DEFAULT 0,
		    description   TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE broadcast_groups (
		    id    TEXT PRIMARY KEY,
		    name  TEXT NOT NULL,
		    color TEXT NOT NULL DEFAULT '#ff5555'
		);
		`,
	},
	{
		2,
		`
		CREATE TABLE app_settings (
		    key   TEXT PRIMARY KEY,
		    value TEXT NOT NULL
		);
		`,
	},
	{
		3,
		`CREATE TABLE IF NOT EXISTS known_hosts (
		    id          TEXT PRIMARY KEY NOT NULL DEFAULT (lower(hex(randomblob(16)))),
		    hostname    TEXT NOT NULL,
		    port        INTEGER NOT NULL DEFAULT 22,
		    key_type    TEXT NOT NULL,
		    key_b64     TEXT NOT NULL,
		    fingerprint TEXT NOT NULL,
		    added_at    INTEGER NOT NULL DEFAULT (unixepoch())
		);
		CREATE UNIQUE INDEX IF NOT EXISTS known_hosts_uniq ON known_hosts(hostname, port, key_type);
		`,
	},
	{
		4,
		`CREATE TABLE credential_folders (
		    id         TEXT PRIMARY KEY,
		    parent_id  TEXT REFERENCES credential_folders(id) ON DELETE CASCADE,
		    name       TEXT NOT NULL,
		    sort_order INTEGER NOT NULL DEFAULT 0,
		    created_at INTEGER NOT NULL,
		    updated_at INTEGER NOT NULL
		);
		CREATE INDEX idx_credential_folders_parent ON credential_folders(parent_id);
		ALTER TABLE credential_refs ADD COLUMN folder_id TEXT REFERENCES credential_folders(id) ON DELETE SET NULL;
		CREATE INDEX idx_credential_refs_folder ON credential_refs(folder_id);
		`,
	},
	{
		5,
		`-- Image blobs, content-addressed by md5 so multiple connections /
		-- folders sharing the same icon (common with RDM imports) deduplicate
		-- rather than storing the same PNG 50 times.
		CREATE TABLE images (
		    id         TEXT PRIMARY KEY,
		    md5        TEXT NOT NULL UNIQUE,
		    mime_type  TEXT NOT NULL DEFAULT 'image/png',
		    data       BLOB NOT NULL,
		    created_at INTEGER NOT NULL
		);
		ALTER TABLE folders     ADD COLUMN icon_image_id TEXT REFERENCES images(id);
		ALTER TABLE connections ADD COLUMN icon_image_id TEXT REFERENCES images(id);
		`,
	},
	{
		6,
		`ALTER TABLE credential_refs ADD COLUMN icon_image_id TEXT REFERENCES images(id);`,
	},
	{
		7,
		`ALTER TABLE connections ADD COLUMN password_vault_key TEXT;`,
	},
	{
		8,
		`ALTER TABLE port_forwards ADD COLUMN bookmarks TEXT NOT NULL DEFAULT '[]';`,
	},
	{
		9,
		`-- Snippet library: short command strings the user can fire into
		-- an active terminal. Global by default; per-connection snippets
		-- attach by setting connection_id (cascade-delete with conn).
		CREATE TABLE snippets (
		    id            TEXT PRIMARY KEY,
		    connection_id TEXT REFERENCES connections(id) ON DELETE CASCADE,
		    name          TEXT NOT NULL,
		    body          TEXT NOT NULL,
		    tags_json     TEXT NOT NULL DEFAULT '[]',
		    use_count     INTEGER NOT NULL DEFAULT 0,
		    last_used_at  INTEGER,
		    created_at    INTEGER NOT NULL,
		    updated_at    INTEGER NOT NULL
		);
		CREATE INDEX idx_snippets_conn ON snippets(connection_id);
		CREATE INDEX idx_snippets_last_used ON snippets(last_used_at DESC);
		`,
	},
	{
		10,
		`-- Workspaces: named bundles of "these tabs in this layout".
		-- layout_json holds an array of tab records with embedded pane
		-- trees + group metadata. Restore re-opens every listed
		-- connection and rebuilds the splits as they were.
		CREATE TABLE workspaces (
		    id            TEXT PRIMARY KEY,
		    name          TEXT NOT NULL UNIQUE,
		    layout_json   TEXT NOT NULL DEFAULT '[]',
		    last_opened_at INTEGER,
		    created_at    INTEGER NOT NULL,
		    updated_at    INTEGER NOT NULL
		);
		CREATE INDEX idx_workspaces_last_opened ON workspaces(last_opened_at DESC);
		`,
	},
	{
		11,
		`-- Dynamic inventory: folders backed by an external source
		-- (proxmox, hetzner, …). Sit in the folders tree as a row in
		-- the folders table (so the inherit cascade keeps working);
		-- this side table holds the provider config + last refresh
		-- timestamp. Cached entries live in dynamic_entries below.
		CREATE TABLE dynamic_folders (
		    folder_id        TEXT PRIMARY KEY REFERENCES folders(id) ON DELETE CASCADE,
		    provider         TEXT NOT NULL,
		    config_json      TEXT NOT NULL DEFAULT '{}',
		    refresh_seconds  INTEGER NOT NULL DEFAULT 300,
		    last_pulled_at   INTEGER,
		    last_error       TEXT NOT NULL DEFAULT ''
		);

		-- Cached children pulled from the provider. external_id is
		-- the provider's stable identifier (vmid / containerid /
		-- node name). The row is read-only from the user's POV -
		-- regenerated on every refresh. kind buckets entries into
		-- the "Hosts" / "Guests" pseudo-folders the tree renders.
		CREATE TABLE dynamic_entries (
		    id            TEXT PRIMARY KEY,
		    folder_id     TEXT NOT NULL REFERENCES dynamic_folders(folder_id) ON DELETE CASCADE,
		    external_id   TEXT NOT NULL,
		    name          TEXT NOT NULL,
		    hostname      TEXT NOT NULL,
		    kind          TEXT NOT NULL,
		    status        TEXT NOT NULL DEFAULT '',
		    tags_json     TEXT NOT NULL DEFAULT '[]',
		    raw_json      TEXT NOT NULL DEFAULT '{}',
		    sort_order    INTEGER NOT NULL DEFAULT 0,
		    UNIQUE (folder_id, external_id)
		);
		CREATE INDEX idx_dynamic_entries_folder ON dynamic_entries(folder_id);
		CREATE INDEX idx_dynamic_entries_kind   ON dynamic_entries(folder_id, kind);
		`,
	},
	{
		12,
		// Host-key algorithm pinning. The original schema keyed
		// known_hosts on (hostname, port, key_type), which allowed
		// silent algorithm downgrade: after first-trusting an
		// ed25519 key, an active MITM serving a freshly minted RSA
		// host key would miss the existing row and surface as an
		// ordinary "unknown host" prompt instead of "CHANGED!".
		//
		// We collapse to one row per (hostname, port). If the user
		// somehow accumulated multiple algo rows for the same host
		// (legacy data), we keep the most recently added one - any
		// later connect with a different algo will now flag as
		// changed and require an explicit confirmation.
		`-- Deduplicate: keep newest row per (hostname, port).
		DELETE FROM known_hosts
		WHERE id NOT IN (
		    SELECT id FROM known_hosts kh1
		    WHERE added_at = (
		        SELECT MAX(added_at) FROM known_hosts kh2
		        WHERE kh2.hostname = kh1.hostname AND kh2.port = kh1.port
		    )
		    GROUP BY hostname, port
		);

		-- Replace the index with the (hostname, port) form.
		DROP INDEX IF EXISTS known_hosts_uniq;
		CREATE UNIQUE INDEX known_hosts_uniq ON known_hosts(hostname, port);
		`,
	},
	{
		13,
		// Local audit log: append-only record of sensitive operations
		// (vault unlock/lock/rotate, backup create/restore, SSH
		// connect/disconnect, forward start/stop). Visible in
		// Settings → Audit log and CSV-exportable. metadata_json
		// carries action-specific details (target host, error, …).
		`CREATE TABLE audit_events (
		    id            INTEGER PRIMARY KEY AUTOINCREMENT,
		    ts            INTEGER NOT NULL,
		    action        TEXT NOT NULL,
		    target        TEXT NOT NULL DEFAULT '',
		    metadata_json TEXT NOT NULL DEFAULT '{}'
		);
		CREATE INDEX idx_audit_events_ts ON audit_events(ts DESC);
		CREATE INDEX idx_audit_events_action ON audit_events(action, ts DESC);
		`,
	},
	{
		14,
		// Sealed secret history. Each row records one rotation of a
		// password (or rotated API-token secret) - the previous value
		// is kept under a fresh vault account so it can be retrieved
		// later (paranoid rollback, audit). vault_account points at
		// a vault entry "history:<this row's id>" sealed with the
		// same master key as the live credential. note is a free-form
		// label (e.g. "password rotated"); rotated_by mirrors the
		// existing audit field.
		//
		// Retention is enforced at write time by the application
		// (default keep last 5) - there is no foreign-key cascade
		// from credential_refs because vault account cleanup must be
		// driven by the application anyway (Vault.Delete) and a
		// trigger that called out of the DB would defeat the
		// fail-closed design.
		`CREATE TABLE credential_secret_history (
		    id            TEXT PRIMARY KEY,
		    credential_id TEXT NOT NULL REFERENCES credential_refs(id) ON DELETE CASCADE,
		    rotated_at    INTEGER NOT NULL,
		    vault_account TEXT NOT NULL,
		    note          TEXT NOT NULL DEFAULT '',
		    rotated_by    TEXT NOT NULL DEFAULT 'user'
		);
		CREATE INDEX idx_cred_secret_history_cred ON credential_secret_history(credential_id, rotated_at DESC);
		`,
	},
	{
		15,
		// Pinned dynamic entries. A "pin" promotes a single dynamic
		// inventory entry into a permanent connection: the row in
		// `connections` is the real record (carries user-editable
		// overrides, port forwards, notes, tags); this side-table
		// just remembers which dynamic external_id under which
		// dynamic folder the connection came from, so the next
		// inventory refresh can skip that external_id (otherwise the
		// host would re-appear as a dynamic ghost beside its pinned
		// twin).
		//
		// Unpin is the reverse: delete the connection (cascade nukes
		// this row), the next refresh re-includes the external_id.
		`CREATE TABLE pinned_dynamic_entries (
		    folder_id     TEXT NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
		    external_id   TEXT NOT NULL,
		    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
		    pinned_at     INTEGER NOT NULL,
		    PRIMARY KEY (folder_id, external_id)
		);
		CREATE INDEX idx_pinned_dynamic_conn ON pinned_dynamic_entries(connection_id);
		`,
	},
	{
		16,
		// VNC console support. The RFB password for a generic VNC
		// connection lives in the vault; only the key is stored on the
		// connection row (mirrors password_vault_key). vnc_port and
		// vnc_use_tunnel are inheritable settings carried inside
		// overrides_json, so they need no column.
		`ALTER TABLE connections ADD COLUMN vnc_password_vault_key TEXT;`,
	},
	{
		17,
		// Userspace WireGuard network profiles. config_json is the
		// parsed wg.Profile WITHOUT secrets - the interface private
		// key and per-peer preshared keys live in the vault under
		// wg_private_key:<id> / wg_psk:<id>:<peer_public_key>. The
		// inheritable network_profile_id rides inside overrides_json /
		// folder settings, so connections need no column.
		`CREATE TABLE network_profiles (
		    id          TEXT PRIMARY KEY,
		    name        TEXT NOT NULL UNIQUE,
		    config_json TEXT NOT NULL DEFAULT '{}',
		    created_at  INTEGER NOT NULL,
		    updated_at  INTEGER NOT NULL
		);`,
	},
	{
		18,
		// A KeePass database ssh-tool reads secrets from at connect time. The
		// .kdbx is never written to; only the encrypted file is cached locally
		// (source=local means path IS the live file; remote means path is a
		// cache under DataDir fetched from url). The master password and
		// optional key file live in the vault under the referenced accounts,
		// NEVER here - this row holds only pointers. A credential references an
		// entry via config_json.keepass_ref {db_id, entry_uuid, field}.
		`CREATE TABLE keepass_databases (
		    id              TEXT PRIMARY KEY,
		    name            TEXT NOT NULL UNIQUE,
		    source          TEXT NOT NULL,          -- 'local' | 'webdav' | 'sftp'
		    path            TEXT NOT NULL DEFAULT '', -- local file path, or empty for remote
		    url             TEXT NOT NULL DEFAULT '', -- remote URL / sftp target, empty for local
		    master_ref      TEXT NOT NULL DEFAULT '', -- vault account holding the master password
		    keyfile_ref     TEXT NOT NULL DEFAULT '', -- vault account holding the key file, empty if none
		    remote_cfg_json TEXT NOT NULL DEFAULT '{}', -- source-specific fetch config (host, user, etc.)
		    last_fetched_at INTEGER,                 -- unix seconds of last successful remote fetch
		    last_etag       TEXT NOT NULL DEFAULT '', -- conditional-GET validator for webdav
		    created_at      INTEGER NOT NULL,
		    updated_at      INTEGER NOT NULL
		);`,
	},
}

// LatestSchemaVersion is the version a freshly-migrated DB lands on.
// Exposed so the UI (Settings → About) can display the current
// schema version without hard-coding it.
func LatestSchemaVersion() int64 {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

// SchemaVersion returns the version currently recorded in
// schema_meta, i.e. the highest migration that has run against this
// DB instance. Equals LatestSchemaVersion() after a successful
// Open() under normal conditions.
func (d *DB) SchemaVersion() (int64, error) {
	var versionStr string
	row := d.conn.QueryRow("SELECT value FROM schema_meta WHERE key = 'version'")
	if err := row.Scan(&versionStr); err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
	`); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	var versionStr string
	var current int64
	row := db.QueryRow("SELECT value FROM schema_meta WHERE key = 'version'")
	if err := row.Scan(&versionStr); err == nil {
		if n, perr := strconv.ParseInt(versionStr, 10, 64); perr == nil {
			current = n
		}
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', ?)",
			fmt.Sprintf("%d", m.version),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("bump migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}
