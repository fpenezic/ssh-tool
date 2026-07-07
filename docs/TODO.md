# TODO

Backlog. Grouped by area, not strictly prioritised - pick what you
feel like. Items marked `(deferred)` have a captured discussion
explaining why they aren't urgent; revisit when the trade-offs shift.

For what's already shipped, see `CHANGELOG.md`.

---

## Connection ergonomics

- **Smart command autocomplete** *(deferred)* - three approaches
  captured in earlier discussions: shell history scrape, per-host
  cached command list, or local LLM. No clear winner; all add
  surface area for marginal gain.

- **Health probe** *(deferred)* - scale concern for 300+ connections
  behind VPN. Even a 5s timeout × 300 = 25min worst case if
  unreachable; needs careful concurrency + back-off. Design captured
  in earlier discussion.

- **Jump host auto/always mode** *(deferred)* - today a configured jump
  host is unconditional ("always"). A network profile has auto (probe
  direct first, tunnel on failure) / always / paused; the same could
  apply to a jump host: mode `auto` = probe a direct dial to the
  target, fall through the bastion only if that fails. The valid case
  is "target reachable directly when on the VPN, needs the bastion when
  off it". Not urgent - the far more common shape is `VPN -> bastion ->
  server`, which the first-hop-tunnel + jump-chain model already
  handles (the bastion is the first hop, so it rides the tunnel; the
  target rides the bastion). Adding it means a per-jump probe timeout +
  a mode field on `JumpHostSpec` + UI. Revisit if the direct-when-on-
  VPN scenario actually comes up.

- **Dynamic inventory: Proxmox notes / description** - would need a
  per-VM `/nodes/{node}/{type}/{vmid}/config` call (cached) since
  `/cluster/resources` doesn't carry it. Single extra HTTP per
  detail-pane open, low priority.

- **Dynamic inventory: more providers** - AWS EC2, Hetzner Robot
  (dedicated servers), DigitalOcean, libvirt. Pattern is established
  (see `internal/inventory/proxmox.go` and `hetzner.go`); each is
  ~150 lines.

- **Dynamic inventory: filter persistence per folder** - currently
  the visibility settings (hide-stopped, tag whitelist/blacklist)
  are in the folder config. UI exposes them but no "save as
  default" or sharable preset.

- **tcpdump tshark fallback** - if `tshark` is available on the
  remote, prefer it for protocol decoding instead of raw tcpdump
  with `-v`. Toggle in tcpdump modal.

- **tcpdump insights, more detectors** - v0.26.0 shipped the live
  network-health analyzer (UDP wrong-source-IP, half-open TCP,
  ICMP unreachable/redirect/TTL, ARP off-subnet, RST storm) with a
  per-flow `ip route get` check. Possible follow-ups: MTU/fragment
  black-hole detection, duplicate-IP/ARP-conflict, asymmetric
  routing across captured flows, DNS no-response. Detach re-attach
  recovers history from a 2000-packet backend ring - bumping that
  or persisting to disk is a separate call.

- **Remote tcpdump capture-to-pcap (maybe)** - open a second SSH
  exec session running `sudo -n tcpdump -i <if> -U -w - <bpf>`,
  stream pcap bytes into a local temp file, offer "Open in
  Wireshark". Defer until we decide it's worth the surface area.

---

## SSH layer

- **Agent forwarding** - opt-in per connection.
- **X11 forwarding** - opt-in per connection (low priority).
- **Per-hop opkssh cert refresh trigger** - currently we resolve
  auth once and reuse the cert through every hop. A stale cert on
  hop N+1 fails the chain. Pre-flight refresh per hop, or surface
  "refresh now" inline on connect failure.
- **Clear opkssh cache button on the credential detail.** When the
  remote opkssh-verifier is rotated, current cached cert becomes
  invalid until next interactive login. A "regenerate now" button
  in the credential editor would let the user pre-empt.
- **Connection retry / reconnect button on disconnect.**

---

## SFTP

- **Native drag-OUT (download)** *(deferred)* - WebView2 / WebKitGTK
  can't advertise drop-as-download cleanly. In-app "save as" works,
  but native DnD into Explorer / Finder is the user-facing gap.

---

## Remote GUI / consoles

Field ask: "MobaXterm has X11, can we?" Analysis below picks the
targets that fit a webview app and pay off, and parks the ones that
don't.

- **noVNC console tab - SHIPPED in v0.35.0.** Both targets landed:
  Proxmox VM/LXC console ("Open console" on a dynamic entry, reusing
  the inventory API token via vncproxy + vncwebsocket) and generic VNC
  (per-connection vnc_port + optional SSH tunnel, vault VNC password).
  noVNC bundles as a lazy chunk; a loopback Go websocket bridge
  (`internal/ssh/vnc.go`) relays RFB so the webview needs no custom
  headers / TLS-skip. VNC tabs are locked single-leaf (no split/SFTP).
  Confirmed working against a live PVE cluster (LDAP realm, API token).
  Key fix: pin the Proxmox API client to HTTP/1.1 - the vncproxy task
  starts but returns 500 on an HTTP/2 stream. Node (host) consoles are
  NOT offered: PVE's /nodes/<n>/vncshell rejects API tokens ("value
  'user@realm!token' does not look like a valid user name") - it needs
  a real user login, unlike guest vncproxy which accepts tokens. Guest
  VM/LXC consoles work.

  Open VNC follow-ups:
  - **Two-way clipboard - DONE (v0.35.1).** Ctrl+V / Cmd+V pastes the
    local clipboard into the remote (read via the native Wails clipboard
    IPC, since the webview blocks clipboard reads over the canvas), and
    the remote's RFB cut-text is mirrored back to the local clipboard on
    copy. See ClipboardGetText / ClipboardSetText in app_vnc.go and the
    keydown + "clipboard" event handlers in VncPane.svelte.
  - **Proxmox node (host) shell - IMPLEMENTED (v0.35.0).** PVE's
    vncshell rejects API tokens at a username-format check (a token must
    not get a root host shell), so node consoles use a real realm login
    instead: set a "VNC console login" (a password-kind credential whose
    name is the PVE username, e.g. user@ldap) on the Proxmox dynamic
    folder. The backend calls POST /access/ticket, then uses the
    PVEAuthCookie + CSRFPreventionToken for vncshell + vncwebsocket.
    Guest consoles still use the API token (no login needed). Without a
    login set, the node "Open console" returns a clear error.
  - A standalone "VNC host" tree node (vs the current per-connection
    toggle); clipboard copy *from* the remote (RFB cut-text).

- **X11 forwarding (parked)** - `ssh -X`: run a remote GUI app, its
  window appears on the local desktop. We can add a ForwardX11 /
  ForwardX11Trusted toggle + an X11 channel in the SSH layer, BUT it
  is useless without a local X server (VcXsrv / X410 on Windows,
  XQuartz on macOS) - which we cannot embed (MobaXterm bundles a full
  Cygwin X.Org; that's out of scope for a Go/webview app). Low value
  for the cost: the user reports using it rarely and doesn't want to
  install an X server. VNC covers the "see a remote GUI" need without
  any external install.

- **RDP (parked, hardest)** - no good pure-JS RDP client; the real
  path is an Apache Guacamole-style server-side proxy, which is
  infrastructure rather than a client feature. Revisit only if there
  is clear demand.

---

## Security follow-ups (from v0.16.0 audit)

Multi-agent audit ran 2026-05-28; Critical and High landed in v0.16.0.
Mediums + selected Lows tracked here.

- **Sidecar machine binding is hostname + username, not DPAPI**
  *(needs scoped project)*. On Windows the auto-unlock sidecar
  derives its key from `SHA256(appSalt | machineID | username)`
  where machineID falls back to `%COMPUTERNAME%`. Steal
  `vault.enc + vault.enc.local.key` + spoof the two env vars
  elsewhere and the sidecar decrypts. Fix: CryptProtectData
  (DPAPI) on Win, Keychain w/
  `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` on macOS,
  require `/etc/machine-id` on Linux. CLAUDE.md gotcha #14
  currently overclaims "DPAPI-style" - fix code, not docs.
  Deferred from the v0.25 quick-wins pass because it touches three
  platforms with per-OS FFI / CGO surface and needs its own test
  matrix (rotation across user accounts, machine-id absence,
  fallback chain). Treat as a standalone v0.x project.
- **Pending-restore staging is plaintext until next start.**
  `pending-restore/store.db` sits decrypted between Restore() and
  the swap that runs at next sql.Open. Seal it with the just-typed
  passphrase; ApplyPending unseals before rename.
- **Heap zeroing on Lock.** Derived 32-byte AEAD key and the
  `memory map[string]string` cache are GC'd, not wiped. Convert the
  cache to `map[string][]byte` so Lock() can scrub. Same for the
  passphrase `[]byte` in deriveKey / Init / Unlock paths.
- **Password override sent to server even after key auth succeeds.**
  A honeypot SSH server failing pubkey then accepting password can
  harvest the override. Mitigation: UI warning when both are set on
  the same connection; can't gate at config-build time.
- **Pre-auth banner painted to terminal.** `BannerCallback` writes
  raw server bytes before HostKeyCallback returns. xterm.js sanitises
  most dangerous sequences but title-set / OSC slip through. Buffer
  banner; emit only after auth completes.
- **Vault on-disk size leaks entry count + secret-length class.**
  `MarshalIndent` plus per-entry base64 ciphertext (no padding) makes
  the file size profile distinctive. Pad ciphertexts to size buckets
  (64 B / 1 KiB / 4 KiB / 16 KiB) before sealing.
- **Inventory: Proxmox `base_url` accepts arbitrary hostnames.**
  Token sent in Authorization header against whatever URL the user
  pastes. Surface the resolved host before first refresh; consider
  reusing the SSRF guard from `FetchArchiveURL` here.
- **Backup-layer tests + vault-layer tests.** Both crypto layers have
  zero unit coverage; the audit found bugs that a basic round-trip
  test would not catch but a tamper test would (flip header byte,
  expect Open() failure under v2).
- **Stray DEBUG log in app.go around `ConnectionsCreate`.** Removed
  per audit Low; keep an eye out for new ones before open-sourcing.

From the 2026-06-09 review (sha256 verification + backend-derived
update params + download progress shipped right after; what remains):

- **Signed update manifest.** sha256 verification against
  `/api/latest` is in, which covers MITM / CDN tampering - but a
  compromised release server can still rewrite hash and binary
  together. Proper fix: ed25519/minisign-signed manifest with the
  public key baked into the binary. Needs key management + signing
  in the publish pipeline; standalone project, realistic priority
  once the app goes open-source / distribution widens.

---

## Sync

- **Cert pinning for WebDAV sync (TOFU)** - if self-signed demand
  appears, the right tool is trust-on-first-use by certificate
  fingerprint (same UX as SSH host keys), NOT an InsecureSkipVerify
  toggle. Today: https enforced (loopback exempt), Go default chain
  verification, private CAs work via the OS trust store.
- **Auto-sync pull UX** - shipped: push-on-change (mtime signal +
  quiet period), flush-on-quit, periodic remote check with
  notification. Possible follow-up: one-click "pull and relaunch"
  instead of pull + manual reopen.
- **S3 / additional transports** - the sync engine is
  transport-agnostic; WebDAV is just the first backend.

## Vault / credentials

- **Manual "Lock vault now" from status bar.** Previously wired and
  removed when the status bar got busier. Re-add as a small button
  next to the vault state pill, or as a Ctrl+Shift+L shortcut.
- **Bulk credential rotation** - select N password creds, walk
  through them with a "Set new password" prompt, push to each
  remote via the ssh-copy-id helper (next item).
- **SSH key deployment helper (ssh-copy-id-style)** - given a
  credential's public key, push it to a target's `authorized_keys`
  after authenticating with another method. Useful for first-time
  setup on a new host.
- **Hardware key (FIDO2 / YubiKey) support** - `x/crypto/ssh` has
  limited support; `ssh-agent` forwarding to the platform agent
  may be the realistic path.
- **Biometric vault unlock** *(investigated, parked)* - Windows
  Hello / Touch ID / Linux Polkit. WebAuthn API would need to be
  bridged from Wails since the webview's WebAuthn calls don't see
  platform authenticators by default. Deferred.
- **HashiCorp Vault / Vaultwarden sync** - `kind=external` credential
  that fetches the secret at use-time from a remote secret store.
- **History retention** - opt-in keep last N old password values for
  rollback. Backed by the existing vault, key `conn_pass_history:{id}`.

---

## Dynamic inventory providers

- **Terraform state file provider** - local `terraform.tfstate`
  first, remote S3 / GCS later. `aws_instance.*` →
  `KindGuestVM`, tags from `tags.Name`, hostname from
  `public_ip` / `public_dns` / `private_ip` (configurable).
  Skipping Terraform Cloud API for now.
- **Ansible dynamic scripts** - `./inventory.py --list`
  (`Provider.ConfigSchema { source_type: "dynamic_script" }`),
  with the script's exit code surfaced as a refresh error.
- **Remote Ansible sources** - git repo (clone shallow on
  refresh) + HTTP URL (single file).
- **Ansible `group_vars/` / `host_vars/` directories** - parse
  side-by-side files alongside the inventory main file. Skipped
  for the MVP "single file" constraint.
- **Configurable retention slider for password history** -
  hard-coded keep-last-5 today. Slider in Settings → Vault.

---

## Import / Export / Sync

- **RDM XML import** - only if anyone still has an XML-only export.
- **Git-as-sync** - point ssh-tool at a git repo; pull on launch,
  commit on change. Encrypted credential payload would need to be
  scrubbed before commit - possibly an `export-without-secrets`
  mode.
- **Cloud-folder sync between machines** - Drive/OneDrive/Dropbox
  folder as the transport. MVP would reuse the existing encrypted
  backup format: a "sync folder" path in Settings, manual Push /
  Pull buttons, optional auto-pull on launch + auto-push on quit
  gated on ModTime. Last-write-wins; pre-restore snapshot is the
  undo path. Concurrent edits on two machines will lose one side
  of the diff - explicitly out of MVP scope. Per-entity diff sync
  (real conflict resolution, tombstones, updated_at, 3-way merge)
  is a much larger v2 if the simple flow proves insufficient.
  Alternative path: a central service for connection metadata
  sync with the vault kept strictly per-machine - that doubles as
  a security feature (one vault key per host).

---

## OS integration

- **Windows 11 top-level context menu** - the classic registry verb
  lands under "Show more options" on Win11 by design. Appearing in
  the default right-click menu needs an IExplorerCommand COM DLL +
  sparse MSIX package with identity, which in turn needs a trusted
  code signature - revisit together with the code-signing story
  (docs/why-not-signed). msix scaffolding exists in build/windows.
- **macOS "Open in ssh-tool" Finder action** - Windows Explorer and
  Linux (Dolphin/Nautilus) shipped; Finder needs a Quick Action
  (Automator workflow) or Finder Sync extension bundled into the
  .app - packaging work, revisit with the macOS build.

## Window management

- **Save/restore window layout** between launches. Currently
  workspaces snapshot tab grouping; pane splits inside a tab are
  rebuilt by sshing fresh, not from a saved layout.
- **Reopen last session: multi-pane splits.** Reopen restores SSH,
  dynamic-inventory and local-shell tabs (title + group), but pane
  splits inside a tab collapse to the active leaf. Same restore gap
  as workspaces - fix both together.
- **Multi-pane split restore** inside a workspace.
- **Remember detached-window positions** by tab id (per-monitor).

---

## Broadcast

- **Persist broadcast groups across restarts.** Today groups live
  only in backend RAM - relaunch wipes them. Settings KV is the
  obvious place to stash `broadcast_groups_v1` as a JSON snapshot;
  hydrate at startup, save after every mutation.

---

## Terminal

- **Sixel / image rendering** - niche but nice for users who pipe
  graphs through SSH (matplotlib, btop, etc). xterm.js has a sixel
  addon.
- **Scrollback search beyond visible** - Ctrl+F currently searches
  what's rendered + recent scrollback; large historical buffers
  may need an indexed approach.
- **Font ligatures toggle** - JetBrains Mono ligatures look nice in
  some prompts and ugly in others.
- **Session recording follow-ups** - "include scrollback at start"
  option and plain-text export (ANSI stripped). Recording + in-app
  playback (browser, scrubber, speed, idle-skip) shipped; these are
  the leftovers.
- **Translucent terminal window (macOS, experimental)** - parked
  until Wails v3 is stable. Needs xterm `allowTransparency` (WebGL
  perf hit / renderer fallback), NSVisualEffectView + WKWebView
  compositing is flaky in the alpha (resize flicker), and themes
  assume a solid background across the whole shell. If ever: opt-in
  setting, macOS only, default off.

---

## UI / UX polish

- **Connect retry on transient failures** - DNS, ECONNREFUSED with
  back-off, single auto-retry, surface as a inline pill not as a
  fresh error toast.
- **Hover preview for jump chain** - tooltip on the conn row
  showing the resolved chain (bastion → target).
- **Conditional formatting on tag colours** - high-contrast
  override for the colorblind palette.
- **Command palette: more commands.** Ctrl+K now carries app
  commands (">" prefix: settings, lock vault, local shell, update
  check, open workspace). Candidates for the next pass: new
  connection / folder, start backup now, toggle theme, open
  specific Settings sections.

---

## Settings / app config

- **Remaining hardcoded knobs** - paste-guard threshold (multi-
  line cutoff). Density, font size, font family, scrollback limit,
  connect timeout, vault auto-lock all wired.
- **Auto-update channel** - currently update *check* exists; auto-
  download + relaunch flow not built. Opt-in.

---

## Packaging

- **Intune / managed Windows deployment.** PE version resource
  already ships (windows:generate:syso normalizes `git describe` to
  numeric x.y.z.w), so file-version detection works. Order of work:
  (1) NSIS per-user installer into `%LOCALAPPDATA%\Programs\ssh-tool`
  so the self-updater keeps working without admin (Program Files
  would break the apply script); (2) code signing - Azure Trusted
  Signing is the cheap route, unsigned exe trips SmartScreen on a
  fleet; (3) wrap as Win32 .intunewin, detection rule "version >="
  (not "==" - self-updated clients drift ahead of the Intune catalog
  and exact-match causes reinstall loops). Optional follow-up: HKLM
  policy (`Software\Policies\ssh-tool`) to disable the in-app
  updater for centrally-managed installs - today
  `update_check_disabled` is a per-user DB setting.
- **Linux .AppImage** - Taskfile + AppImage config exist; needs
  smoke testing.
- **Linux .deb / .rpm** - `nfpm` config exists; needs sign + publish.
- **Windows .msi / NSIS** - installer config exists; code-signing
  cert acquisition + EV process is the open question.
- **macOS universal** - Taskfile + Info.plist exist; needs Apple
  Developer ID + notarisation.
- **macOS NetBird helper.** The `build-helpers` CI job builds the
  NetBird sidecar for Linux (amd64/arm64) and Windows (amd64) only.
  A macOS helper needs a signed + notarised binary or Gatekeeper
  blocks the spawn - same signing story as the app. Add `darwin-amd64`
  / `darwin-arm64` legs once macOS signing exists; `PluginsStatus`
  already reports `supported=true` on darwin, so until then a mac user
  sees the plugin as available but the download 404s. Consider gating
  the darwin download until the asset ships.

---

## Android / mobile

Android shipped in v0.36.0 (boot, vault unlock + biometric, connections,
live SSH, sync, broadcast, foreground-service background survival, opkssh
browser login). v0.37.x added live-test UX fixes; v0.38.x renamed the app
id off the scaffold default and stamped the build version. Built locally
(`task android:package`), out of CI. Remaining for a real distributable:

- **Release keystore + signed release build.** Builds are debug-signed
  (`assembleDebug`) - fine for sideload from one machine, but a stable
  release keystore (generated once, stored OUTSIDE the repo, never lost -
  losing it means no existing install can ever update) is the prerequisite
  for: a real update flow, Play, and F-Droid. Switch the android Taskfile
  to `assembleRelease` + a gradle signing config once the keystore exists.
- **In-app auto-update (deferred - decided to skip for now).** Desktop's
  binary-swap updater can't work on android (sandboxed APK, PackageManager
  owns the install). The only no-Play path is: download the APK + fire an
  install Intent (needs `REQUEST_INSTALL_PACKAGES` + a FileProvider + the
  update APK signed with the SAME key as the install). `CheckForUpdate`
  already works on android (it matches the `android-arm64` asset key);
  only the apply step is missing. Cheapest interim step if revisited: an
  "update available -> open the sshtool.app download link" prompt on
  mobile (no install Intent, manual install). Blocked on the release
  keystore above for the real auto-install.
- **Distribution: Play vs F-Droid.** F-Droid fits an OSS app with no
  account fee and no Play dependency (reproducible build + open source,
  both planned anyway). Play needs a (new) Developer account + the $25 fee;
  the previous Play Console account was dormant-closed (not policy-banned),
  so a fresh account is the path if ever needed. Sideload via sshtool.app
  works today with zero store dependency.
- **Android in CI.** CI builds desktop only; android stays a local NDK
  build. Add an android job once the project goes open-source. There was
  never an android CI step, so nothing to disable now.
- **iOS build.** Build tags already cover it (`android || ios`) but no iOS
  build is produced or tested. Needs a Mac toolchain + Apple Developer.
- **VNC / SFTP on mobile.** `app_vnc.go` is `!android`; VNC tabs untested
  on a phone. SFTP open-dir returns an error on android.
- **Wails runtime version.** The android IPC transport is hand-rolled
  (`src/lib/androidTransport.ts`) because npm `@wailsio/runtime` is
  alpha.79 (no android transport). Revisit when the published runtime
  catches up. Likewise the Go->JS event long-poll (`MobilePollEvents`)
  could become a native push transport (custom `application.Transport`
  dispatching via JNI `executeJavaScript`) - deferred; the long-poll
  works and settles to a clean cadence.

---

## Misc

- **Crash / panic reporting** - opt-in (off by default), sanitised.
- **Onboarding tour** - first-launch walkthrough.
- **Internationalisation** - Croatian + English at least; structure
  UI strings via a small i18n shim.
- **Accessibility audit** - tab order, ARIA labels, keyboard nav
  across modals.

---

## Tech debt

- **api.ts cleanup** - decide if the plain-interface facade stays,
  or wire a Wails autogen post-processor that emits cleaner TS
  without the `convertValues` class member. Right now we cast at
  the boundary.
- **Tests for vault layer** - file_vault, machine sidecar, end-to-
  end facade. Also cover Lock() wiping the memory mirror and Put()
  refusing writes when locked (the regressions that motivated
  v0.12.8).
- **Tests for backup layer** - round-trip Create/Restore, checksum
  failure path, pending-restore swap, scheduler interval gating and
  prune-N retention.
- **Tests for SSH auth resolution** - ResolveAuth paths,
  `ssh.AuthMethod` shape per credential kind.
- **slog migration** - verbose lines (opkssh, ssh hops, vault)
  would benefit from field-based filtering. Rolling log file
  exists; structure is the missing piece.
- **`as any` cast cleanup** in `api.ts` - once the autogen post-
  processor lands.
- **Dynamic inventory: error visibility** - last error currently
  hidden behind a red `!` dot tooltip. Could surface as a toast +
  retry button if the user is actively expanding a broken folder.
