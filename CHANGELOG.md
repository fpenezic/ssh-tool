# Changelog

All notable changes to ssh-tool are documented here.
Format loosely follows [Keep a Changelog](https://keepachangelog.com/).
This project uses SemVer with a `0.x` prefix while Wails v3 remains
in alpha upstream.

---

## [0.59.0] - Read secrets straight out of KeePass

### Added

- **KeePass as a live credential backend.** Point ssh-tool at a KeePass
  `.kdbx` file and reference its entries directly: the password (or
  private key) is read out of KeePass at connect time and never copied
  into ssh-tool's own store. KeePass stays the source of truth - the
  file is opened read-only and never written to.

  - **Register databases in Settings -> KeePass.** Local file (with a
    native Browse button so you don't type the path by hand), or remote
    over WebDAV / SFTP. The database's master password (and optional key
    file) are sealed in this app's vault, so unlocking ssh-tool once
    opens KeePass too - no second prompt per connection.
  - **Reference an entry straight from the connection.** Next to the
    Credential picker on any connection or folder there's a "From
    KeePass" button (shown only once you have a database registered): it
    opens the database as a searchable group tree, and picking an entry
    creates (and assigns) a credential for it in one step - no need to
    hand-build a credential first. Picking the same entry again reuses
    it. Auto-created KeePass credentials collect under a "KeePass"
    credential folder and show a database icon (labelled "keepass") so
    they're easy to tell apart from vault passwords. The credential
    editor's "From KeePass database" kind does the same thing when you
    want to create one up front. You choose the entry's password, a
    custom field, or an attachment (for a private key stored as a file
    inside KeePass); entries are referenced by their stable UUID, so
    renaming or moving them in KeePass doesn't break the link.
  - **Remote databases stay fresh, safely.** A remote `.kdbx` is fetched
    when you unlock and again whenever the cached copy is more than a few
    minutes old, using a conditional request so an unchanged file isn't
    re-downloaded. If the remote is unreachable the last cached copy is
    used and you're told it's stale rather than silently authenticating
    with old data. A Refresh button (in Settings and in the entry picker)
    forces a pull after you've just added an entry in KeePass. The cached
    file is stored encrypted - it's the original KeePass blob, worthless
    without the vault-held master.
  - Decrypted databases live in memory only and are wiped the moment the
    vault locks, exactly like the vault's own secrets. opkssh is
    unaffected - it keeps its own vault-backed lifecycle.

---

## [0.58.0] - Share a live session to a web browser

### Added

- **Share a live SSH (or local) session to a plain web browser.** Turn
  it on in Settings -> Sharing, then right-click a tab and choose
  "Share to browser". A colleague opens the link in any browser - no
  ssh-tool needed on their side - and watches your terminal live.
  Read-only by default, or full control, where the guest types into the
  same terminal as you (tmux-style).

  - **You approve every guest.** When someone opens the link you get a
    prompt showing their IP and a short word-code (like
    "cobalt-otter-viola-medley"); you confirm that code with them
    out-of-band, so a leaked link is worthless without your approval.
  - **You pick which tabs to share and which network interface to serve
    on**, and whether the guest sees the existing scrollback or only new
    output. A full-control guest is shown loudly - a red banner and a
    tab marker - and can be kicked, or all sharing stopped, in one click
    from the status bar.
  - **Live layout.** Splitting a shared tab, adding a tab to a share, and
    switching tabs all follow through to the guest; the guest can follow
    your active tab or look around on their own.

  The connection is encrypted (a self-signed certificate whose
  fingerprint is the word-code you compare). Both machines must be able
  to reach each other - use it on a LAN or over an existing VPN /
  WireGuard profile; there's no cloud relay. If the chosen port (8443 by
  default) is already in use, the share falls back to a free one.

- Also fixed a long-standing bug where breaking a split into separate
  tabs (ungroup, or popping a pane out) named the new tabs with a raw
  session id instead of the connection name.

- **Dragging the window between monitors with different scaling** (say a
  125% laptop screen and a 100% external) no longer leaves the interface
  rendered tiny at the old scale (Wails runtime bump).

### Known issues

- Grouping two already-shared tabs and then ungrouping them drops the
  second tab from the share; re-add it with "Add to share". A share of a
  single tab, splitting/ungrouping a single shared tab, and adding tabs
  all work correctly.

---

## [0.57.0] - Sending batch results to someone

### Added

- **"Copy all" in the batch command runner.** Running a command across
  a dozen hosts is easy; handing the result to a colleague was not.
  Dragging a selection across the results picked up the output but
  dropped the host names, so what you pasted was a wall of text with
  nothing saying which host each part came from.

  The button copies everything with each host named, and pastes into
  Teams, Slack or Outlook as a heading per host with its output in a
  real code block. An editor or a shell gets the same content as plain
  text. Healthy hosts are just name and output - the exit code and the
  timing are noise to whoever you are sending this to. A host that timed
  out or exited non-zero says so, because there the absence of output is
  the whole point.

  Dragging a selection still works and now picks up the host names too;
  that path stays raw (timings and exit codes included) for when you
  want exactly what is on screen.

- **Ctrl+C copies a selection in Windows copy/paste mode**, the way
  Windows Terminal and PuTTY do, and clears the selection as it goes -
  so the next Ctrl+C interrupts as usual. With nothing selected it is
  SIGINT, unchanged. Linux and macOS modes are untouched: selecting
  already copies in Linux mode, so Ctrl+C stays an interrupt there.

- **Selecting text in Linux mode now confirms the copy** with a toast.
  It was already copying silently, which left you guessing.

---

## [0.56.0] - Dead sessions get noticed; recording asks first

### Fixed

- **A session whose chain died silently now gets noticed.** Behind a
  jump host, a session could hang forever with the tab still green:
  keystrokes went nowhere, no output ever came, and the only way out
  was closing the tab by hand.

  Nothing below the SSH layer can see this happen. The TCP socket
  your machine owns goes to the JUMP, and the far hop rides inside it
  as an SSH channel - so when a firewall or VPN on the jump's far side
  drops that flow without notice, your socket stays perfectly healthy.
  The jump is still up; the kernel is right. Only a probe travelling
  the whole chain and back can tell that the far end has stopped
  answering.

  That probe now always runs. Keepalive set to 0 means "send no
  keepalive traffic", not "never notice that the link is gone", so a
  session with no keepalive configured still gets a slow
  detection-only probe (once a minute). Probes also gained a deadline
  - previously one could block forever on a dead chain, which is why
  even a configured keepalive prevented drops without ever detecting
  one. When a probe goes unanswered the session now disconnects the
  normal way: the tab turns red, and auto-reconnect kicks in if you
  have it on.

- **Launching ssh-tool while it is already running raises the existing
  window** instead of quietly starting a second copy of the whole
  application. Two instances each opened the database and each held
  their own picture of your connection tree, which meant one could
  overwrite an edit you had just made in the other, with no error
  anywhere.

### Added

- **Recording asks before it starts.** A recording writes everything
  the session prints to a plaintext file - a config you `cat`, a token
  a command echoes back - so it no longer begins on a single
  unconfirmed click. The prompt names the destination folder and can
  be turned off ("Ask before starting a recording") if you record
  routinely.

- **Session recording is its own settings section** (under Security),
  instead of being buried at the bottom of the Terminal page. It
  decides whether the contents of your sessions land on disk
  unencrypted, which is where it belongs.

---

## [0.55.2] - Numeric fields could not be saved

### Fixed

- **Keepalive was impossible to set.** Typing a value into the
  keepalive field (on a connection or a folder) lit up the Save
  button, but clicking it did nothing at all - no error, no toast,
  nothing saved. The same silent dead-end hit the VNC port field,
  the keepalive and port fields in the batch editor (Apply did
  nothing), and the "add port override" button in the tcpdump
  capture dialog.

  All of these are `<input type="number">` fields whose backing
  variable is declared as a string. Svelte hands back a number for
  a numeric input as soon as the user types, so the save handler's
  `.trim()` call threw before it ever reached the backend, and the
  exception was swallowed. The dirty check compared the same number
  against a string, which is why Save lit up but nothing else
  happened. The numeric fields are now normalised before use.

---

## [0.55.1] - Android build fix

### Fixed

- **Android build.** The v0.55.0 auto-unlock strength warning added a
  `SidecarStrength` helper to the desktop machine layer but not to the
  Android stub (auto-unlock is disabled there), breaking the Android
  compile. Added the parity stub (always reports "none"). Desktop
  unaffected.

---

## [0.55.0] - Security hardening

A pass over the newer network- and LLM-facing surfaces. No behaviour you
rely on day to day changes; the defaults just got safer.

### Changed

- **Give internet now only reaches the public internet by default.** The
  reverse proxy dials out from this machine's network, so previously a
  process on the borrowing server could ask it to reach your own localhost
  or private LAN. It now refuses internal / private / loopback / link-local
  targets (including cloud-metadata `169.254.169.254`) unless you tick the
  new **Allow reaching my local/private network** box in the tunnels
  popover. Names are resolved on this side, so a hostname pointing at an
  internal IP is caught too.
- **The LLM activity log no longer stores command output by default.**
  Output could contain secrets the LLM read (a `.env` file, environment
  variables, kubernetes secrets), and the audit log is a plaintext file on
  disk. The command, gate decision, and exit status - the actual audit
  trail - are still recorded. A new **Also store command output in the
  audit log** toggle (under the audit setting) re-enables full capture if
  you want it.
- **The local MCP bridge socket now requires a token too.** The
  cross-boundary TCP leg already did; the primary socket now matches it, so
  an unrelated local process that finds the socket can no longer attach and
  read shared-session scrollback. The bridge subprocess reads the token
  from a `0600` file, same as before - no setup change.

### Fixed

- **The LLM can now see dynamic-inventory hosts.** `list_connections` (the
  MCP bridge search) only returned saved connections, so hosts pulled from a
  Proxmox / Hetzner / cloud dynamic folder were invisible to a connected LLM.
  They now appear (marked "dynamic") and `connect` can open them.

### Added

- **Auto-unlock strength warning.** On platforms where the machine-bound
  auto-unlock uses the older format (macOS, or a container with no
  `/etc/machine-id`), the Vault settings page now says so - its key can
  fall back to the hostname, which is weaker binding. Your passphrase and
  the encrypted vault are unaffected; this only concerns the convenience
  auto-unlock.

---

## [0.54.0] - Multi-window tab moves, split/detach fixes

### Added

- **Send a tab to another window.** Right-click a tab and pick
  **Send to <window>** to move it to any other open window - useful for
  pushing a terminal onto a second monitor without opening a new window.
  (A native drag can't cross window boundaries in a WebView, so this is a
  menu action.) The session stays live; works between the main window and
  detached windows in any direction.
- **Move a single pane to its own tab.** A split pane's toolbar now has a
  button to pop just that one pane out into its own tab, leaving the rest of
  the split intact (unlike "Ungroup tabs", which splits every pane out).

### Fixed

- **Detaching a tab no longer spills query-response garbage into the remote
  shell.** Rebuilding the terminal on detach replayed the scrollback, which
  could make xterm re-answer terminal queries (colour / device-attributes) in
  the replayed history and send those answers to the shell as junk like
  `2RR0;276;0c10;rgb:...`. Those responses are now suppressed during replay.
- **Redocking a detached window brings back all its tabs.** Previously only
  the first tab returned and the rest were lost with their sessions left live
  in the background (visible in the counter and as green in the tree, but
  unreachable).
- **The connection detail header stays visible while the form scrolls** - the
  name, Save, Connect, Use-different-credential and Delete actions no longer
  scroll out of reach on a long form.
- **Connect failures also raise a toast**, so a failed connection is visible
  even when you've scrolled the form down past the top-of-form error banner.

## [0.53.0] - LLM activity log, notifications, and MCP polish

### Added

- **LLM activity log.** Everything a shared LLM does - run, type, connect,
  read - is now recorded and visible in an **LLM activity** panel: the
  command, whether it auto-ran or needed your approval, the exit status, and
  (for runs) the captured output, expandable per row. Open it from the robot
  icon in the status bar (all sessions) or from a session's Share-with-LLM
  popover (that session). It reads newest-at-bottom like a terminal and
  auto-scrolls. A toggle under LLM settings also keeps a durable copy in the
  audit log so it survives restarts.
- **Desktop notifications + taskbar flash for prompts that need you.** When
  the app is in the background and something blocks on you - an LLM approval
  request or a host-key confirmation - ssh-tool now flashes the taskbar and
  pops an OS notification with the actual ask ("An LLM wants to run a command
  on <host>"), so a prompt you're waiting on from your LLM client in another
  window doesn't sit unseen. The notification is opt-out (on by default) under
  LLM settings; the flash clears when you focus the window or answer.
- **System-prompt doc for LLM clients.** `docs/MCP_SYSTEM_PROMPT.md` is a
  ready-to-paste system prompt that teaches an LLM client how to use the
  ssh-tool tools well and safely (start with list_sessions, treat terminal
  output as untrusted, respect approvals). Drop it in your CLAUDE.md (Claude
  Code) or the system prompt (LM Studio).

### Fixed

- **A session the LLM opened via connect now shows a terminal tab.** Previously
  a headless connect from the MCP bridge left the session live but without a
  visible tab; it now appears and switches into view.
- The status-bar robot icon only shows when a session is actually shared with
  an LLM (it was showing whenever the bridge was enabled).
- The LM Studio `mcp.json` snippet now escapes the Windows binary path so it's
  valid JSON.

## [0.52.0] - Give internet + share a session with an LLM

### Added

- **"Give internet" - one-click reverse proxy for a server with no
  outbound net.** Open the tunnels popover on a connected session and
  click **Give internet**: ssh-tool raises a reverse tunnel on the
  server (loopback `127.0.0.1:3182` by default, overridable) and serves
  the proxying itself - no squid, no external tooling. It shows a ready
  to paste `export http_proxy=...` block; run that on the server and its
  HTTP/HTTPS traffic (apt, curl, wget, pip, dnf) flows out through your
  machine. DNS is resolved on the ssh-tool side, so the server does not
  need a working resolver for proxied traffic. The running proxy appears
  in the popover with live byte counters and a Stop button, and tears
  down automatically when the session disconnects.

- **Share a live session with an LLM (MCP bridge).** You can now attach
  an external LLM client (Claude Code, etc.) to a running SSH session so
  it can help you debug - read what's on screen, pull logs, propose and
  run commands. It's off by default: enable it under
  **Settings -> LLM (MCP) access**, register ssh-tool once with your LLM
  client, then share a specific session with the **Share with LLM**
  button in the pane toolbar (read-only or read+run). The registration command
  is shown for Claude Code and as an `mcp.json` block for LM Studio (any
  MCP client works the same way). The bridge is local-only (a unix
  socket on Linux/macOS, a loopback pipe on Windows) - nothing is
  exposed to the network, and no session is reachable until you share
  it. Read-only commands run on their own; anything that could change
  state pops an approval prompt where you Run it, type it into your
  terminal without pressing Enter, or Deny. The LLM can also **search
  your saved connections and open one** (by name/folder only - hostnames
  aren't exposed until a connect, which you approve, and the new session
  is then shared automatically). A shared session shows a badge on its
  tab so you always know what the LLM can see. If your LLM client runs
  in WSL while ssh-tool runs on Windows, enable the optional
  token-guarded loopback-TCP listener. Terminal output handed to the LLM
  is treated as untrusted data, never as instructions. Grants are
  cleared when the session disconnects. Desktop only.

## [0.51.0] - Credential expiry + dark dropdowns on Linux

### Added

- **Credential expiry dates.** API tokens, setup keys and auth keys are
  usually time-limited - you can now set an **Expires** date on a
  credential (API token, password, or SSH key) when you create or edit
  it. The credential list shows an amber "expires in Nd" badge when one
  is within two weeks of lapsing and a red "expired" badge once it has,
  so a dead token is visible at a glance instead of surfacing as a
  mystery auth failure. Leave the date blank for no expiry.

### Fixed

- **Native dropdowns rendered white on the dark theme (Linux).** The
  `<select>` popup lists (credential kind, conflict mode, ...) drew
  white-on-white under WebKitGTK because the engine wasn't told the UI
  is dark. They now follow the theme. Windows was unaffected.

## [0.50.0] - Tailscale + helpers on their own release track

### Added

- **Tailscale network profiles.** Tailscale joins WireGuard and NetBird
  as a userspace tunnel kind: route a connection's first SSH hop through
  your tailnet with no TUN adapter and no admin. Like NetBird it runs as
  an optional one-click plugin and each machine registers as its own
  node, so a synced profile is safe across machines. A profile takes a
  control URL (blank for Tailscale's own, set for self-hosted
  Headscale), a hostname (pre-filled from the machine name), and a
  reusable `tskey-auth-` key stored as a credential. Desktop only (the
  helper is a separate process Android can't spawn). See the guide's
  Network-profiles section.

### Changed

- **VPN helpers now update on their own schedule.** The NetBird (and new
  Tailscale) sidecar helpers ship on a separate release track, decoupled
  from the app version, so a helper can be patched without an app update
  and updating the app no longer forces a helper re-download unless the
  underlying protocol actually changed. The helper and app negotiate a
  protocol version on start; a mismatch now reports a clear "update the
  helper" / "update ssh-tool" message instead of failing obscurely.
  First upgrade from 0.49.0 re-downloads the helper once (it predates
  the versioned protocol), then stays decoupled.

## [0.49.0] - Remote-disconnect for synced VPN profiles

### Added

- **Take over a WireGuard / NetBird profile that's live on another
  machine.** When the same network profile is shared over sync, only
  one machine can hold its userspace tunnel up at a time. Try to
  connect through a profile another machine already has up and the app
  now offers to take it over: it writes a kill request through sync,
  shows a countdown while the other machine tears its tunnel down, then
  connects - no more racing the disconnect button not knowing what's
  happening on the other end. "Connect anyway" is still there if you
  really want both up. Presence is published through the existing sync
  channel (a small plaintext presence file, no secrets), so it only
  works when sync is configured; single-machine users see nothing new.
  The take-over offer now covers every connect path - saved
  connections, bulk connect, dynamic-folder hosts, and a manual
  dynamic-folder refresh - not just single saved connections.

- **NetBird device name defaults to "<hostname>.ssh-tool".** The
  create form pre-fills the device name from this machine's hostname so
  a peer is recognisable in the NetBird dashboard and distinct per
  machine, instead of every peer registering as a bare "ssh-tool". Still
  editable before saving; a profile left blank falls back to the same
  derived name at connect time.

- **Dynamic-folder hosts and guests sort by name.** Proxmox (and other
  provider) entries render alphabetically now, case-insensitive and
  numeric-aware (web-2 before web-10). Guests are one flat alphabetical
  list across the whole cluster - in a multi-node Proxmox setup you find
  a VM by its name, not by which node it runs on.

- **See and free a busy profile from Settings.** Settings -> Network
  profiles now shows an "up on <machine>" badge for a WireGuard profile
  whose tunnel is live on another synced machine, with a "Disconnect on
  <machine>" button that asks that machine to drop it - a plain hand
  free-up without bringing the tunnel up locally (distinct from the
  take-over the connect flow offers).

### Fixed

- **"Tunnel running on another machine" never showed.** Presence
  identified each machine by a UUID kept in the store - which the store
  syncs, so both machines ended up with the SAME id and each read the
  other's presence record as its own, hiding the conflict entirely.
  Presence now identifies a machine by its stable hardware/OS id (the
  same value the auto-unlock sidecar is bound to), which never travels
  through sync, so two machines sharing a profile always differ.

- **Network profiles didn't sync.** Two independent gaps, both fixed:
  creating / editing / deleting a WireGuard / NetBird profile didn't
  mark the profile as changed, so auto-sync never pushed it; and even
  when a snapshot did carry a profile, the live pull (the no-restart
  apply) mirrored every table EXCEPT network_profiles, so the pulling
  machine got the connections that inherit a VPN profile but never the
  profile itself. The visible symptoms were profiles that never arrived
  on the other machine and inventory folders logging "network profile
  <id> not found" on every refresh (a connection's inherited profile
  pointing at a row that never came across). Profiles are now part of
  the sync change signal AND the live-pull mirror. The upgrade
  re-baselines silently: a machine that already has profiles pushes
  them on next launch; a machine without any doesn't push an empty
  update over one that has them.

## [0.48.2] - No console flash on Windows

### Fixed

- **Opening Settings -> Network profiles briefly flashed a console
  window on Windows.** The version check that shows the installed
  NetBird helper's version spawned it without hiding the console;
  it's now suppressed, like the tunnel process already was.

## [0.48.1] - Version-stamp fix

### Fixed

- **v0.48.0 was stamped "v0.48.0-rc2" internally.** The release build
  ran while the rc2 tag still sat on the same commit as the release
  tag, and the version derivation picked the wrong one. Cosmetically
  that showed the wrong version in Settings -> About; more importantly
  it broke the NetBird plugin download, which looks for the helper in
  the release matching the app's own version - and rc2 had been
  deleted. Builds now stamp the exact tag that triggered them. Update
  from v0.48.0 to get a correctly-stamped app and a working plugin
  download.

## [0.48.0] - Network profiles: WireGuard + NetBird

### Added

- **Userspace VPN profiles.** Route a connection's first SSH hop
  through an overlay VPN so you can reach hosts on a private network -
  a client's WireGuard, a Hetzner internal network, a NetBird tailnet -
  without a system-wide VPN. Everything runs in userspace: no TUN
  adapter, no admin rights, no system routes, and several tunnels can
  be up at once. Manage profiles in Settings -> Network profiles;
  assign one to a folder or connection via its Network setting, which
  inherits down the tree so a whole client folder can go through one
  tunnel. A pane whose hop went through a tunnel shows a VPN badge, and
  the status bar lists running tunnels.
- **WireGuard**, built in. Paste a standard wg-quick config; the
  private key and preshared keys live in the vault. DNS servers in the
  config resolve hostnames inside the tunnel. Editing shows the config
  back with secrets as a **KEEP** placeholder so you can tweak without
  re-pasting keys.
- **NetBird**, via an optional plugin. Install it in one click from the
  Plugins card (downloaded from the matching release and checksum-
  verified). A profile takes a management URL, a device name, and a
  setup key (stored as a credential; a + New button creates it inline).
  Unlike WireGuard's single shared identity, each machine registers as
  its own NetBird peer - the right choice for connecting from several
  machines. See docs/netbird-setup.md for which key to use and how
  groups / access policies work.
- **Connect policy per profile.** Always (first hop always tunnels),
  Auto (probe direct first, tunnel only when that fails - direct
  on-site, tunnel remote), or Pause (a kill switch that dials direct
  and stops the tunnel). Tunnels start on demand and stop about two
  minutes after the last session using them closes. Dynamic-inventory
  folders can fetch their provider API through a profile too, for a
  Proxmox reachable only over the VPN.

  WireGuard works everywhere including Android; NetBird is desktop-only
  (its helper is a separate process Android can't spawn). A synced
  WireGuard profile carries one identity across machines - a warning in
  the editor explains the trade-off and points at NetBird for
  multi-machine use.

### Fixed

- **Linux: the app now has an icon and identifies itself properly.**
  The window / taskbar / tray showed no icon and the taskbar hover said
  "wails app"; the tray icon rendered as "...". The app now ships its
  icon, sets its program name and window class to match the launcher
  entry, and the tray uses a PNG the GTK tray can actually draw.
- **Linux: "Restart to apply" after an update now relaunches the app.**
  It used to close and never come back (a manual launch worked) because
  the new instance inherited the closing app's systemd scope and was
  killed with it. The relaunched instance now detaches into its own
  session.

## [0.47.0] - Terminal workflow polish, profile statistics

### Added

- **Jump from a terminal straight to its connection's settings.** New
  gear button in the pane title bar (next to the tunnels button):
  switches to the Connections view with that connection selected,
  ancestor folders expanded and the row scrolled into view. Handy
  when you want to add a port forward or tweak a setting without
  hunting the connection down in a large tree.
- **Profile statistics in Settings -> About.** Connection count (and
  how many have VNC enabled, inheritance included), folders (and how
  many are dynamic), dynamic inventory broken down into hosts / VMs /
  LXC containers / cloud servers, configured tunnels with their
  bookmark count, credentials, open sessions and live tunnels.
- **Expand / collapse all folders.** One toggle button in the
  Connections and Credentials sidebar headers: collapses everything
  when any folder is open, expands the whole tree otherwise.

### Changed

- **The WebGL terminal renderer is now off by default.** On some
  GPUs the WebGL glyph atlas corrupts into garbled glyphs - at times
  spontaneously, with the app sitting idle - so every terminal now
  uses the reliable canvas renderer out of the box. If you had
  explicitly toggled the WebGL setting before, your choice is kept.
  Opting back in (Settings -> Terminal) is worthwhile mainly for
  very heavy output; theme changes now also clear the glyph atlas
  for opted-in users, and Ctrl+Shift+L still forces a clean redraw.

### Fixed

- **Closing a tab or pane leaves the keyboard in the next terminal.**
  After Ctrl+D with auto-close enabled, closing a tab with the X /
  Ctrl+Shift+W, or closing one pane of a split, the promoted
  terminal now receives focus immediately - no extra click before
  you can type.

## [0.46.0] - Open in ssh-tool from the file manager

### Added

- **"Open in ssh-tool" in the file manager's right-click menu.**
  Right-click a directory (or the background inside one) and pick
  Open in ssh-tool: the app opens (or focuses, if already running)
  with your default local shell as a tab, already in that directory -
  like "Open in Terminal", but inside the window that holds your SSH
  sessions. Install/remove it from Settings -> Connection -> File
  manager integration; per-user, no admin rights. Supported in
  Windows Explorer (on Windows 11 it appears under "Show more
  options" - the modern top-level menu needs app signing, tracked in
  the backlog), KDE Dolphin and the GNOME Nautilus Scripts menu.
  Also works from scripts: `ssh-tool --open-dir <path>`. On Windows
  the WSL shell lands in the /mnt/... equivalent automatically.

### Fixed

- **Light terminal themes: the character under the block cursor was
  invisible while the cursor blinked.** The glyph inside the cursor
  is now painted in the theme's background color (a proper inverse),
  in the live terminal and the recording player alike. Dark themes
  were never affected.

## [0.45.2] - Fully automated releases

### Changed

- **Every release, including the Android APK, is now built and
  published by CI.** The APK is compiled and signed in the release
  pipeline with the same key as previous builds (a pipeline gate
  verifies the signature, so a wrongly-signed APK can never ship)
  and attached to the GitHub Release next to the desktop binaries.
- **sshtool.app is now a mirror.** The website follows GitHub
  Releases and this repo's feature manifest on its own; downloads
  for new versions redirect to the GitHub release assets. Nothing
  changes for users - update checks, download URLs and the releases
  page keep working, including for installs older than v0.45.0.
- The Settings -> Updates text now describes the actual check:
  GitHub Releases first, sshtool.app as fallback.

## [0.45.1] - Android opkssh login fix, Apache 2.0 license

### Fixed

- **Android: opkssh login no longer fails on the first try.** After
  confirming the OIDC login in the browser, the app used to show
  "failed to exchange token ... no such host" and you had to close
  the browser and log in again. Android freezes a backgrounded app's
  network while you are in the browser, and the token exchange fired
  into that frozen window. The app now keeps itself network-alive
  for the duration of the login (a "Signing in..." notification
  appears briefly when no sessions are open).

### Changed

- **ssh-tool is now licensed under the Apache License 2.0.** The
  LICENSE, NOTICE and contribution terms are in the repo root.

## [0.45.0] - GitHub home, GitHub Releases updater, Wails alpha2.111

The project now lives at
[github.com/fpenezic/ssh-tool](https://github.com/fpenezic/ssh-tool).
Releases are built by GitHub Actions and published both to
[sshtool.app](https://sshtool.app) and as GitHub Releases.

### Changed

- **Update checks now ask GitHub Releases first.** The in-app updater
  resolves the latest version, download URL, size, sha256 and release
  notes from the GitHub Releases API, falling back to sshtool.app
  when GitHub is unreachable or rate-limited. Nothing changes for
  older installs - they keep polling sshtool.app, which stays
  populated on every release. If you pointed
  `update_check_base_url` at your own server, that still wins and
  GitHub is skipped entirely.
- **Wails bumped to v3 alpha2.111.** Brings upstream stability
  fixes we care about: crashes on long-running Linux sessions under
  frequent asset loads (WebKit calls now complete on the GTK main
  thread), and a Windows crash when restoring the app after WebView2
  suspended during a long minimise. WebView2 component updated to
  1.0.27.

### Internal

- Release pipeline ported from GitLab CI to GitHub Actions; tags
  with a suffix (`-rc1`, `-test`) now land as GitHub prereleases
  for testing and never touch sshtool.app.
- Android: migrated to the new Wails mobile platform-manager API
  (`application.Android.*`) and the renamed `common:biometric`
  bridge event.

## [0.44.0] - Edit existing port forwards

### Added

- **Edit a port forward.** Forwards could be created and deleted but not
  changed - a wrong address, port or description meant deleting and
  recreating. Each forward now has an Edit button that reopens the form
  pre-filled; Save applies the change. The kind (local / remote / dynamic)
  stays fixed - delete and recreate to switch that - and editing a running
  forward notes that it must be restarted to take effect.

## [0.43.0] - Server status readout, scrollback search button

### Added

- **Optional server status in the status bar.** When enabled (Settings ->
  Terminal, off by default), the status bar shows load average, memory,
  disk and logged-in users for the SSH host of whichever pane is focused,
  refreshed every 10 seconds. It runs a small read-only probe (reads
  /proc, df, who) only for the focused session - not every open one - so it
  stays cheap even with many connections, and non-Linux hosts or network
  gear simply show nothing.
- **Scrollback search button.** The per-terminal scrollback search
  (Ctrl+Shift+F) now has a search button in the pane header that toggles
  it open and closed, so it's discoverable without the shortcut.

## [0.42.2] - Keepalive discoverability, scrollback note

### Changed

- **Keepalive is easier to find and understand.** The per-connection (and
  per-folder) keepalive field is relabelled "Keepalive / anti-idle (s)"
  with a tooltip and a short note explaining it sends a periodic SSH
  keepalive so an idle connection isn't dropped by a bastion or firewall.
  Blank inherits the folder's value, 0 turns it off. (The setting itself
  already existed and worked - it was just easy to miss.)
- **Scrollback setting notes the replay limit.** The Terminal scrollback
  hint now explains that while the live on-screen history goes up to
  100000 lines, only about the last 2000 lines replay after a tab is
  detached, re-docked, or the UI reloads.

## [0.42.1] - Clearer port-forward and hostname labels

### Changed

- **Port-forward fields are labelled by kind, with hints.** The create
  form now names the listen side and the target side explicitly per kind
  (local vs remote), so a remote forward no longer shows a confusing
  "Remote host" field for what is actually the local target. Every address
  field has a mouse-over hint, and both default to `127.0.0.1`.
- **Connection "Hostname" is now "Hostname / IP address".** With a tooltip
  distinguishing it from the connection's display Name, so it's clear which
  field is the label and which is the address SSH dials.

## [0.42.0] - Forward bookmarks, opkssh provider + cancel, broadcast fix

### Added

- **Edit SOCKS proxy bookmarks.** A bookmark on a dynamic (SOCKS5) forward
  can now be edited in place - a pencil button on each chip opens the
  inline form seeded with the current label and URL, instead of having to
  delete and re-add it.
- **opkssh provider picker.** The credential's opkssh config now offers a
  dropdown of the providers defined in your YAML, so you can pin which one
  to log in with and skip the chooser. Previously a config with
  `default_provider: webchooser` always opened the first provider (usually
  Google) with no way to choose another - `webchooser` is now flagged as
  unsupported with a hint to pick a provider explicitly.
- **Cancel a stuck opkssh login.** If an OIDC login never finishes - wrong
  config, or you close the browser - the connect used to hang until it
  timed out, with an app restart the only escape. There's now a Cancel
  button next to Connect while a connection is in progress.

### Fixed

- **Broadcast no longer leaks terminal escape codes into other sessions.**
  Running a full-screen program (vim, less) in a broadcast session dumped
  garbage like `^[[>0;276;0c` / `rgb:1111/...` into the other members'
  shells, where it ran as commands. Those were terminal report responses
  the remote program requested; only what you actually type is broadcast
  now.

### Changed

- **Port-forward bind field is labelled per kind.** For a remote forward
  the bind address/port field is now labelled "Remote bind address /
  port", with a tooltip explaining that `127.0.0.1` keeps the listener
  loopback-only and `0.0.0.0` exposes it. Default stays `127.0.0.1`.

## [0.41.1] - Stop auto-sync pushing on every inventory refresh

### Fixed

- **Auto-sync no longer pushes a new generation on every inventory
  poll.** A dynamic-inventory folder (Proxmox, Hetzner, etc.) stamps its
  last-refresh time on every successful pull, and that timestamp was part
  of the change signature that drives auto-sync - so the profile looked
  "changed" after each poll and pushed a fresh snapshot even when nothing
  you edited had changed (generation counter climbing on its own). The
  refresh bookkeeping is now excluded from the signature; a real change
  to a dynamic folder's configuration (or adding/removing one) still
  syncs.

## [0.41.0] - Terminal render fix, VNC over jump hosts, copy toasts

### Fixed

- **Garbled terminal output on large colorized listings.** `ls -l` /
  `ll` of a big directory could render with the shell prompt landing
  mid-listing, lines duplicated or out of order (pressing Enter
  redrew it). The cause was output arriving out of order: each chunk
  carries a cumulative byte counter, but the events that deliver them
  were dispatched on independent goroutines and could overtake one
  another. Output is now reassembled strictly in order on the cumulative
  counter before it reaches the terminal, and the backend drives it
  through a single stream, so a big listing always renders correctly.
- **VNC console froze the whole app.** Opening a console could lock the
  entire UI (you couldn't even close the window). A reactive loop in the
  console-controls wiring spun the render thread; that path was
  reworked so it can't happen.
- **VNC console through a jump host.** A console on a connection with a
  jump host failed unless you were on the VPN - the direct path dialed
  the VNC host straight from your machine, ignoring the bastion. It now
  routes the RFB connection through the jump host, the same as SSH.
- **VNC failures now say why.** A console that couldn't reach its target
  used to show only "Connection closed unexpectedly". It now reports the
  actual reason - jump-host login rejected, host not found, connection
  refused, or timed out.

### Added

- **"Copied" toast on copy actions.** Copying a hostname, port, public
  key, password, terminal selection, etc. now shows a brief
  confirmation. Password copies note the 30-second auto-clear.
- **VNC connect feedback.** While a console connects it now tells you how
  it's reaching the host (direct, via a named jump host, or over an SSH
  tunnel) and gives up with a clear message after a timeout instead of
  spinning forever.

### Changed

- **VNC console controls moved into the tab header.** Status and the
  Fit / Dot cursor / Ctrl+Alt+Del / Reconnect buttons are now inline in
  the header instead of a separate toolbar row, giving the console back
  that vertical space.

## [0.40.1] - Sealing robustness

### Fixed

- **Backup / sync sealing no longer fails when USER isn't in the
  environment.** The machine-bound seal derived part of its key from
  the USER / USERNAME environment variable and errored outright when
  neither was set (some containers, cron, CI). It now falls back to the
  OS account (and finally a constant), so backups and sync snapshots
  seal everywhere. Existing auto-unlock sidecars keep working.

## [0.40.0] - SFTP sync

### Added

- **Sync over SSH/SFTP, not just WebDAV.** Settings -> Sync now has a
  transport choice: keep using a WebDAV server, or store the encrypted
  snapshot in a directory on any SSH server you have. The snapshot is
  still sealed locally (argon2id + XChaCha20-Poly1305) before upload, so
  the server only ever sees ciphertext. SFTP uses an atomic POSIX rename
  for the snapshot swap.
- **SFTP auth, two ways.** Reuse a credential from your vault tree
  (key / password / opkssh - convenient on a machine that already has
  it), or type the auth in directly (password or a pasted private key).
  The inline option is what lets a brand-new machine bootstrap: a vault
  credential wouldn't exist there until the first pull brings it in. The
  host key is verified the same way as any other connection.

## [0.39.0] - Android deep links

### Added

- **`ssh-tool://` deep links work on Android.** "Open in ssh-tool"
  import links (and QR codes) now open the app and run the import, the
  same as on desktop. Cold and warm launches are both handled
  (`launchMode=singleTask`). The Settings URL-scheme registration
  control stays desktop-only - on Android the scheme is bound at
  install time by the manifest, nothing to register at runtime.

### Fixed

- **Android App info no longer shows "Version 1.0".** The gradle
  `versionName` / `versionCode` were the scaffold defaults; they're now
  injected from git at build time, so the OS-level App info matches the
  in-app version.

## [0.38.1] - Android version stamp

### Fixed

- **Settings -> About showed "dev" on Android.** The android build never
  injected the version into the binary (unlike the desktop builds), so it
  fell back to the `dev` default. The git-describe version + commit are now
  stamped in.

## [0.38.0] - Android app id

### Changed

- **Renamed the Android app id off the Wails scaffold default**
  (`com.wails.app` -> `app.sshtool`). Only the installed identity moves;
  the internal package stays `com.wails.app` because the Wails runtime
  hardcodes its JNI exports there. **This installs as a new, separate
  app** - it does not upgrade an existing `com.wails.app` build in place
  and starts with an empty profile. Migrate by sync-pulling into the new
  install, then remove the old one.

## [0.37.1] - Android folder long-press

### Fixed

- **Folder long-press on Android no longer navigates away.** Opening a
  folder's context menu used to also select the folder, which on mobile
  slid in the folder settings pane underneath the menu. The long-press
  now just shows the menu; a new "Folder settings…" item is the
  deliberate way into the settings pane.

## [0.37.0] - Android UX fixes

Live-testing follow-ups on the Android app. Desktop is unaffected -
every change is gated behind a build tag or an `isMobile` runtime check.

### Fixed

- **opkssh login now opens the browser on Android.** The OIDC flow used
  to shell out to `xdg-open`/`open`, which don't exist on android, so
  the login silently never started. The login URL is now routed to the
  system browser via an `Intent.ACTION_VIEW` through the JNI bridge
  (new `WailsBridge.openURL` + a `SetOpenBrowserOverride` hook on the
  opkssh provider). The loopback callback still lands back in-process.
- **Sync pull no longer demands a restart on Android.** There is no
  machine-bound sidecar on android, so a live pull always fell back to
  "restart to apply passwords/keys" - and the Restart button (a desktop
  process re-exec) did nothing on a phone. The pull now reads the vault
  passphrase from the Keystore-backed secure store (when biometric
  auto-unlock is on) and merges the pulled vault in place, so it applies
  live. In the rare auto-unlock-off case it shows a clear "close and
  reopen the app" instruction instead of a dead button.
- **System Back steps through the UI instead of exiting.** Android Back
  (button or gesture) now goes detail -> list, or a secondary tab ->
  connections, by arming a synthetic history entry that `WebView.goBack()`
  consumes. It only exits the app at the root.
- **Folders are easier to expand on a phone.** A plain tap on a folder
  row toggles it (the chevron alone was a tiny touch target and
  double-tap was awkward); rows and the chevron hit area are larger on
  touch screens.
- **Dynamic-inventory entries open their preview on tap.** Tapping a
  dynamic VM/host now slides in its detail pane on mobile (the
  single-pane view previously ignored dynamic-entry selection), and the
  dynamic Connect button shows a "Connecting…" busy state so a slow
  connect doesn't look like a no-op.
- **Hid the desktop-only ssh-tool:// handler registration on mobile.**
  Registering an OS URL handler isn't wired on android; the control
  used to surface its failure as an error. Pulling an archive from a
  URL still works.

## [0.36.0] - Android / mobile app

ssh-tool now runs on Android (and the same code path is ready for iOS).
The desktop build is unchanged - everything mobile is gated behind build
tags and an `isMobile` runtime check, so the Windows/Linux/macOS app
behaves exactly as before.

### Terminal rendering fix (desktop + mobile)

- **No more "hieroglyph" corruption.** Garbled / overlapping glyphs that
  occasionally appeared after a font-size change or when toggling
  broadcast were a stale WebGL glyph-atlas. The atlas is now cleared on
  every font-size change (Ctrl+wheel) and on any broadcast-membership
  change, so the next paint always re-rasterises cleanly. On mobile the
  WebGL renderer is off by default (the Android WebView's WebGL atlas is
  flaky); the DOM/canvas renderer is used instead.

### Password fields

- **Show/hide toggle on every password field.** The eye icon that already
  existed on a couple of fields now appears on all of them - vault
  unlock/create, connection and credential passwords, sync and
  import/export passphrases, API token secrets, VNC credentials.

### Android (new)

- Boots as a real app ("ssh-tool" with its own icon), unlocks the vault,
  manages connections, and opens SSH sessions with a full terminal - all
  on the same Go core and Svelte UI as the desktop.
- **Biometric vault auto-unlock.** The passphrase is stored in the
  device's hardware-backed keystore (EncryptedSharedPreferences) and
  unlocked with fingerprint / face. Opt in with the checkbox on the
  unlock screen; falls back to the typed passphrase.
- **Sessions survive backgrounding.** A foreground service (with an
  ongoing notification) keeps SSH sessions connected while the app is in
  the background, instead of the OS suspending the process and dropping
  the sockets.
- **On-screen key bar.** A soft-keyboard accessory row supplies the keys
  a phone keyboard lacks - Esc, Tab, Ctrl/Alt (latching), arrows,
  Home/End/PgUp/Dn, pipe/slash/tilde/dash, and ^C/^D/^Z/^L/^R/^A/^E/^U/
  ^K/^W control combos.
- **Mobile layout.** The two-pane tree+detail view collapses to a single
  pane with a Back affordance; the top nav compacts to icons; Settings
  stacks to one column and hides desktop-only options (local shell,
  external terminal, tray, browser launcher, copy-paste modes). Two-finger
  pinch in the terminal adjusts the font size.

## [0.35.1] - VNC polish + text-selection UX

- **Clipboard sharing between your machine and a VNC console.** Ctrl+V /
  Cmd+V puts your local clipboard onto the remote (then paste with the
  remote's own shortcut); copying in the remote mirrors back to your
  local clipboard. Read and written through the OS clipboard natively,
  since the webview blocks clipboard access over the console canvas.
  Whether the remote adopts an incoming clipboard is up to its VNC
  server - x11vnc / TigerVNC / Proxmox guest consoles honour it; macOS
  Screen Sharing largely ignores incoming clipboard, so paste-into-a-Mac
  is unreliable on that server.
- **Cursor no longer disappears.** Some servers (macOS Screen Sharing)
  send no pointer shape until the screen changes, leaving the cursor
  invisible at first. A dot cursor is now always drawn; toggle it off
  from the toolbar if the server draws its own.
- **VNC console on pinned Proxmox connections.** Pinning a Proxmox guest
  to a permanent connection keeps its console: "Open VNC console" routes
  back through the Proxmox API (it re-finds the guest's node by vmid), so
  you get the real console, not a generic VNC dial.
- **Numpad Enter works in Proxmox consoles.** It was being sent as a
  keypad-Enter the LXC/serial text console ignored; it's now a plain
  Return.
- **The app no longer text-selects like a web page.** Dragging across the
  UI doesn't paint a selection and Ctrl+A doesn't grab the whole window.
  Selection is kept where copying matters - logs, the audit table, code
  and path blocks, the terminal, notes. In the log viewer and audit
  table, Ctrl+A selects just those rows. Inputs are excluded so a
  selection started in a log can't sweep up a nearby field's text.

## [0.35.0] - VNC console: Proxmox + generic, in-app noVNC

- **See a remote desktop or VM console in a tab.** A new VNC console
  renders inside the app via noVNC - no external client, no X server,
  no separate install. It opens as a regular tab alongside terminals
  and local shells (locked to a single full pane - a desktop wants the
  whole tab, so VNC tabs don't split or swap to SFTP). Detaching the
  tab to its own window and redocking works like any other tab.
- **Proxmox VM / LXC console.** Right-click a Proxmox guest (in the
  inventory or its open tab) and pick "Open VNC console" to get the
  guest's screen. It reuses the folder's API token - the app drives
  Proxmox's vncproxy + vncwebsocket for you, honouring
  `insecure_skip_verify` for self-signed clusters. Works for QEMU VMs
  and LXC containers.
- **Proxmox node (host) console.** Hosts work too, but PVE refuses API
  tokens for a node shell, so it needs a real login: set a "VNC console
  login" on the Proxmox dynamic folder (a password credential whose
  name is your PVE user, e.g. `user@ldap`). The app logs in for a
  ticket and opens the node shell.
- **Generic VNC over your own hosts.** Turn on "VNC console" for any
  connection (it's off by default - most SSH hosts have no VNC server).
  Set the RFB port (default 5900) and choose to dial it directly or
  tunnel it through the connection's SSH session (reaching a
  localhost-bound x11vnc / TigerVNC / macOS Screen Sharing on the
  remote's loopback). The login pre-fills from the connection; an
  optional VNC password lives in the vault, and noVNC prompts in-panel
  if the server still needs one (e.g. macOS Screen Sharing).
- The console toolbar has fit-to-window scaling, Ctrl+Alt+Del, and
  reconnect. Pixels flow over a loopback bridge inside the app (no
  secrets in the URL); the webview never needs custom headers or
  TLS-skip.
- **Sync is now schema-skew tolerant.** A live pull between machines on
  different app versions only copies columns both sides have, so a
  newer version's additions (like the VNC fields) never break an older
  version's pull. Unknown settings ride along untouched.

## [0.34.0] - live sync pull, settings polish, htop fix

- **Pulling a synced profile no longer needs a restart.** The pulled
  profile is applied straight into the running app: the store is
  mirrored into the live database and the vault's secrets are
  re-encrypted under this machine's key and merged into the unlocked
  vault. SSH sessions stay open, the UI just refreshes. When the
  status-bar pull pill (or its toast) reports another machine has
  newer data, one tap applies it - no confirm, no restart.
- This machine's own state is never overwritten by a pull: window /
  session layout, "recently connected" order, and the sync
  configuration (server, passphrase, generation) are preserved while
  the rest of the profile is replaced.
- The vault merge uses this machine's auto-unlock passphrase, so for a
  single user with the same vault passphrase on every machine it's
  fully silent. In the rare case the machines use different vault
  passphrases, the connections apply live and only the passwords/keys
  fall back to a one-click restart - never a passphrase prompt.
- **Apply incoming changes automatically** (opt-in, under Settings >
  Sync > Automatic): when another machine pushes a newer version and
  this one is clean, the change is pulled and applied in the
  background - but only while the UI is idle (nothing focused, no
  dialog open), so a pull never rearranges the tree out from under
  you; otherwise it waits for an idle moment or leaves the
  notification.

### Settings

- The whole **Sync** page is reorganised into clear sections (Server /
  Status / Manual sync / Automatic) with plain descriptions instead of
  one dense block.
- **Wide-window layout fixed**: the sidebar now runs full height
  (it used to stop short and leave a blank strip), section cards are
  centred at a readable width instead of floating in empty grey, and
  nav / labels / hints read with more contrast. Settings honours your
  chosen UI font size.

### Terminal

- **Fixed htop / btop ghosting**: full-screen TUIs that repaint every
  tick could leave the WebGL renderer compositing old and new cells
  (header painted over the process list). The glyph atlas is now
  cleared on resize, with **Ctrl+Shift+L** as a manual force-redraw
  for the rare in-place case (plain Ctrl+L stays the shell's
  clear-screen).

### Other

- **Connection-folder editor** now shows unsaved changes (Save * /
  Saved) like the connection editor, and no longer double-toasts on
  save.
- Updated to **Wails v3 alpha.101**, picking up upstream fixes to the
  Windows self-updater (cross-volume temp dir), the macOS screen-info
  crash on display changes, and a Linux asset-server crash.

## [0.33.2] - sync: no phantom push on the first launch after an update

- **Updating the app no longer creates a phantom "update to pull".**
  The sync change signal is a fingerprint string whose internal format
  can shift between versions; the first launch on a new build read the
  old fingerprint as a change and pushed an otherwise-unchanged
  profile, which the other machine then flagged as something to pull.
  The fingerprint now carries a format tag and a format-only difference
  re-baselines silently - no push. A genuine offline edit still pushes.

## [0.33.1] - sync polish: no idle pushes, machine-local state, one-click restart

Follow-up fixes to v0.33.0 from two-machine field testing.

- **No more spurious auto-pushes.** Auto-sync was pushing every few
  minutes (and on every launch) with nothing actually changed. The
  change signal is now a content fingerprint of the profile tables -
  stable across restarts and across machines, moving only on a real
  edit - instead of file mtimes (which a WAL checkpoint bumped) or a
  per-session counter (which reset each launch). Several writers that
  were dirtying the profile without a user change are split out or
  silenced: the audit log moves to its own machine-local database,
  "recently connected" timestamps and the open-tab / window-geometry
  state become machine-local, dynamic-inventory refreshes no longer
  rewrite an unchanged host list, and provider tags now have a stable
  order (a randomised Hetzner tag order was the main culprit).
- **One-click restart after a pull.** Pulling a profile (which applies
  on the next start) now offers a Restart now button that relaunches
  the app for you, instead of a missable "quit and reopen" toast.
  Relaunch is fast again - the parent-exit wait no longer stalls on
  Windows.
- **Sync only carries the profile.** Open tabs and window position are
  per-machine now and no longer travel in the sync snapshot, so a pull
  doesn't rearrange the machine you pulled onto.
- **Settings text larger on Windows.** The Settings panel was cramped
  at the default 13px UI base; its sections render ~8% larger now.

## [0.33.0] - encrypted profile sync over WebDAV

- **Sync your whole profile between machines** via any WebDAV server
  you control (Nextcloud, Apache mod_dav, rclone serve webdav).
  Everything travels: connections, folders, credentials AND their
  secrets (the vault), custom icons, settings, snippets, workspaces.
  Setup under **Settings > Security > Sync**.
- **Encrypted before a single byte leaves the machine.** The snapshot
  is the same sealed envelope backups use (argon2id +
  XChaCha20-Poly1305), locked with a sync passphrase that is
  independent of the vault passphrase. The server stores ciphertext
  plus a tiny meta file with a generation counter - a compromised
  WebDAV host learns nothing. HTTPS is required (plain http only for
  localhost); certificates verify against the OS trust store, so a
  private CA installed system-wide works. No skip-verify toggle, by
  design.
- **Snapshot semantics, git-like guard.** Push uploads this
  machine's profile; Pull replaces this machine's profile (staged
  like a backup restore - pre-restore safety copy kept, restart to
  apply). Push is refused when the remote has changes this machine
  hasn't pulled; Force push overwrites deliberately. No row-level
  merging - a vault cannot be half-merged.
- **Auto sync** (opt-in): pushes ~90 seconds after the last change
  and best-effort on quit (10s cap so a dead network can't block
  exit), and checks the server every N minutes plus at startup -
  wake the laptop from standby and within a minute a toast + a
  pulsing "pull" pill in the status bar tell you another machine
  pushed. Conflicts are never auto-resolved: auto-push steps aside
  when the remote is ahead.
- **One-click pull.** Clicking the toast or the status-bar pill
  pulls right there when this machine has no unsynced changes
  (lossless); with local changes pending it opens Settings > Sync
  so you decide with the full picture.
- **New machine setup**: enter the WebDAV details + sync passphrase,
  Pull, restart, then unlock once with the vault passphrase from the
  source machine (the machine-bound auto-unlock re-arms itself after
  that first unlock).

## [0.32.0] - import hub, archive completeness, creds tree parity

### Import

- **MobaXterm import.** Load a `.mxtsessions` export (right-click
  User sessions > Export in MobaXterm): SSH sessions become
  connections, the bookmark folder tree is rebuilt (existing folders
  reused on re-import), RDP / telnet / VNC entries are counted and
  skipped. MobaXterm's export carries no passwords - attach
  credentials afterwards.
- **PuTTY / KiTTY import.** PuTTY has no export button; run
  `reg export "HKCU\Software\SimonTatham\PuTTY\Sessions" file.reg`
  and load the file. Only `Protocol=ssh` sessions import,
  `user@host` hostnames split into username + host, UTF-16 `.reg`
  files (the `reg.exe` default) are handled. PuTTY stores no
  passwords, so nothing is lost in the move.
- **One Import page.** The three import sections merge into a
  single **Settings > Import** with a source picker: ssh-tool
  archive (default - also hosts the ssh-tool:// handler
  registration), ssh_config, PuTTY/KiTTY, MobaXterm, Devolutions
  RDM. Every import stays additive and safe to re-run.

### Export archive

- **Per-connection passwords now travel.** A password set directly
  on a connection (no credential entry) was silently dropped by
  export. With "include credentials" on it now ships sealed in the
  same encrypted block, and import restores + relinks it - onto
  rows the import created or overwrote, never onto skipped ones.
- **Custom icons now travel.** Folder / connection / credential
  icons ride in the archive (deduplicated by content on import, so
  a logo shared by 200 connections costs one blob). The export
  modal gets a "Custom icons" strip toggle alongside notes / tags /
  color.

### Credentials

- **Cascade delete, properly.** Deleting a credential folder used
  to ask for a typed DELETE and then dump its credentials flat at
  the root. It now shows the same staged confirm modal as the
  connections tree - listing the folder, every credential inside
  and all subfolders - and deletes exactly that, with vault secrets
  and sealed history cleaned up per credential.
- **Folder multi-select.** Ctrl/Cmd+click toggles credential
  folders in and out of a multi-selection, Shift+click ranges
  across the visible list - the same gestures connections (and
  credentials themselves) already had. Delete takes the whole
  selection.
- **Save feedback parity.** Key and password rotation now toast a
  confirmation like every other save in the credential editor.

### opkssh

- **Cert status at a glance.** The credential editor shows what's
  in the vault: "Certificate in vault: issued 2h ago - re-login in
  ~6d22h" (or "re-login on next connect" once overdue). Read-only -
  never triggers a refresh itself.
- **Human-friendly durations.** Max cert age and refresh threshold
  accept free text: 7d, 6d23h, 48h, 90m, spaces allowed. The live
  hint shows exactly what will be saved (storage stays whole
  hours / minutes).

### Fixed

- **macOS: Cmd+Q skipped the quit prompt.** Application-level quit
  bypassed the window-close hook and killed live SSH sessions with
  no warning. Quit now routes through the same "N active sessions
  will be disconnected" confirmation on every platform.
- **Windows: Task Manager name regression mine.** The build-assets
  template still carried the long app description; one regeneration
  would have reverted the v0.29.2 fix. (If Task Manager still shows
  the old name on an updated install, that's Windows' MuiCache for
  the exe path - the binary itself is correct.)

## [0.31.0] - session recording + built-in player

- **Record any session to an asciicast v2 file.** Right-click a
  terminal tab > **Record session** (acts on the active pane; in a
  split tab each pane records separately), or use the palette
  command **Record / stop recording session**. A pulsing red dot
  marks recording tabs. Works for SSH and local shell sessions;
  closing the session finalises the file - it is never discarded.
  Files land in `<data dir>/recordings/` as
  `<connection>-<timestamp>.cast`; the folder is configurable under
  **Settings > Terminal > Session recording**. Start, stop and
  delete are audit-logged.
- **Output only, by design.** Keystrokes are never written to the
  file, so a password typed at a sudo prompt cannot leak into a
  recording. There is intentionally no option to record input.
  Mid-session resizes are recorded so playback reflows exactly
  where the live terminal did.
- **Built-in player.** **Browse session recordings** in the palette
  (or Settings > Browse recordings…) lists every recording with
  date, duration and size. Playback runs on the same terminal
  engine, font and theme as live sessions: play/pause (Space, or
  click the terminal), seek scrubber, 0.5-4x speed, and **Skip
  idle** - gaps longer than 2 s are jumped, so a half-hour capture
  plays in the time the output actually took. Fit-to-window font
  scaling keeps large recorded grids visible; a fullscreen toggle
  gives them the whole window. Text selection and copy (Ctrl+C /
  right-click) work, including inside htop/vim recordings. Files
  are standard asciicast v2, so `asciinema play` works too.
- **Fixed: quit prompt could hide behind a modal.** The "Quit
  ssh-tool?" confirmation rendered underneath the recordings player
  - it now sits above every other layer.

## [0.30.1] - export archive completeness

- **Credential folders survive export/import.** Imported credentials
  used to land flat at the root of the credential tree - the archive
  carried each credential's folder reference but not the folder
  hierarchy itself. Archives now include the credential-folder tree
  and the importer rebuilds it; re-imports reuse existing folders
  (matched by name + parent) instead of duplicating them. Old
  archives still import, just flat - re-export to pick up the
  structure.
- **Dynamic-inventory folders survive export/import.** A Proxmox /
  Hetzner / ... folder used to import as a plain empty folder: the
  provider config wasn't in the archive, and the API-token credential
  it references was never collected for export. Both travel now -
  config (provider, base URL, refresh interval) on the folder, token
  credentials included when "include credentials" is on, with all
  references remapped on import. Cached host entries stay out of the
  archive; the first refresh on the importing side repopulates them.

## [0.30.0] - macOS catch-up

- **ssh-tool:// links work on macOS.** The handler used to be a stub
  that errored (and showed the error as raw JSON). The .app bundle
  now declares the scheme the way macOS expects (Info.plist), the
  Register button forces a Launch Services re-index when needed, and
  the app listens for the Apple Event macOS uses to deliver opened
  URLs - they never arrive as command-line arguments, so the old
  argv-based path couldn't see them at all.
- **Vault auto-unlock works on macOS.** The machine-bound key
  derivation had no macOS identity source (machine-id is a Linux
  file, HOSTNAME isn't set for Finder-launched apps), so the sidecar
  could never be written. It now uses the hardware UUID
  (IOPlatformUUID) - the platform's stable machine identifier.
- **Settings show only what applies to your OS.** The External
  terminal picker (Windows Terminal / PowerShell / cmd / WSL) was
  rendered on every platform; it's Windows-only now, with a short
  note on what macOS (Terminal.app) and Linux ($TERMINAL + fallback
  list) do instead. Tray options are hidden on macOS - minimise goes
  to the Dock and the menu-bar icon already offers Show / Quit - and
  Linux gets a caveat about needing StatusNotifier support (stock
  GNOME wants the AppIndicator extension).
- **No more grey strip above the status bar.** The gap under the
  last terminal row rendered in the app background - obvious on the
  light theme. The terminal host now paints in the terminal theme's
  own background and the bottom padding is slimmer (the old extra
  padding compensated for cell-height estimates the fit logic no
  longer makes).

## [0.29.2] - mac + windows field fixes

- **macOS builds stamp their real version.** `task darwin:build`
  produced binaries that showed "dev" in the status bar - the darwin
  build task never injected the git version like linux and windows
  do. Also fixed a macOS-only frontend build failure: two imports
  resolved to the wrong file on a case-insensitive filesystem
  (`confirmModal` vs `ConfirmModal`).
- **Task Manager now says "ssh-tool".** Windows shows the exe's
  FileDescription as the process name, and ours carried the full
  marketing tagline.
- **Off-screen window rescue.** A rare maximise glitch could warp the
  window outside every display - and the new geometry memory then
  faithfully saved that spot and restored it on every relaunch,
  leaving the app unreachable without shenanigans. Saving now refuses
  minimised/bogus coordinates, and restore checks that a grabbable
  piece of the title bar lands on a real display - otherwise the
  window opens centred on the primary screen at its saved size.
- **Last terminal row could render clipped at some font sizes**,
  looking like the status bar overlapped it. The fit logic estimated
  the cell height instead of reading the renderer's real one, which
  rounds to device pixels; at certain font size / DPI combinations
  the estimate was off by enough to keep a clipped bottom row.

## [0.29.1] - reopen-last-session fixes

- **Dynamic-inventory and local-shell tabs are now reopened too.** The
  first cut only snapshotted plain SSH tabs, so a mixed set came back
  partial (2 of 3, 1 of 3). Dynamic hosts are remembered by their
  provider-stable external id - the internal row id changes on every
  inventory refresh, which is why Proxmox tabs failed with "dynamic
  entry not found" - and local shells by their shell kind.
- **Quitting right after opening a tab no longer loses it.** The
  snapshot used to lag up to a second behind; it now writes almost
  immediately and flushes once more during window teardown.
- **Slow reconnects are attributed.** A host that takes 30 s to come
  up opens its tab visibly late - it used to look like a ghost
  terminal from nowhere. A "Reopening N tabs from the last session"
  toast now fires when the restore starts, and a "Reopened M of N"
  summary follows if some entries failed. Hosts that left the
  inventory drop out of the snapshot instead of erroring on every
  start.

## [0.29.0] - verified updates, palette commands, reopen last session

- **Updates are checksum-verified.** The release manifest has always
  published a sha256 per binary; the app now actually checks it. The
  downloaded update is hashed and compared against the manifest before
  it gets anywhere near the live binary - a mismatch deletes the
  download and aborts the install. After staging, the update modal
  shows "checksum verified" (or a yellow warning if the manifest
  carried no hash, e.g. a custom release server).
- **Update download shows real progress.** Instead of a spinner until
  done, the modal now renders a progress bar with MB and percentage
  while the binary streams in. The Download button shows the asset
  size up front.
- **Update pipeline hardened.** The frontend no longer tells the
  backend what URL to download or which script to run - the backend
  acts only on what its own update check returned. No user-visible
  change, just a smaller attack surface.
- **No more leftover `.old` after a Windows update.** The previous
  binary could linger next to the exe when the old process held its
  file handle a moment too long. The swap script now retries the
  delete, and any survivor is swept on the next launch.
- **Ctrl+K palette runs app commands.** Type `>` (or just search) to
  reach: Open Settings, New local shell tab, Lock vault, Check for
  updates, and Open workspace: X for every saved workspace. Commands
  stay out of the way on an empty query - the `>` prefix narrows the
  list to commands only, VS Code style.
- **Reopen last session's tabs.** On a cold start, if the last session
  had SSH tabs open, the app offers to reconnect them (titles and tab
  groups included) after the vault unlocks - like a browser's "restore
  tabs?" prompt. Settings - Window - Startup switches between Ask
  (default), Always reopen, and Never. Local shells and
  dynamic-inventory tabs are not reopened yet.

## [0.28.0] - window memory, unsaved-changes hint, friendlier errors

- **The window remembers where it was.** On relaunch the app restores
  its previous size, position, monitor (multi-display aware) and
  maximised state instead of always opening at the default size in the
  middle of the primary screen.
- **Save moved into the header with an unsaved-changes state.** The
  connection Save button now lives in the header next to Connect and
  Delete (so it's always visible no matter how tall the form grows -
  e.g. with several jump hops). It's greyed when there's nothing to
  save, tinted yellow with "Save *" when there are unsaved edits, and
  briefly shows "Saved" after saving. A matching hint sits at the
  bottom of the form. Editing a field (a color tag, a username) and
  clicking away is no longer silent.
- **Human-readable error messages.** Errors surfaced from the backend
  used to show the raw JSON envelope
  (`{"message":"...","kind":"RuntimeError"}`). They're now unwrapped to
  the plain message everywhere - port forwards, the credential editor,
  the connection editor and more.
- **Start tunnels and open bookmarks without connecting first.** In the
  Port forwards panel, Start (now "Connect & start") and the proxy
  bookmarks no longer require an open session - they connect for you,
  start the forward, then run, matching what the Ctrl+K palette already
  did. Bookmarks are no longer greyed out when disconnected.
- **Credential editor no longer carries edit mode across entries.**
  Clicking another credential while one was in edit mode left the old
  form open; saving then tried to write the previous credential's name
  onto the new one and failed with a confusing conflict. Selecting a
  different entry now exits edit mode cleanly.
- **"Use different credential" resets between connections.** The
  per-connect credential override panel used to stay open with its
  values staged when you selected a different connection, so it could
  silently apply to the wrong host. It now clears on selection change.
- **Port forwards tidy-up.** The panel moved above Quick actions in the
  connection editor, and the redundant refresh button is gone (live
  status already polls automatically). Bookmark hover is readable on
  the light theme again (was a hardcoded colour that vanished on a pale
  background).
- **Updated Wails to v3 alpha.98** (from alpha.95), picking up upstream
  window-sizing and WebView fixes.

## [0.27.1] - tcpdump decode fixes

- **Row filter now searches decoded content.** The "Filter captured
  rows" box matched only the raw header line, so it missed anything in
  the decode (a CWMP method, a parameter value) and, in verbose mode,
  failed on search terms longer than tcpdump's 16-byte hex-gloss line
  wrap. It now also searches the decoded summary and field values, so
  filtering for e.g. `GetParameterValues` or a parameter path works.
- **No more "CWMP CWMP" rows.** A CWMP continuation segment (the SOAP
  body split across TCP packets, with no method element) used to show
  a meaningless doubled label. It's now labelled `(continuation)` with
  its parameters when it carries any, or declines to a plain HTTP / raw
  decode when it carries nothing useful.
- **Decode-port editor clarifies when it's locked.** The custom
  port-to-proto controls apply at capture start, so they're disabled
  while a capture runs; a short note now says so instead of leaving the
  Add button mysteriously greyed out. (Note: TCP 7547 is auto-detected
  as CWMP, so you don't need an override for the standard port.)

## [0.27.0] - tcpdump: CWMP / TR-069 decode

- **CWMP / TR-069 decoder.** The tcpdump Decode tab now dissects
  CPE WAN Management Protocol traffic (SOAP/XML over HTTP, TCP 7547).
  Each message shows the RPC method (Inform, GetParameterValues and
  its Response, SetParameterValues, Download, Reboot, ...), the Inform
  device identity (manufacturer, OUI, product class, serial) and event
  codes, and the first few ParameterValueStruct Name/Value pairs.
  Falls back to the plain HTTP decode for non-CWMP bodies on the same
  port. `cwmp` (alias `tr069`) is also available as a custom
  port-to-protocol override for ACS setups on non-standard ports.

## [0.26.4] - cosmetic: ASCII punctuation

- **ASCII punctuation throughout.** Every em-dash, en-dash and Unicode
  minus across the codebase, docs and UI strings is now a plain ASCII
  hyphen. No functional change; purely a consistency / house-style
  pass. (Deliberate UI glyphs like arrows, middots and check marks are
  unchanged.)

## [0.26.3] - tcpdump on busy hosts: memory, CPU, bandwidth + window title

The headline is that tcpdump captures on a busy host are now sane.
Previously a continuous capture on a high-traffic interface pushed the
app's memory into the tens of GB within seconds (enough to OOM the
machine), pinned a CPU core, and pulled tens of Mbit/s over the SSH
link. All three are fixed.

- **Memory stays flat.** The live packet stream is batched and
  tail-capped on the backend instead of streamed one event per packet,
  and the live list renders only the recent tail with the heavier
  flows/decode groupings computed only when their tab is open. Memory
  now holds steady regardless of packet rate or capture duration.
  (The v0.26.2 note about this was premature - it bounded one internal
  table but missed the real causes; this is the actual fix.)
- **Far less SSH bandwidth.** Two changes cut the wire volume
  dramatically on a busy capture:
  - The **SSH control connection is now excluded by default.**
    Capturing over the same SSH session is a feedback loop - every
    captured packet is streamed back over SSH, which generates more
    SSH packets that get captured, and so on. The capture now drops
    its own session's traffic automatically (works on any SSH port).
  - A **snapshot length** is applied so tcpdump no longer ships every
    packet's full payload over the link when the view only needs
    headers (and a short payload window in verbose mode for DHCP / DNS
    / TLS SNI).
- **High-rate nudge.** If packets arrive faster than you can read,
  a dismissible banner suggests adding a BPF filter. It does not stop
  the capture.
- **Readable live view.** The flat view is a plain scrollable list,
  newest at the bottom, that auto-follows the tail unless you scroll
  up. The packet count shows in one place (the footer) and no longer
  flickers.
- **Window / taskbar title shows the active connection.** The OS
  window and taskbar entry now read e.g. `myhost - ssh-tool` for the
  active terminal tab, and the section name on the other views.

## [0.26.2] - credential pane polish, tcpdump analyzer bounds

- **Bounded the tcpdump insight analyzer's state.** Its per-flow
  tracking and de-dupe tables could grow with the number of distinct
  connections seen. Idle conversations are now evicted and both
  tables are hard-capped. (This was billed as the continuous-capture
  memory fix but only addressed one structure - the real fix lands
  in 0.26.3.)
- **Unmask password fields.** Every password / secret / passphrase
  input in the credential editor (new password, key passphrase, API
  token secret, rotation fields) gains an inline eye toggle so you
  can reveal what you typed before saving.
- **"Used by" entries jump to the tree.** Clicking a folder or
  connection in a credential's *Used by* list switches to the
  Connections view, expands the ancestor folders, selects the row
  and scrolls it into view - no more hunting for where a credential
  is referenced.
- **Multi-select credential drag & drop.** Dragging a credential
  that's part of a multi-selection now moves the whole selection
  into the target folder, not just the grabbed row (matching the
  connections tree).
- **Clearing a credential hint sticks.** Emptying the Hint field and
  saving previously snapped back to the old value; the cleared hint
  now persists.
- **Accurate icon usage counts.** The icon picker's "in use" badge
  now counts credentials too, alongside folders and connections.

## [0.26.1] - tcpdump follow-ups

- **Find your captures.** The status-bar tcpdump segment now lists
  every active capture in a picker (connection name, which tab it's
  on, interface, packet/insight counts, live/background state) so
  you can jump straight to any of them - not just the focused one.
  A single capture still opens directly on click.
- **Default to `any`.** A new capture pre-selects the `any`
  pseudo-interface so you start by seeing all traffic regardless of
  which device it rides; narrow to a specific NIC once you know.
- **Clearer "tcpdump missing" error.** A host without tcpdump (or
  with it off PATH) now reports "tcpdump not installed on the remote
  host (or not on PATH)" instead of a bare "exited with status 127".
  Covers bash, dash/Debian and the exit code with no stderr.

## [0.26.0] - tcpdump network insights, background captures

**Network-health insights.** The tcpdump panel grows an
**Insights** tab (on by default) that runs a passive analyzer over
the live packet stream and flags routing / wrong-interface
problems as they happen:

- **UDP reply from a different source IP** - the classic
  0.0.0.0-bound-service symptom, where the kernel's return route
  egresses a different interface than the request arrived on and
  the client drops the answer.
- **TCP SYN with no reply** (half-open), **ICMP
  unreachable / redirect / TTL-exceeded**, **ARP for an off-subnet
  address**, **repeated TCP resets**.

Each routing-related finding has a **Check route** button that
runs `ip route get <dst> [from <src>]` on the host and shows the
interface, source IP and gateway the kernel would actually use -
the ground truth for "is traffic leaving the wrong interface".
TCP flag-based checks need Verbose on (brief output omits flags);
UDP / ICMP / ARP work in either mode.

**Background captures that survive detach.** A capture is now tied
to its session, not the window or pane that started it:

- Minimise a capture (the `-` button) and it keeps running in the
  background - the modal hides but packets, Insights and counters
  keep flowing. The bottom **status bar** gains a pink activity
  segment with the running packet/insight totals (click to
  restore); the pane's tcpdump icon shows a small green dot.
- Splitting, SFTP-splitting, closing one side of a split, and
  switching tabs no longer interrupt a running capture.
- **Detaching a tab** to its own window keeps the capture alive;
  the new window re-attaches automatically, pulling recent history
  from a backend ring buffer and continuing the live stream.
- A capture's context (interface, BPF filter, verbose / insights /
  continuous, packet count) shows in a status line under the modal
  header and is restored on re-attach, so you never lose track of
  what's running where.

**Continuous mode.** A new toggle runs tcpdump without a packet
cap, for long-lived captures (and the only way to keep one running
long enough to detach). Capped captures that hit their limit now
say so explicitly instead of looking like they crashed.

**Fixes.**
- TCP packets are now classified correctly (tcpdump prints them
  with a `Flags [...]` field and no literal "tcp" token), so the
  half-open and RST-storm detectors actually fire on real
  captures.
- Non-BOOTP traffic that tcpdump mislabels as DHCP (PacketCable /
  DOCSIS provisioning rides UDP port 67 too) is detected and left
  as a plain UDP row instead of rendering a bogus DORA transaction
  with scrambled IPs. The filter keys on the packet header, not
  the port, so genuine DHCP on non-standard ports still decodes.

## [0.25.0] - Multi-broadcast, v0.16 security follow-ups, quick wins

**Multi-broadcast groups.** The single global broadcast set
becomes a map of named groups. Open the broadcast manager, pick
a group from the dropdown (or hit "+ New group"), drop sessions
into it. A session can sit in zero, one, or several groups;
keystrokes from any member fan out to the union of every group
it belongs to. The legacy default group stays - existing
single-group call sites work unchanged.

Visual cues throughout:
- The peach corner badge on broadcasting panes adds an inline
  pill listing the groups (`⊕ BROADCAST [ops, default]`); hover
  for the full list when truncated.
- Tab labels grow a small peach pill next to the radio glyph
  when the tab's sessions span 2+ groups (`ops,dr`).
- Pane toolbar tooltip names every group the session belongs to.
- Manager dropdown shows the per-group member count; status-bar
  pill shows the unique total across every group.

Manager UX:
- "Show sessions in other groups" toggle (off by default)
  hides candidates already in another group so the picker stays
  focused. Sessions in the active group always show so they can
  be unticked.
- Select all / none / invert operate on what's visible - never
  silently pull in hidden cross-group members.

Groups live in memory on the backend only; persistence across
restarts tracked in TODO.

**Reconnect button on disconnected panes.** A prominent blue
inline pill renders next to the disconnect hint when a pane
loses its session. One click tears down the dead handle and
opens a fresh session bound to the same connection.

**Connect auto-retry on transient failures.** A first connect
attempt that trips DNS / connection-refused / network-unreachable
/ timeout / connection-reset now triggers one silent retry after
800ms before surfacing the error. Auth / handshake / host-key
failures never retry - those are deterministic.

**Vault-locked detected during connect.** When an SSH attempt
fails because the credential's secret couldn't be decrypted
(the vault is locked, not the secret missing), the backend now
emits a typed event the frontend listens for: VaultGate re-pops
and a toast names the connection that was blocked. Previously
the user saw "password missing in vault" with no obvious next
step.

**Jump-chain hover tooltip.** Connection rows in the tree show
`hostname` plus, when a jump host applies through the folder
cascade, `via bastion1 -> bastion2` on hover. No click needed.

**Inherited settings shown inline in the editor.** Username,
port, credential, and jump chain inputs in the connection
editor render an italic "inherited from <folder>: <value>"
hint underneath whenever the field has no override. The
placeholder also picks up the inherited value so the input
visually previews what would be used. Replaces the old
collapsed "Resolved settings" raw-JSON dump at the bottom of
the form.

**Vault Lock in Settings → Vault.** Manual "Lock now" button
in the Vault settings panel, next to the auto-lock minutes
slider. Hits `VaultLock(true)` (forgets the sidecar so the
next launch prompts too) and dispatches the same window event
the auto-lock timer fires, so VaultGate re-prompts immediately.

**Dynamic-folder error retry.** The red `!` dot next to a
broken dynamic folder is now a button - click triggers an
immediate refresh, and the dot clears on a successful pull
(the folder metadata is re-fetched alongside the entry list).
First expand of a folder with a stored `last_error` also pops
a one-time toast so the dot doesn't get missed.

**Dynamic inventory bulk connect.** Ctrl-click selects multiple
dynamic entries (existing); Enter on a selected one now opens
every selected entry in parallel via
`connectionActions.connectDynamicMany` instead of just the
focused row. Partial failures land per-row in the existing
last-connect-error map.

**Filter-aware shift-click range.** Type "vpn" to narrow the
tree, shift-click two visible matches - the range walker
honours the active name / tag filter so only visible rows get
picked up. Previously the walker walked the underlying tree
order and pulled in invisible rows between anchor and target.
Applies to connections, folders, and dynamic entries.

**Layout: tab rows wrap when the window is narrow.** The root
nav row (Connections / Credentials / Settings / Terminal /
Local shell / Search) and the per-tab session bar both grow to
fit however many rows the layout needs. Used to clip at a
fixed grid height; long-named connection clusters now wrap to
a second row instead of disappearing off-screen behind a
scrollbar.

**SQLite "database is locked" fix.** The modernc/sqlite pool
allowed concurrent writers, so an editor save that landed
mid-dynamic-refresh hit SQLITE_BUSY instantly. `busy_timeout`
goes to 5 s and the pool caps writers at 1 (SetMaxOpenConns(1))
so SQLite serialises transactions itself; WAL keeps readers
wide.

**Security follow-ups (v0.16 audit Medium findings):**
- **Vault heap zeroing on Lock.** In-memory mirror switched
  from `map[string]string` to `map[string][]byte`. Lock wipes
  every cached buffer with zeros before delete; Put overwrites
  the previous value on rotation. A crash dump after Lock now
  sees zeros, not plaintext.
- **Vault ciphertext padding.** Plaintext gets length-prefixed
  and zero-padded to 60 / 1020 / 4092 / 16380 bytes before
  AEAD seal (or next 16 KiB multiple for larger secrets). An
  attacker with file-read but no key can no longer fingerprint
  short-vs-long passwords by file size. Legacy entries open
  fine and re-pad on next Put.
- **Pre-auth banner buffered until host key verified.** The
  SSH BannerCallback fires *before* HostKeyCallback returns;
  painting it raw lets an unverified peer drop VT100 sequences
  (cursor jumps, title set, color reset) into the user's
  terminal. Banner now accumulates into a buffer per hop and
  only flushes after `ssh.NewClientConn` succeeds.
- **Pending-restore staged files encrypted.** Between
  `Restore()` and the swap that `ApplyPending()` runs at next
  start, staged `store.db` + `vault.enc` previously sat as
  plaintext under `pending-restore/`. They're now sealed with
  the same passphrase the user proved at restore time; the
  passphrase itself is sealed under the machine+user key so the
  next startup decrypts without re-prompting. A stolen
  pending-restore directory off the machine decrypts nothing.
- **Sidecar v2: real DPAPI on Windows, machine-id strict on
  Linux.** Auto-unlock sidecar no longer derives its key from
  `%COMPUTERNAME%` or hostname. Windows builds wrap with
  `CryptProtectData` (user scope); Linux requires
  `/etc/machine-id` and refuses to write v2 without it (v1
  fallback for containers etc still works). macOS keeps v1
  until Keychain integration lands.

---

## [0.24.0] - Tcpdump: 8 new decoders, custom port mapping, any-iface

**Eight new protocol decoders** in Verbose mode: HTTP (request +
response, method/path/Host/UA, status code/reason/content-type),
ICMP/ICMPv6 (echo, unreachable, time-exceeded, NDP), SSH banner
(client + server software version), NTP (mode/version/stratum),
SNMP (v1/v2c/v3 + community string + PDU tag), LDAP (op +
messageID), SMB (SMB1/2/3, encrypted SMB3, command name), MQTT
(packet type + PUBLISH topic). Each lands in the Decode tab as a
typed row with its own color in the proto column. DHCP / DNS /
ARP / TLS-SNI from earlier releases are unchanged.

**Custom port → proto mapping.** Top of the tcpdump modal now
has a chip row (Verbose only) where you teach the decoder about
non-standard ports - pick a port, pick a proto, hit Add. Now an
HTTP server on 9000 or an MQTT bridge on 1885 dissects properly
without forking tcpdump. Sent to the backend as
`port_overrides` on TcpdumpStart.

**"any" interface.** The interface dropdown now includes
Linux's `any` pseudo-iface up front. tcpdump supports it but
the kernel doesn't expose it through `/sys/class/net`, so it
never showed. Pre-select still picks a real NIC so a plain
Start doesn't quietly fleet-capture.

**Tcpdump memory bloat fix.** Two parts:
- Frontend: incoming packets now batch through a
  requestAnimationFrame queue, so a 1000 pkt/s burst triggers
  ~60 reactive Svelte updates per second instead of 1000. The
  in-memory cap also drops from 2000 to 800 in Verbose mode
  because each packet's payload + decoded fields runs ~5x
  larger than brief mode.
- Backend: hard cap of 256 lines on the verbose continuation
  buffer so a malformed stream or a giant hex dump can't grow
  payload without bound.

**Flow detail light-theme fix.** The expanded "flow packets"
panel inside the Flows tab had a hardcoded near-black
background (`#0e0e16`) that ignored the theme switch. Routes
through `var(--crust)` now, so light theme stays light.

**Decoder helpers refactored.** `findIPStart` auto-detects L2
vs L3 captures (`tcpdump -X` is L3 most of the time; -e or
some platforms include a 14-byte Ethernet header). The
previous helpers assumed L2 and silently returned -1 on L3
dumps, killing every text-protocol decode. `extractASCIIPayload`
now parses the hex column directly rather than guessing where
tcpdump's ASCII gloss starts.

---

## [0.23.2] - Local shell defaults, archive file picker, UI polish

**Local shell follows Settings.** The top-bar **Local shell**
button used to always hand `""` to the backend, which resolved
to the first available shell per platform (WSL on Windows
whether you wanted it or not). It now follows a new
`local_shell_kind` setting. The button label shows the current
default (e.g. `Local: WSL`, `Local: PowerShell`), the dropdown
chevron got a "Default for plain click" selector, and Settings →
Connection has a matching radio list filtered to the current
platform (no WSL row on Linux).

**Import archive: file picker.** Settings → Import archive
got a **Load file…** button next to Fetch. Pops a native open
dialog and reads the archive's contents into the textarea so
you don't have to copy-paste a 5 MB TOML by hand. 32 MiB limit
keeps a stray binary pick from freezing the renderer. The
chosen path is shown under the textarea for verification.

**Color picker custom hex actually applies.** Custom hex
override didn't reach the parent's editing state when the user
clicked Save before the input blurred (race in some WebView2
builds). Apply now fires on every keystroke as soon as the
value parses as a valid hex. Auto-prepends `#` when missing so
pasted `a9a9a9` works the same as `#a9a9a9`.

**Search button label trimmed.** The `Ctrl+K` pill next to
Search in the top bar looked cramped and was redundant with the
hover tooltip - removed. Search button is now just the icon +
"Search".

**Button heights aligned.** The Connect / Use different
credential / Delete trio in the connection detail header didn't
line up because `.ghost` painted a real 1px border while the
base `button` had `border: 0`. Base now uses
`border: 1px solid transparent` so every variant lands at the
same height, with `line-height: 1.2` for predictable vertical
sizing.

**`Ctrl+S` save-row label removed.** The Save row in the
folder / connection editor had a far-right `Ctrl+S` hint that
sat awkwardly aligned and added noise next to the prominent
Save button. The shortcut still works.

---

## [0.23.1] - In-app dialogs everywhere, tree shift-select fixes

**Native dialogs replaced.** Every remaining `window.confirm` and
`window.alert` call site now routes through the in-app
`PromptModal` / `ConfirmModal` / toast helpers introduced in
0.23.0. The ugly `wails.localhost says` chrome won't appear from
credential / port-forward / SFTP / palette / settings / update /
dynamic-entry flows anymore. Error popups become non-blocking
toasts; destructive confirms render with a red action button at
z-index 2000 above every editor overlay.

**Palette → terminal focus.** Closing the command palette
(Ctrl+K) or the snippet palette (Ctrl+Shift+P) with Escape now
returns focus to the active terminal so you can keep typing
without an extra click. Focus is only stolen when the terminal
view is current; opening a palette from Settings / Connections
still leaves focus where it was.

**Folder shift-click selects ranges.** Holding Shift while
clicking a folder row in the connections tree now selects the
range between the current anchor folder and the clicked target,
matching the existing behaviour for connections. Ctrl/Cmd-click
still toggles individual folders. Same range support was added
to credentials so you can multi-select credentials across folder
boundaries for batch delete.

**Cross-folder connection range fixed.** Shift+clicking a
connection that lived under an expanded grandchild folder was
selecting the wrong span (or nothing at all) because the flat
index used by the range walker didn't match the on-screen order.
The flat index now descends into subfolders first, then emits
sibling connections - matches what the user sees.

**Export archive: orphan parent_id fix.** Exporting a subtree
(quick-share or Settings panel) or a hand-picked set of
connections used to write `parent_id` / `folder_id` references
that didn't exist inside the archive itself. The importer's
defensive "place at root" fallback kicked in, dropped every
top-level folder under root, and emitted a long warnings list.
The exporter now strips those references at write time so the
import lands cleanly.

---

## [0.23.0] - Dynamic-to-static conversion

**Pin dynamic host as connection.** Single-entry detail pane gets
a **Pin as connection…** button that promotes a dynamic
inventory host into a permanent connection inside the same
folder. The new row carries any lifted Ansible vars
(`ansible_user`, jump chain) into its overrides; the per-attempt
credential override (if set) becomes the connection's
default credential. The pin is recorded in a new
`pinned_dynamic_entries` table - future inventory refreshes
skip the original `external_id` so the host doesn't appear
twice (once as the real connection, once as a dynamic ghost).
Deleting the pinned connection drops the pin via FK cascade and
the next refresh re-includes the host as a dynamic entry, so the
operation is reversible without a dedicated Unpin button.

**Convert dynamic folder to static.** Inside the dynamic folder
editor, a new **Convert to static…** action snapshots every
current host into a regular connection in the same folder, then
strips the provider link entirely (drops the `dynamic_folders`
row, clears cached entries, stops the refresh timer). The base
folder stays in place so inheritance keeps working for the
freshly-created connections. Useful when you're handed a one-off
Ansible inventory that won't change again, or when you want to
freeze a cloud provider's snapshot before retiring the API
token. Irreversible from the UI; existing pinned connections are
left untouched.

**Schema.** Migration `v15` adds `pinned_dynamic_entries`.

---

## [0.22.0] - Ansible inventory, password history, security hardening, UX polish

**Ansible static-inventory provider.** New dynamic-folder provider
reads a local `.ini` or `.yml` inventory file (extension picks
the parser) and surfaces every host as a flat dynamic entry,
with every group it belongs to as a tag. Per-host
`ansible_user` / `ansible_port` / `ansible_host` lift into SSH
overrides at connect time. Jump host parsed out of
`ansible_ssh_common_args` / `_extra_args` (`-J`, `ProxyJump=`,
and `ProxyCommand=ssh -W` shapes recognised). Dedicated
**Jump host credential** picker on the folder config (target
host creds rarely authenticate on the bastion) and per-connect
override for the jump host + jump credential. Native file
picker for the inventory path. New entry kind `server` (icon:
Server tower) so Ansible hosts don't carry the misleading "VM"
badge - consistent VM / LXC / Server icons across the tree,
Ctrl+K palette, and detail-pane header.

**Password / API-token history.** Every successful rotation now
snapshots the previous secret into a sealed vault entry (same
master key as the live credential) and records a row in the new
`credential_secret_history` table. The credential detail panel
gets a collapsed **Previous secrets** section listing the last 5
rotations newest-first: per-row Reveal / Copy buttons with the
same 30-second auto-clear as live reveals, and a × to forget a
single snapshot. Retention is hard-coded at 5 for now (slider
follow-up later). On credential delete every history vault
entry is purged alongside the live one. Schema bumped to v14.

**Quick palette polish.** Label always shows the entry's
canonical name (folder path + name) - typing a tag no longer
makes every matching row read as that tag. Tag chips render
in the sub-line with the matching one highlighted yellow so
the user still sees *why* a row matched. Dynamic entries are
indexed by tags too; auto-connect chain on bookmark click
opens the SSH session, starts the tunnel, and launches the
browser in one shot.

**Sidebar tag filter** narrows down by tag substring across
both regular connections and dynamic entries. `TreeNode`
respects the same match when expanding a dynamic folder so
only matching hosts show under the bucket.

**Security follow-ups (from the v0.16.0 audit).**

- **SSH agent socket path now validated** before we dial it. Any
  UNIX socket on the same machine would previously be dialled as
  if it were the user's agent and asked to sign challenges. We
  now require the path resolves to a real socket inode (not a
  symlink), is owned by the current user, and its parent
  directory is owned by the current user with permissions no
  looser than 0700. No-op on Windows where the agent is a named
  pipe with OS-managed ACLs.
- **Host-key challenge has a 2-minute timeout.** If the user
  closed the modal without responding (frontend bug, hard
  crash, or a deliberate hang), the connect goroutine sat on
  the response channel until app shutdown, holding a half-open
  SSH handshake. The callback now `select`s on the channel,
  context cancel, AND a 2-minute fallback that logs and
  rejects.
- **opkssh provider YAML is validated before the OIDC flow
  fires.** Hostile YAML pasted into the credential's config
  editor used to be honoured as-is: an attacker-controlled
  `issuer` could redirect the user's identity to a hostile
  IdP, and a non-loopback `redirect_uris` entry would
  exfiltrate the auth code (and any access_token) to an
  attacker host. We now require the issuer is `https://`
  (loopback `http://` allowed for OIDC dev work), every
  redirect_uri is loopback (`localhost`, `127.0.0.0/8`,
  `::1`), and the same checks run against the optional
  `remote_redirect_uri`. Per-rule unit coverage in
  `opkssh_validate_test.go`.

**Quality-of-life polish.**

- **Toast notification system.** Bottom-right corner host renders
  short non-blocking confirmations (`saved`, `failed: …`, etc.).
  DetailPane Ctrl+S, credential save / rename / change-type /
  API-token rotation / opkssh-config save / credential create
  now all fire a toast - visible regardless of where the Save
  button has scrolled.
- **About panel shows the live schema version** instead of a
  hard-coded "migration 10". Backend resolves `SchemaVersion()`
  from the store and ships it in `AppVersion()`; future
  migrations no longer require a doc update.
- **Status-bar version pill jumps straight to About.** Previously
  it opened Settings on the user's last-active section. The
  Workspaces "Manage…" link uses the same new
  `view.setTabSettingsSection()` helper.
- **Documentation refresh.** USER_GUIDE picks up the new
  shortcuts, tunnels-from-terminal flow, status-bar tunnels +
  toasts, and the auto-connect bookmark behaviour. `gotchas.md`
  archives the recent Windows-update CMD-window, xterm
  Ctrl+Tab, tab-focus-restore, palette-stopPropagation, and
  detach/redock-pane-tree traps.

---

## [0.21.0] - tunnels & bookmarks from terminal view

**Per-pane tunnel popover.** The SSH-only toolbar group on every
pane header gains a cable-icon button that opens a compact
popover listing every port-forward configured on the pane's
connection. Each row has a Start/Stop toggle that operates on
the pane's own session; dynamic (SOCKS5) forwards expand to show
their bookmarks, and each bookmark is one click away from
opening in the isolated browser (auto-starting the SOCKS5
tunnel first if it's down). The button shows a small green
badge with the running-tunnel count when at least one forward
on this session is up, otherwise no badge. Disabled when the
session isn't connected - the popover stays read-only rather
than auto-reconnecting.

**Quick palette indexes tunnels and bookmarks.** `Ctrl+K` now
matches forward descriptions and bookmark names alongside
connections and folders. A matched forward row shows
`↵ start` / `↵ stop` depending on its current state; a
bookmark row shows `↵ open` and routes through the SOCKS5
listener (auto-starts it first if needed). Forwards and
bookmarks are excluded from the empty-query view and given a
small ranking penalty so a query that matches both a host and a
tunnel description surfaces the host first. Search hits across
description, parent connection name, and "tunnel" /
"bookmark" / "browser" synonyms.

Backend gains `ForwardsListAll()` so the palette can build its
index in one IPC instead of one-per-connection.

### Bug fixes carried in this release

- Auto-update on Windows left a stray CMD window open after the
  app restarted. `DETACHED_PROCESS` only severs the parent's
  console; when the helper executes `cmd.exe` (a console-subsystem
  binary), cmd allocates its own console regardless. Swapped to
  `CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP` so the helper
  runs invisibly and the window-less behaviour is the actual
  flag, not a side effect.
- `Ctrl+Tab` / `Ctrl+Shift+Tab` / `Ctrl+1..9` did not fire when
  the terminal had focus: xterm's textarea swallowed the
  keystroke before it bubbled to the window-level listener.
  Terminal's custom-key handler now refuses those combos so the
  global shortcut handler in App.svelte gets them.
- Tab switching (via shortcut, `Ctrl+1..9`, or tab-bar click) no
  longer leaves focus on the previous tab's xterm. A shared
  `focusActiveTerminal` helper restores focus to the active
  pane's xterm after two animation frames, gated on the
  `.tab-content.active` host so the right tab's terminal wins.
  The snippet palette migrated to the same helper for
  consistency.

---

## [0.20.0] - tab navigation shortcuts + detach/redock keeps splits

**Keyboard shortcuts for tab navigation.** Five new global combos
on the terminal view:

- `Ctrl+Tab` / `Ctrl+Shift+Tab` - cycle to the next / previous
  tab with wrap-around.
- `Ctrl+1` … `Ctrl+8` - jump straight to tab N.
- `Ctrl+9` - jump to the last tab (Chrome / VS Code parity).
- `Ctrl+Shift+W` - close the active terminal tab. The Shifted
  variant is used so we don't fight readline's `delete-word` in
  the embedded shell.
- `Ctrl+Shift+T` - reopen the most recently closed tab. SSH
  connections are reopened via `sshConnect`; local-shell tabs
  aren't reopenable (their identity dies with the PID) and are
  skipped at close time. The undo stack is capped at 32 entries.

All five only fire when the terminal view is showing so they
don't hijack `Ctrl+1` on the sidebar or steal focus inside
Settings forms.

**Detaching a tab with split panes no longer flattens them into
separate tabs.** The detach IPC used to ship only the
comma-separated session IDs, so the detached window rebuilt the
tab by calling `addTab()` once per session - every split surfaced
as its own bare tab in the new window, and the same flattening
happened on the way back through redock. Detach + redock now
serialize the full `PaneTab` (title, pane tree, group name +
colour) as a URL-safe base64 JSON blob carried alongside the
session list. Both the detached window's bootstrap and the main
window's `window_redock` listener prefer that blob and fall back
to the old per-session reconstruction only when it's missing.
Internal pane / split / tab ids are regenerated on restore so the
moved tab can never collide with ids already in the destination
window.

---

## [0.19.4] - snippet focus + Check now modal

**Snippet palette returns focus to the active terminal.** After a
snippet fires, the palette closes and the view flips to the
terminal - but focus stayed on `<body>`, so the user had to click
into the pane before they could keep typing. The palette now
restores focus to the active `.xterm-helper-textarea` after two
`requestAnimationFrame` hops (waits out the modal teardown and
the terminal view's `display: none → flex` flip).

**Settings → Updates → "Check now" opens the release-notes modal.**
The page no longer pops a Download + Apply pair of buttons next
to the Check button. Instead a successful check with a newer
release available opens the same modal the status-bar pill uses,
giving the user the release notes, Download, and Restart and
install flow in one place. When no update is available a short
"You're on the latest version" line shows inline.

---

## [0.19.3] - audit log: wider table, sort, expand, substring filter

### Audit log

The Settings → Audit log table was cramped, truncated metadata
strings, and the action filter only accepted an exact match
(typing `vault` returned 0 hits because the events are
`vault.unlock`, `vault.rotate`, etc).

Changes:

- The section drops the 780 px width cap and the table expands to
  fill the window.
- Host / port, user, and a target name are extracted into their
  own columns so the row tells you "what host, which user" at a
  glance.
- Long values wrap inside their cells (`word-break: break-word`)
  instead of overflowing the container.
- Time and Action headers are sortable - click to flip direction.
- A per-row "expand" toggle reveals the full key=value blob in a
  grid; collapsed view shows the first two extra fields plus a
  `+N` chip.
- Filter is now client-side over the loaded page (no DB round-
  trip per keystroke) and searches the action, target, **and**
  every metadata value. `vault` matches every `vault.*` event,
  `ssh` matches both `ssh.connect` and `ssh.disconnect`, an IP
  like `10.0.1.5` matches every row that touched that host, a
  username like `root` matches by user. The label changes from
  "Filter action" to "Filter" to reflect the broader match.

---

## [0.19.2] - auto-update fixes

### Updates

**Status-bar "vX.Y.Z available" pill is now end-to-end.** Clicking
it opens the release-notes modal, which now downloads the new
binary and triggers the swap helper in-app, matching what
Settings → Updates already did. Previously the modal's Download
button only opened the system browser, leaving the user to
manually replace the exe.

**"Batch file cannot be found" on Windows install.** The apply
helper was spawned via `cmd /c start "" /b script.cmd` which
resolves the script path relative to whatever cwd cmd inherited
(often the user's Desktop, never the app's data directory). The
helper is now invoked with an absolute path under
`cmd.exe /c <abs>` with the working directory anchored to the
script's own folder. The relaunch inside the helper also pins
its working directory via `start "ssh-tool" /d "<exeDir>" %TARGET%`.

### Known issue

Unsigned binaries trip Microsoft Defender's generic ML detector
(`Wacapew.A!ml`) on a fraction of Windows installs. Code-signing
the release is on the roadmap; in the meantime download links
are SHA-256 stamped and the release page lists the expected hash
so users can verify out-of-band.

---

## [0.19.1] - richer audit metadata, dynamic folder shortcut, provider sort

### Audit log

`ssh.connect` and `ssh.connect.dynamic` now record `host`, `port`,
and `user` alongside the existing `session_id`. The dynamic flavour
also carries the entry's display `name`. `ssh.disconnect` reads
the session's metadata cache before the entry is freed so the log
line gets `host` + `name` too. Previously you could see "something
connected" but not what host or user - now the audit log answers
the only question that matters during an incident review.

### UI

- New "Add dynamic folder" button in the Connections sidebar header
  (globe icon) next to "New folder" / "New connection". Two
  alternative entry points (empty-area context menu, per-folder
  context menu) stay as they were.
- Dynamic provider picker is now sorted alphabetically: AWS EC2,
  DigitalOcean, Hetzner Cloud, Linode, Proxmox VE, Scaleway, Vultr.

---

## [0.19.0] - passphrase rotation, audit log, encrypted pre-restore, auto-update

### Security

**Master passphrase rotation.** Settings → Vault now has a "Change
master passphrase" form. Re-derives the file key from a fresh
Argon2id salt, re-encrypts every entry under the new key, and
atomically swaps the on-disk file. The vault must be unlocked
first; the old passphrase is independently re-verified against the
file on disk before any mutation so a stale in-memory key can't
silently destroy data. If an auto-unlock sidecar exists it's
refreshed with the new passphrase so subsequent launches keep
working. Existing backups still need the OLD passphrase to restore
- make a fresh backup right after rotating.

**Encrypted pre-restore safety snapshot.** `backup.Restore`
previously wrote a plaintext `store.db` + `vault.enc` into
`backups/pre-restore-<ts>/` so the user could roll back a bad
restore. That left two unencrypted copies of every credential
sitting under 0700 dir perms only. The safety snapshot is now
sealed with the same envelope format as a regular backup, keyed
with the live vault passphrase the user just typed. Recovery is
now identical to restoring any other backup file.

**Local audit log.** New SQLite table `audit_events` records
sensitive operations: vault unlock / lock / init / rotate, backup
create / restore, SSH connect / disconnect, update download /
apply. Settings → Audit log shows the live tail with filter +
CSV export + retention purge. The data never leaves the machine
- useful for "did I really connect to that host yesterday" and
for handing evidence to a compliance review without integrating
a SIEM yet.

### Updates

**One-click install on Windows.** Settings → Updates has a
"Restart and install" button alongside the existing check.
Downloads the new binary into the app's own directory, writes a
small `.cmd` helper that waits for the running exe to release its
file lock (poll-rename, up to 60 s), swaps the binary, and
relaunches. The app quits cleanly so the helper isn't fighting
the OS. On Linux / macOS the rename happens during the download
itself; you just relaunch yourself.

### Tests

Two new test files: `internal/creds/rotate_test.go` (round-trip,
empty-passphrase guard) and `internal/backup/backup_test.go`
(round-trip, wrong-passphrase, AAD tamper rejection, pre-restore
snapshot is encrypted not plaintext SQLite).

---

## [0.18.1] - themes, override block, dynamic multi-select, snippet fixes

### UI

**Three themes, full palette swap.** Settings → Appearance → UI
theme picks one of:

- **Mocha** (default) - Catppuccin Mocha. Same dark theme as
  before, now routed through CSS variables.
- **Latte** - Catppuccin Latte. Light background, dark text. For
  bright rooms / projector demos / anyone tired of dark mode.
- **High contrast** - Mocha with muted text steps and borders
  pushed up for direct-sun readability outdoors.

77 Svelte/TS files were swept to replace hard-coded mocha hex
values with `var(--token)` so the palette swaps cleanly. Two
exceptions stay raw on purpose: `lib/themes.ts` (xterm terminal
colour schemes, picked separately) and `lib/ColorPicker.svelte`
(connection / folder colour-tag preset swatches).

**Checked toggles get a coloured border.** `.toggle:has(input:
checked)` adds an accent border + 6% accent-tint fill so the
state is obvious. Settings radio cards got the same treatment so
checkboxes and radios look consistent across the page.

### Connect flow



**Inline connect error replaces the `wails.localhost says` alert.**
The dynamic-inventory detail pane now surfaces failed connects as a
styled inline error row (same look the regular DetailPane already
had), with a "Show raw error" disclosure and a Clear button. The
WebView2 native alert dialog is gone.

**Per-attempt credential override.** New "Use different credential…"
button in both the regular and dynamic-inventory detail panes.
Picks a credential from the dropdown, the next Connect press uses
it for the target hop only, then resets. Nothing is persisted; the
connection's saved auth_ref is left alone. Jump-hosts in the chain
keep their inherited credentials so a temp override doesn't leak
into a bastion.

**Multi-select on dynamic inventory.** Ctrl-click adds / removes,
Shift-click selects a range across the entries in a single dynamic
folder. The detail pane switches to a bulk view with two actions:
Connect all (opens N terminal tabs in parallel) and Batch exec
(runs one command across the selection, results modal). Batch exec
on the backend now accepts `dyn:<entryId>` synthetic ids and
resolves them through the same folder-inheritance path as
SshConnectDynamic.

**Snippet palette: shortcut, broadcast, navigation.** Ctrl+Shift+P
now actually opens the palette when focus is in a terminal (xterm's
key handler was swallowing the keystroke); the arrow-key navigation
inside the modal stopped working because of an overzealous
`stopPropagation` on the modal-keydown handler, fixed by routing
the navigation events through the local handler before stopping
propagation. Snippet send is now broadcast-aware on the backend -
firing into a session that's in an active broadcast group fans the
snippet body out to every member (SSH or local PTY), matching the
keystroke fan-out behaviour. The palette also switches to the
Terminal view after send so the user sees the result without
having to click the tab themselves.

**Keyboard-shortcut help in the tab toolbar.** New `?` button next
to the broadcast pill opens a modal listing every shortcut and
mouse gesture. Cleaner than burying them in docs. - CI/CD release pipeline + broadcast / terminal fixes

**CI/CD pipeline.** Releases now build through CI: an amd64 and
an arm64 runner build their respective Linux binaries natively,
while Windows amd64/arm64 cross-compile from the amd64 runner
with CGO disabled. A custom Ubuntu 24.04-based image
hosts the Wails + Go + Node toolchain, built per-arch and
combined into a multi-arch manifest. Pre-release tags
(`v0.18.0-test`, `v0.18.0-rc1`) upload artefacts to the
project's GitLab Generic Package Registry for testing without
touching the public release server. Plain semver tags publish
to sshtool.app as before.


**Tab-bar reorder.** Drag a tab onto another tab's label to
re-insert it at that position; cursor X relative to the target
tab's midpoint decides whether the source lands before or after.
A 2px accent bar shows the insertion side while you hover.

**Pane drop zones.** Dragging a tab (or sidebar connection)
over a pane shows left / right / top / bottom drop zones that
split the pane in the chosen direction. The earlier experiment
with a "centre = swap" zone is gone - replacing the session in
place was too easy to trigger accidentally and left users
without a way to recover the old pane.

**Drag-and-drop merge reuses the source session.** Dragging tab
A onto tab B used to do a fresh `ssh connect` for the merged
pane, which threw away whatever was running on A - open htop,
vim, a half-typed pipeline. The DnD path now moves the source
tab's active session into the target split (no disconnect, no
reconnect); side effect: broadcast membership and any session
state survive the gesture for free, because the sessionId is
the same row.

If the source tab had multiple panes (its own split), only the
active pane moves and the others' sessions stay in the pool
without a UI surface. That's a known limitation; if it turns
out to matter the merge can grow to preserve the full split
tree later.

**Local PTY allowed in broadcast.** WSL / local-shell sessions
can now be members of a broadcast group alongside SSH sessions.
The picker modal lists them, and the backend fan-out writes to
both pools. Useful for sweeps like "type `uptime` on three
remote boxes plus my local journal at once".

**Terminal last-row clipping.** Three layered fixes for the
bottom-row clip seen on htop / less at certain window heights:
two-pass fit() with `requestAnimationFrame` in between so the
second pass measures against committed cell geometry; 50ms
debounce on the ResizeObserver so a drag storm collapses to one
resize per pause; explicit row-count probe after fit that sheds
one row when the host has less than ~60% of a cell-height of
remainder (the descenders on the proposed bottom row would have
clipped). The terminal host also gained `overflow: hidden` and
the xterm wrapper got a `max-height: 100%` cap so the canvas
can't bleed past its container while shrinking.

**Settings checkboxes use the accent colour.** Native WebKit /
WebView2 default checkbox rendering on dark themes drew a
near-invisible grey tick on a near-black box. `accent-color` is
now pinned to the accent blue so checked toggles read at a
glance.

**Batch exec results expand by default.** The results modal used
to leave clean-exit hosts collapsed, which forced an N-click
expand pass to see what the command actually printed across the
fleet. New "Expand all" toggle, default on; per-host details can
still be collapsed individually.

---

## [0.17.0] - in-app release notes + arm64 build targets

**In-app release notes.** The "vX.Y.Z available" pill in the status
bar now opens a modal that fetches and renders the release notes
inline instead of pushing the user to the browser. Backed by a new
`/api/notes/{version}` endpoint on the release server. The
"Download" button in the modal still opens the binary URL in the
system browser - auto-update is a separate, larger piece of work.

**arm64 builds.** `scripts/publish-all.sh` now accepts `linux-arm64`
and `windows-arm64` as opt-in platforms. The default invocation
still only builds amd64; arm64 must be requested explicitly:

```
scripts/publish-all.sh windows linux linux-arm64 windows-arm64
```

Both are **untested** - author has no native arm64 hardware to
validate on. Windows-arm64 cross-builds cleanly because CGO is off
on Windows; linux-arm64 requires a one-time `task setup:docker` to
fetch the Wails cross-compile image.

Per-platform binaries are now named with their suffix
(`ssh-tool-linux-amd64`, `ssh-tool-windows-arm64.exe`, …) when
uploaded so multiple platforms coexist in the same release.

---

## [0.16.1] - relax FetchArchiveURL SSRF guard to metadata-IP only

v0.16.0's SSRF guard rejected loopback and RFC1918 alongside cloud
metadata, which broke legitimate use cases: catalog server on a LAN
(192.168.x.y), local dev catalog on 127.0.0.1. In a single-user
desktop app the attacker needs the user to paste a URL or click an
ssh-tool:// link - most internal targets are things the user is
actively asking for. Cloud metadata (169.254.169.254, fd00:ec2::254)
is the one IP the user would never knowingly fetch and an attacker
can't otherwise reach.

Guard now blocks only those two metadata IPs.

---

## [0.16.0] - security batch (audit-driven)

Multi-agent security audit covering vault crypto, SSH/opkssh paths,
and the IPC surface. Fixes ranked **Critical** and **High** are in
this release; remaining Mediums tracked for follow-up.

**Critical**

- **Host-key algorithm downgrade closed.** `known_hosts` previously
  keyed on `(host, port, key_type)` so an active MITM serving a fresh
  RSA host key after the user trusted an ed25519 key landed as an
  ordinary "unknown host" prompt instead of "CHANGED". The schema
  collapses to one row per `(host, port)`; on connect we now pin
  `HostKeyAlgorithms` to the stored algo so the SSH handshake aborts
  if the remote tries to serve any other type. Migration 12 dedupes
  legacy multi-algo rows (newest wins).
- **`SshLaunchInSystemTerminal` command injection closed.** The
  Windows / macOS / Linux paths each interpolated `hostname` and
  `username` into a shell string, so a hostname like
  `evil.example;calc.exe` (e.g. imported from a malicious RDM JSON)
  ran code locally on click. Each platform now receives a real argv:
  `wt.exe new-tab -- cmd /k ssh …` on Windows, `osascript` with POSIX
  single-quote escaping inside the AppleScript literal on macOS,
  `bash -c` with the same shellJoinPosix on Linux.
- **SFTP DownloadDir path traversal closed.** A hostile remote
  feeding `..`/absolute paths through Walk could write outside the
  user-picked local root (e.g. into `~/.ssh/authorized_keys`).
  Joined paths are now Clean'd and bounded against the canonical
  root with a separator-aware prefix check.

**High**

- **HostKeyCallback no longer fail-opens on DB error.** A transient
  SQLite error (busy lock, disk full) previously caused the callback
  to return nil - silently accepting any host key. It now returns
  the error and aborts the connect.
- **Backup envelope authenticates its header.** v1 sealed the JSON
  envelope with nil AAD, leaving `magic`, `salt`, `nonce`, and the
  Argon2 parameters tamperable. A flipped `m=1<<31` would OOM the
  process on Restore. v2 binds the full header as AEAD additional
  data and sanity-bounds the KDF params before deriving. v1 files
  still decrypt for backward compatibility (re-create them under v2
  by running a fresh backup).
- **opkssh email redacted from logs.** Cert ValidPrincipals (which
  for opkssh is the user's OIDC email) was written cleartext to the
  desktop log on every connect and every cert refresh. Logs now show
  only the count.
- **FetchArchiveURL SSRF closed.** Catalog bundle fetches now refuse
  loopback, link-local, RFC1918, ULA, multicast, unspecified, and
  the cloud-metadata IP. Check runs in `net.Dialer.Control` on the
  actually-connected peer so DNS rebinding can't slip a public name
  past resolution.

**Notes**

- Schema migration 12 runs automatically on first launch.
- Existing v1 backups continue to decrypt; new backups are v2 and
  cannot be opened by older builds.
- Per-(host, port) known_hosts dedup keeps the newest row; if you
  see a CHANGED-key prompt for a host you trust, verify the
  fingerprint against the server before accepting.

---

## [0.15.2] - auto-close tab on user-initiated signal exits

`closeOnCleanExit` now also fires for exit codes 130 (SIGINT) and 143
(SIGTERM), not just 0. Background: typical "Ctrl+C then Ctrl+D"
sequence in bash exits the shell with status 130 (last command was
the SIGINT), which is still a user-initiated end - same intent as
`exit 0`, the tab should close. Real failures (`exit 1`, segfaults,
network drops) still keep the tab open.

---

## [0.15.1] - terminal: optional WebGL-renderer toggle

New Settings → Terminal toggle: **Disable WebGL renderer (use canvas
fallback)**. Workaround for sluggish keystroke echo seen on Linux
WebKit builds where the WebGL pipeline ends up on software GL
(`LIBGL_ALWAYS_SOFTWARE=1`). xterm's canvas renderer is often faster
in that setup.

Default off. Takes effect on newly-opened terminal tabs (existing
tabs keep whatever renderer they were created with - reopen to
apply).

---

## [0.15.0] - five new dynamic inventory providers

Dynamic folders now speak DigitalOcean, Linode (Akamai), Vultr,
Scaleway, and AWS EC2 in addition to Proxmox and Hetzner. All reuse
the existing `api_token` credential shape - for AWS EC2 the
credential's `token_id` field doubles as the access key, the secret
holds the secret access key. Scaleway and AWS EC2 also need a
zone / region in the folder config (one folder per zone/region;
the APIs scope listings).

SigV4 is implemented inline for AWS rather than pulling
`aws-sdk-go-v2` - one endpoint (`DescribeInstances`) doesn't justify
that surface area.

The dynamic-folder editor learned a generic cloud-token form covering
all providers except Proxmox, with per-provider hostname-source
vocabularies (e.g. AWS adds "Public DNS", Linode/Vultr use "label"
where Hetzner/DO/Scaleway use "name") and per-provider token hints
pointing at the right control-panel page.

**Untested across providers** - the author doesn't have accounts at
any of the five. Feedback / bug reports welcome on any that misbehave.

---

## [0.14.0] - daily auto-backup

A background scheduler kicks off a backup at start and then hourly,
gated on "at least N hours since the last auto-backup file" (default
24h). Passphrase is recovered from the machine-bound auto-unlock
sidecar - if it isn't set up, the run is silently skipped so the
feature doesn't pester users who keep the vault locked across
restarts.

Auto-backups land in the same `backups/` directory as manual ones
but use a distinct `ssh-tool-auto-<ts>` prefix so pruning can leave
manual snapshots alone. After each successful run the pruner trims
auto-backups and `pre-restore-*` safety snapshots down to "Keep last
N" (default 7, max 365); manual backups never auto-delete.

Settings -> Backup & restore got an "Automatic daily backup" toggle
and a "Keep last N" number input. Changes flow through `SetConfig`
and the scheduler swaps its atomic config pointer without restart.

---

## [0.13.0] - encrypted backup & restore

A new Settings -> Backup & restore tab takes an encrypted snapshot of
the SQLite store and the vault file with one click. The bundle lands
in <DataDir>/backups/, sealed with the user's vault master passphrase
(Argon2id 64 MiB / XChaCha20-Poly1305). The database is captured via
SQLite VACUUM INTO, so the snapshot is consistent even mid-write.

Restore is two-phase to work around the fact that SQLite holds the
live store.db open on Windows: the chosen backup is decrypted,
verified against its embedded SHA-256 checksums, snapshotted into
backups/pre-restore-<timestamp>/ as a safety undo, then staged in
<DataDir>/pending-restore/. The next app start applies the swap
before opening the database, drops stale WAL/SHM, and invalidates
the auto-unlock sidecar. The UI tells the user to quit and reopen.

Backups don't auto-rotate yet; manual delete from the list works.
Scheduled backups + rotation will come in a follow-up.

The passphrase prompt grew a password-masked mode along the way
(used by Create and Restore here, but available to other callers).

---

## [0.12.8] - vault: lock actually locks

The vault had a silent third storage layer alongside the in-memory
mirror and the encrypted file vault: the OS keychain (Windows
Credential Manager, macOS Keychain, Linux secret service). Every
credential secret was mirrored into it on save and read back as a
fallback on lookup. Because those OS stores stay unlocked for the
entire user login session, a "locked" vault still leaked every
secret on the next Get(). The Lock button and the idle auto-lock
looked like they were working while actually doing nothing.

This release removes the keychain entirely. The encrypted file vault
plus the master passphrase (typed or recovered from the machine-
bound sidecar at startup) is the only persistence path. Lock now
also wipes the in-memory plaintext cache, so Get returns nothing
until a real unlock. Saving a secret while the vault is locked is
no longer accepted - previously it would land only in memory, vanish
on the next restart, and silently break the user's expectation that
saved means persisted.

A one-shot migration runs once at startup to delete legacy keyring
entries under the old service name. Credentials whose secrets only
ever lived in the keyring (e.g. tokens saved while the vault was
locked under an earlier version) will need to be re-entered once.

Frontend: after a lock - manual or via the idle auto-lock - the
sidecar auto-unlock is suppressed for that re-prompt so the
passphrase modal actually appears instead of being bypassed in the
same tick. Subsequent app starts still auto-unlock as before.

---

## [0.12.7] - redock: bring back local PTY sessions

Companion to v0.12.6. Re-docking a detached window worked for SSH
tabs but a local terminal tab evaporated: the main window's
`window_redock` handler walked `SshActiveSessions` only, so when
the matched session id was a local PTY, the loop skipped it and
the user ended up with the detached window closed but no tab in
the main window.

Handler now also walks `LocalShellList` and re-adds those
sessions to both stores with `kind: "local"`.

---

## [0.12.6] - detach: recover local PTY sessions too

The detached-window recovery loop only walked `SshActiveSessions`,
so tearing off a local terminal tab landed in a new window with
the tab strip rendered (session id visible) but no xterm canvas:
Terminal.svelte saw no entry in `sessions.tabs`, `isLocal()`
defaulted to false, and the SSH IPCs it tried for write/resize/
scrollback went nowhere.

DetachedWindow.svelte now also fetches `LocalShellList` and
adds each matching local session to both `sessions` and
`paneTabs` with `kind: "local"`, so Terminal.svelte routes
through `local_shell_*` IPCs.

---

## [0.12.5] - detached window: don't auto-close before tabs arrive

DetachedWindow.svelte had an auto-close $effect that fires when
`paneTabs.tabs.length` drops to zero (intentional for the Ctrl+D
session-cleanup path). Trouble was the same effect ran during the
window's first paint while sessions were still being recovered -
if recovery finished with zero matching tabs (e.g. the URL's
`?sessions=` list was empty or stale) the window slammed shut a
frame after open. Added a `hadTabs` latch so the auto-close only
arms after at least one tab has appeared.

Also defers wiring the WindowClosing hook on the detached
WebviewWindow until 500ms after creation, in case the Wails v3
alpha runtime fires a phantom WindowClosing during open on the
WebView2 build - would have torn down the freshly-transferred
sessions.

---

## [0.12.4] - single-instance hand-off scoped to deep links

v0.12.2 made every secondary launch hand off to the primary, which
broke detached terminal windows: Wails v3 spawns a child process
per detach and that process was exiting instantly because the
hand-off intercepted the launch. Now the single-instance code
only fires when this launch's argv actually carries an
`ssh-tool://…` URI or `--import-url=…` flag. Plain launches
(including Wails detach children) take the normal startup path.

---

## [0.12.3] - import: skip leading # comments

The catalog's multi-entry bundle prepends `# catalog-bundle …`
comment lines. ssh-tool's importer used to fail JSON detection
on those because it took the first character as the format
signal. New `skipLeadingComments` walks past every `#` line plus
surrounding whitespace before the JSON/TOML discriminator runs.
The JSON branch also reads the cleaned tail (encoding/json
doesn't tolerate the comments either); TOML keeps the original.

---

## [0.12.2] - single-instance handoff

Companion to v0.12.1. Two issues from real-world deep-link use:

### Single instance

Launching ssh-tool while it's already running used to spin up a
fresh process - second window, fresh tab pool, the deep link
landing in the new instance instead of the one the user already
has open. Now the second process detects the primary via a
loopback TCP listener (port persisted in `DataDir/instance.lock`),
hands off its argv + exits. The primary re-emits
`deep_link_import` and refocuses its window so the catalog
"Open in ssh-tool" click feels instant on subsequent uses.

Falls back to "act as the primary" if the lock file is stale
(port unreachable) so a hard-killed previous run doesn't trap
fresh launches forever.

Loopback-only, no auth on the wire - we trust anything that can
already write to `DataDir/`.

### Deep-link handoff

`startInstanceServer` runs alongside the cold-start dispatcher
so the same `parseDeepLinkArg` codepath fires either way: the
`deep_link_import` event lands in App.svelte with a 200ms delay
(vs 1200ms on cold start) and Settings → Import archive picks
it up via the existing deepLink store.

---

## [0.12.1] - register ssh-tool:// URL handler

Companion to v0.12.0. The deep-link feature only works if the OS
knows where to launch when a browser hits `ssh-tool://…` - and
nothing previously wrote that association.

### Features

- **`Register handler` button** in Settings → Import archive.
  Calls a new RegisterURLScheme IPC that writes the per-user OS
  registration:
  - Windows: `HKCU\Software\Classes\ssh-tool\…` keys pointing at
    the current exe.
  - Linux: `~/.local/share/applications/ssh-tool-url.desktop` +
    `xdg-mime default x-scheme-handler/ssh-tool`.
  - macOS: returns "not supported" - bundling the .app with the
    proper `CFBundleURLTypes` is the right way; runtime
    registration via private LaunchServices APIs would be fragile.
- **Status row** above the URL fetcher shows whether the handler
  is registered and, if so, the exact launch command it'll use.
- No admin elevation on Windows; no sudo on Linux.

After registering once, clicking "Open in ssh-tool" in the catalog
launches this app with the bundle URL pre-filled and the dry-run
preview ready.

---

## [0.12.0] - deep-link import from ssh-tool-catalog

### Features

- **`ssh-tool://import?source=URL` deep link.** When ssh-tool is
  launched with a deep-link arg (either the URI form via an OS
  protocol handler, or `--import-url=URL` via the command line),
  the app routes to Settings → Import archive, pre-fills the URL
  field, and fetches the archive automatically. The catalog gets
  a new "Open in ssh-tool" button per entry that fires this URL
  via a hidden iframe - works in Chrome / Firefox / Edge once the
  scheme is registered locally.
- **Import from URL** added to Settings → Import archive. The URL
  field sits above the archive textarea, with a Fetch button that
  hits the catalog's `/api/bundle?ids=…` endpoint (or any
  http(s) URL returning an archive payload). Fetched content
  lands in the textarea so dry-run / conflict mode still apply.
- New `FetchArchiveURL` IPC backs this: 20-second timeout, 10 MiB
  body cap, http(s)-only scheme guard. Server-side fetch sidesteps
  WebView CORS for catalog URLs that don't add CORS headers.

### Notes for protocol handler registration

Windows / macOS / Linux all need a one-time OS-level association
between `ssh-tool://` and the executable. The packaging step that
emits installers is the natural place to wire that up - until
then, the `--import-url=…` CLI flag works as the always-available
fallback.

---

## [0.11.0] - export port forwards + SOCKS bookmarks

### Features

- **Port forwards travel with the archive** now. Previously
  `Export folder → TOML/JSON` shipped only folders, connections,
  and credentials. The receiving ssh-tool re-created hosts but
  every forward + every SOCKS bookmark had to be rebuilt by hand
  - the most painful part of any cross-machine setup, since the
  bookmarks are typically internal URLs you'd never remember.
- Archive `Archive` struct gets a `forwards[]` block. Each entry
  carries kind / local addr+port / remote host+port / auto_start
  / description / bookmarks[]. SOCKS5 dynamic forwards round-trip
  with their full bookmark list inline.
- Importer reads `forwards[]` and recreates the rows under the
  newly-imported (or matched-existing) parent connection. Bookmark
  array applied via `SetPortForwardBookmarks` once the forward row
  exists. `ImportSummary` grows a `forwards_created` count.

### Notes for sharing via catalog

The companion catalog service (`ssh-tool-catalog`) detects the new
`forwards[]` block when you upload an archive and surfaces a small
chip on the entry card: `3 fwd · 5🔖`. Older archives without the
field are still accepted as-is.

---

## [0.10.0] - editable API token credentials

### Features

- **API token credentials are now fully editable** in the
  Credential detail pane. Previously only the metadata
  (name / hint / default username / icon) could be changed and
  the secret was write-once. Now:
  - **Token ID** field - for providers that use one
    (Proxmox: `user@realm!tokenid`). Hetzner Cloud leaves it
    blank.
  - **New token secret** field - type a new value to rotate;
    leave empty to keep the current one.
  - **Show token secret** button reveals the current secret in
    a read-only box (auto-hides after 30s), same UX as
    passwords and private keys.
  - "Save changes" is enabled when either field has a change;
    saving updates both in one call and bumps
    `last_rotated_at` when the secret moves.

### Backend

- New `CredentialsRotateAPIToken(id, token_id?, new_secret)`
  IPC + `creds.Service.RotateAPIToken` implementation. The
  token id passes through `UpdateCredential` (config map); the
  secret goes through the standard vault put + history append.

---

## [0.9.0] - dynamic entry detail pane + chevron fix

### Features

- **Click a dynamic entry, get the info ploča.** Previously a
  click on a Proxmox VM / LXC or a Hetzner server in the tree
  did nothing - only double-click connected. Now a single click
  selects the entry and renders a read-only DetailPane:
  - Header with name, status pill, provider chip, "from
    {folder}" breadcrumb.
  - Action buttons: Connect (with stopped-VM confirm),
    Copy host, Refresh inventory.
  - Facts grid with name, hostname, kind, plus provider-
    specific fields. Proxmox: resource type, hosting node,
    VMID, vCPUs, memory, disk, uptime. Hetzner: server id,
    server type, datacenter, image, public IPv4 / IPv6,
    private IPv4, created.
  - Tags / labels row.
  - Live load bars (CPU / Memory / Disk) for Proxmox, colour-
    coded by usage. Hetzner doesn't expose live load on the
    /servers endpoint, so the section is hidden there.
  - Collapsible "Raw provider payload" with the JSON the
    provider returned, for everything not covered above.

- **Proxmox payload widened.** The Proxmox provider now keeps
  `maxcpu / maxmem / maxdisk / cpu / mem / disk / uptime` from
  `/cluster/resources`, so the detail pane can show real numbers
  without an extra round-trip.

### Fixes

- **Chevron now appears on dynamic folders.** The tree was
  computing `hasChildren` only from manual children (folders +
  connections), so a dynamic folder with 50 entries but no
  manual rows lost its `▸ / ▾` arrow - you could expand it via
  double-click, but there was no visual hint anything was
  there. Now dynamic entry count (or the not-yet-loaded state)
  counts as having children too.

### Tech

- `DynamicEntry.Raw` switched from `[]byte` to
  `json.RawMessage` so the JSON IPC layer surfaces it as a
  parsed object on the frontend instead of base64.
- New `Selection` kind: `dynamicEntry` with
  `{ folderId, entryId }`. ViewStore clears it when switching
  to the Credentials tab.

---

## [0.8.1] - dynamic folder UX: optional token id + visual accent

### Fixes

- Inline "+ New token" form inside the dynamic-folder editor
  refused to save without a token id, but Hetzner Cloud tokens
  don't have one. Made the id optional (the standalone
  Credentials → New flow already accepted it as optional).

### UX

- **Dynamic folders look different** in the tree now:
  - Globe icon instead of folder.
  - Folder name + icon tinted teal so they stand out from
    regular folders at a glance.
  - Provider pill (`proxmox` / `hetzner`) sits inline next to
    the name.
  - A red `!` dot appears when the last refresh errored -
    hover for the message.
  - Tooltip on the name shows provider + "refreshed Nm ago"
    + last error if any.
  - Child count badge counts the cached dynamic entries
    instead of (empty) manual children.

---

## [0.8.0] - Hetzner Cloud dynamic inventory

### New

- **Hetzner Cloud provider** for dynamic inventory. Pick
  "Hetzner Cloud" in the provider dropdown when creating a
  dynamic folder; supply a Cloud Console API token (any
  permission level - read is enough for inventory). Pulls
  every server (paginated `GET /v1/servers`), maps them as
  guests, and respects the same hide-stopped + label
  whitelist/blacklist filters as the Proxmox provider.
- **Hostname source picker** (Hetzner specific): Cloud
  doesn't auto-DNS, so pick whether to connect by server
  name (relying on your own DNS), public IPv4, or first
  private-network IPv4.
- Labels arrive as `key=value` tag strings, so the whitelist /
  blacklist match them literally - `env=prod` and so on.

---

## [0.7.5] - info.json mutated tree, fixed via generated copy

### Fixes

- The Win-version-injection landed in v0.7.4 rewrote
  `build/windows/info.json` in place every build, so the next
  build always saw a dirty tree and stamped `-dirty` into the
  binary version (visible especially in the Linux build, which
  ran second in the publish-all chain). Switched to writing
  `build/windows/info.generated.json` (gitignored) next to the
  template and pointing wails3 syso at the generated copy.
  Source `info.json` stays as the canonical template.

---

## [0.7.4] - Linux binaries + correct Win .exe version metadata

### Fixes

- The Windows `.exe` Properties dialog reported version `0.1.0.0`
  on every build because `build/windows/info.json` hard-codes the
  template default and Wails embeds it as-is into the
  `VERSIONINFO` resource. The build chain now rewrites
  `file_version` + `ProductVersion` from `git describe` right
  before `wails3 generate syso` runs, so the Properties dialog
  matches the actual release.

### New

- **Linux amd64 build + publish path.** `task linux:build`
  produces `bin/ssh-tool`; `scripts/publish-all.sh` builds and
  uploads every platform in one go (Windows + Linux today;
  darwin scaffolded but not enabled by default). The release
  server's `/api/latest` now serves a `linux-amd64` asset
  alongside `windows-amd64`, so the in-app update check picks
  the matching binary per host.

---

## [0.7.3] - search reaches dynamic-inventory entries

### Fixes

- **Ctrl+K palette** now indexes every cached entry of every
  dynamic folder alongside regular connections + folders. Pre-
  fetches uncached folders when the palette opens so a search
  hits VMs in dynamic folders the user hasn't expanded yet.
  Selecting a dynamic entry goes through `SshConnectDynamic`;
  stopped guests still get the "connect anyway?" confirm.
- **Sidebar quick filter** now walks dynamic entries during
  `folderHasMatch`, so a dynamic folder auto-expands and
  surfaces matching VMs when the filter narrows. First
  keystroke of the filter eagerly pulls entries for all
  dynamic folders that haven't been opened yet, so the search
  isn't stuck on an empty cache.

---

## [0.7.2] - API token kind in the Credentials view

### Fixes

- Credentials → New… didn't list the new `api_token` kind, so
  the only way to create one was the inline "+ New" in the
  dynamic-folder editor. Added the option to the kind dropdown
  in CredentialCreate plus an icon (`globe`) in the tree.

---

## [0.7.1] - dynamic inventory polish: token-as-credential, hide-stopped

### New

- **API tokens live in the credentials vault.** A new credential
  kind `api_token` stores `{ token_id, secret }` with the secret
  in the vault. The dynamic-folder editor binds to a credential
  by reference (`api_token_credential_id`) instead of holding the
  secret inline. Side effects:
  - Same token can back multiple dynamic folders.
  - Secret rotation goes through Credentials → that token -
    nothing to re-enter in the dynamic-folder editor on every
    save.
  - On-disk dynamic_folders config no longer carries any secret
    material.
  - Editor has a "+ New" inline form so first-time setup is
    one screen.
- **Hide stopped guests** toggle in the dynamic-folder editor.
  Server-side filter - stopped guests are dropped from the cache
  entirely when set, freeing the tree from clutter on
  short-lived-VM clusters. Hosts are always kept (their
  "online"/"unknown" status is uninformative for this filter).
- **Stopped guests remain searchable** when the hide toggle is
  off: the name filter walks all cached entries before bucketing,
  so a query matches greyed-out stopped rows too.

### Internals

- Manager.resolveSecrets() inlines the resolved secret into a
  copy of the provider config just before Fetch. Providers stay
  unaware of the credential-reference indirection.
- New CreateCredential kind in creds.Service (`createAPIToken`).

---

## [0.7.0] - dynamic inventory (Proxmox)

### New

- **Dynamic inventory folders**: folders whose children are pulled
  from an external source instead of stored by the user. Tree
  right-click → **New dynamic folder…** or the empty-area menu's
  third option opens the editor. Children render under the same
  inherit cascade as regular folders, so the credential / port /
  jump host you set on the folder apply to every VM listed under it.
- **Proxmox VE provider**:
  - Single GET to `/api2/json/cluster/resources` per refresh -
    one call covers every VM, LXC, and node from the whole
    cluster. Works through a load balancer (no per-node
    failover logic needed).
  - PVE API token authentication
    (`user@realm!tokenid` + secret).
  - Optional self-signed TLS skip.
  - Include hosts (PVE nodes), guests (qemu + LXC), or both.
  - Tag whitelist + blacklist filters.
- **Hosts / Guests pseudo-sub-folders** under a dynamic folder
  group entries by kind so the tree stays readable on clusters
  with 100+ VMs.
- **Stopped-guest UX**: rendered greyed out with a "stopped"
  badge; a connect attempt prompts "Connect anyway?" since the
  VM may still be reachable on another address.
- **Refresh model**: per-folder configurable timer (default 5
  min, 0 disables) + **Refresh now** right-click action + lazy
  load on first expand. Errors surface in the folder editor's
  info panel; the previous good entry list stays visible
  during transient outages.
- **DNS-based hostnames**: VM `name` is used directly as the
  hostname (matches the user's setup where every VM/LXC has an
  FQDN A-record).

### Internals

- New `internal/inventory` package: `Provider` interface,
  `Filter` helper, `Manager` (timer + cache orchestration),
  Proxmox provider implementation. Roll-own HTTP client, no
  external dep.
- Migration 11: `dynamic_folders` + `dynamic_entries` tables.
  Folder row lives in the regular `folders` table so inherit
  cascade keeps working; the dynamic side data is a JOIN on
  `folder_id`.
- New IPCs: `DynamicFolderCreate / Update / Get`,
  `DynamicFoldersList`, `DynamicFolderRefreshNow`,
  `DynamicEntriesList`, `SshConnectDynamic`. The connect path
  constructs a synthetic `store.Connection` in memory and
  feeds it through the standard resolver + SSH layer - no
  persistent connection row.

---

## [0.6.3] - reproducible build + missing v0.6.1 release note

### Fixes

- `go mod tidy` was wired as a parallel dep of `generate:bindings`
  in the Wails Taskfile. Tidy walks the whole module tree
  including `frontend/`, racing against the concurrent
  `npm install` that adds/removes platform-specific
  `node_modules/` folders (fsevents, rollup-darwin, etc.). Every
  `task windows:build` invocation hit the race and either failed
  outright or stamped `-dirty` into the binary version. Dropped
  tidy from the build chain - run it manually when `go.mod`
  changes.
- v0.6.1 had no dedicated changelog hunk because the publish
  script extracts `## [vX.Y.Z]` by exact version match. Backfilled
  one (it was a re-tag of v0.6.0 after a clean `go mod tidy`).

---

## [0.6.2] - hide copy host/user/pass on local panes

### Fixes

- Local-shell pane toolbar still showed the copy host / username
  / password / ssh command buttons even though there's no
  remote to copy from. Hidden alongside the other SSH-only
  groups so local tabs only render the layout controls.

---

## [0.6.1] - build fix re-tag of 0.6.0

### Internals

- v0.6.0 tag pointed at a commit whose `go.mod` had `go-pty` in
  the `require ... // indirect` block from when it was a
  transitive dep through the wrong import path. The build chain
  fixed it via `go mod tidy` which marked the tree dirty and
  injected `v0.6.0-dirty` into the binary. v0.6.1 is the same
  feature set with `go.mod` already tidied so the build is
  reproducible from the tag.

---

## [0.6.0] - in-app local shell tabs

### New

- **Local shells as in-app tabs.** Opens a real PTY subprocess
  (bash / zsh / sh on Unix, WSL / PowerShell / cmd on Windows)
  and renders it through the same xterm pane as SSH sessions -
  scrollback, search, copy/paste, themes, fonts, splits all
  inherit unchanged. The split button on the top bar (Local
  shell + chevron) opens the default shell or pops a dropdown
  with platform-specific options. The old "Native terminal"
  external-window flow is still available at the bottom of the
  dropdown.
- Local sessions survive UI reload the same way SSH sessions
  do - the PTY lives in the backend pool, the tab repopulates
  on next mount via the new LocalShellList IPC.
- Per-platform shell auto-pick: respects `$SHELL` on Unix
  (falls back to bash on Linux, zsh on macOS); on Windows
  prefers WSL when installed, then PowerShell, then cmd.

### Changes

- Pane toolbar hides SFTP / tcpdump / HTTP / broadcast /
  reconnect buttons on local-shell panes - none of them make
  sense without an SSH channel underneath.

### Internals

- New `internal/local` package wraps
  `github.com/aymanbagabas/go-pty` for cross-platform PTY
  spawn + resize + scrollback. Mirrors the SSH-side
  `Pool` + `Session` + `scrollbackBuf` shape so the IPC layer
  can treat both kinds the same.
- New IPCs: `LocalShellOpen`, `LocalShellWrite`,
  `LocalShellResize`, `LocalShellDisconnect`,
  `LocalShellGetScrollback`, `LocalShellList`. Re-uses the
  existing `pty_output:<sessionID>` event channel so the xterm
  component needed only a tiny dispatch helper to pick the
  right write/resize/scrollback IPC per session kind.

---

## [0.5.5] - WSL as external terminal option

### New

- **WSL (default distro)** added as a fourth external-terminal
  choice on Windows. Native terminal button opens `wsl.exe`
  (preferring Windows Terminal as the host); Open in external
  terminal connection action wraps the resolved
  `ssh user@host -p … -J …` in
  `wsl.exe -e bash -lc "ssh …; exec bash"` so SSH uses the WSL
  distro's OpenSSH client, `~/.ssh/config`, and `known_hosts`.
  Useful when your WSL side already has SSH keys, agent,
  jumphosts, and identities the Windows OpenSSH client doesn't.
- macOS / Linux native terminal launchers were already correct
  for the OS default; the WSL choice has no effect there.

---

## [0.5.4] - opkssh credential exposes all config fields

### New

- **Edit provider hint, max cert age, and refresh threshold** on
  the credential detail panel for opkssh credentials. Previously
  only the provider YAML + key basename were editable; the
  other knobs were locked in at create time. Each input gets a
  short hint explaining what it does and the default.

---

## [0.5.3] - Updates moved to its own Settings section

### Fixes

- Update check setting lived under Settings → Connection, which
  was the wrong home - it's an app-level toggle, not a
  connect-time tunable. Promoted it to a dedicated **Updates**
  section under a new **App** group in the side nav.

---

## [0.5.2] - UI font size actually resizes, Appearance toggles styled

### Fixes

- **UI font size buttons did nothing.** App.svelte had a
  hardcoded `font-size: 14px` on html/body via `:global()` which
  beat the `var(--ui-font-size)` declaration that the setter in
  appPrefs flips on :root. Switched to
  `font-size: var(--ui-font-size, 13px)` so changes flow through.
- **UI font size editor**: bare `<input type="number">` replaced
  with `-` / `+` / Reset buttons. The input + onblur path was
  unreliable (stale parseInt could push NaN past the clamp and
  the apply() call silently dropped). Hardened setBaseFontSize
  to reject non-finite values, round, and re-apply even on
  no-op so reload edges still push the CSS var.
- **Appearance toggles** (color tag as row background, emphasise
  active session row, tab uptime timer) were bare checkbox
  labels - looked nothing like the bordered card checkboxes
  used for tray / external terminal / update check elsewhere
  in Settings. Converted to the same `.check-cards` fieldset
  so checked state shows the blue border + tinted background.

---

## [0.5.1] - update check, type-to-search, PS/cmd console fix

### New

- **Auto update check.** The app polls
  `https://sshtool.app/api/latest` 5 s after launch and then
  every 6 hours. A green "↑ vX.Y.Z available" pill appears in
  the status bar when a newer release is out; click opens the
  changelog. Default on, opt-out under Settings → Connection →
  Updates. With the toggle off no HTTP request leaves the app.
- **Type-to-search in the tree.** With the connections tree
  focused, hit any printable character and focus jumps to the
  search input, with the keystroke preserved (no first-char
  loss). Modifier-prefixed shortcuts (Ctrl+C copy, Cmd+A, etc.)
  are skipped so existing keybindings keep working.
- **Search row promoted to the top** of the sidebar, above
  Quick access and Tag filter. It's the most-used entry point.
- **Escape clears the filter** from anywhere in the tree - not
  just while focus is in the search input. Useful after
  ArrowDown / Enter moves focus into a row.
- **`scripts/publish-release.sh`** ships a binary to the
  release server: pulls version from `git describe
  --exact-match`, computes sha256 locally, extracts the
  matching CHANGELOG hunk, posts a multipart form with the
  bearer token from `$RELEASE_TOKEN` or
  `~/.config/ssh-tool/release-token`.

### Fixes

- **PowerShell / cmd external terminal launches no longer flash
  and disappear.** A GUI-subsystem process spawning shells
  directly leaves the child with no console host. Wrapped both
  the Native terminal button and the Open in external terminal
  connection action in `cmd /c start "" <shell>` so Windows
  allocates a new console and detaches the child cleanly.
  Windows Terminal was unaffected (ships its own conhost).

---

## [0.5.0] - folder export, target-folder import, native terminal

### New

- **Export whole folder subtrees.** Tree right-click on a folder
  has an "Export folder…" option that bundles every connection
  under it (recursively, nested structure preserved). Drives the
  same archive format as the connection-only export, so the
  import side hasn't changed.
- **Strip toggles in the export modal.** Tick to drop notes,
  tags, color tags, or convert dangling credential overrides to
  inherit (so shared exports don't carry broken `auth_ref`s).
  The preview regenerates reactively when you flip any toggle.
- **Import into a specific folder.** Settings → Import archive
  gains an "Import into" picker. Root folders / root connections
  from the archive land under the chosen folder; the archive's
  internal structure is preserved relative to that root.
- **Native terminal button** in the top tab bar opens a fresh
  local OS shell (no SSH attached). Windows respects the new
  preference; macOS opens Terminal.app; Linux uses
  `$TERMINAL` first, then a fallback list
  (`x-terminal-emulator`, gnome-terminal, konsole, xfce4-terminal,
  alacritty, kitty, foot, xterm).
- **Open connection in external terminal.** Connection right-click
  spawns the same external terminal but runs
  `ssh user@host` with the resolved port (`-p`) and jump-host
  chain (`-J user@bastion:port,…`).
- **External terminal preference** under Settings → Connection →
  External terminal. Radio cards: Windows Terminal (`wt.exe`,
  falls back to PowerShell), PowerShell (`-NoExit`), or Command
  Prompt (`/k`). Only matters on Windows - Linux/macOS pick
  their default.

---

## [0.4.0] - tray, warn-before-quit, tree filter, Delete/Ctrl+S

### New

- **Live name filter above the connections tree.** Search input
  filters by connection name OR hostname (case-insensitive
  substring). Folders auto-expand while the filter is active so
  matches don't hide under collapsed parents. Sits alongside the
  existing Ctrl+K quick palette - palette is fuzzy-fire-and-
  connect; this one narrows the tree so you can browse / select /
  right-click / edit as usual.
- **Search button in the top tab bar** with a `Ctrl+K` hint chip,
  for discoverability of the quick palette.
- **Delete key** removes the selected connection / folder /
  credential. Confirm modal lists exactly what will be deleted
  and auto-focuses the danger button so Enter confirms.
- **Ctrl+S saves** in the connection / folder editor. A green
  "✓ Saved" pill flashes briefly next to the Save button.
- **Right-click on empty area of the tree** offers New connection
  / New folder, matching the row-level menus.
- **Warn before quit when SSH sessions are alive.** Closing the
  window with at least one live session pops a confirm modal
  ("N active SSH session(s) will be disconnected"). Routed
  through a Wails hook on the main window so it actually blocks
  the close - registering it as a normal listener raced the
  default close handler.
- **Minimise to tray + close to tray** (split toggles under
  Settings → Connection → Window). Minimise hides the window
  when the user clicks the OS minimise button; close hides when
  they click X. SSH sessions and forwards keep running in the
  background. Tray icon (uses `build/windows/icon.ico`) has a
  Show window / Quit menu and toggles the window on click.

### Fixes

- **Multi-select visual for root-level connections.** Rows at the
  root of the tree only highlighted the selection anchor - they
  read from `selection.current` directly instead of going
  through `isConnectionSelected`. Now matches nested rows under
  folders.
- **Quick palette surfaces connect failures.** The palette
  swallowed errors silently before; failures now log + show an
  alert with the message.

### Removed

- **Status-bar "lock vault" button.** The pill click triggered
  `VaultLock(false)`, but the machine-bound sidecar re-unlocked
  the vault on the very next frame - VaultGate flashed and
  disappeared without locking anything. Hidden until the
  lock-vs-sidecar semantics are reworked; backlog entry tracks
  it.

---

## [0.3.3] - strip DECRQM to dodge xterm parser crash

### Bug fixes

- **vim still crashed after the v0.3.2 xterm bump** - the bug
  isn't fixed in 6.1.0-beta.220 either, and the throw fires from
  an async parser callback so try/catch around `term.write` can't
  catch it. Strip DECRQM (`CSI ? … $ p`) and DECRPM (`… $ y`)
  sequences from the byte stream before write. The remote uses
  these as feature probes; dropping them just means the remote
  falls back to defaults - same effect as the remote not getting
  a reply. The URL-in-file detail was a coincidence: vim emits
  the probe regardless of file content.

---

## [0.3.2] - vim crash with URLs, Ctrl-click to open links

### Bug fixes

- **vim crashed when opening files with URLs** in them.
  `Uncaught ReferenceError: s is not defined at requestMode` -
  upstream xterm 6.0.0 has an open bug in its DECRQM (request mode)
  handler that fires when the WebLinks addon is loaded and the
  buffer contains link-shaped text. Bumped `@xterm/xterm` to
  `6.1.0-beta.220` plus matching addons. Also wrapped every
  `term.write()` call in a try/catch so a single bad VT sequence
  can't poison subsequent output - a stray parser throw now lands
  in `console.warn` and the next chunk renders cleanly.
- **Plain click on URL no longer opens browser** - WebLinks addon
  now requires Ctrl (or Cmd on Mac) to open. Saves accidentally
  launching a browser while selecting near a URL; matches the
  VS Code terminal convention.

---

## [0.3.1] - vim-in-terminal works, multi-select robust across WebViews

### Bug fixes

- **vim / less / bash readline froze on Ctrl+F** - the in-app
  scrollback search bar was bound to Ctrl+F, intercepting it from
  the remote shell. Vim's page-forward, less's forward-screen,
  and bash readline's forward-char all silently went to the
  search input which then stole focus. Moved the shortcut to
  **Ctrl+Shift+F** (Cmd+Shift+F on Mac). Plain Ctrl+F now reaches
  the shell as it should.
- **Multi-select (Ctrl/Cmd+click + Shift+click) didn't work for
  some users** - some WebView2 / WebKitGTK builds drop modifier
  flags off the `click` event when the target is `draggable=true`.
  Added a mousedown-time modifier capture as a fallback across
  TreeNode, Sidebar root-conn rows, CredentialList, and
  CredFolderNode. Sidebar root-conn rows additionally gained the
  missing Ctrl/Shift handlers (only TreeNode had them before - a
  root-level connection couldn't multi-select at all).

---

## [0.3.0] - TLS SNI decoder, DHCP xid transaction grouping, verbose multi-line fix

### tcpdump

- **TLS ClientHello SNI decoder** - verbose mode now uses
  `tcpdump -v -X`. The hex+ASCII dump is re-assembled into raw
  bytes and walked through the TLS record → handshake →
  extensions to pull the SNI hostname. Decode tab shows
  "TLS ClientHello SNI: example.com" for any tcp/443 or
  tcp/8443 ClientHello. Truncated / non-ClientHello packets
  fail silently so the tab isn't littered.
- **DHCP transactions grouped by xid** - Decode tab now collapses
  packets sharing an xid into a single row with D / O / R / A
  pill stages (coloured per stage). Standard DORA cycles render
  as one timeline; non-DHCP decoded packets (DNS, ARP, TLS) drop
  into the flat list below.
- **BOOTP op inference** for non-standard captures. When the
  header is `BOOTP/DHCP, unknown (0x89)` (custom relay setups,
  port 67 → arbitrary), we infer BOOTREQUEST / BOOTREPLY from
  port direction (dst 67 = request, src 67 = reply) so the pill
  reads "BOOTREPLY" instead of "?".
- **Verbose multi-line parse fix** - tcpdump prints each packet
  on two lines under `-v`: timestamp + IP preamble, then
  "src.port > dst.port: …". The stdout pump now joins them
  before parsing so src/dst/proto are filled and packets
  actually reach the DHCP / TLS / DNS decoders.
- **Multi-line packet content** - full header + payload joined
  in `pkt.Raw` before `Decode()` runs, so regexes for fields
  like `xid` (which lives on the BOOTP header line, not the IP
  preamble) match correctly. Without this every packet collapsed
  into a single `(no xid)` transaction bucket.

---

## [0.2.1] - tcpdump payload decode (DHCP / DNS / ARP), modal selection fix

Follow-on to v0.2.0's Flows view - same feature family, no new
top-level surface.

### tcpdump

- **Decode tab** - new "Verbose (decode)" checkbox switches
  tcpdump from `-q` to `-v -nn`. Backend collects multi-line
  packet output and runs per-protocol parsers:
  - **DHCP** - DORA timeline summaries ("DHCPDISCOVER",
    "DHCPOFFER → 10.0.0.5", "DHCPACK → 10.0.0.5"); fields:
    msg_type / client_mac / requested_ip / assigned_ip /
    server_id / gateway / subnet_mask / lease_time / domain.
  - **DNS** - query qtype/qname + reply rrtype/rdata, txid,
    answer/auth/additional counts.
  - **ARP** - request/reply, target IP, sender, target MAC.
  Decoders + tests live in `internal/ssh/tcpdump_decode*.go`.

### UI / UX

- **Modal click-outside fixed** - selecting text and dragging out
  no longer tears down the modal. New `clickOutside` Svelte
  action tracks mousedown origin and only fires on
  press-AND-release outside. Applied across every modal
  (tcpdump, http, batch exec, snippet palette, quick palette,
  paste guard, prompt, credential create, …); HostKeyModal +
  VaultGate intentionally skipped (security choice).

---

## [0.2.0] - pane toolbar polish + tcpdump deep-dive scoped

### UI

- **Pane toolbar regrouped** - Copy / Tools / Session / Layout
  groups separated by subtle vertical dividers. Tools group:
  SFTP, tcpdump, HTTP. Session group: broadcast + reconnect.
  Broadcast button gets an orange-tinted icon + filled background
  when active so it stops disappearing among the others.

### Build

- Vite config silences the >500 KB chunk warning - desktop app
  loads from embedded FS so the warning is noise.

### Bug fixes

- Terminal scrollback was being re-fetched and re-written on every
  click in the terminal (parent `paneTabs.tabs` mutation made the
  `sessionId` prop look "fresh" to Svelte 5's $effect tracker).
  The visible buffer doubled with each click. Fixed by guarding
  the effect with an outer `wiredSid` ref and moving listener
  cleanup to `onDestroy` instead of the effect's return value.

### Docs / TODO

- New TODO entry: tcpdump packet flow + payload decode (Wireshark-
  lite). Conversation grouping, DHCP/ARP/DNS dissectors, optional
  tshark integration. Parked - design captured.

---

## [0.1.0] - first tagged release

The Wails v3 + Svelte 5 + Go rewrite reaches feature parity with
the daily-driver targets we set out to hit. Promoted from the
`wails3-experiment` branch to `main`.

### Connection management

- Folder tree with inherited settings (username, port, credential,
  jump chain, color tag, auto-reconnect, verbose, keepalive).
- Multi-select with tri-state batch editor (leave / inherit / set)
  per field including tags add/remove, color, credential, jump
  host, keepalive, auto-reconnect, verbose.
- Tag filter sidebar with plain tags AND auto-derived facets
  (`auth`, `user`, `via`, `port`).
- Drag & drop reorganisation (connections + folders) with
  before / after / inside drop intents.
- Quick palette (Ctrl+K) - fuzzy search across folders +
  connections.
- Custom icons (PNG / SVG / JPG / WebP / GIF, ≤ 256 KB),
  deduplicated by md5; "Choose existing" picker.
- Color tags with three resolution paths (connection override →
  folder ancestor → none); optional row-background tint.
- Right-click parity between connection and credential context
  menus (New subfolder / New X here / Rename / Move to / Delete /
  Export).
- Tree keyboard nav: Arrow Up/Down auto-select, Home/End,
  Arrow Left/Right collapse/expand, Enter to connect, Space to
  select.
- Sidebar scroll position survives every `tree.load()` reload.

### Credentials

- Vault: Argon2id (interactive-grade, m=19MiB t=2 p=1) +
  XChaCha20-Poly1305 AEAD; per-credential master record + file
  vault; optional OS keychain auto-unlock sidecar.
- Credential kinds: password, key (managed / file_ref / imported
  PEM), agent, opkssh, vault (placeholder).
- opkssh native (no external binary) - uses
  `openpubkey/openpubkey` + `openpubkey/opkssh` as Go libs.
- Credential folders, drag & drop reorganisation, icon parity
  with connections.
- Password strength meter on create / rotate / per-connection
  override.
- Idle auto-lock (`vault_autolock_minutes` setting, default off).

### Terminal

- xterm.js with WebGL renderer + canvas fallback; copy / paste
  modes (Windows / Linux / Mac), Ctrl+wheel zoom, Ctrl+F search,
  WebLinks addon.
- Multi-tab + binary pane splits (right / down / close); drag-out
  to detach to a second native window; drag-back to redock.
- Broadcast input across selected sessions, shared between
  windows (backend-owned membership).
- Themes (Catppuccin Mocha/Latte, One Dark Pro, Dracula, Nord,
  Solarized Dark/Light, Gruvbox Dark/Light, Tomorrow Night),
  configurable font family + size + scrollback limit.
- Paste guard for multi-line clipboard; per-session opt-out.
- Auto-close tab on clean exit (Ctrl+D), opt-in.
- Auto-reconnect with exponential backoff on unexpected
  disconnects.

### SFTP

- Per-session file browser with native OS drag-and-drop upload
  (files + directories) via Wails v3 `EnableFileDrop`.
- Pane-shared session model - open SFTP in a split without
  re-authenticating.

### Port forwards

- Local / remote / SOCKS5 dynamic forwards with byte counters,
  auto-start, OS-assigned port display.
- Isolated-browser launcher (Chromium / Firefox families) using
  a temporary profile so your main browser stays clean.
- Bookmark list per SOCKS5 forward - quick "open URL via this
  proxy".

### Power tools for sysadmins

- **Live tcpdump panel** - pane toolbar action; auth path
  auto-detects root / sudo-cached / sudo-prompt; auto-feeds the
  connection's saved password to sudo when applicable; live row
  stream with BPF + client-side filter; 5000-packet cap server-
  side.
- **HTTP / SOAP request modal** - GET / POST / PUT / PATCH /
  DELETE / HEAD / OPTIONS, headers + body, routes through the
  active session's SOCKS5 forward when present. JSON / XML
  pretty-print, TLS-skip-verify toggle.
- **Batch exec** - "Run command…" on a multi-selection. PTY-less
  parallel SSH (capped at 8 in flight) with per-host
  stdout / stderr / exit / duration aggregated in a modal.
  Save-as-snippet.
- **Snippets** (Ctrl+Shift+P) - fuzzy-search palette + CRUD in
  Settings; sends snippet body + newline to the active session.
- **Workspaces** - named tab bundles + CRUD. Status bar dropdown
  + Settings panel. Tabs carry optional group chips with
  deterministic colour from name.
- **Notes** - per-connection markdown with Edit / Preview
  toggle; HTTP/HTTPS links route to the system browser.
- **Live progress hint** on Connect (TCP dial → SSH handshake →
  Opening shell stages), friendly error messages with raw error
  behind a toggle.

### Import / export

- Devolutions RDM JSON import (folders, connections, jump
  chains, MD5-deduplicated icons).
- ssh_config import (ProxyJump chains; IdentityFile recorded in
  notes - we never auto-copy private keys off disk).
- Encrypted archive export / import (Argon2id + XChaCha20),
  TOML / JSON. Credentials excluded by default. Full OLD → NEW
  ID remap for cross-machine restore.
- Per-connection / multi-select right-click → Export modal.

### Observability

- In-app log tail (Settings → Logs) backed by a 2000-line ring
  buffer.
- Rolling log file at `%APPDATA%\ssh-tool\logs\app.log`
  (equivalent XDG path on Linux), 5 MiB cap, 3 historical files.

### UI / UX

- Density preference (compact / comfortable / cozy) + base font
  size in Settings → Appearance.
- Status bar (22 px) at the bottom: live session count, broadcast
  indicator, focused-pane info, vault state, Workspaces dropdown.
- Configurable connect timeout (Settings → Connection).
- Searchable credential dropdowns in DetailPane + JumpChainEditor
  (scales to 200+ credentials).
- Quick-action row: Copy Host / User / Password / ssh-command,
  Launch in system terminal (Windows Terminal / Terminal.app /
  Linux emulators).

### Multi-window (Wails v3)

- Native multi-window: drag a tab out of the tabbar to detach into
  a fresh top-level window; drag back to redock. Sessions live in
  the shared backend pool; scrollback survives the move.
- Detached windows auto-close once their last tab is gone (e.g.
  Ctrl+D); main window's session counter stays accurate.

### Schema

- Migrations 1..10. Auto-migrates older stores.

---

## Pre-history

For the long path leading here - Rust + Tauri prototype, the
russh / opkssh incompatibility that forced the move to Go,
Wails v2 phase, the v3 alpha experiment - see `CLAUDE.md` and the
commit history. The `rust-legacy` branch preserves the Rust
implementation; do not merge it back.
