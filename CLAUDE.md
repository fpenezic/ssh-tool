# Claude Code orientation - ssh-tool

Read this first. For deeper context: `docs/USER_GUIDE.md` (what ships
today), `docs/TODO.md` (backlog), `docs/gotchas.md` (archive of
implementation traps), `CHANGELOG.md` (version history).

## What this is

Cross-platform SSH connection manager that replaces Devolutions RDM
for daily use. Tree of connections with folders + inheritance,
encrypted credential vault, multi-tab terminal with split panes,
port forwards (incl. SOCKS5 with isolated-browser launcher),
opkssh certificate auth, dynamic inventory from Proxmox, Hetzner,
DigitalOcean, Linode, Vultr, Scaleway, AWS EC2, Ansible static
inventory (`.ini` / `.yml`). Local audit log + sealed
password / API-token history (last 5 rotations).

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
- Wails v3 alpha2.111 (`github.com/wailsapp/wails/v3`) - desktop shell + IPC.
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
│  ├─ store/               SQLite + migrations + CRUD (DB schema at v16; audit log in a separate audit.db)
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

## Live gotchas

These still bite. The archive of older / now-handled traps lives in
`docs/gotchas.md` - check there before assuming something is novel.

1. **Wails v3 OnWindowEvent listeners run in goroutines.** Calling
   `event.Cancel()` from a normal listener races the default handler.
   For close-blocking (warn-before-quit, close-to-tray, minimise-to-
   tray) use `RegisterHook` which runs synchronously. See
   `main.go`: WindowClosing + WindowMinimise.

2. **Wails v3 `@wailsio/runtime` Events wrap payloads.** v2's
   `EventsOn(name, cb)` passed the raw payload as `cb`'s first arg;
   v3 wraps in `WailsEvent`, payload at `ev.data`. Shim in
   `frontend/src/lib/wailsRuntime.ts` keeps subscription sites
   unchanged.

3. **Svelte 5 reactivity isn't deep.** Mutating `tabs[i].status =
   "X"` does NOT notify downstream `$derived` consumers. Reassign
   the array. See `SessionStore.setStatus`.

4. **`SshConnect` IPC blocks until auth+PTY succeed.** Its return is
   itself the "connected" signal. The `session_state` event for
   that transition has already fired by the time the Promise
   resolves and is therefore un-listenable. DetailPane pushes the
   tab with `status: "connected"` immediately; SessionStore
   subscribes for subsequent transitions only.

5. **Forward listeners need explicit cleanup when sessions die on
   their own** (server kill, network drop). `Session.SetOnClose`
   runs inside `closedOnce.Do`. App.startup wires this to
   `forwards.StopAllForSession + pool.Remove + sessionMeta delete`.

6. **opkssh certs have `valid_before = u64::MAX` ("forever").**
   Server-side enforces lifetime separately. Surfaced as "no expiry"
   in the UI; fall back to vault-stored `issued_at` for age-based
   refresh. `ssh.CertTimeInfinity` is the sentinel.

7. **opkssh is implemented natively - no binary dependency.**
   `internal/ssh/opkssh.go` uses `openpubkey/opkssh` as a Go lib.
   Browser flow via `opkclient.Auth(ctx)`. Key+cert stored
   exclusively in the vault; `~/.ssh/` and `~/.opk/` never touched.
   Provider config in credential's `opkssh_config_yaml` field.

8. **Argon2id is interactive-grade** (m=19MiB, t=2, p=1), not OWASP
   (m=64MiB). 64MiB took 10+s on WSL2; threat model is local file
   with 0600 perms, not offline brute force at scale.

9. **Host key verification is request/response over a single
   event.** Go SSH HostKeyCallback emits `host_key_challenge` and
   blocks on a Go channel waiting for `SshRespondHostKey(...)`. If
   the user closes the modal without responding, the connect
   attempt stalls until they do.

10. **xterm `term.open(host)` and display:none.** `term.open()`
    bakes renderer canvas size from clientWidth/Height at that
    moment. If the host is `display:none` (tab added before view
    switched), canvas locks to 0×0 and no later `fit()` un-stains
    it. Terminal.svelte awaits `waitForHostLayout` (offsetParent +
    non-zero dimensions) before opening.

11. **PTY cum watermark / snapshot-then-subscribe race.** Subscribe
    FIRST, buffer chunks, fetch snapshot, then dedupe: chunks with
    `cum ≤ watermark` dropped, straddling chunks trimmed. Without
    this a chunk landing between snapshot-call and subscribe was
    silently lost. Banner output (pre-Session) gets `cum=0` as
    "always write" sentinel.

12. **Image cache: peek vs ensure.** `imageCache.peek(id)` is pure
    read for `$derived`. `imageCache.ensure(id)` triggers fetch and
    is safe only in `$effect`. Earlier version mutated reactive
    state from a derived getter and tripped
    `state_unsafe_mutation`, which froze the whole tree.

13. **Bindings regenerate on every build.** Adding/renaming `App.*`
    methods or changing struct field types invalidates
    `frontend/bindings/`. Tree shows as dirty until you re-run
    `wails3 generate bindings ./...` and commit. If you build and
    see `-dirty` in the version stamp, that's the cause.

14. **Backup restore can't overwrite live files on Windows.**
    SQLite holds `store.db` open for the lifetime of `sql.Open`, so
    in-process rename returns "access is denied". `backup.Restore`
    stages the new files in `<DataDir>/pending-restore/` and writes
    a `READY` flag; `backup.ApplyPending` runs in `initialise()`
    BEFORE `store.Open` and does the swap. UI tells the user to
    quit and reopen.

15. **OS keyring is no longer a vault layer.** Pre-v0.12.8 the
    vault mirrored every secret into the OS keychain as a "best
    effort" fallback. Because Windows / macOS keep the keychain
    unlocked for the entire user session, that meant Lock() was
    silently bypassed on every Get(). The keyring is fully gone
    from `internal/creds/vault.go` and a one-shot purge runs on
    startup (`keyring_legacy_purged_v1` settings flag) to delete
    leftover entries under the legacy service name. Auto-unlock
    sidecar (machine-bound file next to vault.enc) remains the
    only convenience path.

16. **Sidecar v2 wraps differently per platform.** v0.25.0
    introduced a version-2 sidecar format used when
    `platformHasStrongSidecar()` returns true:
    - **Windows**: payload sealed via DPAPI
      (`CryptProtectData`, user scope, no LOCAL_MACHINE flag).
      The OS is the only key holder; a stolen file plus a
      different user account on the same host can't decrypt.
    - **Linux**: SHA256(app_salt | `/etc/machine-id`) with no
      hostname fallback. Sidecar refuses to write v2 when
      `/etc/machine-id` is missing - containers etc fall back
      to v1 with the old weaker derivation.
    - **macOS**: v1 only for now. Keychain Services integration
      via cgo is the proper fix; tracked in TODO.md.
    `ReadSidecar` detects the version field and routes to the
    matching opener so the first WriteSidecar after upgrade
    migrates from v1 to v2 silently.

17. **SQLite write pool capped at 1.** modernc/sqlite is single-
    writer; letting `database/sql` open multiple write
    connections in parallel hits SQLITE_BUSY immediately even
    with WAL. `SetMaxOpenConns(1)` plus `PRAGMA busy_timeout =
    5000` so SQLite's own queue serialises writes. Readers
    still go wide through WAL.

18. **Broadcast is multi-group.** `app.broadcastGroups` is a
    `map[string]map[string]bool` (groupID → memberSet); the
    empty key `""` is the default group kept for back-compat.
    `BroadcastFanOut` walks every group containing the origin
    and unions the targets - a session in two groups
    broadcasts to both. Two events fire on every mutation:
    `broadcast_changed` (legacy default-group flat list) for
    old clients, `broadcast_groups_changed` (full snapshot)
    for the multi-group manager UI. Frontend store also keeps
    a `groupsVersion` counter so `$derived` consumers
    re-evaluate on Set-replace inside a single key (Svelte
    deep tracking misses that).

### Android / mobile gotchas

The app runs on Android (v0.36.0+). Built locally via the NDK
(`task android:package`), not in CI. Desktop stays byte-equivalent -
everything mobile is behind a build tag or an `isMobile` check.

19. **Build tags: `!android && !ios` is desktop; `android || ios` is
    mobile.** Negative desktop tag chosen so the plain dev loop (`go
    run .`, no `-tags`) still builds the desktop path. Desktop stays
    `CGO_ENABLED=0`; android needs `CGO_ENABLED=1` + NDK
    `-buildmode=c-shared` (JNI / libwails.so). The true android compile
    gate is `task android:package` (or the `compile:go:shared` c-shared
    build), NOT a `GOOS=android go build` tag check - the latter fails
    inside Wails' own `mobile_features_android.go` (needs the NDK).
    Desktop-only `App` methods stay in the shared `app.go` (guarded by
    `runtime.GOOS` / mobile stubs in `app_mobile_stubs.go`); they were
    not relocated into a desktop-tagged file.

20. **Android has no Go WebviewWindow.** The native Activity IS the
    window. `configurePlatform` (`main_android.go`) creates NO window -
    `app.Window.NewWithOptions` broke asset serving ("wails.localhost
    connection refused"; the Go window hijacks the Activity's asset
    path). Consequence: the default event transport, which dispatches by
    iterating `app.windows`, reaches nobody.

21. **Android Go->JS events go through a frontend long-poll.** Because of
    #20, `EventsEmit` (`wails3_runtime.go`) also enqueues on a poll queue
    (`mobile_events_android.go`); the frontend pump
    (`src/lib/mobileEvents.ts`) calls `App.MobilePollEvents` (25s long-
    poll) and re-dispatches via `window._wails.dispatchWailsEvent`.
    Subscription sites are unchanged. Native Wails events (e.g.
    `native:biometric`) are bridged by a Go listener
    (`registerMobileBiometricBridge`) re-enqueuing onto the same queue.

22. **Android IPC transport is hand-rolled.** npm `@wailsio/runtime` is
    alpha.79 with no android transport (it does a `fetch` POST the
    WebView can't service). `src/lib/androidTransport.ts` registers a
    custom transport via `setTransport`, routing through
    `window.wails.invokeAsync` -> Java `handleRuntimeCall` ->
    `nativeHandleRuntimeCall` and back via `window._wailsAndroidCallback`.
    Android-only methods absent from the committed (desktop) bindings are
    called with `Call.ByName("main.App.MethodName")`.

23. **Java env does not reach Go on android.** Go snapshots `environ` at
    `.so` load (a static initializer, before `onCreate`), so
    `Os.setenv("HOME", ...)` in the Activity is invisible to Go.
    `ensureMobileTempDir()` (`mobile_env_android.go`) sets HOME + TMPDIR
    from Go, deriving the path deterministically from the app id
    (`androidAppFilesDir`). Without TMPDIR, `os.TempDir` falls back to
    `/data/local/tmp` (not writable) and sync/backup fail.

24. **xterm WebGL atlas corruption ("hijeroglifi").** WebGL is off by
    default EVERYWHERE since v0.47.0 (`terminalPrefs.disableWebgl`
    defaults true; opt back in via Settings). The glyph atlas corrupts
    into garbled glyphs on some GPUs - on font-size, broadcast and
    theme changes, and sometimes spontaneously with no user action
    (seen on desktop and android). Known triggers still clear the
    atlas (`clearWebglAtlas()`) for opted-in users, but the
    spontaneous case is why canvas is the default.

25. **JNI export names pin the Java package - rename the app id, NOT the
    namespace.** The Wails runtime (`.so`) hardcodes 18 JNI exports as
    `Java_com_wails_app_WailsBridge_*` (`GetMethodID` by mangled name).
    JNI mangling uses the fully-qualified Java class name = the gradle
    `namespace` (the `.java` `package` decl), not the `applicationId`.
    So `namespace` MUST stay `com.wails.app` or the native methods don't
    link (UnsatisfiedLinkError at boot). To get off the scaffold default
    we renamed only `applicationId` (-> `app.sshtool`), which is the
    installed identity + the `/data/data/<applicationId>/` path. A new
    `applicationId` installs as a SEPARATE app (different icon, no
    in-place upgrade, empty profile) - migrate via sync pull. DataDir +
    `androidAppFilesDir` follow the applicationId.

26. **`task android:package` regenerates bindings under the android tag -
    never commit them.** The build runs `wails3 generate bindings`, which
    under `-tags android` DROPS every desktop-only IPC method (VNC,
    clipboard, local shell, ...) from `frontend/bindings/`. Those
    bindings are committed and used by the DESKTOP frontend; committing
    the android-stripped set breaks the desktop build. Always
    `git checkout -- frontend/bindings/` after an android build. The
    android frontend calls android-only methods via `Call.ByName`, so it
    doesn't need them in the committed bindings.

    Knock-on: this regen also taints the version stamp. Both the Go
    ldflags and the gradle `versionName`/`versionCode` come from `git
    describe --tags --dirty`, computed mid-build - AFTER the bindings
    regen has dirtied the tree, so a full `task android:package` stamps
    `vX.Y.Z-dirty` even on a tagged commit. For a clean release APK, run
    the full build once, then `git checkout -- frontend/bindings/` and
    re-run just the gradle assemble (`./gradlew assembleDebug -PversionName=
    ... -PversionCode=...`) with a clean tree. The Go `.so` stamp is
    usually already clean (it's relinked before the regen); the gradle
    versionName is the one that needs the clean-tree re-assemble.

27. **Android sync pull, sidecar, and relaunch.** There is no machine-
    bound sidecar on android (`machine_android.go` stubs it). `SyncPullLive`
    therefore reads the vault passphrase from the Keystore secure store
    via `App.localAutoUnlockPass()` (split per platform: desktop reads the
    sidecar) to merge a pulled vault in place; without it a pull always
    fell back to "restart to apply". And `AppRelaunch` (a desktop process
    re-exec) is a no-op on android - `relaunchApp()` is platform-split:
    android emits `profile_reloaded` and returns a "close and reopen the
    app" message (the pending-restore swap only runs in `initialise()` on
    a cold start; we don't tear down the live store).

28. **opkssh browser on android needs an Intent.** openpubkey's
    `util.OpenUrl` shells out to `xdg-open`/`open`/`start`, none of which
    exist on android, so the OIDC login silently never opened a browser.
    `internal/ssh.BrowserOpenHook` (a package var the host wires) is set
    on the provider via `SetOpenBrowserOverride` - reached through a
    narrow type assertion since it lives on the concrete `*StandardOp`,
    not the `OpenIdProvider` interface, and the assertion needs the exact
    named `providers.BrowserOpenOverrideFunc`, not its underlying func
    type. `main_android.go` points the hook at `application.AndroidOpenURL`
    -> `WailsBridge.openURL` -> `Intent.ACTION_VIEW`. The loopback
    callback server still runs in-process so the redirect lands back in Go.

29. **Per-platform Taskfiles must keep ldflags parity.** The android
    Taskfile originally omitted the `-X main.appVersion` / `-X
    main.appCommit` ldflags the desktop Taskfiles inject from `git
    describe`, so Settings -> About showed "dev". When touching version /
    build-stamp logic, update ALL of `build/{linux,windows,darwin,android,
    ios}/Taskfile.yml`, not just the desktop ones.

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
- **Older gotchas + archived traps** → `docs/gotchas.md`
- **Architecture, data model, roadmap** → `docs/02-architecture.md`,
  `docs/03-data-model.md`, `docs/07-roadmap.md`
- **Security model + crypto choices** → `docs/06-security.md`
