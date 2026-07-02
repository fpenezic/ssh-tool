# Security

## Threat model

**In scope:**
- Credential file leak from disk (laptop stolen, backup leak).
- Malware on the user's machine reading app state.
- Careless export containing secrets.
- Logfile containing secrets.
- Memory dump revealing secrets.

**Out of scope:**
- State actor with root on the target machine - kernel-level
  compromise can't be defended against here.
- Hardware side channels.
- Compromised `golang.org/x/crypto/ssh` or OS keychain - we rely on
  those primitives.

## Credential storage

**Rule: secret values never touch the SQLite database.** The DB
stores only pointer rows (`credential_refs`); actual secrets live in
the encrypted file vault keyed by `vault_key`.

- File vault: `DataDir/vault.sealed`, 0600.
- KDF: Argon2id (m=19MiB, t=2, p=1, interactive). Threat model is
  local file access, not offline brute force at scale; that's the
  trade-off behind the loosened-from-OWASP params (see CLAUDE.md
  gotcha).
- AEAD: XChaCha20-Poly1305 over the sealed payload.
- Master key derivation: passphrase → Argon2id → key. Salt + params
  are stored in the file header.

### Machine-bound auto-unlock (optional)

The master key can be wrapped with a machine-derived secret stored
in the OS keychain so the app unlocks without a passphrase prompt:

- Windows: Credential Manager (`go-keyring` → `wincred`).
- macOS: Keychain (`security` framework).
- Linux: Secret Service (gnome-keyring / KWallet via D-Bus).

Auto-unlock is opt-in; disabling it requires the passphrase on
every launch. Idle auto-lock is also configurable
(`vault_autolock_minutes` setting) - vault re-locks after timeout
while SSH sessions and forwards stay alive.

### Linux headless / no Secret Service

Disable auto-unlock. The vault still works with a passphrase prompt
at launch.

## Vault key namespace

Common keys:
- `cred:{credentialID}` - credential secret (password, key PEM,
  opkssh key+cert, API token).
- `credhist:<random hex>` - sealed previous secret kept by the
  password-history feature (one vault entry per snapshot, linked
  from `credential_secret_history.vault_account`). Random suffix
  so two rotations within the same second can't collide.
- `conn_pass:{connectionID}` - per-connection password override
  (migration 7).
- Various app-level keys for machine binding, etc.

## Credential secret history (migration 14)

- Every successful password / API-token rotation seals the previous
  value under a fresh `credhist:*` vault account and records a
  `credential_secret_history` row.
- Retention: hard-coded keep-last-5 today, slider follow-up later.
  Older snapshots are pruned from BOTH the DB and the vault on
  every rotation; nothing leaks past N.
- Reveal goes through `CredentialsRevealSecretHistory(historyID)`
  with the same 30-second clipboard auto-clear the live reveal
  uses. Reveal + delete both record an audit row.
- Deleting a credential purges every history vault entry alongside
  the live one (`Service.Delete`).

## Audit log (migration 13)

- Append-only `audit_events` table; every security-relevant
  operation fans in via `app.go:recordAudit`. Best-effort: a write
  failure logs and falls through, never blocks the underlying op.
- Captured today: vault unlock / lock / passphrase rotate, backup
  create / restore, SSH connect / disconnect (with hostname / port
  / user), dynamic connect (folder + entry), credential history
  reveal + delete, forward start / stop.
- UI: Settings → Audit log with text filter, sort, CSV export,
  retention slider.
- Never stores secret material - `metadata_json` is intended for
  identity and intent only.

## Memory hygiene

- Secret strings cleared after use where the Go runtime allows.
  We don't have Rust's `zeroize`; for short-lived buffers we set
  the slice contents to zero and let the GC reclaim.
- Don't log credential values, even at DEBUG.
- Don't log keystroke payloads (may contain typed passwords).
- Terminal output logging is opt-in, per-session, explicitly
  flagged so the user knows.

## Filesystem

- DataDir (per-OS conventions):
  - Linux: `$XDG_CONFIG_HOME/ssh-tool/` (defaults `~/.config/...`).
  - macOS: `~/Library/Application Support/ssh-tool/`.
  - Windows: `%APPDATA%\ssh-tool\`.
- SQLite: `DataDir/ssh-tool.db`, 0600.
- Vault: `DataDir/vault.sealed`, 0600.
- Logfile: `DataDir/logs/app.log`, rotated 5 MiB × 3 historical
  copies. Path visible in Settings → Logs.

## SSH host key handling

- TOFU via `known_hosts` table (migration 3). First connection to
  a host prompts; user can accept once or remember + trust.
- Known hosts are stored in the app DB, separate from the user's
  system `~/.ssh/known_hosts` (so the app doesn't modify a system
  file behind the user's back; trade-off is a possible double
  prompt if the host is already trusted system-wide).
- Host key mismatch → loud red modal, blocks the connect until
  the user explicitly accepts the new key. The "Trust & remember"
  button overwrites the stored fingerprint.
- Challenge response has a **2-minute timeout fallback**. If the
  user closes the modal without responding (frontend bug, hard
  crash, deliberate hang), the connect goroutine doesn't sit on
  the response channel forever holding a half-open SSH handshake;
  it rejects + logs.

## SSH agent socket validation

`socket_path` on an agent credential is dialled only after a
validation pass (`internal/ssh/agent_validate_unix.go`):

- Path must resolve to a real socket inode, not a symlink.
- Socket must be owned by the current user (uid match).
- Parent directory must be owned by the current user with
  permissions `0700` or stricter - looser perms would let
  another local user swap a hostile socket in between our stat
  and our dial.

No-op on Windows where the agent is a named pipe with OS-managed
ACLs. Without the check, any UNIX socket on the machine could
sign as the user's agent.

## opkssh

- Native implementation via `github.com/openpubkey/openpubkey` +
  `github.com/openpubkey/opkssh` as Go libraries. No external
  binary, no `~/.ssh/` or `~/.opk/` filesystem dance.
- Cert lifetime: opkssh uses `valid_before = u64::MAX` ("forever").
  Server-side enforces lifetime separately. The app surfaces this
  as "no expiry" and falls back to a vault-stored `issued_at` for
  age-based refresh prompts.
- Cert + key stored exclusively in the vault.
- Provider config (`opkssh_config_yaml`) is editable in the
  credential editor; same YAML format as `~/.opk/config.yml`.
- **Validated at save time AND at login time**
  (`internal/ssh/opkssh_validate.go`): `issuer` must be `https://`
  (loopback `http://` permitted for OIDC dev), every
  `redirect_uris` entry and the optional `remote_redirect_uri`
  must be a loopback address (`localhost`, `127.0.0.0/8`, `::1`).
  Hostile YAML would otherwise redirect the auth code (and any
  access_token) to an attacker host, or run the browser flow
  against an attacker IdP.
- OIDC browser flow handled by `opkclient.Auth(ctx)` - opens
  default browser, waits for localhost callback.

## Logging principles

- Default level: INFO.
- Structured logging via `log.Printf` + rolling file. `slog`
  migration is a TODO.
- Redacted fields: every credential value. Connect-time hop info
  may show hostnames but not secrets.
- Settings → Logs has a "Disable log collection" toggle for users
  who don't want even the rolling file.

## Export safety

- Default export = no credentials. Credentials can be included
  *encrypted* (separate option) so the receiving side can decrypt
  with a passphrase.
- Force-confirm dialog when exporting folders marked sensitive.
- Strip toggles in the export modal: notes / tags / colour /
  convert-auth-ref-to-inherit.

## Update channel

- Update *check* lives in the status bar pill; opt-out under
  Settings → App → Updates.
- Auto-download + relaunch is not built yet. When it lands, will
  use signed releases (signing cert is a packaging blocker).
- Release server is the author's own (`sshtool.app`) - no GitHub
  Releases, no third-party CDN.

## Telemetry

**Default: OFF. Explicit opt-in only.**

If ever added:
- Anonymized crash reports only.
- Never: hostname, username, file paths, command output.
- Local log first so the user can review before sending.

## Dependency hygiene

- `go mod tidy` regularly.
- `npm audit` - current `npm run check` flags get reviewed.
- Manual review for upgrades of `golang.org/x/crypto`,
  `openpubkey`, `go-keyring`.

## What specifically not to do

- ❌ "Sync to cloud" feature without end-to-end encryption design.
- ❌ Browser extension exposing the local store.
- ❌ Remote control / web UI on top of ssh-tool.
- ❌ Plaintext export anywhere, anytime, without an explicit user
  action.
- ❌ Skip host key verification by default.
