# Roadmap

> **Note:** Earlier drafts of this doc were a Rust/Tauri phased plan
> ("Phase 0 scaffolding" → "Phase 7 forwards"). All those phases
> shipped - see `CHANGELOG.md` for the version-by-version history.
> What's left is grouped by theme below; granular items live in
> `TODO.md`.

## Where we are (v0.21.x, May 2026)

The app is a daily driver. The Wails v3 port is on `main`; multi-
window detach + redock works on Windows including split-pane
preservation; Linux runs from a clean binary; dynamic inventory
pulls from Proxmox VE, Hetzner Cloud, DigitalOcean, Linode, Vultr,
Scaleway, AWS EC2, and Ansible static inventory.

689-connection RDM import + TOML/JSON round-trip both work. opkssh
native via the OpenPubkey Go libraries (no external binary). Vault
is Argon2id + XChaCha20-Poly1305 with master-passphrase rotation
and sealed password / API-token history (keep last 5). Local audit
log records every security-relevant operation. Auto-update lands
in-app on Windows (CREATE_NO_WINDOW helper script + verified swap).

The MVP "success criteria" from `00-vision.md` are all met. From
here on, the work is polish, niche workflows, more providers, and
packaging.

## Near-term themes (next few minor releases)

### Provider expansion
Pattern is established (`internal/inventory/*.go`), each provider
is ~150 lines.

- Terraform state file (local / S3 / GCS - design in TODO).
- Hetzner Robot - dedicated servers (separate API from Cloud).
- libvirt - local KVM via libvirt-go.
- Ansible dynamic inventory scripts (`./inventory.py --list`),
  remote sources (git / HTTP) on top of the local-file MVP.

### Vault polish
- Configurable retention slider for password history.
- Bulk credential rotation with ssh-copy-id helper.
- HashiCorp Vault / Vaultwarden as backing store (`kind=external`).
- Hardware-key (FIDO2 / YubiKey) - investigation needed.

### Reliability
- Connect retry / reconnect with back-off.
- Health probe for the tree (scoped, opt-in - design captured in
  TODO).
- Slog migration for filtered debugging.

### Packaging
- Linux .AppImage / .deb smoke test + publish.
- Windows .msi (NSIS exists; signing cert is the blocker - Wacapew
  false positive will resolve with signing).
- macOS universal build + notarisation (no Mac in CI yet).

## Mid-term

### Multi-broadcast
Multiple broadcast groups in parallel ("ops" + "client" being driven
from two pane groups at once).

### Layout persistence
Save/restore window layout between launches. Workspaces snapshot tab
grouping; pane splits inside a tab aren't restored from layout.

### Accessibility
Tab order, ARIA labels, keyboard nav across modals. Not a priority
yet but worth a pass before going wider open-source.

### i18n
Croatian + English at minimum. Structure UI strings via a small i18n
shim.

## Out of scope

- **Team sync server / multi-user RBAC** - not planned in the core app.
- **Integrated web browser through SOCKS** - SOCKS endpoint yes, but
  browser launches the system Chrome/Firefox (already done).
- **RDP / Telnet / Serial** - SSH (SFTP as an SSH subprotocol) and VNC
  only.
- **iOS** - the Android port (v0.36.0+) shares build tags with iOS
  (`android || ios`), but no iOS build is produced or tested. Android
  is the only mobile target shipping.
- **Cloud sync (proprietary)** - use git or user-managed file sync
  (Syncthing, Dropbox folder).

## Mobile (Android)

Shipped in v0.36.0. The Go core runs natively on Android (arm64,
DeX-ready) - same SSH stack, store, vault, syncer; no separate
codebase. Built locally via the NDK (`task android:package`), out of
CI for now. App id `app.sshtool`. See `CLAUDE.md` (gotchas 19-29) for
the android-specific traps and `TODO.md` for the packaging backlog
(release keystore, signed build, distribution, in-app update).

## How releases happen

SemVer with `0.x` prefix while Wails v3 stays in alpha. Author cuts
releases as they go - minor for new features, patch for fixes.
`scripts/publish-all.sh` from a tagged commit builds Windows + Linux
and uploads to `sshtool.app`. The app checks for updates via
`/api/latest` and shows a pill in the status bar when newer is
available. See `CLAUDE.md` "Versioning workflow" for the exact
steps.
