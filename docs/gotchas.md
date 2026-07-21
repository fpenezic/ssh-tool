# Gotchas

Implementation traps and load-bearing context for the ssh-tool codebase.
Two parts:

- **Live gotchas** - these still bite. Read the relevant ones before
  touching a subsystem; assume they are still true but verify against the
  code (a named file/flag may have moved).
- **Archive** - older traps that aren't actively biting but stay useful
  when modifying the named module. If one starts biting again, move it up
  into Live gotchas.

CLAUDE.md links here; it no longer inlines the gotchas (they were ~70% of
that file and loaded every session).

---

# Live gotchas

These still bite. Check the archive below before assuming something is
novel.

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

30. **"Give internet" is a reverse-proxy forward, DNS resolved
    app-side.** `SshGiveInternet` (app.go) starts a
    `ForwardReverseProxy` forward (`internal/ssh/forward.go`): a
    remote listener on the server (`client.Listen`, loopback
    `127.0.0.1:3182` by default) whose accepted conns are serviced by
    an in-process HTTP CONNECT proxy (`internal/ssh/httpproxy.go`),
    NOT dialed to a fixed local port like a plain `-R`. The proxy
    `net.Dial`s the destination from the ssh-tool machine, so name
    resolution happens on OUR side (the point: the server may have no
    resolver). It handles both CONNECT (HTTPS tunnel) and plain-HTTP
    absolute-URI proxying (apt/curl). Header reads are size-capped
    (8KB). Forwards are ad-hoc (never persisted to `store.PortForward`),
    surface only in the TunnelPopover + `ForwardsActive`, and are torn
    down by the existing `StopAllForSession` on disconnect. The
    reverse-proxy leg uses `tunnelBuffered` (not `tunnel`) because the
    client side has a `bufio.Reader` that may already hold bytes read
    past the header block (request body / TLS ClientHello).

31. **MCP bridge = separate stdio subprocess proxying to the live app
    over a local socket.** The desktop app runs the MCP server itself
    (`app_mcp_desktop.go`): per accepted socket connection it builds an
    `mcp.Server` (go-sdk) and runs it over an `mcp.IOTransport` on that
    conn. `ssh-tool --mcp-bridge` (`bridge_desktop.go`) is a DUMB pipe -
    `io.Copy` between the LLM's stdio and the socket - so MCP-over-socket
    IS the protocol; there is no hand-rolled framing. Sessions live in
    the running desktop process, which is why the subprocess can't hold
    them and must proxy. Transport is local-only: unix socket (0600) on
    Linux/macOS, loopback TCP + a 0600 addr file on Windows (no unix
    sockets without winio) - see `app_mcp_listen_{unix,windows}.go`. The
    whole feature is `!android && !ios`; mobile gets no-op stubs
    (`app_mcp_mobile.go`, `bridge_mobile.go`). Off by default
    (`mcp_bridge_enabled`); toggling the setting starts/stops the
    listener live via `SettingsSet`. Grants are per-session, in-memory
    only (`a.mcp.grants`), cleared in the session `SetOnClose` teardown
    via `clearMcpGrant`. The command-approval gate copies the host-key
    TOFU pattern exactly (register a channel, emit `mcp_approval_request`,
    select on channel/ctx/2-min timeout -> default deny). Read-only
    classification is `internal/ssh/cmdallow.go` `IsReadOnly` (conservative:
    unknown -> prompt). Scrollback returned to the LLM is framed as
    UNTRUSTED data - it is not an instruction channel; only a run/type
    tool call touches the host, and that is allowlisted-or-gated.
    THREE grant levels (`app_mcp.go`): `read`, `read-run` (exec+type,
    gated), and `read-run-yolo` (auto-approves writes WITHOUT a prompt -
    an explicit per-session opt-in, never a default). YOLO still routes
    catastrophic commands through the approval modal via
    `cmdallow.go` `IsDangerous` (a deliberately NARROW, catastrophic-only
    deny-list - recursive rm/chmod/chown on root-ish paths, mkfs/wipefs/
    dd-to-device, shutdown/reboot, fork bomb - NOT the broad
    `mutationTokens` set, or YOLO would prompt on mkdir/touch/git and
    defeat itself). `canRun(lvl)` is the shared authorisation check; the
    activity gate is recorded as `"yolo"` for auto-approved writes. The
    LLM system prompt is a frontend const (`mcpSystemPrompt.ts`, the
    single source of truth) surfaced by a "Copy system prompt" button in
    Settings->LLM + the share popover; `docs/MCP_SYSTEM_PROMPT.md` is the
    hand-synced human-readable mirror.
    Tools: list_sessions, read_terminal, run, type_into_terminal, plus
    list_connections (name + folder path only, Sensitive connections
    omitted) and connect (approval-gated `SshConnect` then auto-share).
    WSL->Windows: an optional token-guarded loopback-TCP leg
    (`mcp_bridge_tcp`, `startMcpTCP`, addr+token in
    `<DataDir>/mcp-bridge.tcp`) - the bridge's `dialMcp` prefers it when
    present (WSL2 forwards localhost to the host but can't see the pipe).
    Do NOT add `/mnt/c` exec paths for this - the Windows `.exe` is the
    bridge, launched from WSL, and it talks to the Windows app over
    loopback; the app stays WSL-agnostic. A shared session shows a badge
    on its terminal tab (mcpShared store, fed by `mcp_grants_changed`).
    Every tool call is recorded via `recordActivity` (in-memory ring cap
    500, output cap 16KB; emitted as `mcp_activity`; optionally mirrored
    to audit.db as action `mcp_run`/etc behind `mcp_audit_enabled`) and
    shown in `McpActivityPanel` (status bar = all sessions, robot popover
    = one session). The robot affordances (pane Share button, status-bar
    segment) hide when the bridge is off - pane gated on `mcpBridge.enabled`
    (fed by `mcp_bridge_toggled`), status-bar segment on `mcpShared.size`.
    Blocking prompts (MCP approval, host-key) flash the taskbar
    (`RequestAttention`, Windows FlashWindowEx, only when unfocused via the
    `windowFocused` atomic) AND pop an OS toast (`SendPromptNotification`,
    Wails notifications service; Windows needs a non-empty `ID` or it fails
    silently).
    Bulk provisioning (v0.73.0) adds a SEPARATE store-wide "manage" grant
    (`manageStore`, `McpSetManageStore`/`McpGetManageStore`, not persisted,
    toggle in the share popover) gating create tools. These do NOT write on
    call: they STAGE into an in-memory `mcpPlan` (`app_mcp_plan.go`) with
    `tmp:`-prefixed temp ids so later entries ref earlier ones; `commit_plan`
    renders a rich preview (`McpPlanPreview`, cred/profile shown by NAME only)
    via `requestPlanApproval` (reuses the same `approvals` channel map + a new
    `mcp_plan_approval_request` event + `McpPlanApprovalModal`), then writes the
    whole plan in ONE `db.WithTx` (`internal/store/tx.go`; the `*Tx` create
    variants share SQL via `execer`-taking `insertX` helpers - keep those and
    the non-tx methods in sync). All-or-nothing: any error rolls back, the plan
    is discarded either way (a half-applied plan must never be re-committed).
    HARD RULE: the LLM never sets a secret - a connection/bastion only
    references an EXISTING vault credential by id (`auth_ref`), validated at
    commit; `list_credentials` returns id/name/kind only.

32. **Cross-window tab moves go through a backend pending-drag slot, not
    native drag.** A WebView drag can't cross OS window boundaries (the
    drag ends when the pointer leaves the source window), so moving a tab
    to another window is a menu action ("Send to <window>"), not a drop.
    The mechanism reuses the detach/redock plumbing: the source stashes the
    tab in `a.pendingTabDrag` (`WindowSendTab`), a name-targeted
    `window_receive_tab` event fires, and only the window whose `selfWindowName`
    matches claims the payload via `WindowAcceptTabDrag` and reconstructs it.
    The main window is named `"main"`; detached ones `detached-<tabID>`.
    Session ownership is transferred in `detachedSessions` so window-close
    teardown disconnects the right sessions (else dangling green sessions).
    Redock ships EVERY tab (`encodePaneLayouts` -> `{tabs:[...]}`,
    `decodePaneLayoutsMulti`), not just `tabs[0]` - the multi-tab-loss bug.
    `decodePaneLayout` still returns the first tab so single-tab callers
    (send-to-window, initial detach `?layout=`) are unaffected. On detach
    replay, non-user xterm `onData` is suppressed while `replaying` so query
    responses in the scrollback don't land in the remote shell as garbage.

33. **KeePass is a read-only live secret backend, routed via a package-var
    hook - not a new credential kind.** `internal/keepass` parses `.kdbx`
    with `gokeepasslib/v3` (pure Go, CGO-free, android-safe; `tobischo/argon2`
    is a pure-Go x/crypto fork). A credential does NOT get a new `Kind`: it
    stays `password` or `key` with `StorageMode=external` and a
    `config_json.keepass_ref {db_id, entry_uuid, field}` (store v18 table
    `keepass_databases` holds the file + vault-account pointers, never
    secrets). `sshlayer.ResolveAuth` calls the package var
    `sshlayer.KeepassResolveHook` (wired in `app_keepass.go`, exactly like
    `BrowserOpenHook` gotcha 28) BEFORE the kind switch; `handled=false` means
    "not a KeePass cred, fall through". Field routing: `password` ->
    `ssh.Password`; an attachment or non-standard String field -> parsed as a
    signer. The decrypted DB lives ONLY in `keepass.Manager` memory and is
    dropped in `VaultLock` via `a.forgetKeepass()` - same lifecycle as the
    vault (opkssh untouched, keeps its own vault-backed refresh). Freshness for
    remote (WebDAV/SFTP) files: fetch-on-unlock + fetch-on-connect when the
    open is older than `staleAfter` (5 min), conditional GET via `If-None-Match`
    for WebDAV, and a stale-on-offline fallback (serve the still-open decrypted
    copy marked `FreshStale` rather than break an in-flight connect) - NEVER a
    background timer poll. Cached blob is the ENCRYPTED `.kdbx` under
    `<DataDir>/keepass-cache/<id>.kdbx` (0600), worthless without the
    vault-held master. The manager (`app.keepass`) is built in `initialise()`
    after db+vault; the parser (`keepass.go`/`browse.go`) has zero app-internal
    imports so it stays unit-testable (see the encode-then-decode fixtures -
    v4 binaries go through `db.AddBinary`, which routes to the InnerHeader; a
    manual `Meta.Binaries.Add` won't be found on decode). The connection
    auth-picker path (`KeepassEnsureCredential`) auto-creates a credential for
    the picked entry (dedup by matching keepass_ref) and files it under an
    auto-created "KeePass" CREDENTIAL folder. Trap: `credential.folder_id`
    references `credential_folders`, a DIFFERENT tree from a connection's
    `folder_id` (which is in `folders`) - passing a connection folder id here
    is a foreign-key violation, so the picker sends `folder_id: null` and the
    backend fills in the KeePass credential folder. The "From KeePass" button
    is gated on `keepass_dbs_changed` (emitted from create/update/delete) so it
    appears live without an app restart. Local paths get a native Browse dialog
    (`KeepassPickFile` -> `OpenFileDialog`, desktop-only).

34. **Vaultwarden / Bitwarden is the HTTP sibling of KeePass (gotcha 33) -
    same read-only live-backend shape, different source + real crypto.**
    `internal/bitwarden` (no app imports, unit-testable) implements the
    Bitwarden EncString scheme natively: AES-256-CBC + HMAC-SHA256, PKCS7,
    HKDF master-key stretch, RSA-OAEP org-key unwrap, PBKDF2 / Argon2id KDFs -
    all stdlib + `golang.org/x/crypto`, ZERO new deps. Decrypt chain: API-key
    (`client_credentials`) login -> `/api/sync` -> derive master key from the
    account's `Kdf` field -> unwrap userKey (stretched-master-key-decrypts
    `profile.Key`) -> for each org, decrypt the RSA private key with userKey
    then RSA-OAEP-unwrap the org key. A cipher with `organizationId` decrypts
    with its org key, else userKey - `Vault.keyFor` is the router. A credential
    does NOT get a new `Kind`: it stays `password`/`key` with
    `StorageMode=external` and `config_json.bitwarden_ref {server_id, cipher_id,
    field}`; `sshlayer.BitwardenResolveHook` (package var, gotcha 28 pattern) is
    called right after the KeePass hook, before the kind switch. Two auth secrets
    per server, DELIBERATELY split: the **master password** is written through
    the Settings form and sealed to a hidden vault account
    (`bitwarden:<id>:master`), write-only / never returned (like the KeePass
    master); the **API key** (client_id + client_secret) is a NORMAL `api_token`
    credential (token_id = client_id, vault secret = client_secret) picked via
    the credential picker with inline create - visible, rotatable, flows through
    backup/sync. Store v19 table `bitwarden_servers` holds only pointers + a
    `network_profile_id`. Cache/freshness copy KeePass exactly: fetch-on-unlock +
    fetch-on-connect-if-stale (5 min), stale-on-offline fallback (serve the
    in-memory decrypt marked `FreshStale`, or the on-disk cache), NO timer poll.
    The cache blob is the sync JSON SEALED with the app vault
    (`Vault.Seal`/`Open` over `UnlockedVault.SealBlob`/`OpenBlob`, 0600 under
    `<DataDir>/bitwarden-cache`) - large blobs must NOT bloat the JSON account
    store. Decrypted vaults live only in `bitwarden.Manager` memory, dropped in
    `VaultLock` via `forgetBitwarden()` (zeroes key material). The manager is
    testable because `Syncer`/`Sealer`/`SecretReader`/`CredentialLookup` are all
    injected - the production `Syncer` (`bitwardenSyncer`, app.go) routes the
    HTTP through the server's WireGuard profile via `wgDialerFor` installed as
    the transport's `DialContext`; Netbird/Tailscale are sidecar-SOCKS only and
    fall back to a direct dial, so the settings dropdown offers WireGuard
    profiles only. `EventsEmit("bitwarden_servers_changed")` gates the
    "From Bitwarden" connection-pane button live. Same credential_folders-vs-
    folders FK trap as KeePass: the picker sends `folder_id: null` and the
    backend files auto-created creds under a "Bitwarden" CREDENTIAL folder.
    Bitwarden native SSH-key items resolve via the `privatekey` field ->
    `externalAuthMaterial` (shared with KeePass) parses the PEM to a signer.
    Out of scope v1: write-back, TOTP auto-fill, non-API-key login
    (email+password/2FA), self-signed certs (needs a cert the OS trust store
    accepts).

35. **Interactive auth prompts (username + keyboard-interactive/password)
    reuse the host-key TOFU plumbing and are wired via package-var hooks.**
    `internal/ssh` exposes `UsernamePromptHook` and `InteractiveAuthHook`
    (package vars, gotcha 28 pattern); `app_auth_prompt.go` `initAuthPrompts`
    points them at IPC-backed impls that register a channel, emit an event
    (`username_prompt` / `auth_prompt`), and block on it with a 2-min
    cancel-default timeout - a direct clone of the host-key challenge
    (gotcha 9). `SshRespondAuthPrompt(promptID, answers, cancel)` delivers the
    reply. TWO distinct concerns, one modal (`AuthPromptModal`, FIFO
    `authPromptStore`): (a) a hop with no configured username is prompted at
    `session.go` where it used to hard-fail with "no username" - username is
    SSH-handshake state so it MUST be collected before dial, not mid-flight;
    (b) `interactiveAuthMethods` appends `ssh.KeyboardInteractive` +
    `ssh.PasswordCallback` LAST on the TARGET hop only (not jump hosts - a
    bastion asking interactively mid-chain is surprising; jump hosts are
    expected to carry fixed creds), so key/stored-password/opkssh are tried
    first and the prompt fires only when they fail or the server demands it
    (PAM `publickey,password,keyboard-interactive`). Always on, no setting.
    TRAP that cost a round of testing: two fail-fast guards in app.go
    (`sshConnectInternal` and the dynamic-inventory connect) refused a connect
    when `AuthRef == nil && PasswordOverride == nil` - they predate this
    feature and short-circuit it, so a fully credential-less connection never
    reached the prompt (a connection WITH a cred but no username already worked,
    since that path cleared the guard). Both guards were removed; the SSH layer
    now offers the interactive method regardless and still errors cleanly if the
    server has no method the prompt can satisfy.

36. **Infisical is the THIRD external secret backend - the per-request,
    zero-crypto sibling of Bitwarden (gotcha 34).** `internal/infisical` (no app
    imports, unit-testable) reads SSH secrets straight out of an Infisical server
    at connect time. Unlike KeePass/Bitwarden there is NO client-side crypto and
    NO master password: Infisical decrypts server-side and returns plaintext over
    TLS, so a resolve is a single HTTP read and the only secret is the machine-
    identity API key (Universal Auth client_id/client_secret, a normal `api_token`
    credential - identical to the Bitwarden API key). The chain (proven against a
    live server via a spike, since removed): `POST /api/v1/auth/universal-auth/
    login` {clientId, clientSecret} -> access token (30-day TTL) cached in memory;
    `GET /api/v1/workspace` -> projects + environments (the browse tree source -
    NOT `/api/v1/projects`, which 404s); `GET /api/v3/secrets/raw/<key>?
    workspaceId=..&environment=..&secretPath=..` -> {secret:{secretValue}}
    plaintext. A credential does NOT get a new `Kind`: it stays `password`/`key`
    with `StorageMode=external` and `config_json.infisical_ref {server_id,
    project_id, environment, secret_path, key}` - FIVE fields (vs Bitwarden's
    three) because a secret is addressed by project + environment + folder path +
    key, not one cipher UUID. A ref like `cloudflare/password` is folder path
    `/cloudflare` + key `password` (`splitSecretRef`). `sshlayer.
    InfisicalResolveHook` (package var, gotcha 28) is called right after the
    Bitwarden hook, before the kind switch; handled=false = "not an Infisical
    cred, fall through". Store v20 table `infisical_servers` holds only pointers
    (no master_ref, no last_hash - per-request, not synced). Freshness is NOT the
    Bitwarden full-vault-sync model: every resolve fetches the one secret fresh;
    on a failed fetch the LAST-KNOWN VALUE PER REF, sealed with the app vault
    under `<DataDir>/infisical-cache/<serverID>-<hash8(ref)>.sealed` (0600), is
    served marked `FreshStale` so an in-flight connect survives a brief outage.
    The in-memory access token is dropped in `VaultLock` via `forgetInfisical()`;
    a 401 triggers a transparent re-login + retry. Same WireGuard-only routing as
    Bitwarden (`infisicalClientFor` -> `wgDialerFor`; Netbird/Tailscale are
    sidecar-SOCKS only and fall back to a direct dial, so the settings dropdown
    offers WireGuard profiles only). Same credential_folders-vs-folders FK trap:
    the picker sends `folder_id: null` and the backend files auto-created creds
    under an "Infisical" CREDENTIAL folder. `EventsEmit("infisical_servers_
    changed")` gates the "From Infisical" connection-pane button live. The
    "Sync" affordance the Bitwarden settings has is replaced by "Test login"
    (there is nothing to sync - reads are per-request); it just verifies the API
    key logs in. Also wired into `ConnectionRevealPassword` (the v0.62.2
    copy-password regression class) alongside the keepass_ref / bitwarden_ref
    branches. Out of scope v1: write-back, non-Universal-Auth login, dynamic
    secret leasing, self-signed cert trust.

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

30. **NetBird lives in a SEPARATE module (`netbird-helper/`), built as a
    sidecar plugin - never import it into the main module.** netbirdio/
    netbird needs 8 go.mod `replace` directives (its own wireguard-go fork
    among them), which would silently swap the upstream `wireguard-go`
    that `internal/wg` runs on. The helper is a standalone binary
    (`ssh-tool-netbird[.exe]`) the app spawns; the main module has ZERO
    netbird imports. Pinned to netbird v0.73.2 with the exact replace set
    from that version - do not bump one without the other.

31. **NetBird helper: `WireguardPort=0` (random) is mandatory; Windows
    needs a CGO build.** Two traps cost a full debugging session:
    - The embedded netstack peer still binds a real UDP socket for the WG
      transport; the default is 51820. On a machine also running the real
      NetBird client (or Birdview, or a second helper), 51820 is taken -
      the bind fails, the half-built device is torn down, and the netstack
      tun panics on a double-close. The log says `wt0` and "creating
      tunnel interface", which looks like it's trying a real adapter, but
      netstack IS on (`IsEnabled()=true`); the real cause is the port
      clash. `embed.Options.WireguardPort = &zero` fixes it.
    - The helper's Windows binary must be built with `CGO_ENABLED=1` via
      the `wails-cross` docker image (zig + mingw, `CC=zcc-windows-amd64`).
      A plain `CGO_ENABLED=0` cross-compile misbehaves. Linux/macOS build
      native. (Same toolchain remote-tool uses for its in-process embed.)

32. **NetBird is desktop-only; WireGuard is everywhere.** `internal/wg`
    is pure-Go netstack and compiles for android, so WG profiles work on
    mobile. NetBird needs the sidecar helper PROCESS, which android can't
    spawn - `PluginsStatus` reports `supported=false` off desktop and the
    UI hides / disables the NetBird path there. Tailscale (gotcha 33) is
    the same: desktop-only sidecar.

33. **Helpers ship on their OWN `helper-vN` release, decoupled from the
    app version - and speak a versioned protocol.** Two sidecar kinds now:
    `netbird-helper/` (CGO, wireguard-go fork, needs Zig for the Windows
    cross-build) and `tailscale-helper/` (tsnet, pure Go, CGO-free).
    Both are separate go modules - never import either into the main app.
    Key points:
    - The helper `ready` event carries `"protocol":N`. The app declares a
      supported range in `internal/tunnelhelper` (minProtocol/maxProtocol,
      currently 1..1) and rejects a mismatch with an actionable error. A
      helper with no protocol field = 0 = the pre-split app-era build =
      "update the helper". See `checkProtocol`.
    - Helpers are built + published by `.github/workflows/helper-release.yml`
      on a `helper-v<N>` tag (major == protocol major), NOT by the app
      release. `release.yml` no longer builds helpers. The app downloads
      the newest `helper-v<=maxProtocol>` release at runtime
      (`updater.FetchGitHubHelperRelease`, `PluginDownload`), and
      "update available" compares the installed helper against that
      release, not the app version.
    - One `tunnelhelper.Manager` (`app.nbman`) drives BOTH kinds - it's
      keyed by profile id and just spawns whatever exe `pluginPath(name)`
      resolves. `ensureTailscaleTunnel` mirrors `ensureNetbirdTunnel`;
      `resolveHelperSecret` is the shared vault/credential lookup.
    - Design + migration notes: `docs/helper-release-plan.md`.

34. **Shared bastion pool: a pooled session must NOT close its jump
    prefix.** `Connect` normally puts every hop's client in
    `Session.stack`, and both teardown paths (`Disconnect` + the Wait
    goroutine) call `cleanup(stack)`, closing all of them. When the
    `JumpPrefixHook` (app `jumpPool`) hands back a SHARED bastion client,
    `Connect` skips building hops `0..n-2` and dials only the target
    through the shared client - so `stack` holds ONLY the target. Closing
    the shared prefix is the POOL's job (refcounted, via the `release`
    the session stores in `releasePrefix` and calls once through
    `releaseSharedPrefix`). If you ever put shared clients back into
    `stack`, `cleanup` will drop every sibling session behind that
    bastion. The share key is `ssh.JumpPrefixKey` - the resolved jump
    PREFIX (hosts+user+authRef of every hop except the target) plus the
    network-profile id, NOT the folder: two connections share iff they
    resolve to the same prefix. Teardown order on quit: sessions first,
    then `jumpPool.stopAll()`, then WG (a prefix may ride a WG tunnel).
    A connect that fails AFTER acquiring the prefix must release it - a
    `defer` in `Connect` (armed until `connectDone`) covers that, else the
    pool refcount leaks and the bastion never idle-stops. See
    `app_jumppool.go` + `docs/shared-bastion-design.md`.
    - Batch exec (`buildChainQuiet`) uses the SAME pool via `JumpPrefixHook`,
      dialing the target through the shared prefix with
      `dialChainFrom(initialPrev=shared, ...)`; its teardown composes
      `cleanup()` (closes only the target it opened) then `release()` (drops
      the pool ref). `dialChainFrom` with `initialPrev != nil` skips the
      TCP/network-profile first-hop dial and rides `prev.Dial` for the first
      hop, and its cleanup never closes `initialPrev`. VNC-through-jump
      (`BuildJumpChain`) is still deliberately NOT pooled - single console.

---

# Archive

Traps that aren't actively biting anymore but stay useful when modifying
the relevant subsystem. Cross-reference when working in the named module.

## RDM importer

### Jump-only entry skip
Before pass3 creates connection rows, the importer flags entries
whose `Group + "\" + Name` matches an existing folder path. Those
entries (e.g. "proxy" sitting next to a "proxy/" folder) are
registered in `connByName` for VPN resolution but NOT created as
standalone connections. Without this skip the tree ends up with a
phantom connection alongside its own folder of children.

A folder is only flagged as "existing" if some other entry's `Group`
references it, so a lone entry with no same-named children stays a
normal connection.

### Username location for key entries
`PrivateKeyOverrideUsername` (added to the Credentials struct)
carries the username for key-type RDM entries. Their `UserName`
field is empty; the importer reads from
`PrivateKeyOverrideUsername` instead.

### `PVE.VPNGroupName` and `Terminal.SSHGateways[]`
RDM's VPN reference shape is split across two field positions
depending on the entry vintage. The importer handles both. If you
see jump-host wiring break after a new RDM dump, check whether
either field shape changed.

---

## Wails v2 → v3 port history

### Autogenerated bindings location moved
v2 emitted into `frontend/wailsjs/`; v3 emits into
`frontend/bindings/`. The v2 path is gone. Strict-TS friction with
the autogenerated `convertValues` member persists in v3 - `api.ts`
still casts at the boundary.

### v3 dialog API split
v3 alpha.95 split open/save dialog builders;
`SaveFileDialogStruct` has no `SetTitle`. Save uses `SetMessage(...)`
instead. The shim in `wails3_runtime.go` papers over it.

### Wails v2 `EventsEmit` shape compatibility
The frontend `wailsRuntime.ts` shim was originally written so v2
subscription sites could survive the v3 wrapping (`WailsEvent.data`).
That shim now ships with the main branch as well - don't remove it.

### Promise<T | null> for pointer returns
v3 binding generator declares Go pointer returns as
`Promise<T | null>` because nil maps to JS null. `api.ts` exposes
`nn()` that throws on null and wraps the few callsites where we
deref (`sshConnect`, `connectionsCreate` etc).

### WSL GTK4 fragility
Without `WEBKIT_DISABLE_DMABUF_RENDERER=1` the webview either blanks
or the process hangs at start. `GDK_BACKEND=x11` forces X11 via
WSLg. Native Windows build doesn't hit this.

### `build/ios` placeholder breaks `go build ./...`
`build/ios/main_ios.go` depends on iOS-only files. Use `go build .`
(root only) or `task <os>:build`.

### Drag-out-of-window uses dragover heartbeat, not dragleave
`dragleave` fires whenever the pointer enters a child of the
listening node - can't be trusted to mean "left the container". We
track a timestamp on every `dragover` on the tabbar; when no event
has arrived in ~80ms while a tab drag is in flight, treat the
gesture as "dropped outside". See commit `0ea8394`.

### Windows tray icon needs `.ico`, not `.png`
`tray.SetIcon([]byte)` accepts raw image bytes by docs, but the
alpha.95 Windows backend silently drops PNGs or shows a blank
notification-area slot. Embed `build/windows/icon.ico` (real
multi-size .ico) for the tray to show. Other platforms haven't been
smoke-tested.

### Detached tab id is stable across reconstructions
Detached window's `paneTabs.addTab(...)` mints a fresh tabId when
it rebuilds its pane tree on boot. The window itself is addressed
by the *original* tabId (`detached-<originalTabId>`). Drag-to-
redock has to register the drag payload under the original key,
NOT under the local fresh tabId. See commit `afc85e3`.

### Detach + redock session lifecycle
- `WindowDetachTab(tabID, sessionIDs)` opens a new window
  with URL `/?detached=<tabId>&sessions=<csv>`.
- `WindowStartTabDrag(tabID, sessions)` registers a pending
  cross-window drag payload.
- `WindowAcceptTabDrag()` pops the payload (one-shot).
- `WindowRedockTab(...)` emits `window_redock` so the main window
  restores the tab.
- `WindowCloseSelf(name)` closes a named window without touching
  sessions.

Sessions live in the shared backend pool throughout; scrollback
survives the move (xterm scrollback is on the backend session, not
the Terminal component).

---

## Connect flow

### Jump host JSON shape
`{kind:"none"}` or `{kind:"chain", chain:{...JumpHostSpec...}}`.
`UnmarshalJSON` also accepts the legacy Rust-era flat shape
`{kind:"chain", hostname:...}` for backwards compatibility with
vaults from before the migration. See `internal/store/models.go`.

### Initial PTY output race
`pumpOutput` goroutines start inside `Connect()` before it returns
to `SshConnect`. By the time the JS Promise resolves and Terminal
mounts + calls `EventsOn`, the shell has already emitted its prompt
- dropped. Fix: at the end of Terminal.svelte's `onMount` (after
EventsOn is wired), a 50ms `setTimeout(() => notifyResize(), 50)`
fires SIGWINCH, which causes bash/zsh to redraw the prompt line.
Do NOT remove this timeout.

### Per-connection password (migration 7)
`connections.password_vault_key` stores a vault reference (key
`conn_pass:{connectionID}`) for direct password override.
`ResolvedSettings.PasswordOverride` carries the decrypted password
to the SSH layer - it has `json:"-"` so it never appears in the
resolved-settings preview or API responses. In `session.go`
Connect(), `ssh.Password()` is appended (after any key/agent
methods) only for the last hop (target). A connection with no
credential but a password set is valid - the early
`AuthRef == nil` bail in `SshConnect` was relaxed accordingly.

### Username fallback from credential
`session.go` Connect() and `app.go SshConnect` both fall back to
`cred.DefaultUsername` when the connection/hop has no explicit
username. Required for RDM-imported credentials that carry the
username in `default_username`, not in the connection overrides.

---

## Terminal

### Pane tree is a binary tree
`PaneNode = PaneLeaf | PaneSplit`. When you split a leaf, the OLD
leaf becomes one child of the new split; we never mutate the old
object's identity. Closing collapses single-child splits
automatically.

### Paste guard intercepts at the host div in capture phase
The capture-phase listener fires BEFORE xterm's textarea consumes
the paste. Single-line clipboard (counting a lone trailing newline
as still single-line) passes through unchanged; multi-line opens
PasteGuard. Per-session opt-out is component-local state - resets
on fresh tab.

### Shift+Enter needs preventDefault, not just return false
The terminal maps Shift+Enter to ESC+CR (`\x1b\r`) - the bytes
Alt+Enter already produces, which Claude Code and similar TUIs read
as "newline, do not submit". Returning `false` from
`attachCustomKeyEventHandler` is NOT enough: xterm still emitted a
second bare CR via onData, so the PTY got ESC+CR then CR and the
trailing CR read as a submit. You MUST also
`e.preventDefault(); e.stopPropagation()` to suppress that second
CR. (Confirmed by logging onData bytes: Alt+Enter = [27,13]; the
stray [13] is the one to kill.)

### Clipboard writes must go through writeClipboard(), not navigator
The mac WKWebView refuses `navigator.clipboard.writeText` in most
contexts ("clipboard unavailable") because the app isn't a secure
context with a guaranteed user gesture - the LLM system-prompt copy
hit this. `clipboard.ts` `writeClipboard()` tries the native Go
clipboard (`api.clipboardSetText`) FIRST, then falls back to
navigator; it always works on mac. `copyText` / `copySensitive`
route through it. Any new copy button must use one of those, never
raw `navigator.clipboard.writeText`. (The terminal selection copy
happened to work via navigator because the pane has focus, but it
was switched to writeClipboard too for consistency.)

### macOS notifications need a signed .app (UNErrorDomain error 1)
`RequestNotificationAuthorization` fails with `UNErrorDomain error 1`
(not-allowed) for an unsigned `.app` or a bare `bin/` binary - the
Wails notifier's `Startup` also refuses a binary with no bundle
identifier outright. There is NO code fix: macOS notifications
require the `.app` to be code-signed + notarized with an Apple
Developer ID, and the user to grant permission in System Settings.
The auth failure is logged best-effort and does not break anything
else (`SendNotification` just no-ops after). Windows/Linux are
unaffected.

---

## Dynamic inventory

### Provider config carries credential reference, not secret
`Manager.resolveSecrets` reads `api_token_credential_id` from the
config, looks up the credential, pulls the vault secret, and
inlines it as `api_token_secret` (plus `api_token_id` if the
credential has a token id - Hetzner doesn't). The on-disk config
keeps only the credential id. Providers stay unaware of the
indirection.

### Hostname source picker (Hetzner)
Hetzner has no DNS auto-record. The config has `hostname_source`:
`"name"` / `"public_ipv4"` / `"private_ipv4"`. `pickHetznerHostname`
falls back to name if the requested IP is missing.

### Proxmox cluster load-balancer
`/cluster/resources` returns the whole cluster regardless of which
node answers, so a single URL is sufficient - no per-node failover
logic needed.

### Dynamic entries don't persist as connections
`SshConnectDynamic` constructs a synthetic `store.Connection` in
memory from the cached entry + the dynamic folder's inherit
cascade, then runs through the standard resolver. No persistent
connection row is created. Tab connection_id is `dyn:<entryID>`.

### Ansible inventory: `-J` is the shorthand for ProxyJump
The ProxyJump regex only knew the long `-o ProxyJump=` form
originally, so inventories using the short `-J root@bastion`
(very common form in `ansible_ssh_extra_args`) silently fell
through and the user had to add the bastion chain manually.
`AnsibleParseJumpHosts` now tries `-J`, then `ProxyJump=`,
then `ProxyCommand=ssh -W`.

### Ansible jump credential needs explicit pick
Target host credentials almost never authenticate against the
bastion. The Ansible inventory file carries the jump *host*
but not its credentials, so the dynamic folder config gets a
**Jump host credential** picker (`jump_credential_id`). The
backend stamps that credential onto every parsed hop's
`AuthRef` at connect time. Without it the connect through a
bastion fails with "no auth method".

### Ansible host kind: `server`, not `guest_vm`
First version of the parser marked every Ansible host as
`KindGuestVM` so it landed in the "Guests" bucket alongside
cloud VMs. Wrong - Ansible has no hypervisor concept and the
"VM" badge was misleading. New `KindServer` ("server") buckets
them under "Hosts" with the same tower icon plain SSH
connections use.

---

## Multi-window

### Detached window close disconnects sessions
The detached window's close handler iterates its owned sessions
and calls `SshDisconnect`. The session count in the main window
then decrements via the existing `session_state` event.

### Visible counter is liveCount, not raw tab count
Terminal counter in the top bar reads `liveCount` from
SessionStore, not `tabs.length`. Otherwise closed/dead tabs that
hung around in the pool would still inflate the badge.

### Detach / redock must ship the full pane tree, not session IDs
Original detach IPC sent only a comma-separated list of session
IDs. The detached window then rebuilt the tab via `addTab()`
once per session, which flattened every split pane into its own
flat tab; redock did the same in reverse and the layout was
lost forever. Detach + redock now ship a URL-safe base64 JSON
blob of the full `PaneTab` (title, pane tree, group meta)
alongside the session list; both ends prefer the blob and fall
back to flat reconstruction only when it's missing. Internal
pane / split / tab ids are regenerated on restore so the moved
tab can't collide with ids already in the destination window.

---

## Misc

### Windows auto-update: CREATE_NO_WINDOW, not DETACHED_PROCESS
The helper `.cmd` that swaps the running .exe used to be spawned
with `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`. After every
auto-update a stray CMD window stayed on the user's desktop.
DETACHED_PROCESS only severs the *parent's* console; when we
exec'd `cmd.exe` (a console-subsystem binary), cmd allocated its
own console because none was inheritable. Swapped to
`CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP` so the window-less
behaviour is the actual flag, not a side effect.

### xterm swallows Ctrl+Tab / Ctrl+1..9
xterm.js' `attachCustomKeyEventHandler` defaults to returning
true (let xterm handle the key) for combos without a named
binding. Tab cycle / number-jump shortcuts therefore never
bubbled up to the window-level handler in `App.svelte` while a
terminal had focus. Terminal.svelte now explicitly returns false
for `Ctrl+Tab`, `Ctrl+1..9`, and `Ctrl+Shift+W/T` so the global
handler gets them.

### Tab switch leaves focus on the previous tab's xterm
Activating a new tab via shortcut or click flips
`.tab-content.active` in the DOM but doesn't move focus - the
previous tab's `.xterm-helper-textarea` keeps it, so keystrokes
go nowhere visible. Shared `focusActiveTerminal()` helper waits
two `requestAnimationFrame` hops (display flip + xterm settle)
then targets the textarea inside `.tab-content.active
.term-wrap.active`. Selector must include the `.tab-content`
gate - every Terminal component renders `.term-wrap.active`
because pane focus is per-pane and unrelated to which tab is
shown.

### QuickPalette modal stopPropagation killed its own nav
The palette stops keydown propagation on its modal so typed
input doesn't trigger global shortcuts (Ctrl+K, tab cycle, …).
That same stop also blocked the palette's own
`svelte:window onkeydown` listener - Arrow / Enter / Esc no
longer worked once focus was inside the search input. Nav now
runs inside the modal's keydown handler directly; the window
subscription was dropped.

### Argon2id was loosened from OWASP defaults
m=19MiB / t=2 / p=1 (interactive). OWASP recommends m=64MiB; that
took 10+s on WSL2 to derive. Threat model is a local file with
0600 perms, not offline brute force at attacker scale.

### Rotating log file
`DataDir/logs/app.log` (5 MiB, three historical files retained).
Path shown in Settings → Logs. `log_file.go` sets a MultiWriter
fanning into stderr + ring buffer + file. Per-process, single-
writer - don't run two app instances against the same log dir.

### Global `text-align: center` was inherited into tree rows
From `src/style.css` (Wails template default), made short names
look centered and the indent unreadable. Now `text-align: left`
explicitly on tree rows.

### Connect error visibility
DetailPane.svelte connect errors auto-scroll into view and have
`.connect-err` styling. `connectErrors.ts` maps 10+ Go/SSH error
substrings to friendly summary + hint; raw error hidden behind a
`<details>` toggle. Hop-prefix recognition ("Failed at jump host
bastion1") preserves which hop failed.

### tcpdump capture lives above the pane tree, not inside it
PaneNode is rebuilt on every layout mutation (split, SFTP-split,
closing one side of a split, drag, redock - all go through
`replaceLeaf`, which creates fresh split nodes). Anything
capture-related mounted inside that subtree dies on the rebuild.
So the tcpdump overlay is mounted once per session in
`TerminalArea` (above the tree), keyed by the stable `sessionId`,
and tracked in a window-local store (`tcpdumpStore.svelte.ts`).
PaneNode only reads the store for its toolbar icon. The capture
itself is a backend goroutine keyed by session, independent of
any window - `App.tcpdumpBySession` lets a window that received a
session via detach re-attach (`TcpdumpActiveForSession` +
`TcpdumpSnapshot`) instead of starting a second capture. The
modal's `onDestroy` deliberately does NOT stop the backend
capture (an unmount is usually a detach); only the explicit ✕
(`closeCapture`) does.

### tcpdump store: two version counters, not one
`tcpdumpStore` bumps `membershipVersion` on open/minimize/close
and `statsVersion` on setStats - separately. The mounted modal
pushes stats from an `$effect` on every render; if that bump also
invalidated the host's mount list, the host would re-render the
modal, re-running its effect, calling setStats again → infinite
loop (`effect_update_depth_exceeded`, which freezes the whole UI
mid-render). Mount/mode consumers read membershipVersion; stats
consumers (chip / status bar) read statsVersion; neither crosses.
setStats also guards against no-op equal-value writes.

### rAF is paused while a tab/modal is hidden
`requestAnimationFrame` doesn't fire when the element is
`display:none` (minimised modal) or the browser tab is
backgrounded. TcpdumpModal's packet flush coalesces via rAF, so a
minimised capture would queue packets that never flush. Fixed
with a `setTimeout(250)` fallback alongside the rAF - whichever
lands first flushes and cancels the other.

### tcpdump TCP flag parsing needs the proto fixed up
tcpdump prints TCP packets as `... 443: Flags [S], ...` with no
literal "tcp" token, so `ParseTcpdumpLine`'s L4-word capture used
to land on "Flags" and fall back to proto "ip". The insight
analyzer switches on `p.Proto`, so half-open / RST detectors
never ran on real captures. `Flags [` is now treated as an
unambiguous TCP marker in the parser. The plain unit tests built
`ParsedPacket` structs by hand and missed this; the integ tests
feed real tcpdump lines through the actual parser to catch it.

### tcpdump DHCP decode rejects mislabeled non-BOOTP
PacketCable / DOCSIS MTA provisioning rides UDP port 67 (the DHCP
app reuses 67 as source when forwarding to a collector), so
tcpdump labels it BOOTP/DHCP and prints garbage: op `unknown
(0xNN)`, `htype/hlen/hops 136`, scrambled IPs, one repeated xid
(which collapsed into a fake DORA with a dozen "Reply" stages).
`decodeDHCP` bails when the header shows a non-Ethernet htype/hlen
or an unknown op - the filter is on header content, NOT port, so
genuine DHCP on a non-standard port still decodes.

### tcpdump insight analyzer must evict flows (continuous-capture leak)
`InsightAnalyzer` keeps per-flow tracking state in `flows`
(keyed by the 4-tuple) and a `seen` de-dupe set. Both used to grow
without bound: a continuous capture on a busy host sees a new
ephemeral-port 4-tuple for every short-lived connection, so the
maps ballooned to multiple GB within seconds. Fixed by stamping
`lastSeen` on each flow, evicting flows idle past `flowIdleTTL`
(30s) in `Sweep` (already on a 1s ticker), and hard-capping both
maps (`maxFlows` / `maxSeen` = 20000) with oldest-first eviction
in `Observe` as a backstop. The ring buffer and the frontend
packet list were already capped - the analyzer was the one
unbounded structure. Regression guard:
`TestFlowTableBoundedUnderManyFlows` /
`TestIdleFlowsEvictedOnSweep`.
