# Data Model

The DB lives at `DataDir/ssh-tool.db` (SQLite via `modernc.org/sqlite`,
WAL mode, foreign keys on). Schema is versioned in `schema_meta.value`
and migrated in order on every startup. Current head: **v17**. The audit log lives in a separate audit.db (machine-local, not in this schema).

The canonical migration source is `internal/store/migrations.go`. This
doc summarises the current shape and lists each migration's purpose.

## Tables

### `folders` (v1)
Tree of folders. `parent_id NULL` = root. `settings_json` holds
inherited per-folder settings (credentials, jump host, SSH options).

```sql
CREATE TABLE folders (
    id            TEXT PRIMARY KEY,
    parent_id     TEXT REFERENCES folders(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    settings_json TEXT NOT NULL DEFAULT '{}',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    icon_image_id TEXT REFERENCES images(id)      -- v5
);
```

### `connections` (v1)
Leaves in the tree. `overrides_json` holds override-only fields;
inherited fields come from the folder cascade via `resolver`.

```sql
CREATE TABLE connections (
    id                  TEXT PRIMARY KEY,
    folder_id           TEXT REFERENCES folders(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    hostname            TEXT NOT NULL,
    sort_order          INTEGER NOT NULL DEFAULT 0,
    overrides_json      TEXT NOT NULL DEFAULT '{}',
    tags_json           TEXT NOT NULL DEFAULT '[]',
    notes               TEXT NOT NULL DEFAULT '',
    favorite            INTEGER NOT NULL DEFAULT 0,
    sensitive           INTEGER NOT NULL DEFAULT 0,
    last_used_at        INTEGER,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    icon_image_id       TEXT REFERENCES images(id),  -- v5
    password_vault_key  TEXT                          -- v7
);
```

### `credential_refs` (v1)
Pointer rows. The actual secret lives in the encrypted vault keyed by
`vault_key`, or on disk (for `storage_mode='file_ref'`), or in an
external system (`agent` / `opkssh`). Supported `kind` values:
`password`, `key`, `agent`, `opkssh`, `api_token`, `vault`.

```sql
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
    updated_at              INTEGER NOT NULL,
    folder_id               TEXT REFERENCES credential_folders(id) ON DELETE SET NULL,  -- v4
    icon_image_id           TEXT REFERENCES images(id)                                  -- v6
);
```

### `credential_history` (v1)
Audit log for credential changes. `has_value=1` means the previous
secret is preserved in the vault under `:hist:<id>` for rollback
(opt-in via `credential_refs.retain_history`).

### `credential_folders` (v4)
Folder tree for credentials, parallel to `folders` for connections.

### `known_hosts` (v3)
TOFU host key store. Unique on `(hostname, port, key_type)`. Migration
3 also added the host-key challenge / response flow in the SSH layer.

### `port_forwards` (v1)
Per-connection forward definitions. `bookmarks` (v8) is a JSON array
of named bookmarks for dynamic SOCKS forwards (URLs the user opened
via that forward).

### `app_settings` (v2)
Key/value bag for user preferences. Used for terminal font/theme,
density, base font size, connect timeout, vault auto-lock minutes,
active-settings-section, broadcast group state, etc.

### `images` (v5)
Content-addressed (md5) image blob store. Connections, folders, and
credentials reference an image via `icon_image_id`. The dedup is
load-bearing for RDM imports where many entries share the same icon.

### `snippets` (v9)
Short command strings the user fires into an active terminal via
Ctrl+Shift+P. Global by default (`connection_id NULL`); per-connection
snippets attach by setting `connection_id` and cascade-delete with
the parent conn.

### `workspaces` (v10)
Named bundles of "these tabs in this layout". `layout_json` is an
array of tab records with embedded pane trees and group metadata.
Opening a workspace re-uses existing sessions when their
`connection_ids` overlap.

### `dynamic_folders` (v11) + `dynamic_entries` (v11)
Side tables alongside `folders` for inventory backed by an external
provider. The `folders` row holds the inherit cascade (credentials,
jump host, SSH options). `dynamic_folders` adds provider config +
refresh state. `dynamic_entries` is the read-only cache, regenerated
on every refresh.

```sql
CREATE TABLE dynamic_folders (
    folder_id        TEXT PRIMARY KEY REFERENCES folders(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,            -- 'proxmox' | 'hetzner'
    config_json      TEXT NOT NULL DEFAULT '{}',
    refresh_seconds  INTEGER NOT NULL DEFAULT 300,
    last_pulled_at   INTEGER,
    last_error       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE dynamic_entries (
    id            TEXT PRIMARY KEY,
    folder_id     TEXT NOT NULL REFERENCES dynamic_folders(folder_id) ON DELETE CASCADE,
    external_id   TEXT NOT NULL,
    name          TEXT NOT NULL,
    hostname      TEXT NOT NULL,
    kind          TEXT NOT NULL,               -- 'host' | 'guest_vm' | 'guest_lxc'
    status        TEXT NOT NULL DEFAULT '',
    tags_json     TEXT NOT NULL DEFAULT '[]',
    raw_json      TEXT NOT NULL DEFAULT '{}',
    sort_order    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (folder_id, external_id)
);
```

`SshConnectDynamic` doesn't create a `connections` row - it constructs
an ephemeral `store.Connection` in memory from the cached entry +
inherited folder settings and feeds it into the standard SSH layer.

### `broadcast_groups` (v1)
Defined but no longer load-bearing. Broadcast state lives in-memory
in `BroadcastManager`. The table sticks around for migration safety.

## Inheritance

`resolver.Resolve(connectionID)` walks the folder cascade from root
down, merging `settings_json` at each level, then layers
`connections.overrides_json` on top. The result is `ResolvedSettings`
(see `internal/resolver/resolver.go`) and is what the SSH layer
consumes. The dynamic-inventory path uses the same cascade but with a
synthetic Connection.

`overrides_json` keys override the inherited value when present (even
if the value is empty); inherited values are used when the key is
absent. Lists (tags, port forwards) accumulate down the cascade.

`ResolvedSettings.PasswordOverride` is special-cased: `json:"-"` so
it never appears in the resolved-settings preview or any API response
- it's pulled fresh from the vault inside the SSH layer.

## Vault

The vault file lives at `DataDir/vault.sealed`, 0600.

- KDF: Argon2id (m=19MiB, t=2, p=1). Threat model is local file
  access, not offline brute force at scale.
- AEAD: XChaCha20-Poly1305.
- Optional machine-bound auto-unlock: the master key is wrapped with
  a machine-derived secret stored in the OS keychain. Disable to
  require passphrase on every launch.

Vault keys for various features:
- `conn_pass:{connectionID}` - per-connection password override (v7).
- `cred:{credentialID}` - credential secret (password / key PEM /
  opkssh key+cert / API token).
- `cred:{credentialID}:hist:{historyID}` - retained previous value
  (when `retain_history=1`).

## Schema migration history

| Version | Added |
|---|---|
| 1 | folders, connections, credential_refs, credential_history, port_forwards, broadcast_groups |
| 2 | app_settings |
| 3 | known_hosts (TOFU) |
| 4 | credential_folders + folder_id FK on credential_refs |
| 5 | images table + icon_image_id on folders/connections |
| 6 | icon_image_id on credential_refs |
| 7 | password_vault_key on connections |
| 8 | bookmarks column on port_forwards |
| 9 | snippets |
| 10 | workspaces |
| 11 | dynamic_folders + dynamic_entries |
| 12 | known_hosts uniqueness widened (hostname, port) - supports MITM-resistant per-algo TOFU |
| 13 | audit_events (append-only security log; ts/action/target/metadata_json) |
| 14 | credential_secret_history (sealed previous values keyed by vault_account) |
| 15 | pinned_dynamic_entries (dynamic-inventory host → permanent connection mapping) |
| 16 | vnc_password_vault_key on connections (per-connection VNC/RFB password key) |
| 17 | network_profiles (WireGuard + NetBird overlay profiles; secretless config, secrets in vault) |

Migration runner: `runMigrations` in `internal/store/migrations.go`.
Each migration applies inside a transaction; failure rolls back and
the app refuses to start. UI surfaces the live schema version in
Settings → About via `AppVersion().schema_version`.

## Sync and schema evolution (cross-version compatibility)

Sync ships the whole `store.db` between machines that may run different
app versions. A newer version can push data an older version doesn't
understand, so every schema change must stay backward- AND
forward-compatible. The rules that keep this safe:

1. **New inheritable settings go in `overrides_json`, never a new
   column.** It's free-form JSON: an older app preserves unknown keys
   verbatim through a pull (Go `json.Unmarshal` ignores unknown fields,
   and the mirror copies the whole blob, not field-by-field). VNC's
   `vnc_enabled` / `vnc_port` / `vnc_use_tunnel` follow this.

2. **A genuinely new column must be nullable with a default.** Never
   `NOT NULL` without a default - an older snapshot has no value for it,
   and a newer snapshot pulled by an older app must be droppable.

3. **`MirrorFrom` (live pull) intersects source and destination
   columns.** `copyTable` copies only columns present on BOTH the pulled
   snapshot and the running DB. A newer column is dropped when an older
   app pulls; an older snapshot leaves a newer app's extra column at its
   default. See `internal/store/mirror.go`. (Before this, a live pull of
   a newer-schema snapshot by an older app hit "no such column".)

4. **The staged whole-file restore is forward-tolerant by nature.** It
   swaps `store.db` wholesale then runs migrations: an older app sees a
   higher `schema_meta.version`, no-ops the migrations it lacks (no
   downgrade), and ignores unknown columns because every read SELECTs
   explicit columns, never `SELECT *` on a profile table.

5. **Never rename or drop a synced column while two versions coexist.**
   Only add. A rename looks like "drop + add" to the other side and
   loses data.

The generation counter (optimistic concurrency) guards concurrent
writes; the above guards schema skew. They're independent.

### `audit_events` (v13)

Append-only local audit log. Every security-relevant operation
fans in via `recordAudit` (app.go):

- `vault.unlock`, `vault.lock`, `vault.passphrase_rotate`
- `backup.create`, `backup.restore`
- `ssh.connect`, `ssh.disconnect` (with host/port/user metadata)
- `ssh.connect.dynamic` (folder + entry name)
- `dynamic.pin`, `dynamic.unpin`, `dynamic.convert`
- `credential.history.reveal`, `credential.history.delete`
- `forward.start`, `forward.stop`

`metadata_json` is a free-form key/value map per action - no
secret material ever lands here. UI: Settings → Audit log with
text filter, sort, CSV export, retention slider.

### `credential_secret_history` (v14)

Append-only per-credential snapshot of previous secret values.
Rotation flows (`RotatePassword`, `RotateAPIToken` secret path)
read the current vault value, seal it under a fresh vault
account (`credhist:<random hex>`), insert a row, then overwrite
the live entry. Retention is enforced at write time - anything
older than the keep-last-N (currently hard-coded to 5; slider
follow-up) gets pruned from both DB and vault. On credential
delete, every history entry's vault account is purged alongside
the live one.

| Column | Type | Notes |
|---|---|---|
| id | text PK | snapshot uuid |
| credential_id | text FK → credential_refs ON DELETE CASCADE | |
| rotated_at | int64 | unix seconds |
| vault_account | text | "credhist:..." - used by Reveal |
| note | text | e.g. "password rotated" |
| rotated_by | text | "user" today; future "policy" / "import" |

Plaintext stays in the vault; the UI calls
`CredentialsRevealSecretHistory(historyId)` on demand and
applies the same 30-second clipboard auto-clear as the live
reveal.

### `pinned_dynamic_entries` (v15)

Maps a single dynamic-inventory host (by its provider-side
external_id under a dynamic folder) onto a real connection row.
A "pin" promotes one Proxmox VM / Hetzner server / Ansible host
into a permanent connection that survives provider outages, can
hold per-host overrides and port forwards, and accumulates audit
history like any other connection.

Refresh path (`internal/inventory/manager.Refresh`) filters out
entries whose `external_id` is in this table before writing to
`dynamic_entries` - otherwise a pinned host would show up twice
(once as the connection, once as the dynamic ghost).

| Column | Type | Notes |
|---|---|---|
| folder_id | text FK → folders ON DELETE CASCADE | the dynamic folder the host came from |
| external_id | text | Proxmox vmid / Hetzner server id / Ansible host name |
| connection_id | text FK → connections ON DELETE CASCADE | the real connection row |
| pinned_at | int64 | unix seconds |

Primary key: `(folder_id, external_id)`. Deleting the connection
cascades and drops the pin row, so the next refresh re-includes
the host as a dynamic entry - that's the "unpin" path (no
dedicated reverse IPC needed).

The companion "convert whole dynamic folder to static" flow
(`ConvertDynamicFolderToStatic`) is a different operation: it
snapshots every cached entry into a connection, then drops the
`dynamic_folders` row + cached entries entirely, leaving the
base `folders` row in place. Pin rows for that folder are
cleared at the same time since the dynamic source is gone.

### `network_profiles` (v17)

Overlay-network profiles for routing a connection's first SSH hop
through a userspace tunnel. One table serves both kinds; the `kind`
field inside `config_json` selects WireGuard vs NetBird.

| Column | Type | Notes |
|---|---|---|
| id | text PK | |
| name | text UNIQUE | display name |
| config_json | text | secretless profile; see below |
| created_at / updated_at | int64 | unix seconds |

`config_json` is **secretless by construction**:

- **WireGuard** (`kind` absent / `"wireguard"`): addresses, DNS, MTU,
  peers (public keys, endpoints, allowed IPs, `has_psk` flags), plus
  `mode` (`always` / `auto`) and `paused`. The interface private key
  lives in the vault under `wg_private_key:<id>`; each peer's optional
  preshared key under `wg_psk:<id>:<peer_public_key>`.
- **NetBird** (`kind: "netbird"`): `management_url`, `device_name`,
  `setup_key_credential_id` (a reference to an `api_token` credential
  that holds the setup key in the vault), plus `mode` / `paused`. No
  secret is stored on the row. Peer registration state (device keys)
  lives on disk under `DataDir/netbird/<id>/` and is intentionally
  **not** synced, so each machine registers as its own peer.

Connections and folders select a profile through a new inheritable
setting **`network_profile_id`** (in `overrides_json` / folder
`settings_json`): tri-state - absent = inherit, `""` = explicit direct
(break an inherited profile), otherwise a profile id. It rides the
normal inheritance cascade, so a whole folder can go through one
tunnel. `resolver` normalises `""` to nil.

Platform note: WireGuard profiles work everywhere the core runs,
**including Android** (pure-Go netstack). NetBird needs the sidecar
plugin binary, which is **desktop-only** (Windows / Linux / macOS) -
Android can't spawn a separate native helper process.
