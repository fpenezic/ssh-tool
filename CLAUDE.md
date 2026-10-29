# Claude Code orientation - ssh-tool

Read this first. For deeper context: `docs/USER_GUIDE.md` (what ships
today), `docs/TODO.md` (backlog), `docs/gotchas.md` (implementation
traps - live + archived; read before touching a subsystem),
`CHANGELOG.md` (version history).

## What this is

Cross-platform SSH connection manager that replaces Devolutions RDM
for daily use. Tree of connections with folders + inheritance,
encrypted credential vault, multi-tab terminal with split panes,
port forwards (incl. SOCKS5 with isolated-browser launcher),
userspace VPN profiles (WireGuard built in; NetBird, Tailscale,
OpenConnect (AnyConnect, Fortinet, F5, GlobalProtect, Pulse), OpenVPN
and Check Point as optional sidecar plugins from the separate
ssh-tool-plugins repo),
opkssh certificate auth, dynamic inventory from Proxmox, Hetzner,
DigitalOcean, Linode, Vultr, Scaleway, AWS EC2, Ansible static
inventory (`.ini` / `.yml`), userspace WireGuard network profiles
(in-process netstack, no TUN adapter; first SSH hop + provider APIs
dial through them) plus the plugin-backed profiles (desktop-only).
TOTP codes from Bitwarden / KeePass items answer SSH and VPN logins.
A Kafka tab per connection (topics, consumer, produce, groups; franz-go
in core) whose broker connections ride that connection's SSH session,
and a Database tab the same way (PostgreSQL / MySQL via pgx and
go-sql-driver, Oracle / SQL Server / Db2 via go-ora, go-mssqldb and
go-db2 (all pure Go), ClickHouse over its HTTP interface and OpenSearch /
Elasticsearch over REST with net/http, Redis with its own RESP2 client,
internal/dbx).
Local audit log + sealed password / API-token
history (last 5 rotations).

Author wants 300+ connections, daily-driver UX, full opkssh support
(non-negotiable; reason we're on Go).

## Repo state

- VPN plugins (NetBird, Tailscale, OpenConnect, OpenVPN, Check Point) live in the sibling repo
  `git@github.com:fpenezic/ssh-tool-plugins.git`, one Go module + licence
  per plugin, released on `<plugin>-vN[.P]` tags (gotcha 40,
  `docs/helper-release-plan.md`). The protocol is its `PROTOCOL.md`.
- Remote: `git@github.com:fpenezic/ssh-tool.git`
- Default branch: `main`.
- History starts at the open-source release; earlier development
  happened in a private repo (full pre-OSS history archived there).
- SSH stays on `golang.org/x/crypto/ssh`: russh's forked ssh-key crate
  rejects opkssh "forever" certs (`valid_before=u64::MAX`).

## Tech stack

Backend (Go 1.26):
- Wails v3.0.0 (`github.com/wailsapp/wails/v3`) - desktop shell + IPC.
  When bumping this in go.mod, bump `WAILS3_VERSION` in
  `.github/workflows/release.yml` to match: the release build regenerates
  `frontend/bindings/` with the CLI that env pins (`build:frontend` ->
  `generate:bindings`), so a stale CLI ships bindings from the wrong version.
- SQLite via `modernc.org/sqlite` (pure Go, no CGO).
- SSH: `golang.org/x/crypto/ssh` + `.../ssh/agent`.
- Vault: Argon2id KDF + XChaCha20-Poly1305 AEAD. File-only persistence
  + optional machine-bound auto-unlock sidecar. No OS keyring layer
  (used to be one - was bypassing Lock; see CHANGELOG v0.12.8).
- Backup: same primitives, sealed JSON envelope around store.db (via
  SQLite `VACUUM INTO`) + vault.enc. Optional daily auto-backup.
- opkssh: `github.com/openpubkey/openpubkey` + `.../opkssh` as Go libs
  (no external binary).
- PTY: `github.com/aymanbagabas/go-pty` (creak/pty on Unix, ConPTY on Win).

Mobile (Android, v0.36.0+):
- Same Go core, compiled for android via the NDK (`CGO_ENABLED=1`,
  `-buildmode=c-shared` -> libwails.so / JNI). Built locally
  (`task android:package`), not in CI. Desktop unaffected.
- Installed app id `app.sshtool`; gradle namespace stays
  `com.wails.app` for the JNI bridge (gotcha #32).
- Biometric vault unlock via EncryptedSharedPreferences (Keystore) +
  BiometricPrompt; foreground service keeps backgrounded SSH alive.
- See gotchas 26-36 for the android-specific traps.

Frontend (Node 20):
- Svelte 5 (runes: $state, $derived, $effect, $props).
- Vite 6.
- xterm.js 6 + addon-fit + addon-webgl.
- Wails autogenerates TS bindings into `frontend/bindings/`. Our
  facade in `frontend/src/lib/api.ts` wraps those with plain-object
  types - the autogen has `convertValues` members that trip strict TS.

## Layout

```
ssh-tool/
├─ main.go                 shared entrypoint: platformPreflight/buildApp/configurePlatform/Run
├─ main_desktop.go         (!android&&!ios) desktop wiring: window, tray, hooks, deep-link
├─ main_android.go         (android||ios) RegisterAndroidMain, no window, opkssh browser hook
├─ app.go                  App struct + all IPC methods (shared; desktop-only methods stay
│                            here, guarded by runtime.GOOS / mobile stubs)
├─ app_mobile_stubs.go     (android||ios) stubs for desktop-only free funcs app.go calls
├─ app_relaunch_{desktop,android}.go   relaunchApp(): exec+quit vs reload-message
├─ mobile_{events,secure,env,service}_android.go   long-poll, biometric/secure store, env, fg service
├─ mobile_*_desktop.go     (!android&&!ios) no-op stubs for the above
├─ wails3_runtime.go       shim: EventsEmit (+ mobile enqueue) / BrowserOpenURL
├─ Taskfile.yml            top-level task routing (android: namespace too)
├─ internal/
│  ├─ store/               SQLite + migrations + CRUD (DB schema at v28; audit log in a separate audit.db)
│  ├─ importer/
│  │   ├─ rdm/             Devolutions RDM JSON importer (3-pass)
│  │   ├─ sshconfig/       ~/.ssh/config importer
│  │   ├─ mobaxterm/       MobaXterm .mxtsessions importer
│  │   ├─ puttyreg/        PuTTY / KiTTY .reg importer
│  │   ├─ superputty/      SuperPuTTY Sessions.xml importer
│  │   └─ kafkatool/       kafka-tool profiles.json importer
│  ├─ syncer/              encrypted WebDAV profile sync (push/pull/live)
│  ├─ recorder/            asciicast v2 session recording
│  ├─ resolver/            inheritance: folder tree → ResolvedSettings
│  ├─ creds/               vault lifecycle, machine-bound auto-unlock
│  ├─ backup/              encrypted store+vault snapshots, scheduler
│  ├─ ssh/                 SSH client, opkssh, forwards, browser, tcpdump, VNC bridge
│  ├─ wg/                  userspace WireGuard (wireguard-go + netstack) network profiles
│  ├─ tunnelhelper/        sidecar (plugin) process manager: spawn, login relay, SOCKS5 dialer
│  ├─ totp/                RFC 6238 codes from otpauth URIs / KeePass fields
│  ├─ inventory/           dynamic providers: Proxmox, Hetzner, DO, Linode, Vultr, Scaleway, AWS EC2, Ansible
│  ├─ kafka/               Kafka tab: franz-go client dialing through the connection's SSH session
│  ├─ dbx/                 Database tab: PostgreSQL / MySQL / Oracle / SQL Server / Db2 / ClickHouse / OpenSearch / Redis, sessions, read-only guards
│  ├─ sshchan/             LazySSH: a tab's TCP connections as direct-tcpip channels on one SSH session
│  ├─ httpc/               HTTP/SOAP probe (used by HttpModal)
│  ├─ local/               in-app local PTY (Win/Mac/Linux shells)
│  ├─ keepass/             .kdbx credential backend
│  ├─ bitwarden/           Bitwarden / Vaultwarden credential backend
│  ├─ infisical/           Infisical credential backend
│  ├─ share/               session sharing: browser guest + MCP
│  ├─ presence/            tunnel ownership across machines sharing a synced profile
│  ├─ updater/             in-app update check + staged install
│  ├─ exporter/            encrypted archive export / import
│  ├─ cmdmarks/            command timestamps in the terminal
│  ├─ outbatch/            coalesces terminal output into fewer, larger chunks
│  ├─ mcpprompt/           the one MCP system prompt text
│  └─ initcmd/             initial-command sequencing
├─ frontend/
│  ├─ src/                 App.svelte + lib/* (~80 components/stores)
│  ├─ bindings/            wails-generated, committed (regenerated on build)
│  └─ package.json
├─ build/                  per-OS task config, icons, NSIS, AppImage
│  └─ android/             gradle + JNI bridge (MainActivity, WailsBridge, SecureStore, SessionService)
├─ docs/                   user guide, TODO, gotchas, planning docs
├─ docs/features.json      landing feature manifest (pushed to web on release)
├─ CHANGELOG.md            version history
└─ CLAUDE.md               this file
```

## How to run

Dev loop (Linux/WSL):
```bash
cd frontend && npm install && npm run dev  # terminal 1
GDK_BACKEND=x11 WEBKIT_DISABLE_DMABUF_RENDERER=1 \
  WEBKIT_DISABLE_COMPOSITING_MODE=1 LIBGL_ALWAYS_SOFTWARE=1 \
  FRONTEND_DEVSERVER_URL=http://localhost:5173 go run .   # terminal 2
```

Windows cross-build (from WSL):
```bash
CGO_ENABLED=0 task windows:build   # bin/ssh-tool.exe
```

Linux native build:
```bash
task linux:build                   # bin/ssh-tool
```

Tests + checks:
```bash
go build ./...
go test ./...                      # 32 packages carry tests
cd frontend && npm run check       # svelte-check, 0 errors expected
cd frontend && npm run test        # vitest, 37 files / 349 cases
```

The Database tests that need a real server skip unless `DBTEST_PG` /
`DBTEST_MYSQL` / `DBTEST_CH` / `DBTEST_OS` / `DBTEST_ES` / `DBTEST_REDIS` / `DBTEST_ORA` / `DBTEST_MSSQL` / `DBTEST_DB2` name one (URLs; docker one-liners in
`internal/dbx/integration_test.go`, `oracle_mssql_integration_test.go` and `db2_integration_test.go`).

Narrower runs while iterating: `go test ./internal/resolver/`
(inheritance), `./internal/creds/` (vault + machine sidecar),
`./internal/ssh/` (the largest suite). Android still has to compile,
the main package included (gotcha 100 has the NDK command; the CI
android job is the only other place an untagged file meets android):
`GOOS=android GOARCH=arm64 go build -tags android ./internal/ssh/`.

End-to-end testing in a browser: server mode serves the same frontend
over HTTP, so a UI change can be clicked through without a desktop
build, by hand or with a browser-automation tool such as Claude in
Chrome. Point the XDG dirs at a scratch directory so it gets its own
store:
```bash
task build:server
XDG_DATA_HOME=/tmp/rig/data XDG_CONFIG_HOME=/tmp/rig/config HOME=/tmp/rig \
  bin/ssh-tool-server              # http://localhost:8080
```

Regenerate bindings after IPC changes:
```bash
wails3 generate bindings ./...     # rewrites frontend/bindings/
```

Release:
```bash
git tag -a vX.Y.Z -m "..."
git push origin HEAD vX.Y.Z
# CI (GitHub Actions) builds + publishes on tag push; see below
```

## Platform note

App runs natively on Windows (not WSL). Do NOT add WSL-specific code
paths or `/mnt/c/...` exec paths. Zero WSL dependency required.

## Gotchas (moved)

The live gotchas + Android gotchas that used to be inlined here now live
in `docs/gotchas.md` (they were most of this file and loaded every
session). **Read the relevant ones there before touching a subsystem** -
Wails v3 event/threading traps, PTY/xterm races, vault/sidecar rules,
opkssh, forwards, MCP bridge, cross-window tab moves, the external secret
backends (KeePass/Bitwarden/Infisical), interactive auth prompts, and the
Android/mobile build-tag + event-transport traps. The file is split into
"Live gotchas" (still bite) and an "Archive" (older, per-module).

## Branch / commit conventions

- Conventional commits (`feat:`, `fix:`, `docs:`, `chore:`).
- Each phase commit has a long body explaining the why + gotchas -
  load-bearing context for the next handoff. Match the style.
- Don't squash phase commits.
- No `Co-Authored-By` footer (personal project).
- Before committing, verify `git config user.email` matches what
  you expect. If unset/wrong, ask the author once and remember it
  for the session - never bake an email into a repo file.

## Versioning workflow

SemVer. `v1.0.0` is the first release on Wails v3.0.0 stable; before it
the app stayed on `0.x` while Wails v3 was a prerelease. Upstream ships
Wails minors often: bump it when a fix or feature is needed, not on
every release (and keep `WAILS3_VERSION` in release.yml in step).
Current: see latest `v*` tag on `main`.

**Every user-facing change ships with a version bump.** When the
author asks to ship/promote/release:

1. Decide the bump:
   - **patch** (`vX.Y.Z -> vX.Y.(Z+1)`) - bugfix only.
   - **minor** (`vX.Y.Z -> vX.(Y+1).0`) - new feature(s), back-
     compatible. New decoder, new tab, new provider - all minor.
     `v1.40.x` is perfectly fine.
   - **major** (`v2.0.0`) - only for a real break: a store / vault /
     sync format an older app cannot read, a removed feature or
     platform. Schema migrations that older apps tolerate are not one.

2. Update `CHANGELOG.md`: new `## [vX.Y.Z]` block at the top,
   grouped by area, prose written for a returning user. The block opens
   with one or two short plain-language sentences (before the first `###`)
   naming what the release is about - the GitHub release notes and the
   website show it first. A summary, not a second changelog.
   No hard wrap in the release block: one line per paragraph and per
   bullet. The block is rendered as release notes (GitHub, the website,
   the in-app update dialog), and a line break inside `**bold**` or a
   link breaks the formatting there. Older blocks keep their wrapping.

3. Before a stable tag: Dependabot shows no open alert in this repo or
   ssh-tool-plugins (or each is dismissed with its reason), and
   `python3 scripts/vulncheck.py . . ./internal/...` reports nothing the
   code reaches. Keep go.mod's `toolchain` line on the latest Go patch
   release: dependabot does not see standard-library fixes. The release
   workflow enforces the govulncheck part (stable tags only;
   `.vulncheck-allow` for an ID with no fix yet, the repository variable
   `SKIP_VULNCHECK=true` to bypass once).

4. Commit changelog as final release commit: `chore(release): vX.Y.Z`.

5. Tag annotated: `git tag -a vX.Y.Z -m "vX.Y.Z - one-line summary"`.

6. Push commits + tag. GitHub Actions builds every platform
   (desktop x4 + the signed android APK) and publishes a GitHub
   Release with the tag's CHANGELOG block as notes. sshtool.app
   mirrors GitHub Releases (metadata sync + download redirect)
   within ~10 minutes - nothing is uploaded there anymore. Local
   escape hatch (CI down): `task <os>:build` + `gh release create`.

Notes:
- Release candidates: tag `vX.Y.Z-rcN` from a commit whose CHANGELOG
  still has `[Unreleased]` (or already `[X.Y.Z]`). CI publishes a GitHub
  prerelease with that block as notes; apps on the *Release candidates*
  update channel are offered it, nobody else is (apt/dnf/AUR/sshtool.app
  skip prereleases). The real `vX.Y.Z` tag supersedes it; delete the RC
  release afterwards (`gh release delete vX.Y.Z-rcN --cleanup-tag`).
  `-test` tags are prereleases no channel offers.
- Wiki (`ssh-tool.wiki`) tracks the latest release: the release
  workflow's `wiki` job runs `scripts/gen-wiki.py` on every stable tag
  and pushes the pages built from `docs/USER_GUIDE.md`. Home, FAQ, MCP
  and Session sharing are written by hand in the wiki itself. A new
  guide section needs a page in the script's PAGES map (or SKIP); the
  script refuses to run with an unmapped one.
- New landing feature? Edit `docs/features.json`; the website
  fetches it from this repo (raw.githubusercontent) on a timer -
  no push, no web deploy.
- `task build` injects version from `git describe --tags --always
  --dirty`. Tagged build → `vX.Y.Z`; untagged → SHA + `-dirty`.
- Plain `go run .` shows `dev` / `unknown`.

## Style conventions

- App runs natively on Windows (not WSL). Don't add WSL-specific
  paths. Must work without WSL installed.
- No emojis in code, docs, commits, or UI unless explicitly asked.
- No em-dashes anywhere - code, comments, docs, commits, UI
  strings. Use a plain ASCII hyphen with spaces (` - `) instead;
  keep all punctuation ASCII. (En-dashes too.)
- Sensitive data NEVER in repo files. Real email addresses, internal
  hostnames, customer names - keep them out of CLAUDE.md, USER_GUIDE,
  commit bodies, even examples - test fixtures included. Use
  `example.com` and generic names throughout.
- Maintainer-local preferences live in `CLAUDE.local.md` (gitignored);
  if you are the maintainer, keep personal workflow notes there, not
  here.

## Where to look next

- **What's shipped, version by version** → `CHANGELOG.md`
- **How to use the app** → `docs/USER_GUIDE.md`
- **Backlog + future work** → `docs/TODO.md`
- **Implementation traps (live + archived)** → `docs/gotchas.md`
- **Architecture + data model** → `docs/02-architecture.md`,
  `docs/03-data-model.md`
- **Security model + crypto choices** → `docs/06-security.md`
