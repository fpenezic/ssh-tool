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
userspace VPN profiles (WireGuard built in; NetBird + Tailscale as
optional sidecar plugins),
opkssh certificate auth, dynamic inventory from Proxmox, Hetzner,
DigitalOcean, Linode, Vultr, Scaleway, AWS EC2, Ansible static
inventory (`.ini` / `.yml`), userspace WireGuard network profiles
(in-process netstack, no TUN adapter; first SSH hop + provider APIs
dial through them) plus NetBird profiles via an optional sidecar
plugin (desktop-only). Local audit log + sealed password / API-token
history (last 5 rotations).

Author wants 300+ connections, daily-driver UX, full opkssh support
(non-negotiable; reason we're on Go).

## Repo state

- Remote: `git@github.com:fpenezic/ssh-tool.git`
- Default branch: `main`.
- History starts at the open-source release; earlier development
  happened in a private repo (full pre-OSS history archived there).
- The project started as a Rust/Tauri app and was ported to Go.
  Do NOT reintroduce russh-based code - russh's forked ssh-key crate
  rejects opkssh "forever" certs (`valid_before=u64::MAX`).
  `golang.org/x/crypto/ssh` accepts them natively.

## Tech stack

Backend (Go 1.26):
- Wails v3 alpha2.117 (`github.com/wailsapp/wails/v3`) - desktop shell + IPC.
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
  `com.wails.app` for the JNI bridge (gotcha #25).
- Biometric vault unlock via EncryptedSharedPreferences (Keystore) +
  BiometricPrompt; foreground service keeps backgrounded SSH alive.
- See gotchas 19-29 for the android-specific traps.

Frontend (Node 20):
- Svelte 5 (runes: $state, $derived, $effect, $props).
- Vite 5.
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
│  ├─ store/               SQLite + migrations + CRUD (DB schema at v24; audit log in a separate audit.db)
│  ├─ importer/
│  │   ├─ rdm/             Devolutions RDM JSON importer (3-pass)
│  │   ├─ sshconfig/       ~/.ssh/config importer
│  │   ├─ mobaxterm/       MobaXterm .mxtsessions importer
│  │   └─ puttyreg/        PuTTY / KiTTY .reg importer
│  ├─ syncer/              encrypted WebDAV profile sync (push/pull/live)
│  ├─ recorder/            asciicast v2 session recording
│  ├─ resolver/            inheritance: folder tree → ResolvedSettings
│  ├─ creds/               vault lifecycle, machine-bound auto-unlock
│  ├─ backup/              encrypted store+vault snapshots, scheduler
│  ├─ ssh/                 SSH client, opkssh, forwards, browser, tcpdump, VNC bridge
│  ├─ wg/                  userspace WireGuard (wireguard-go + netstack) network profiles
│  ├─ tunnelhelper/        sidecar (plugin) process manager: spawn + SOCKS5 dialer (NetBird)
│  ├─ inventory/           dynamic providers: Proxmox, Hetzner, DO, Linode, Vultr, Scaleway, AWS EC2, Ansible
│  ├─ httpc/               HTTP/SOAP probe (used by HttpModal)
│  ├─ local/               in-app local PTY (Win/Mac/Linux shells)
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
go test ./internal/resolver/       # 9 cases
go test ./internal/creds/          # vault + machine sidecar
cd frontend && npm run check       # svelte-check, 0 errors expected
```

Regenerate bindings after IPC changes:
```bash
wails3 generate bindings ./...     # rewrites frontend/bindings/
```

Release:
```bash
git tag -a v0.X.Y -m "..."
git push origin HEAD v0.X.Y
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

SemVer with `0.x` prefix while Wails v3 stays in alpha upstream.
Current: see latest `v*` tag on `main`.

**Every user-facing change ships with a version bump.** When the
author asks to ship/promote/release:

1. Decide the bump:
   - **patch** (`v0.x.y → v0.x.(y+1)`) - bugfix only.
   - **minor** (`v0.x.y → v0.(x+1).0`) - new feature(s), back-
     compatible. New decoder, new tab, new provider - all minor.
     `v0.101.x` is perfectly fine.
   - **major within 0.x** for breaking changes; `v1.0.0` reserved
     for when Wails v3 stable.

2. Update `CHANGELOG.md`: new `## [vX.Y.Z]` block at the top,
   grouped by area, prose written for a returning user.

3. Commit changelog as final release commit: `chore(release): v0.X.Y`.

4. Tag annotated: `git tag -a v0.X.Y -m "v0.X.Y - one-line summary"`.

5. Push commits + tag. GitHub Actions builds every platform
   (desktop x4 + the signed android APK) and publishes a GitHub
   Release with the tag's CHANGELOG block as notes. sshtool.app
   mirrors GitHub Releases (metadata sync + download redirect)
   within ~10 minutes - nothing is uploaded there anymore. Local
   escape hatch (CI down): `task <os>:build` + `gh release create`.

Notes:
- New landing feature? Edit `docs/features.json`; the website
  fetches it from this repo (raw.githubusercontent) on a timer -
  no push, no web deploy.
- `task build` injects version from `git describe --tags --always
  --dirty`. Tagged build → `v0.X.Y`; untagged → SHA + `-dirty`.
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
  commit bodies, even examples. Use `example.com` placeholders.
- Maintainer-local preferences live in `CLAUDE.local.md` (gitignored);
  if you are the maintainer, keep personal workflow notes there, not
  here.

## Where to look next

- **What's shipped, version by version** → `CHANGELOG.md`
- **How to use the app** → `docs/USER_GUIDE.md`
- **Backlog + future work** → `docs/TODO.md`
- **Implementation traps (live + archived)** → `docs/gotchas.md`
- **Architecture, data model, roadmap** → `docs/02-architecture.md`,
  `docs/03-data-model.md`, `docs/07-roadmap.md`
- **Security model + crypto choices** → `docs/06-security.md`
