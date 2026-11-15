package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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
	{
		19,
		// A Vaultwarden / Bitwarden server ssh-tool reads secrets from at connect
		// time (the HTTP sibling of keepass_databases). Neither the master
		// password nor the API key is stored here: master_ref points at a hidden
		// sealed vault account (bitwarden:<id>:master), api_key_ref at the vault
		// account behind a normal API-key credential. The synced vault is cached
		// SEALED under DataDir, never here. A credential references an item via
		// config_json.bitwarden_ref {server_id, cipher_id, field}.
		`CREATE TABLE bitwarden_servers (
		    id                 TEXT PRIMARY KEY,
		    name               TEXT NOT NULL UNIQUE,
		    server_url         TEXT NOT NULL,            -- https://vault.example.com
		    api_key_ref        TEXT NOT NULL DEFAULT '', -- vault account behind the API-key credential
		    master_ref         TEXT NOT NULL DEFAULT '', -- vault account holding the master password
		    network_profile_id TEXT NOT NULL DEFAULT '', -- WireGuard profile to dial the server through, empty = direct
		    last_synced_at     INTEGER,                  -- unix seconds of last successful sync
		    last_hash          TEXT NOT NULL DEFAULT '', -- content hash of the last sync payload
		    created_at         INTEGER NOT NULL,
		    updated_at         INTEGER NOT NULL
		);`,
	},
	{
		20,
		// An Infisical server ssh-tool reads secrets from at connect time (the
		// per-request sibling of bitwarden_servers). Infisical decrypts
		// server-side, so there is NO master password and NO synced/cached vault
		// blob here - only the machine-identity API key (a normal api_token
		// credential pointed at by api_key_ref). A credential references a secret
		// via config_json.infisical_ref {server_id, project_id, environment,
		// secret_path, key}.
		`CREATE TABLE infisical_servers (
		    id                 TEXT PRIMARY KEY,
		    name               TEXT NOT NULL UNIQUE,
		    server_url         TEXT NOT NULL,            -- https://infisical.example.com
		    api_key_ref        TEXT NOT NULL DEFAULT '', -- credential id (api_token) holding client_id/secret
		    network_profile_id TEXT NOT NULL DEFAULT '', -- WireGuard profile to dial the server through, empty = direct
		    last_used_at       INTEGER,                  -- unix seconds of last successful read
		    created_at         INTEGER NOT NULL,
		    updated_at         INTEGER NOT NULL
		);`,
	},
	{
		21,
		// Repair migration. bitwarden_servers.network_profile_id was added
		// to migration 19's CREATE TABLE *after* migration 19 had already
		// shipped (the WireGuard-routing feature amended it in place). A DB
		// that ran the original migration 19 therefore has the table WITHOUT
		// the column, and the amended CREATE never re-runs - so every
		// bitwarden_servers read failed with "no such column:
		// network_profile_id". Add it here. The runner treats a
		// duplicate-column error on an ADD COLUMN as a no-op, so DBs created
		// by the amended migration 19 (column already present) are fine too.
		`ALTER TABLE bitwarden_servers ADD COLUMN network_profile_id TEXT NOT NULL DEFAULT '';`,
	},
	{
		22,
		// Built-in icons: a connection/folder/credential/credential-folder can
		// carry a named lucide icon (icon_name) tinted by a palette colour
		// (icon_color) instead of an uploaded image. Both nil = default icon;
		// mutually exclusive with icon_image_id (the app clears one when the
		// other is set). credential_folders never had an icon at all, so they
		// gain both a named icon here (no icon_image_id column - named only).
		// Nullable so absence = "no built-in icon chosen".
		`ALTER TABLE connections ADD COLUMN icon_name TEXT;
		 ALTER TABLE connections ADD COLUMN icon_color TEXT;
		 ALTER TABLE folders ADD COLUMN icon_name TEXT;
		 ALTER TABLE folders ADD COLUMN icon_color TEXT;
		 ALTER TABLE credential_refs ADD COLUMN icon_name TEXT;
		 ALTER TABLE credential_refs ADD COLUMN icon_color TEXT;
		 ALTER TABLE credential_folders ADD COLUMN icon_name TEXT;
		 ALTER TABLE credential_folders ADD COLUMN icon_color TEXT;`,
	},
	{
		23,
		// Repair migration. Migration 22 originally added icon_name/icon_color
		// to connections + folders ONLY; the credential_refs +
		// credential_folders columns were appended to v22's SQL afterwards.
		// A DB that ran the earlier 2-table form of v22 is recorded at
		// version 22 and never re-runs it, so credential reads fail with
		// "no such column: icon_name". Re-add all four icon columns here; the
		// runner adds each statement independently and tolerates a
		// duplicate-column error, so the columns v22 already created are
		// skipped and only the missing credential ones are added. Idempotent
		// on fresh DBs too (every column already exists -> all no-ops).
		`ALTER TABLE connections ADD COLUMN icon_name TEXT;
		 ALTER TABLE connections ADD COLUMN icon_color TEXT;
		 ALTER TABLE folders ADD COLUMN icon_name TEXT;
		 ALTER TABLE folders ADD COLUMN icon_color TEXT;
		 ALTER TABLE credential_refs ADD COLUMN icon_name TEXT;
		 ALTER TABLE credential_refs ADD COLUMN icon_color TEXT;
		 ALTER TABLE credential_folders ADD COLUMN icon_name TEXT;
		 ALTER TABLE credential_folders ADD COLUMN icon_color TEXT;`,
	},
	{
		24,
		// Local-shell connections. `protocol` selects the dial path:
		// "ssh" (default - unchanged behaviour for every existing row) or
		// "local" (spawn a local PTY running the connection's
		// InitialCommand - telnet client, serial console, "claude", ...).
		// `local_shell_kind` picks the shell for a local connection
		// (NULL = auto). Both no-ops for SSH connections.
		`ALTER TABLE connections ADD COLUMN protocol TEXT NOT NULL DEFAULT 'ssh';
		 ALTER TABLE connections ADD COLUMN local_shell_kind TEXT;`,
	},
	{
		25,
		// Connections that should open straight into the background: the
		// session connects and runs as normal, but its tab is left out of
		// the tab bar. For long-lived helpers (a keepalive loop, a tail, a
		// watcher) the tab is pure clutter - the point is that it runs, not
		// that it is looked at. The bar shows a count of hidden tabs, so a
		// backgrounded session is never invisible.
		`ALTER TABLE connections ADD COLUMN open_hidden INTEGER NOT NULL DEFAULT 0;`,
	},
	{
		26,
		// Two independent additions that would otherwise be one migration
		// each in the same release.
		//
		// `local_shell_dir` is the working directory a local connection's
		// shell starts in (NULL/empty = the app-wide `local_shell_dir`
		// setting, which itself falls back to the user's home). Before
		// this the shell inherited the app's own cwd, i.e. the directory
		// the executable lives in.
		//
		// `browser_mode` is per-forward, and only consulted when a
		// bookmark or the browser button opens a URL through it:
		//   ""/"system"  - the user's normal browser, no isolation. The
		//                  default for local forwards, because that is
		//                  what opening localhost:<port> by hand has
		//                  always done, and a fresh profile would lose
		//                  every login and extension the user has.
		//   "isolated"   - a throwaway profile, discarded on close.
		//   "persistent" - a profile kept per forward, so logins survive.
		// Dynamic (SOCKS) forwards keep answering to the app-wide
		// `browser_persistent_profile` setting when this is unset: their
		// isolation is the point of the proxy, not an opt-in.
		`ALTER TABLE connections ADD COLUMN local_shell_dir TEXT;
		 ALTER TABLE port_forwards ADD COLUMN browser_mode TEXT NOT NULL DEFAULT '';`,
	},
	{
		27,
		// Credential folders get an uploaded-image icon like folders,
		// connections and credentials already have (v22 gave them only a
		// built-in icon). Mutually exclusive with icon_name / icon_color,
		// cleared by DeleteImage like the other three references.
		`ALTER TABLE credential_folders ADD COLUMN icon_image_id TEXT REFERENCES images(id);`,
	},
	{
		28,
		// "Sensitive" becomes an inheritable setting inside overrides_json,
		// so a folder can mark everything under it. A connection that had
		// the old column set keeps it as its own override; the column stays
		// (SQLite cannot drop it cheaply) and is written as 0 from now on.
		foldLegacySensitiveSQL,
	},
	{
		29,
		// Catalog client tables and the guard on catalog-owned rows. See
		// catalogSchemaSQL for the reasons.
		catalogSchemaSQL,
	},
	{
		30,
		// Publishing a local folder to a catalog: the client-generated
		// uuids are recorded against the local rows BEFORE the request
		// goes out, so a publish the server accepted but this app never
		// finished converting (crash, lost response) is completed by the
		// next sync, in place, instead of the feed creating a second
		// copy next to the still-local originals.
		`CREATE TABLE catalog_publish_pending (
		    catalog_id  TEXT NOT NULL,
		    kind        TEXT NOT NULL,
		    remote_uuid TEXT NOT NULL,
		    local_id    TEXT NOT NULL,
		    created_at  INTEGER NOT NULL,
		    PRIMARY KEY (catalog_id, remote_uuid)
		);`,
	},
}

// catalogSchemaSQL is migration 29: a shared connection catalog the app
// stays attached to.
//
// Catalog records are materialised as ordinary folders / connections /
// port_forwards rows so the resolver and everything that dials sees plain
// rows with local ids. Each such row carries:
//   - catalog_id: which configured catalog (NULL = a local row)
//   - remote_uuid / remote_rev: the record's identity and revision there
//   - catalog_json: the server record verbatim, for later translation
//   - catalog_state: empty while the catalog holds it, "removed" when it left
//     the catalog but carried the user's local data and was kept
//
// A catalog's top-level folder (its "root") has catalog_id set and
// remote_uuid NULL. The local ids stay separate from the remote uuids, and
// upserts find a row by (catalog_id, remote_uuid), so workspaces, pins and
// jump references that point at a local id survive a resync.
//
// port_forwards.disabled lets a user switch off a catalog forward they
// cannot delete (its local port is taken here, they never use it).
//
// The triggers are the safety net for every write path in the app: a
// catalog-owned column on a catalog row changes only while the catalog
// engine holds the catalog_writer marker (WithCatalogWriter inserts it
// inside its transaction and removes it before commit, so it is never
// visible outside that transaction). Local columns - favourite, open
// hidden, stored passwords, recency, a forward's auto start, disabled,
// bookmarks, browser mode - stay writable. Nothing local may be created
// inside a catalog folder, and a local row cannot be moved into one.
// RAISE messages start with "catalog_owned:" (IsCatalogOwned).
const catalogSchemaSQL = `
ALTER TABLE folders ADD COLUMN catalog_id TEXT;
ALTER TABLE folders ADD COLUMN remote_uuid TEXT;
ALTER TABLE folders ADD COLUMN remote_rev INTEGER NOT NULL DEFAULT 0;
ALTER TABLE folders ADD COLUMN catalog_json TEXT NOT NULL DEFAULT '';
ALTER TABLE folders ADD COLUMN catalog_state TEXT NOT NULL DEFAULT '';
ALTER TABLE connections ADD COLUMN catalog_id TEXT;
ALTER TABLE connections ADD COLUMN remote_uuid TEXT;
ALTER TABLE connections ADD COLUMN remote_rev INTEGER NOT NULL DEFAULT 0;
ALTER TABLE connections ADD COLUMN catalog_json TEXT NOT NULL DEFAULT '';
ALTER TABLE connections ADD COLUMN catalog_state TEXT NOT NULL DEFAULT '';
ALTER TABLE port_forwards ADD COLUMN catalog_id TEXT;
ALTER TABLE port_forwards ADD COLUMN remote_uuid TEXT;
ALTER TABLE port_forwards ADD COLUMN remote_rev INTEGER NOT NULL DEFAULT 0;
ALTER TABLE port_forwards ADD COLUMN catalog_json TEXT NOT NULL DEFAULT '';
ALTER TABLE port_forwards ADD COLUMN catalog_state TEXT NOT NULL DEFAULT '';
ALTER TABLE port_forwards ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX idx_folders_remote ON folders(catalog_id, remote_uuid) WHERE remote_uuid IS NOT NULL;
CREATE UNIQUE INDEX idx_connections_remote ON connections(catalog_id, remote_uuid) WHERE remote_uuid IS NOT NULL;
CREATE UNIQUE INDEX idx_port_forwards_remote ON port_forwards(catalog_id, remote_uuid) WHERE remote_uuid IS NOT NULL;

CREATE TABLE catalogs (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    base_url          TEXT NOT NULL,
    server_name       TEXT NOT NULL DEFAULT '',
    protocol_revision INTEGER NOT NULL DEFAULT 0,
    subject           TEXT NOT NULL DEFAULT '',
    account_name      TEXT NOT NULL DEFAULT '',
    root_folder_id    TEXT NOT NULL,
    cursor            TEXT NOT NULL DEFAULT '',
    last_sync_at      INTEGER,
    last_error        TEXT NOT NULL DEFAULT '',
    sync_interval_sec INTEGER NOT NULL DEFAULT 300,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE TABLE catalog_slots (
    catalog_id        TEXT NOT NULL,
    uuid              TEXT NOT NULL,
    rev               INTEGER NOT NULL DEFAULT 0,
    folder_uuid       TEXT NOT NULL DEFAULT '',
    type              TEXT NOT NULL,
    name              TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL DEFAULT '',
    backend           TEXT NOT NULL DEFAULT '',
    external_ref_json TEXT NOT NULL DEFAULT '',
    doc_md            TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (catalog_id, uuid)
);

CREATE TABLE catalog_bindings (
    catalog_id TEXT NOT NULL,
    slot_uuid  TEXT NOT NULL,
    kind       TEXT NOT NULL,
    target_id  TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (catalog_id, slot_uuid)
);

CREATE TABLE catalog_overrides (
    catalog_id  TEXT NOT NULL,
    remote_uuid TEXT NOT NULL,
    kind        TEXT NOT NULL,
    json        TEXT NOT NULL DEFAULT '{}',
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (catalog_id, remote_uuid)
);

CREATE TABLE catalog_writer (
    id INTEGER PRIMARY KEY CHECK (id = 1)
);

CREATE TRIGGER catalog_guard_folders_insert BEFORE INSERT ON folders
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer)
 AND (NEW.catalog_id IS NOT NULL OR NEW.remote_uuid IS NOT NULL
      OR EXISTS (SELECT 1 FROM folders p WHERE p.id = NEW.parent_id AND p.catalog_id IS NOT NULL))
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: a catalog folder holds only catalog records');
END;

CREATE TRIGGER catalog_guard_folders_update BEFORE UPDATE ON folders
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer)
 AND ((OLD.catalog_id IS NOT NULL
       AND (NEW.parent_id IS NOT OLD.parent_id OR NEW.name IS NOT OLD.name
            OR NEW.sort_order IS NOT OLD.sort_order OR NEW.settings_json IS NOT OLD.settings_json
            OR NEW.icon_image_id IS NOT OLD.icon_image_id OR NEW.icon_name IS NOT OLD.icon_name
            OR NEW.icon_color IS NOT OLD.icon_color))
      OR NEW.catalog_id IS NOT OLD.catalog_id OR NEW.remote_uuid IS NOT OLD.remote_uuid
      OR NEW.remote_rev IS NOT OLD.remote_rev OR NEW.catalog_json IS NOT OLD.catalog_json
      OR NEW.catalog_state IS NOT OLD.catalog_state
      OR (NEW.parent_id IS NOT OLD.parent_id
          AND EXISTS (SELECT 1 FROM folders p WHERE p.id = NEW.parent_id AND p.catalog_id IS NOT NULL)))
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: this folder belongs to a catalog and is read-only');
END;

CREATE TRIGGER catalog_guard_folders_delete BEFORE DELETE ON folders
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer) AND OLD.catalog_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: this folder belongs to a catalog and cannot be deleted here');
END;

CREATE TRIGGER catalog_guard_connections_insert BEFORE INSERT ON connections
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer)
 AND (NEW.catalog_id IS NOT NULL OR NEW.remote_uuid IS NOT NULL
      OR EXISTS (SELECT 1 FROM folders p WHERE p.id = NEW.folder_id AND p.catalog_id IS NOT NULL))
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: a catalog folder holds only catalog records');
END;

CREATE TRIGGER catalog_guard_connections_update BEFORE UPDATE ON connections
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer)
 AND ((OLD.catalog_id IS NOT NULL
       AND (NEW.folder_id IS NOT OLD.folder_id OR NEW.name IS NOT OLD.name
            OR NEW.hostname IS NOT OLD.hostname OR NEW.sort_order IS NOT OLD.sort_order
            OR NEW.overrides_json IS NOT OLD.overrides_json OR NEW.tags_json IS NOT OLD.tags_json
            OR NEW.notes IS NOT OLD.notes OR NEW.protocol IS NOT OLD.protocol
            OR NEW.local_shell_kind IS NOT OLD.local_shell_kind
            OR NEW.local_shell_dir IS NOT OLD.local_shell_dir
            OR NEW.icon_image_id IS NOT OLD.icon_image_id OR NEW.icon_name IS NOT OLD.icon_name
            OR NEW.icon_color IS NOT OLD.icon_color))
      OR NEW.catalog_id IS NOT OLD.catalog_id OR NEW.remote_uuid IS NOT OLD.remote_uuid
      OR NEW.remote_rev IS NOT OLD.remote_rev OR NEW.catalog_json IS NOT OLD.catalog_json
      OR NEW.catalog_state IS NOT OLD.catalog_state
      OR (NEW.folder_id IS NOT OLD.folder_id
          AND EXISTS (SELECT 1 FROM folders p WHERE p.id = NEW.folder_id AND p.catalog_id IS NOT NULL)))
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: this connection belongs to a catalog and is read-only');
END;

CREATE TRIGGER catalog_guard_connections_delete BEFORE DELETE ON connections
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer) AND OLD.catalog_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: this connection belongs to a catalog and cannot be deleted here');
END;

CREATE TRIGGER catalog_guard_port_forwards_insert BEFORE INSERT ON port_forwards
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer)
 AND (NEW.catalog_id IS NOT NULL OR NEW.remote_uuid IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: only the catalog creates catalog forwards');
END;

CREATE TRIGGER catalog_guard_port_forwards_update BEFORE UPDATE ON port_forwards
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer)
 AND ((OLD.remote_uuid IS NOT NULL
       AND (NEW.connection_id IS NOT OLD.connection_id OR NEW.kind IS NOT OLD.kind
            OR NEW.local_addr IS NOT OLD.local_addr OR NEW.local_port IS NOT OLD.local_port
            OR NEW.remote_host IS NOT OLD.remote_host OR NEW.remote_port IS NOT OLD.remote_port
            OR NEW.description IS NOT OLD.description))
      OR NEW.catalog_id IS NOT OLD.catalog_id OR NEW.remote_uuid IS NOT OLD.remote_uuid
      OR NEW.remote_rev IS NOT OLD.remote_rev OR NEW.catalog_json IS NOT OLD.catalog_json
      OR NEW.catalog_state IS NOT OLD.catalog_state)
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: this forward belongs to a catalog; only its local settings can change');
END;

CREATE TRIGGER catalog_guard_port_forwards_delete BEFORE DELETE ON port_forwards
WHEN NOT EXISTS (SELECT 1 FROM catalog_writer) AND OLD.remote_uuid IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'catalog_owned: this forward belongs to a catalog and cannot be deleted here');
END;
`

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

// isDuplicateColumnErr reports whether err is SQLite's "duplicate column
// name" error from an ADD COLUMN that hit an already-present column. Used
// to make idempotent column-add repair migrations no-op on DBs that
// already have the column. Matched on the message text - modernc/sqlite
// surfaces it as "duplicate column name: <col>".
func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// splitSQLStatements splits a migration's SQL into individual statements
// on the semicolon boundary, dropping blank fragments. Line comments
// (`-- ...`) are stripped FIRST so a fragment that is only a trailing
// comment isn't handed to the driver as a bogus statement, and so a
// comment's own text (which may contain words like "per-connection")
// can't be misparsed. Our migration SQL contains no string literals
// with embedded semicolons, so a plain split is otherwise safe.
func splitSQLStatements(sqlText string) []string {
	// Drop `--` line comments.
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	parts := strings.Split(b.String(), ";")
	out := make([]string, 0, len(parts))
	// A CREATE TRIGGER body holds its own semicolons; keep joining
	// fragments until the statement reaches its closing END.
	var trigger strings.Builder
	for _, p := range parts {
		if trigger.Len() > 0 {
			trigger.WriteString(";")
			trigger.WriteString(p)
			if isTriggerEnd(p) {
				out = append(out, trigger.String())
				trigger.Reset()
			}
			continue
		}
		if strings.TrimSpace(p) == "" {
			continue
		}
		if isTriggerStart(p) {
			trigger.WriteString(p)
			continue
		}
		out = append(out, p)
	}
	if trigger.Len() > 0 {
		out = append(out, trigger.String())
	}
	return out
}

func isTriggerStart(stmt string) bool {
	f := strings.Fields(strings.ToUpper(stmt))
	return len(f) >= 2 && f[0] == "CREATE" && f[1] == "TRIGGER"
}

// isTriggerEnd: the fragment after the body's last statement is the bare
// END keyword (the split removed the ";" that follows it).
func isTriggerEnd(fragment string) bool {
	return strings.EqualFold(strings.TrimSpace(fragment), "END")
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
		// Execute statement-by-statement so a duplicate-column error on ONE
		// idempotent ADD COLUMN doesn't abort the sibling statements. This
		// matters for repair migrations (v21, v23) that re-add a set of
		// columns where SOME may already exist on a DB that ran an earlier
		// form of the migration - SQLite has no "ADD COLUMN IF NOT EXISTS",
		// and a single multi-statement Exec would stop at the first
		// duplicate, leaving the rest unapplied. The duplicate is tolerated
		// per statement; any OTHER error is fatal as before.
		for _, stmt := range splitSQLStatements(m.sql) {
			if _, err := tx.Exec(stmt); err != nil && !isDuplicateColumnErr(err) {
				_ = tx.Rollback()
				return fmt.Errorf("apply migration %d: %w", m.version, err)
			}
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

// foldLegacySensitiveSQL moves the old connections.sensitive column into
// the connection's overrides (migration 28). MirrorFrom runs it again after
// a sync pull, since a snapshot from an older version still carries the
// column. Idempotent. An overrides_json that is not a JSON object (empty,
// null, an array) is replaced, so the flag is never dropped.
const foldLegacySensitiveSQL = `UPDATE connections
    SET overrides_json = CASE WHEN NOT json_valid(overrides_json) THEN '{"sensitive":true}'
                              WHEN json_type(overrides_json) = 'object'
                              THEN json_set(overrides_json, '$.sensitive', json('true'))
                              ELSE '{"sensitive":true}' END,
        sensitive = 0
  WHERE sensitive != 0;`
