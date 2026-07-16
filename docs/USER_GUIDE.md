# ssh-tool - User Guide

A cross-platform SSH connection manager. Tree of connections with
folder-level inherited settings, encrypted credential vault, multi-tab
terminal with split panes, SFTP browser, port forwards (including
SOCKS5 with isolated-browser launcher), opkssh certificate auth,
dynamic inventory from Proxmox VE, Hetzner Cloud, DigitalOcean,
Linode, Vultr, Scaleway, and AWS EC2.

This guide describes the features that exist in the application today.
Anything not listed here is either planned (see `TODO.md`) or not
implemented.

---

## Table of contents

1. [First launch - vault setup](#first-launch)
2. [Layout overview](#layout-overview)
3. [Connections tree](#connections-tree)
4. [Credentials](#credentials)
5. [Terminal sessions](#terminal-sessions)
6. [SFTP file browser](#sftp-file-browser)
6b. [VNC console](#vnc-console)
7. [Port forwards](#port-forwards)
7b. [Network profiles (WireGuard / NetBird / Tailscale)](#network-profiles)
8. [Broadcast input (multi-session)](#broadcast-input)
9. [Multi-window: detach + redock](#multi-window)
10. [Workspaces](#workspaces)
10b. [Dynamic inventory (Proxmox / Hetzner / DO / Linode / Vultr / Scaleway / EC2 / Ansible)](#dynamic-inventory)
11. [Quick palette (Ctrl+K)](#quick-palette)
12. [Snippets (Ctrl+Shift+P)](#snippets)
13. [Live tcpdump panel](#tcpdump)
14. [HTTP / SOAP request tool](#http-tool)
15. [Connect feedback](#connect-feedback)
16. [Color tags and visual cues](#color-tags)
17. [Custom icons](#custom-icons)
18. [Import / Export](#import-export)
19. [Settings](#settings)
20. [Logs](#logs)
21. [Keyboard shortcuts](#keyboard-shortcuts)
22. [Android / mobile](#android-mobile)

---

<a id="first-launch"></a>
## 1. First launch - vault setup

On first launch the app shows a **vault gate**. The vault stores
credential secrets (passwords, private keys, opkssh material)
encrypted on disk with:

- **Argon2id** key derivation (interactive-grade parameters:
  m=19MiB, t=2, p=1)
- **XChaCha20-Poly1305** authenticated encryption

Two paths:

- **Set master passphrase** (first run) - you choose the passphrase
  that protects the vault.
- **Unlock vault** (subsequent runs) - type the same passphrase.

Optional: **Remember on this machine** ticks the auto-unlock sidecar.
The next launch skips the prompt by deriving the unlock key from a
file kept under the OS keychain when available.

The vault file lives outside the SQLite DB so the encrypted secrets
don't share blast radius with the connection tree.

**Auto-lock**: Settings → Vault → "Auto-lock after idle (minutes)"
re-locks the vault and re-prompts at the VaultGate after the
specified idle period. 0 (default) disables. Open terminal sessions
and port forwards keep running across a lock - only the credential
tree is re-protected.

---

<a id="layout-overview"></a>
## 2. Layout overview

The main window has a top tab bar with four sections:

- **Connections** - folder tree on the left, connection editor on the
  right.
- **Credentials** - credential folder tree on the left, credential
  editor on the right.
- **Settings** - side-nav with grouped settings panels.
- **Terminal** - visible only while at least one session is open.
  Shows tab bar + pane tree.

On the right edge of the top tab bar:

- **Native terminal** button - opens a fresh local OS terminal
  (no SSH attached). Useful for quick local commands without
  leaving the app. Spawns Windows Terminal / PowerShell / cmd
  per Settings → Connection → External terminal on Windows;
  Terminal.app on macOS; `$TERMINAL` or a fallback list on Linux.
- **Search** button (`Ctrl+K`) - opens the quick palette.

The sidebar (left column) of Connections and Credentials views is
**resizable** via the drag handle between sidebar and detail. Width
persists across launches.

The **OS window and taskbar title** reflect what you're looking at:
the active terminal tab's name (e.g. `myhost - ssh-tool`), or the
section name on the Connections / Credentials / Settings views. Handy
when several windows are open or you're alt-tabbing.

The **window remembers its geometry** across runs: size, position,
which monitor (in a multi-display setup) and whether it was maximised.
Relaunching reopens it where you left it.

A **status bar** along the bottom (22 px) surfaces, from left to
right:

- **Workspaces** dropdown - recent workspaces + Save current as… +
  Manage…
- Live session count (clickable, jumps to Terminal). Coloured
  yellow if any are in error.
- **Active tunnels** indicator (green cable + count) when at
  least one port-forward is listening anywhere in the app.
  Hidden at zero.
- Broadcast indicator when a group is active (with member count).
- Focused connection name + host (Terminal view only).
- Update-available pill when a newer release is published.
- Version pill on the right - click to jump to Settings → About.

Toast notifications appear in the bottom-right corner for
non-blocking confirmations (saves, errors). Click a toast to
dismiss early, otherwise they auto-dismiss after a couple of
seconds.

---

<a id="connections-tree"></a>
## 3. Connections tree

### Structure

- Folders form a tree. Each folder has **inheritable settings**:
  default username, default port, default credential, jump host
  chain, color tag, auto-reconnect, verbose connect log.
- Connections live under a folder (or at root). They override any
  inherited setting per-connection.

### Quick access

A pinned **Quick access** panel above the tree lists:

- **Favourites** (any connection flagged with the star)
- **Recent** - last 10 connections you connected to (`last_used_at`).
  Excludes anything already in favourites.

Collapsible; collapsed state persists.

### Name filter

A live search bar above the tree filters by connection name OR
hostname (case-insensitive substring). While the filter is
active every folder containing a match auto-expands so you don't
have to chase them. Clear with the **×** at the right or by
emptying the input. This is separate from the Ctrl+K palette
(see §11) - the palette is one-shot fuzzy-and-connect; the
filter narrows the tree itself so you can still browse, select,
right-click, and edit.

### Tag filter

Above the tree, a **tag filter** row exposes every distinct tag
across your connections. Click a tag to filter the tree to entries
that wear it.

### Drag and drop

- Drag a folder or connection between folders (drop intents:
  before / after / inside).
- Drop on the empty area below the tree to move to root.
- Cycle prevention: you can't drop a folder inside its own subtree.

### Multi-select

- **Ctrl/Cmd+click** toggles individual rows into the selection.
- **Shift+click** selects a range - works **cross-folder** as long
  as both endpoints are visible in the current expand state.
- A **batch panel** appears at the bottom when more than one
  connection is selected. Tri-state per field:
  - **Leave** - touch nothing (default)
  - **Inherit** - strip the override so it inherits from parent
  - **Set** - write this value to all selected
- Batch panel also exposes:
  - **Connect all** - opens a terminal tab per selected connection.
  - **▶ Run command…** - fan out a single one-off command to every
    selected connection in parallel. No PTY, no terminal tab; each
    host opens its own quiet SSH chain, runs the command, returns
    stdout/stderr/exit. Useful for "uptime" / "df -h" / "systemctl
    status nginx" across a flotilla. Capped at 8 concurrent
    connections; per-stream output capped at 1 MiB; default
    per-host timeout 60 s. Save the command as a snippet straight
    from the modal so the next run is one click.

### Right-click on a connection

- **Connect** (or *Connect all (N)* in multi-select)
- **Open in external terminal** (single only) - spawns the OS
  terminal preferred in Settings (Windows Terminal / PowerShell /
  cmd on Windows; default app on macOS / Linux) running
  `ssh user@host` with the resolved port and jump chain. SSH
  client must be on PATH (Windows 10+ ships OpenSSH).
- **Open VNC console** (single only) - opens a noVNC remote-desktop
  tab. See [VNC console](#vnc-console).
- **Mark / Remove favourite**
- **Clone connection** (single only)
- **Export…** - opens an export modal carrying just this set of
  connections in TOML or JSON. See [Import / Export](#import-export).
- **Move to folder…**
- **Delete connection** (with confirm dialog listing each victim)

### Right-click on a folder

- **New subfolder…**
- **New connection here…**
- **Rename…**
- **Move to folder…**
- **Export folder…** - bundles the whole subtree (every nested
  folder + connection) into one TOML / JSON archive.
- **Delete folder** (cascade, with confirm)

### Live indicators

- A green ● next to a connection name means an active session for
  that connection is open.
- Folders carry a green pill with the count of connected subtree
  entries.
- Connecting state shows a small spinning loader; failed connects
  show an ✕ icon on the row (with tooltip carrying the reason).

### Connection editor

Two-column grid (auto-fit on narrow widths). Fields:

- **Name**, **Hostname**, **Username**, **Port**
- **Credential** - pick from your credential list, or leave blank
  to inherit from a folder
- **Password** - per-connection override stored in the vault under
  `conn_pass:<connID>`. Set / Clear buttons; an inline hint shows
  when a password is stored. This is independent of the credential
  selection above.
- **Jump host chain** - recursive editor. Each hop has hostname,
  user, port, credential, and optionally another `via` hop.
- **Color tag** - picker; resolves through folder ancestors if
  unset on the connection.
- **Auto-reconnect** - On / Off / Inherit. When on, sessions that
  drop without a manual Disconnect retry with exponential backoff.
- **Verbose connect log** - On / Off / Inherit. Emits TCP / handshake
  / auth diagnostics to the Connect diagnostics block below the
  header during connect.
- **Keepalive interval (s)** - seconds between SSH keepalive global
  requests. Empty = inherit, 0 = off. Useful for connections behind
  NAT that drop idle TCP sessions after 1-5 minutes.
- **Tags** - chip editor with type-and-enter.
- **Notes** - markdown textarea with an **Edit / Preview** toggle.
  Supports headings (`#` through `######`), `**bold**`, `*italic*`,
  `` `inline code` ``, fenced code blocks, `-`/`*`/`+` unordered
  lists, `1.` ordered lists, `---` horizontal rules, and
  `[link](https://…)` links. HTTP/HTTPS links open in the system
  browser. Use it as a mini runbook per host: what it does, who
  owns it, restart command, gotchas.
- **Icon** - Upload PNG/SVG/JPG/WebP/GIF (≤256 KB). Clears to the
  default monitor icon.

The header carries: favourite toggle (★), Connect button, **Use
different credential…** toggle, Resolved settings (collapsed; shows
the merged settings JSON for debugging).

### One-shot credential / username / password override

The **Use different credential…** button next to Connect opens an
override block with three independent fields:

- *Credential* - drop-down of every credential in the vault; pick
  one to use instead of the connection's saved `auth_ref`.
- *Username* - plain text; overrides whatever the inheritance
  chain would resolve.
- *Password* - plain text; sent as the SSH password for the next
  attempt only.

Any combination is accepted. **All three reset after the next
Connect press** - there is no way to leave an override active
without seeing it. Nothing is persisted; the connection's saved
credential / password / username are untouched. Jump-hosts keep
their inherited credentials so a temp override doesn't leak into
bastions in the chain.

Useful for: hitting a new host before you've made a saved
credential for it, testing a credential rotation, connecting as
root just this once to fix something, etc.

A matching block exists on dynamic-inventory entries (see § 10b).

Below the editor:

- **Quick actions** - copy buttons for Host / User / Password /
  ssh command, plus **Launch in system terminal** which opens the
  OS terminal preloaded with the equivalent `ssh ...` invocation.
  Password copy auto-clears the clipboard after 30 seconds. The
  system-terminal launcher prefers Windows Terminal on Windows,
  Terminal.app on macOS, and falls through gnome-terminal /
  konsole / xfce4-terminal / xterm / alacritty on Linux.
- **Port forwards** - see [Port forwards](#port-forwards).

### Empty state

When the tree has no folders and no connections, an empty card
points the user at the new-connection button + the import paths in
Settings.

### Loading skeleton

Cold load shows pulsing skeleton rows instead of a blank panel.

---

<a id="credentials"></a>
## 4. Credentials

The Credentials view mirrors the Connections layout: folder tree on
the left, credential editor on the right.

### Credential kinds

Each credential has a **kind** that determines auth behaviour:

- **password** - stored in the vault, sent to the remote during
  password auth.
- **key** - SSH private key. Stored in the vault (managed mode) or
  referenced by file path (file_ref mode).
- **agent** - delegates to an SSH agent socket (`SSH_AUTH_SOCK`
  by default; optional custom socket path).
- **opkssh** - OpenPubkey SSH certificate auth. Provider config
  YAML editable in the credential detail panel. Browser-based OIDC
  flow runs on first use; cert + key live in the vault. No
  `~/.ssh/` or `~/.opk/` files touched.
- **vault** - placeholder for other external secret managers
  (e.g. HashiCorp Vault). Schema in place; integration not yet
  implemented.
- **From KeePass** - the secret is read out of a registered KeePass
  `.kdbx` at connect time by entry reference; nothing is copied into
  ssh-tool's own store. Picked as "From KeePass database" in the
  credential editor. See the KeePass section below.
- **From Bitwarden** - the secret is read out of a registered
  Vaultwarden / Bitwarden server at connect time by item reference
  (organizations and collections included); nothing is copied into
  ssh-tool's own store. Picked as "From Bitwarden server" in the
  credential editor. See the Bitwarden section below.

### Storage modes

- **managed** - full secret material lives in the app vault.
- **file_ref** - credential row holds a path; secret stays on disk.
- **external** - for delegated kinds (agent, opkssh, vault).

### Tree operations

- Drag credentials between credential folders.
- Drop on empty area below the tree = move to root.
- Click a folder row to select it (chevron toggles expand).
  **Ctrl/Cmd+click** toggles folders in/out of a multi-selection,
  **Shift+click** ranges across the visible list - same gestures as
  the connections tree (credentials support the same).
- Right-click on a folder: **New subfolder**, **New credential
  here**, **Rename**, **Delete**.
- Right-click on a credential: select / reveal via the detail panel
  (no per-row context menu).
- **Delete is cascading and staged**: deleting a folder shows the
  same confirm modal as the connections tree, listing the folder,
  every credential inside and all subfolders - and deletes exactly
  that (credentials go through the full vault-cleanup path, they no
  longer drop flat to the root). The Delete key works on the
  current selection, multi-selections included.

### Credential detail panel

When a credential is selected:

- **Name**, **hint**, **tags**, **default username** - inline edit.
  Clearing the hint and saving removes it. Edit mode also exposes an
  **Icon** picker (upload PNG/SVG/JPG/WebP/GIF or choose from the
  library), same as connections and folders.
- **Reveal secret** - shows the password / private key for 30
  seconds, then auto-hides. Password / passphrase / token-secret
  input fields throughout the credential editor carry an inline eye
  toggle to unmask what you're typing before saving.
- **Rotate** - generates a new key (with optional passphrase) or
  imports a pasted private key in PEM/OpenSSH format. For password
  credentials, rotate writes a new password into the vault.
- **Used by** - lists the folders / connections referencing this
  credential. Each entry is a shortcut: click it to switch to the
  Connections view, expand the ancestor folders, select the row and
  scroll it into view.
- **Delete** - confirm dialog with usage references so you see
  which folders / connections still point at this credential.
- **History** - list of past rotations (timestamp / note /
  rotated_by). Metadata-only audit trail.
- **Previous secrets** (passwords + API tokens) - collapsible
  panel listing up to the last 5 rotations, newest first. Every
  successful rotation seals the previous value into the vault
  automatically; click **Reveal** for a 30-second view, **Copy**
  to put it on the clipboard (auto-clears after 30s too), or
  **×** to forget a single snapshot. Deleting the credential
  purges every history entry alongside the live one. Retention
  is hard-coded at 5 today; a configurable slider is on the
  roadmap.

When a credential **folder** is selected, the header exposes:
**Rename**, **+ Folder** (subfolder), **+ Credential** (creates in
this folder), **Delete**.

### Creating a credential

Click `+` in the credentials header (or **+ Credential** in a folder
header). Modal walks through:

- **Password** - name + password. A 5-segment **strength meter**
  shows entropy + label (very weak / weak / fair / strong / very
  strong) plus quick feedback (use 12+ chars, mix cases, add
  symbols, avoid common words / keyboard sequences). The same
  meter shows up under the rotate-password flow in the credential
  detail panel and the per-connection password override editor.
- **Generate key** - choose ed25519 / rsa / ecdsa, optional bits +
  comment + passphrase. The new private key is stored in the vault;
  the public key is shown to copy to remote `authorized_keys`.
- **Import pasted key** - paste a PEM / OpenSSH private key. Used
  for migrating existing keys into the vault.
- **Reference key on disk** - file_ref mode; we record a path,
  never copy the file content.
- **Agent** - optional socket path + fingerprint hint.
- **opkssh** - provider config YAML + max cert age / refresh
  threshold parameters.

### Icon

Credentials accept the same custom-icon upload as connections.

### KeePass databases

ssh-tool can read secrets straight out of a KeePass `.kdbx` file
instead of storing them itself. KeePass stays the source of truth -
the file is opened **read-only** and never written to.

**Register a database** in Settings → KeePass:

- **Source** - a local file (a **Browse** button opens a native file
  dialog so you don't type the path), or a remote file over **WebDAV**
  or **SFTP**.
- **Master password** and an optional **key file** - these are sealed
  in ssh-tool's own vault, so unlocking ssh-tool once opens KeePass
  too. There is no second prompt per connection.
- For remote sources, the transport credentials (WebDAV password, or
  SFTP password / host / user) are sealed in the vault as well.

**Reference an entry** two ways:

- Fastest: on a connection (or folder) in the detail panel, next to the
  **Credential** picker, click **From KeePass** (the button shows only
  once you have a database registered, and appears the moment you add
  one - no restart). A picker opens the database as a collapsible group
  tree with a search box (title / username / group), plus a **Refresh**
  button to pull an entry you just added in KeePass. Choose an entry and
  field, and ssh-tool creates a credential for it (named after the
  entry, filed under a "KeePass" credential folder) and assigns it right
  there. Picking the same entry again reuses the same credential.
  KeePass-backed credentials show a **database icon** and a "keepass"
  label so you can tell them apart from vault-stored passwords.
- Or from the credential editor: choose **From
  KeePass database**, pick the database, browse to the entry, and pick
  the field:

- the entry **password**, or
- a **custom field** holding a PEM private key, or
- an **attachment** (a private key file stored inside the entry).

The field type decides how it's used - an attachment or a key-looking
custom field authenticates as a private key; anything else as a
password. Entries are referenced by their **UUID**, so renaming or
moving them in KeePass does not break the link (only deleting the
entry does).

**Freshness for remote databases:**

- The file is fetched when you unlock the vault, and again on connect
  whenever the cached copy is more than a few minutes old - using a
  conditional request so an unchanged file isn't re-downloaded.
- If the remote is unreachable, the last cached copy is used and you
  are told it is **stale** rather than silently authenticating with
  old data.
- **Refresh** (in Settings → KeePass, and in the entry picker itself)
  forces a pull - use it right after adding an entry in KeePass Desktop.
- The cached file is stored encrypted (it is the original KeePass
  blob, worthless without the vault-held master).

Decrypted databases live in memory only and are **wiped the moment the
vault locks**, exactly like the vault's own secrets.

### Vaultwarden / Bitwarden servers

ssh-tool can also read secrets straight out of a self-hosted
**Vaultwarden** (or Bitwarden) server, **organizations and collections
included**. Like KeePass, the server stays the source of truth - it is
read **only** and never written to; the secret is decrypted at connect
time and never copied into ssh-tool's own store.

**Register a server** in Settings → Bitwarden:

- **Server URL** - e.g. `https://vault.example.com`.
- **API key** - sign-in uses an API key, not your password. On the
  server, open **Settings → Security → Keys** and view the API key
  (a client id + client secret). In ssh-tool, pick an existing API-key
  credential or click **Create** to add one inline.
- **Master password** - sealed in ssh-tool's own vault and used only to
  decrypt the fetched vault. It is **write-only**: entered once, never
  shown again, and never sent to the server.
- **Network profile** (optional) - if the server is only reachable over
  a VPN, choose a **WireGuard** profile to dial the sync through.

**Reference an item** the same two ways as KeePass:

- Fastest: on a connection or folder, click **From Bitwarden** next to
  the Credential picker (shown once a server is registered, live - no
  restart). The picker opens the vault as an **Organization → Collection
  → Item** tree with a search box and a **Sync** button. Choose an item
  and field, and ssh-tool creates and assigns a credential for it, filed
  under a "Bitwarden" credential folder. Bitwarden-backed credentials
  show a **shield icon** and a "bitwarden" label.
- Or from the credential editor: **From Bitwarden server**, pick the
  server, item, and field.

Resolvable fields: the item **password** or **username**, a **custom
field**, or a native **SSH-key** item's private key (which authenticates
as a key). Items are referenced by id, so renaming them on the server
does not break the link.

**Freshness and offline** mirror KeePass: the vault syncs on unlock and
re-checks when a cached copy is older than a few minutes; a **Sync**
button forces a pull. If the server is unreachable, a cached copy is
used and marked **stale** rather than breaking a connect. The cache is
sealed with your vault, and decrypted vaults are wiped from memory the
moment the vault locks.

Sign-in is API-key only (no email/password or interactive 2FA login),
and the server needs a certificate your OS trusts.

---

<a id="terminal-sessions"></a>
## 5. Terminal sessions

### Connecting

- Double-click a connection in the tree, click **Connect**, or hit
  Enter while a connection is anchored.
- Multi-select + **Connect all** opens N tabs in parallel.
- Each session lives in the **Terminal** view; the top nav grows a
  Terminal tab once at least one session exists.

### Tabs

- Tab bar **wraps** to multiple rows when names spill past a single
  row; capped at 26vh tall with vertical scroll past that.
- Each tab has a max width of 220 px and uses text-overflow:
  ellipsis on long names.
- Active tab gets a 2 px blue underline so it's findable across
  rows.
- Right-click on a tab: **Duplicate tab**, **Detach to new window**,
  **Record session** (or **Stop recording**), **Add to broadcast**
  (or **Remove from broadcast** / **Add remaining panes to
  broadcast**), **Ungroup tabs** (when grouped), **Close tab**.

### Session recording

Record a session's terminal output to an **asciicast v2** `.cast`
file - the format asciinema and every web cast player understand.

- Start via tab right-click > **Record session**, or the quick
  palette command **Record / stop recording session** (acts on the
  active pane).
- A pulsing red dot appears on the tab while recording. In a split
  tab, recording is per pane - the dot's tooltip shows how many
  panes are recording.
- Stop via the same menu / palette entry, or just close the session
  - the file is finalised either way, never discarded.
- Files land in `<data dir>/recordings/` by default, named
  `<connection>-<timestamp>.cast`; change the folder under
  **Settings > Terminal > Session recording**. A toast shows the
  full path on every stop.
- Recording captures **output only**. Keystrokes are never written,
  so a password typed at a sudo prompt cannot end up in the file.
  Resizes are recorded so playback reflows like the live terminal.
- Works for SSH and local shell sessions alike; start/stop is noted
  in the audit log.

### Playing recordings

**Settings > Terminal > Session recording > Browse recordings…**, or
the palette command **Browse session recordings**, opens the
recordings browser: every `.cast` file in the recordings folder with
date, duration and size. **Play** opens the built-in player - a
read-only terminal that replays the recording with:

- play / pause and a seek scrubber,
- playback speed (0.5x / 1x / 2x / 4x),
- **Skip idle** (default on) - output gaps longer than 2 seconds
  are jumped over, so a half-hour session with long silences plays
  back in the time the output actually took,
- mid-recording resizes replayed exactly where they happened.

**Delete** removes the file from disk (refused while that recording
is still running). Files are plain asciicast v2, so
`asciinema play file.cast` or any web cast player works too.
- Connection's color tag paints a 2 px top border on the tab.

### Split panes

A tab is a binary tree of panes. Each pane shows one terminal **or**
the SFTP browser for the same session.

Pane toolbar (top of each pane):

- Pane title (host name) with a left-side color strip if a tag is set.
- Connection status dot.
- Copy buttons: Host / User / Password / ssh command (colour-coded).
- Open **SFTP** browser on the same session as a split-right pane
  (only on a terminal pane).
- **Broadcast toggle** - when active the icon is orange (see below).
- **Reconnect** - disconnect + open a fresh session.
- **Split right** / **Split down**.
- **Close pane** - splits collapse to the remaining child.

### Terminal output

- xterm.js with the WebGL renderer (canvas fallback if WebGL fails).
  The glyph cache is cleared on every resize so full-screen TUIs
  (htop, btop) that repaint the whole grid don't ghost old cells over
  new ones; **Ctrl+Shift+L** forces a redraw if it ever happens
  in-place.
- Scrollback persists across pane detach/redock and UI reloads -
  backend buffers PTY output, frontend replays on mount.
- Themes - built-in choices including One Dark Pro and several
  Catppuccin / Dracula / Solarized variants. Persisted as
  `terminal_theme`.
- Font size - Ctrl+wheel zooms, persists as `terminal_font_size`.
- Web links - URLs in output are click-to-open in the system browser.

### Copy / paste

The app has three copy/paste models, picked by your platform on
first launch and saved as `terminal_copy_paste_mode`:

- **Windows** - Ctrl+Shift+C copies, Ctrl+Shift+V pastes. Right-click
  is smart toggle: copy if there's a selection, otherwise paste.
  Ctrl+C always sends SIGINT.
- **Linux** - auto-copy on selection, middle-click pastes.
- **Mac** - Cmd+C / Cmd+V.

### Paste guard

Pasting **multi-line** clipboard content opens a confirmation modal
showing line count, byte count, and a warning if shell metacharacters
are present. Per-session opt-out checkbox on the modal. The guard
intercepts at the host div in the capture phase so it fires before
xterm's textarea consumes the paste.

### Search

Ctrl+F (or click the search icon) opens an in-pane search bar over
the terminal. Enter / Shift+Enter for next/previous match, Esc closes.
F3 jumps to next match.

### Host key verification

First connect to an unknown host shows a **host key modal** with
fingerprint, key type, and three actions:

- **Trust once** - accept this session only.
- **Trust and remember** - store the fingerprint in the
  `known_hosts` table (TOFU).
- **Reject** - abort connect.

Subsequent connects compare; a changed key shows a sterner modal
with the old and new fingerprints side by side.

Multiple host-key prompts queue (e.g. parallel "Connect all"); the
modal shows "N more queued" so you know what's coming.

### Interactive username and password / 2FA prompts

ssh-tool does not force every connection to have a stored username and
credential up front:

- **No username?** If a connection (its target host) has no username
  configured, ssh-tool asks for one when you connect instead of failing.
  Useful when one key logs into several accounts on a server - leave the
  username blank and pick it each time.
- **Password or 2FA at the server's request.** If your stored key is
  rejected, or the server requires a typed password and/or a
  verification code (keyboard-interactive / PAM 2FA - e.g. a server
  offering `publickey,password,keyboard-interactive`), a prompt appears
  and your answers are passed straight through. Your configured auth
  (key, saved password, opkssh) is always tried first; the prompt only
  shows when that is not enough or the server insists.

A connection with no credential at all is fully promptable - you are
asked for the username, then for whatever the server challenges you with.
These prompts apply to the connection's **target host**; jump hosts in a
chain still use their configured credentials. Like the host-key modal,
the prompt flashes the taskbar and raises an OS notification when
ssh-tool is in the background.

### Auto-close on clean exit

If a remote shell exits cleanly (`Ctrl+D`, `exit 0`), the tab closes
after a 250 ms delay so the user sees the disconnect message briefly.
Abnormal closes stay open so the reason is visible. Toggle via
Settings → Terminal → "Auto-close tab on clean exit".

### Auto-reconnect

Connections with auto-reconnect on retry with exponential backoff
after a non-user-initiated drop. UI shows "reconnecting…" hint with
attempt number. Cancel via the Reconnect button on the pane toolbar
or by closing the tab.

### Counter

The top-nav Terminal tab carries a count of **live** sessions
(connected / connecting / reconnecting). Disconnected tabs that
remain open don't increment the count.

---

<a id="sftp-file-browser"></a>
## 6. SFTP file browser

Open the SFTP browser via the pane toolbar's folder icon - it splits
the pane to the right and reuses the same SSH session (no extra
auth).

### Listing

- Sortable columns: Name, Size, Modified time.
- Click a file / folder to select it; Ctrl/Cmd-click toggles into a
  multi-selection.
- Double-click a directory enters it.
- Breadcrumbs at the top - click any segment to jump.
- ↑ button = go to parent directory.
- Refresh button (↻) re-lists the current directory.

### Toolbar actions

- **Upload file** - native file dialog → file uploads under current
  cwd with the original filename.
- **Upload folder** - native directory dialog → recursive upload
  with progress tracking per file + aggregate.
- **Download** - single-file download via native save dialog;
  directories use a directory picker and mirror the tree under
  `<chosen>/<folder name>/`.
- **mkdir** - prompt for name, creates under cwd.
- **Rename** - inline rename.
- **Delete** - confirm dialog before unlink.

### Native drag-and-drop upload

Drop files and folders from your OS file manager directly onto the
SFTP pane. A dashed overlay with "Drop to upload to /current/path"
confirms the target while dragging. Each item becomes its own
transfer in the queue. Folders upload recursively.

Multi-pane SFTP works - each pane filters drops by its own session
id so the right pane gets the upload.

### Transfer queue

Bottom panel shows active and recent transfers with direction (↑/↓),
filename, progress bar, and bytes/files-done counters. Cancel button
on each row.

---

<a id="vnc-console"></a>
## 6b. VNC console

Open a remote desktop or VM console as a tab, rendered in-app by noVNC.
No external client, no X server, nothing to install. A VNC tab lives in
the same tab bar as terminals but is locked to one full pane - it never
splits or swaps to SFTP, because a desktop wants the whole tab.

Toolbar: **Fit / 1:1** scaling, **Ctrl+Alt+Del**, **Paste** (sends your
local clipboard to the remote), and **Reconnect**.

### Proxmox VM / LXC console

On a Proxmox dynamic-inventory entry (a VM or container), the detail
pane shows **Open console**. It reuses the dynamic folder's API token -
the app calls Proxmox's `vncproxy` + `vncwebsocket` for you and honours
the folder's `insecure_skip_verify` for self-signed clusters. Works for
QEMU VMs and LXC containers.

### Generic VNC (your own hosts)

Any saved connection can open a VNC console. In the connection editor,
the **VNC console** section has:

- **RFB port** - default 5900 (inheritable from the folder).
- **Reach the port** - *Direct* dials `host:port`; *Through SSH* dials
  `127.0.0.1:port` on the remote's loopback through the connection's
  SSH session (for a localhost-bound x11vnc / TigerVNC / macOS Screen
  Sharing). Tunnelling reuses the connection's SSH credentials and jump
  chain.
- **VNC password** - optional, stored in the vault. If empty and the
  server demands auth, noVNC prompts in the panel. Tunnelled
  localhost-bound servers often have no password.

Open it from the editor's "Open console" button or the connection's
right-click menu.

### How it works

Pixels flow over a loopback websocket bridge inside the app: noVNC
connects to `127.0.0.1` with a single-use token (no secrets in the
URL), and the app relays RFB to Proxmox or the tunnelled/direct VNC
port. The webview never needs custom headers or TLS-skip. Detaching a
console tab to its own window and redocking works like any other tab.

---

<a id="port-forwards"></a>
## 7. Port forwards

Below the connection editor: **Port forwards** section. Three kinds:

- **Local (L)** - `localhost:<local_port>` forwards to
  `<remote_host>:<remote_port>` through the SSH session.
- **Remote (R)** - server-side listener on `<remote_host>:<remote_port>`
  forwards back to `localhost:<local_port>` on your machine.
- **Dynamic (D - SOCKS5)** - local SOCKS5 proxy on
  `localhost:<local_port>`.

### Lifecycle

- Forwards are defined per-connection but only run while a session
  for that connection is connected.
- Each forward has a Start / Stop toggle on the row.
- The Forwards header shows running counts; rows show live byte
  counters (in / out).

### SOCKS5 + isolated browser

Each dynamic forward has an **Open URL…** button that launches an
isolated browser instance routed through the SOCKS5 port. Bookmarks
are per-forward - common URLs you've launched via this forward
appear as quick-launch buttons.

The launched browser uses a temporary profile so cookies and history
don't pollute your normal browsing. Configure the browser binary in
**Settings → Browser launcher** (Chrome, Chromium, Firefox, Edge are
all supported - pick the binary path).

### Tunnels from the terminal view

You don't have to scroll back to the Connections view to toggle a
tunnel or open a bookmark - every pane header gets a **cable**
button in the SSH-only toolbar group:

- Opens a compact popover listing every forward configured on
  that pane's connection.
- Each row has a Start / Stop toggle that runs against the pane's
  own session. Dynamic (SOCKS5) rows expand to show their
  bookmarks; each bookmark launches the isolated browser in one
  click and auto-starts the tunnel first if it's down.
- The cable icon turns **green** when at least one forward on
  this session is listening.
- The button is disabled when the pane's session isn't connected
  - reconnect first.

There's also a global indicator in the **status bar** (bottom): a
green cable + count of all listening forwards across the app, no
matter which window or pane they belong to. Hidden at zero.

### Give internet to an offline server

If the server you're on has no outbound internet, the tunnels
popover (cable button) has a **Give internet** section at the top.
Click it and ssh-tool:

- Raises a reverse tunnel on the server that listens on
  `127.0.0.1:3182` (loopback only, so nothing on the server's LAN
  can reach it). The port is overridable in the field next to the
  button if 3182 is taken.
- Serves the proxying **in-process** - it's a small HTTP CONNECT
  proxy built into ssh-tool, no squid or other tooling on either
  side.
- Shows a ready-to-paste `export http_proxy=... https_proxy=...`
  block. Run that in the server shell and its HTTP/HTTPS traffic
  (apt, curl, wget, pip, dnf) flows out through your machine.

DNS is resolved on your (ssh-tool) side, so the server doesn't need
a working resolver for anything it fetches through the proxy - that
is the whole point. The running proxy shows live byte counters and a
Stop button in the popover, appears in the forwards list, and tears
down automatically when the session disconnects. It is ad-hoc:
nothing is persisted, so it's off until you click it again next
time.

### Share a session with an LLM (MCP)

You can attach an external LLM client (Claude Code, etc.) to a live
SSH session so it can help you debug - read what's on screen, pull
logs, propose and run commands. It is **off by default** and
desktop-only.

Setup, once:

1. **Settings -> LLM (MCP) access** -> tick *Allow LLM (MCP) access
   to shared sessions*. This starts a local-only bridge (a unix
   socket on Linux/macOS, a loopback pipe on Windows). Nothing is
   exposed to the network.
2. Register ssh-tool with your LLM client. The Settings page shows
   the exact command with your binary's path. For **Claude Code**:
   `claude mcp add ssh-tool -- /path/to/ssh-tool --mcp-bridge`. For
   **LM Studio** (or any MCP client), point the server's `command`
   at the same binary with the `--mcp-bridge` argument - the Settings
   page shows a ready-to-paste `mcp.json` block.

   **Running the client in WSL while ssh-tool runs on Windows?** Turn
   on *Also listen on loopback TCP* in the LLM settings. WSL forwards
   `localhost` to Windows but can't see the Windows pipe, so the
   bridge uses a token-guarded `127.0.0.1` port instead. The Settings
   page shows a ready-to-paste command with the binary already
   translated to its `/mnt/c/...` WSL path - run that inside your WSL
   Claude Code.

Then, per session:

3. Connect the session and click the **Share with LLM** button in the
   pane toolbar (the robot-icon button next to tunnels) - *Read only*
   (the LLM can read scrollback and run allowlisted read-only commands)
   or *Read + run* (adds the ability to run other commands and type into
   the terminal, each gated). The button turns blue while the session is
   shared. The LLM only ever sees sessions you have shared.

What the LLM can do:

- **read_terminal** - the recent scrollback. This is handed to the
  LLM as untrusted data; a log line that says "run X" is not a
  command, only a tool call is.
- **run** - runs a command on a side channel and returns the output.
  Read-only commands (ls, cat, journalctl, systemctl status, ...)
  run immediately; anything that could change state pops an approval
  prompt where you **Run** it or **Deny**. You can extend the
  auto-run allowlist in Settings; mutating commands (sudo, rm, ...)
  always prompt.
- **type_into_terminal** - on approval, types text into your live
  terminal **without pressing Enter**, so you review it and submit it
  yourself.
- **list_connections / connect** - the LLM can also search your saved
  connections (by name or folder only - hostnames aren't exposed
  until a connect) and open one. Opening a session always asks you to
  approve first, and the new session is then shared with the LLM
  automatically so it can start working.

A session shared with the LLM shows a small robot-icon badge on
its tab, so you can always see at a glance which sessions the LLM can
see. Shared sessions are listed (and revocable) in Settings, and
every grant is dropped automatically when the session disconnects.

Everything the LLM does (run / type / connect / read) is recorded in
the **LLM activity** panel - open it from the robot icon in the status
bar (all sessions) or from a session's Share-with-LLM popover (that
session). Each entry shows the command, whether it auto-ran or needed
your approval, the exit status, and the captured output. It can also
be kept in the persistent audit log (a toggle in LLM settings).

For a system prompt that teaches your LLM client how to use these
tools well and safely (start with `list_sessions`, treat terminal
output as untrusted, respect approvals), see
[`docs/MCP_SYSTEM_PROMPT.md`](MCP_SYSTEM_PROMPT.md) - paste it into
your `CLAUDE.md` (Claude Code) or the system prompt (LM Studio).

### Share a session to a web browser

Let a colleague watch - or, with your explicit approval, type into -
a live session from a plain web browser. Nothing is installed on their
side; they open a link and see your terminal.

1. **Settings -> Sharing** -> turn on *Enable session sharing*. This is
   off by default. While it's on, the certificate fingerprint (a short
   word-code) is shown here, and you can regenerate it.

2. **Right-click a tab -> "Share to browser".** In the dialog you pick:
   - **which tabs** to share (defaults to the current one - it's a
     snapshot; tabs you open later don't appear unless you add them),
   - **read-only** (the guest can only watch) or **full control** (the
     guest types into the same terminal as you, tmux-style),
   - whether the guest sees the **existing scrollback** or only new
     output (default: only new),
   - the **network interface** to serve on (not `0.0.0.0` by default,
     so you don't expose it on a network you forgot about) and the port
     (8443 by default; if it's in use the share falls back to a free
     one).

3. You get a **link and a word-code** (like `cobalt-otter-viola-medley`).
   Send the link to your guest, and send the word-code separately (by
   phone or chat, not in the same message). When they open the link
   their browser will warn about the self-signed certificate - that's
   expected; they continue past it.

4. When the guest connects, **you get a prompt** showing their IP address
   and the word-code. Read the code back with them out-of-band; if it
   matches, allow them in. A leaked link is worthless without this - no
   session is streamed until you click Allow. The prompt flashes the
   taskbar and pops a notification if the app is in the background.

5. While a guest is attached you see a marker on the tab; a **full-control
   guest** is shown loudly (a red banner across the top). The status-bar
   share segment lists attached guests and lets you **kick** one or
   **stop all sharing** in one click. Closing the shared session (or the
   whole tab) ends the share and disconnects the guest.

**Live layout.** Splitting a shared tab, switching tabs, or adding a tab
to a share all follow through to the guest; the guest can follow your
active tab or click around on their own ("Follow host" re-syncs).

**Security model.** The connection is encrypted with a self-signed
certificate whose fingerprint is the word-code you compare - that
comparison, plus your per-guest approval, is what authenticates the
session (the browser cert warning is not). Read-only is enforced on the
backend, not just hidden in the UI. Both machines must be able to reach
each other: use it on a LAN or over your existing WireGuard / NetBird /
Tailscale profile. There is no cloud relay. Local shells are shareable
too, not just SSH sessions. Optionally, guest keystrokes can be recorded
to the audit log (off by default - the audit log is plaintext and a
controlling guest's keystrokes can include passwords).

### Quick palette shortcut

`Ctrl+K` matches forwards by description / parent connection name,
and bookmarks by name / URL. See section 11 for the full
behaviour, including the one-click bookmark flow that opens an
SSH session and starts the tunnel if needed.

---

<a id="network-profiles"></a>
## 7b. Network profiles (WireGuard / NetBird / Tailscale)

A network profile routes a connection's **first SSH hop** through an
overlay VPN, so you can reach hosts that only live on a private
network (a client's WireGuard, a Hetzner internal network, a NetBird
tailnet) without setting up a system-wide VPN. Everything runs in
**userspace**: no TUN adapter, no admin rights, no system routes, and
several profiles can be up at once toward different networks.

Manage profiles in **Settings -> Network profiles**. Assign one to a
folder or a connection via its **Network** setting (in the detail
pane); the setting inherits down the tree like the others, so a whole
"client-X" folder can go through one tunnel. A pane whose first hop
went through a tunnel shows a small VPN badge with the profile name,
and the status bar shows which tunnels are up.

Tunnels start on demand (first connection through them) and stop on
their own about two minutes after the last session using them closes.
Dynamic-inventory folders can fetch their provider API through a
profile too (for a Proxmox reachable only over the VPN); a background
refresh never starts a tunnel by itself.

### Connect policy

Each profile has a mode and a pause switch:

- **Always** - the first hop always dials through the tunnel; if the
  tunnel can't come up, the connect fails (it never silently falls
  back to a direct dial).
- **Auto** - probe a direct dial first (short timeout); only if that
  fails bring the tunnel up. Good for a host reachable directly when
  you're on-site and only via VPN from elsewhere. On-site connects
  show no VPN badge because they really went direct.
- **Pause** - a per-profile kill switch: every connection using it
  dials direct and the tunnel is stopped immediately. For "I'm on the
  network, leave the VPN alone."

### WireGuard profiles

Paste a standard `wg-quick` config (Interface + Peer). The private key
and any preshared keys are stored in the vault; the rest lives in the
profile. `PostUp` / `PostDown` / `Table` lines are ignored (there is
no system interface to script). DNS servers listed in the config
resolve hostnames inside the tunnel; without them only IP literals
work.

Editing shows the config back with secrets replaced by a `**KEEP**`
placeholder - leave it to keep the stored key, or paste a new key to
replace it.

**One identity across machines.** A WireGuard profile carries a single
key and overlay IP. Because the whole profile syncs (config + vault),
the same identity can end up on two machines. If a tunnel is left up
on one machine and you bring it up on another, both peers fight for
the same identity and both degrade. Stop it on the other machine
first, or use NetBird (below), which gives each machine its own peer.

### NetBird profiles

NetBird needs the optional **plugin** (a sidecar binary); install it
from the Plugins card in the same Settings page (one click, downloaded
from the matching release and checksum-verified). Until it's
installed, the NetBird option points you at the download.

> **Desktop only.** The NetBird plugin runs as a separate helper
> process, which Android can't spawn - so NetBird profiles are
> Windows / Linux / macOS only. WireGuard profiles, by contrast, are
> built into the core and work on Android too.

A NetBird profile needs three things:

- **Management URL** - your NetBird control plane. Blank uses the
  `netbird.io` cloud; for self-hosted enter the URL (a bare host like
  `vpn.example.com` is fine - it's normalised to `https://`).
- **Device name** - the name this peer registers under in NetBird.
- **Setup key** - stored as an API-token credential (the setup-key
  picker has a **+ New** button so you don't have to leave the page).

**Which key.** Use a **setup key**, NOT a personal access token
(PAT). In the NetBird dashboard: **Setup Keys -> Create Setup Key**.
The key is a UUID (e.g. `A1B2C3D4-...`); a PAT (starting `nbp_`) is a
different thing and will be rejected with "setup key is invalid".

- **Reusable** - use a reusable key if you sync this profile across
  machines: each machine registers as its own separate peer (no
  identity conflict, unlike WireGuard). A one-off key works for a
  single machine only and is consumed after one registration.
- **Ephemeral** - optional; an ephemeral peer is auto-removed after
  it's offline for a while. Convenient for laptops, but the machine
  re-registers (new peer) each time it reconnects, so pair it with a
  reusable key.
- **Auto-assigned group** - assign the setup key to a NetBird **group
  that has an access policy to the hosts you need to reach**.
  Registration alone doesn't grant access; NetBird's policies decide
  what the peer can talk to. If SSH through the tunnel connects but
  times out reaching the target, the peer is almost certainly not in a
  group with a policy to that host.
- **Expiration** - set a sensible expiry; an expired key stops new
  registrations (existing peers keep working).

Each machine keeps its NetBird registration state locally (under the
data dir); it is not synced, which is what lets each machine be its
own peer. Deleting the profile removes that state and the peer stops
checking in.

For a full step-by-step (which key, groups, access policies, common
errors) see **`docs/netbird-setup.md`**.

### Tailscale profiles

Tailscale works exactly like NetBird from ssh-tool's side - it needs
the optional **Tailscale plugin** (install it from the Plugins card,
downloaded and checksum-verified), runs as a userspace node (no TUN
adapter, no admin), and is **desktop only** (the helper is a separate
process Android can't spawn). Each machine registers as its own node,
so a synced profile is safe across machines (no single-owner conflict,
unlike WireGuard).

A Tailscale profile needs three things:

- **Control URL** - blank uses Tailscale's own coordination server; set
  it only for a self-hosted **Headscale** (a bare host is normalised to
  `https://`).
- **Hostname** - the name this node registers under in your tailnet
  (pre-filled from your machine's hostname). Tailscale lower-cases it
  and MagicDNS exposes it as `<hostname>.<tailnet>.ts.net`.
- **Auth key** - stored as an API-token credential. In the Tailscale
  admin console: **Settings -> Keys -> Generate auth key**. The key
  starts with `tskey-auth-`.

**Which key.** Use a **reusable** auth key if you sync this profile
across machines - each machine registers as its own node. Mark it
**ephemeral** for laptops if you want the node auto-removed after it's
offline for a while (it re-registers on reconnect). Tag the key
(**tags**) or rely on your tailnet ACLs so the node is allowed to reach
the hosts you need - registration alone does not grant access, the ACL
policy does. If SSH through the tunnel connects but times out reaching
the target, the node is almost certainly not permitted by the ACL.

Each machine keeps its Tailscale node state locally (under the data
dir), not synced. Deleting the profile removes that state and the node
stops checking in.

---

<a id="broadcast-input"></a>
## 8. Broadcast input (multi-session)

Broadcast lets keystrokes typed in one session fan out to several
others simultaneously. Useful for the "run this command on 4 boxes
at once" workflow.

### Groups

Broadcast is organised into **named groups**. There's an always-
present **default** group plus any user-created named groups. A
session can sit in zero, one, or several groups at once; a
keystroke from that session fans out to the *union* of every
group it belongs to (deduplicated).

Typical pattern: keep production cluster panes in `prod` and a
local sandbox in `default`; switch focus to a sandbox pane and
your test commands stay local, switch to a prod pane and the
same keystroke hits the cluster.

### How to enable

Three entry points:

1. **Pane toolbar** - broadcast icon (radio waves) toggles the
   active session in/out of the **default** group. Orange when
   any group contains this session. The hover tooltip lists
   every group the session belongs to.
2. **Right-click a tab** - "Add to broadcast" / "Remove from
   broadcast" (or "Add remaining panes to broadcast" when a tab
   has multiple panes and only some are in). Operates on the
   default group.
3. **Broadcast manager** - button on the right end of the tab bar
   opens a modal:
   - **Group** dropdown - pick the group you want to edit
     (or stay on default).
   - **+ New group** - prompts for a name, switches the picker
     to the new group so subsequent ticks land in it.
   - **Delete group** - only available for non-default groups.
     Members keep running, they're just no longer in this group.
   - Per-session checkboxes - toggle membership in the active
     group. Sessions already in another group show small chips
     listing those other groups.
   - **Show sessions in other groups** - off by default; the
     picker hides sessions that already sit in a different
     group so the active group's candidate list stays focused.
     Members of the active group always show so you can untick
     them.
   - Select all / Select none / Invert - operate on what's
     visible, not on hidden cross-group members.

### How it works

- Groups live on the backend as `map[string]map[string]bool` -
  group ID → member set. The empty key (`""`) is the default
  group. Every window observes the same state via the
  `broadcast_groups_changed` event so detached windows stay in
  sync. The legacy `broadcast_changed` event still fires with
  the default group's flat membership so older code paths keep
  working.
- Fan-out is server-side: a keystroke in any member session is
  written to every other member through one IPC call. The
  backend walks every group containing the origin and unions
  the targets, so a session in two groups broadcasts to the
  combined membership (deduplicated, origin excluded).
- All keys fan out - Ctrl+C, arrow keys, function keys included.
- Output stays per-session - each pane renders its own stream.

### Visual cues

- Every pane in any group gets a peach 2 px inset border and a
  "⊕ BROADCAST" badge in the top-right corner. When the session
  is in more than one group the badge adds an inline pill
  listing the group names (e.g. `⊕ BROADCAST [ops, default]`);
  hover for the full list.
- Tab labels show a small radio glyph next to the title; when
  the tab's sessions span 2+ groups the icon picks up a short
  peach pill with the names (`ops,dr`).
- The manager button on the tab bar and the status-bar pill
  both show the **total unique members across every group**,
  so a session in two groups counts once.

### When a session closes

The session is automatically removed from every broadcast group
through the OnClose hook that also handles forward cleanup.
Applies to both SSH and local-PTY sessions; manager reflects
this on the next open.

### Persistence

Groups live in memory on the backend; they do NOT survive an app
restart. Closing the last window or quitting the app clears
everything. Persistence is tracked in `docs/TODO.md`.

---

<a id="multi-window"></a>
## 9. Multi-window: detach + redock

### Detach a tab to a new window

Drag a tab out of the tab bar - the app opens a new top-level window
carrying just that tab. Right-click → **Detach to new window** does
the same explicitly.

The session pool is shared across windows. A detached window
recovers its session(s) from the backend pool and renders them with
xterm - same scrollback, same status events. **Split panes,
title, and group metadata survive the move** - the detach IPC
ships the full pane tree as an opaque blob, not just session IDs.

### Redock

Click the **Redock** button in the detached window's topbar, or
drag the tab back to the main window's tab bar. The pane tree
travels back with the same serialisation, so a redocked split-pane
tab arrives in the main window intact.

### Close behaviour

- Closing a detached window (X button) **disconnects every session
  that lived in it**. The connection-tree green dot goes away, the
  Terminal counter decrements.
- Redock clears the slot before close so the session keeps running
  in the main window.

### Broadcast across windows

Broadcast works across windows (see above). The set is shared via
backend events.

---

<a id="workspaces"></a>
## 10. Workspaces

A workspace is a named bundle of "these tabs in this layout". Useful
when you regularly bounce between several jobs - production for
client A, staging for client B, your own homelab - each with its
own 5-15 terminals open.

Manage them under **Settings → Appearance → Workspaces** or via the
**Workspaces** segment in the status bar (bottom-left).

- **Save current as workspace** - snapshots every open tab's
  connection + title + group metadata.
- **Open** - disconnects every currently open tab, then fans out
  connect to every connection in the workspace and restores the
  title / group chip.
- **Save here** - overwrite an existing workspace with the current
  tab set.
- **Delete** - remove the workspace; doesn't touch open tabs.

Splits inside a tab collapse to the focused leaf when you save -
multi-pane restore is on the roadmap. Sessions live in the shared
backend pool, so opening a workspace re-uses already-connected SSH
sessions when their connection ids overlap (no double-connect).

### Tab groups

Tabs can be tagged with a **group name** (right-click a tab →
*Set group name…*). The name shows as a small coloured chip in the
tab bar; the colour is deterministic from the name, so all tabs
sharing a group land in the same colour. Group metadata is part of
the workspace snapshot, so a "client-a · prod" group survives
Open/Save cycles.

Clear a group via right-click → *Clear group*, or set the name to
empty.

---

<a id="dynamic-inventory"></a>
## 10b. Dynamic inventory (Proxmox / Hetzner / DO / Linode / Vultr / Scaleway / EC2 / Ansible)

A **dynamic folder** auto-populates its contents from an external
provider on a refresh interval. The entries inside aren't stored
as connection rows in the DB - they're a live cache. Connecting to
an entry constructs an ephemeral connection in memory, inherits
the dynamic folder's credentials / jump host / SSH options, and
runs through the standard SSH layer.

### Creating a dynamic folder

- Right-click a parent folder (or empty tree area) → **New dynamic
  subfolder…** Pick a provider, fill in the config, save.
- Required: an **API token credential** stored in the vault. The
  folder editor has an inline "+ New token credential" form so you
  don't have to switch tabs.

### Providers

**Proxmox VE.** Config: base URL (the load-balancer URL works
fine - `/cluster/resources` returns the whole cluster regardless
of which node answers), API token id (`user@realm!tokenid`), and
the token secret credential. Filter: include hosts (Proxmox
nodes), include guests (VM + LXC), tag whitelist/blacklist,
hide-stopped. The hostname for SSH defaults to the VM name on the
assumption that DNS resolves it.

**Hetzner Cloud.** Config: API token credential, hostname source
(name / public IPv4 / private IPv4 - pick what reaches the server
from where you sit), label whitelist/blacklist, hide-stopped.
Hetzner has no DNS auto-record, so the hostname source picker is
load-bearing.

**DigitalOcean.** API token credential + hostname source
(name / public IPv4 / private VPC IPv4). Read-scope token from
Cloud → API → Tokens & Keys is enough. Droplet `tags` map straight
to the filter tag vocabulary.

**Linode (Akamai Cloud Compute).** API token credential + hostname
source (label / public IPv4 / private IPv4). Read-only Linodes
scope. Tags include a synthetic `region=<region>` plus the user's
own tags. Private-IP detection uses RFC1918 ranges + Linode's
192.168.128/17 private block.

**Vultr.** API token credential + hostname source (label / main
public IPv4 / internal IPv4). Read-access token. Tags include a
synthetic `region=<region>`.

**Scaleway.** API token credential + **zone** (required; e.g.
`fr-par-1`, `nl-ams-1`, `pl-waw-1`) + hostname source. One folder
per zone - the API scopes server listings by zone.

**AWS EC2.** Reuses the `api_token` credential shape: the
credential's token id field holds the **access key**, the secret
holds the **secret access key**. Folder config also needs the
**region** (`eu-central-1` etc.); one folder per region. Hostname
sources include EC2-specific options (Name tag, Public DNS, public
IPv4, private IPv4). The Name tag is the default. Tags include a
synthetic `region=<region>`. Requires IAM permission
`ec2:DescribeInstances`. SigV4 is signed inline so there's no AWS
SDK dependency.

**Ansible static inventory.** Reads a local `.ini` or `.yml`
inventory file (format auto-detected by extension). Config:

- **Inventory file path** - absolute path; **Browse…** button
  opens a native file picker.
- **Host pattern** / **Group pattern** - optional fnmatch globs
  (`web*`, `prod_*`).
- **Display name** - `inventory_hostname` (default) or
  `ansible_host` value if you'd rather see IPs in the tree.
- **Jump host credential** - SSH-capable credential
  (password / key / agent / opkssh) applied to every hop parsed
  out of `ansible_ssh_common_args`. Target host credentials
  almost never work on the bastion; without this picker connects
  through a jump host will fail authentication.

What gets lifted from the inventory at connect time:

| Ansible var | Becomes |
|---|---|
| `ansible_host` | actual SSH dial target (overrides hostname) |
| `ansible_user` | per-host username override |
| `ansible_port` | per-host port override |
| `ansible_ssh_common_args` / `_extra_args` | jump chain (`-J`, `ProxyJump=`, `ProxyCommand=ssh -W` all recognised) |

Each Ansible group the host belongs to becomes a **tag** - group
filter in the sidebar narrows to hosts carrying it, and Ctrl+K
matches tags too. Folder structure stays **flat** (every host as
a single entry); we don't mirror Ansible's DAG.

Per-connect overrides via the entry's detail pane
(**Use different credential…**): credential, username, password,
**jump host** (`[user@]host[:port]` - replaces the entire
parsed chain) and **jump credential** (one-off override of the
folder-level picker). All fields reset after Connect.

> The five non-Hetzner cloud providers were added together and have
> not yet been tested end-to-end against real accounts. Please file
> a bug if your provider misbehaves - the per-provider Fetch path
> is small and easy to inspect.

### Visual cues in the tree

- **Globe icon** instead of folder.
- Folder name + icon tinted **teal** so dynamic folders stand out.
- **Provider pill** (`proxmox` / `hetzner` / `ansible` / …) inline
  next to the name.
- **Red `!` dot** when the last refresh errored - hover for the
  message.
- Tooltip on the name shows provider + "refreshed Nm ago" + last
  error if any.
- Count badge shows cached entry count (or hidden if empty).
- **Per-entry icon** based on kind: VM → Monitor, LXC → Box,
  host / server → Server tower. Consistent across the tree,
  Ctrl+K palette, and the detail-pane header.

### Inspecting an entry

Click a VM / LXC / server / node to open its detail pane. The pane
is **read-only** - the source of truth is the provider, not the
local DB.

- Header: name + status pill + provider chip + folder breadcrumb.
- Actions: **Connect** (with stopped-VM confirm), **Copy host**,
  **Refresh inventory**.
- Facts grid:
  - Proxmox: resource type, hosting node, VMID, vCPUs, memory,
    disk, uptime.
  - Hetzner: server id, server type, datacenter, image, public
    IPv4 / IPv6, private IPv4, created.
  - DigitalOcean: droplet id, public IPv4, private IPv4.
  - Linode: linode id, region, public IPv4, private IPv4.
  - Vultr: instance id, region, main IPv4, internal IPv4.
  - Scaleway: server id, public IPv4, private IPv4.
  - AWS EC2: instance id, public / private IPv4, public DNS.
- Tags / labels row.
- **Live load bars** (CPU / Memory / Disk) for Proxmox, colour-
  coded green<60 / yellow<85 / red≥85. Hetzner doesn't expose live
  load on the `/servers` endpoint, so the section is hidden.
- Collapsible **Raw provider payload** with the JSON for everything
  not covered above.

### Connecting

- **Double-click an entry** in the tree (or click Connect in the
  detail pane) to start an SSH session.
- The session inherits the dynamic folder's credentials, jump
  host, and SSH options - same cascade as a regular tree branch.
- A **stopped** entry triggers a confirm dialog ("connect anyway?")
  in case the VM is reachable on another address or the status is
  stale.
- The session's connection id is `dyn:<entryID>`. It doesn't
  appear as a row in any connection list - it lives in the
  session pool until disconnected.
- A failed connect surfaces inline in the entry's detail pane
  (red row above the facts grid). The previous `alert()` modal
  with the `wails.localhost says` title is gone.

### Bulk actions

- **Ctrl-click** entries to toggle them in / out of a multi-
  selection set; **Shift-click** selects a range. The detail pane
  switches to a bulk view showing the count and two actions:
  - *Connect all* opens one terminal tab per selected entry in
    parallel.
  - *Batch exec…* runs a single command across the whole selection
    and shows per-host stdout / stderr / exit in a single modal
    (same component the regular multi-connection batch panel
    uses; results expanded by default).
- The Batch exec modal also has a **Load snippet…** picker so you
  can pull a saved global snippet straight into the command
  field before pressing Run.

### One-shot credential / username / password override

Same gesture as on regular connections: **Use different
credential…** next to Connect opens an inline block with three
independent override fields. Resets after the next press; nothing
persists. Useful for hitting a fresh dynamic host with a
different identity without editing the folder's inherited
credential.

### Refresh behaviour

- The folder refreshes on its configured interval (default 60s).
- Manual refresh via right-click → **Refresh now** or the button
  in an entry's detail pane.
- A refresh error leaves the previous entries cached (last good
  state) and surfaces the error via the red `!` dot.

### Editing the config

Right-click a dynamic folder → **Edit dynamic config…** Same form
as creation. Changing the refresh interval restarts the timer.

### Pinning a dynamic host as a permanent connection

Some hosts in a dynamic inventory don't really need to be
dynamic - they don't move, you've got custom notes for them, and
you want them to survive even if the API token gets revoked or
the provider rotates the IP. Click the host in the tree to open
the detail pane, then **Pin as connection…**. The host becomes
a real connection inside the same folder (any per-attempt
credential override you picked becomes the new connection's
default credential; Ansible vars like `ansible_user` and the
parsed jump chain are carried over). Future inventory refreshes
skip that host's external ID so it doesn't appear twice - once
as the real connection, once as a dynamic ghost.

To **unpin**: just delete the pinned connection like any other.
The next inventory refresh re-includes the host as a dynamic
ghost.

### Converting a whole dynamic folder to static

When the source of truth stops changing - you got handed a
one-off Ansible inventory, or you're retiring an API token but
want to keep the snapshot - open the dynamic folder editor and
click **Convert to static…**. Every cached host becomes a
regular connection in the folder, the provider link is dropped,
the refresh timer stops, and the folder behaves like any other
folder you created by hand. **Irreversible** from the UI; existing
pinned connections in the same folder are untouched.

---

<a id="quick-palette"></a>
## 11. Quick palette (Ctrl+K)

**Ctrl+K** (or Cmd+K) opens a fuzzy-search palette across folders,
connections, dynamic-inventory entries, port-forwards and SOCKS5
bookmarks. Type to filter by name, hostname, folder path,
description, or bookmark URL.

- ↑ / ↓ - navigate matches.
- **Enter** - context-sensitive: connect to a connection, start /
  stop a tunnel, open a bookmark.
- **Esc** - close.

Connections rank slightly above folders at similar scores; tunnels
and bookmarks rank slightly below so a query that matches both a
host and a tunnel surfaces the host first. The empty query view
shows connections / dynamic entries / folders only; tunnels and
bookmarks appear once you start typing so they don't swamp the
list.

### App commands

The palette also carries app commands: **Open Settings**, **New
local shell tab**, **Lock vault**, **Record / stop recording
session**, **Browse session recordings**, **Check for updates**,
plus an **Open workspace** row per saved workspace. Type `>` to
filter to commands only.

### Visual status

- Connection / dynamic entry label is **green** when at least one
  session is connected to it, with a `connected` pill (and `(N)`
  count when there is more than one).
- Forward row shows a `running` pill when the tunnel is listening,
  and the cable icon turns green.
- Bookmark / forward sub-line shows the parent connection name -
  blue when not connected, green when connected.

### One-click bookmark

Clicking a bookmark whose parent connection isn't connected yet
does the whole chain in one shot: opens the SSH session (and
registers a new terminal tab so you can see it), starts the SOCKS5
tunnel, launches the isolated browser at the bookmark URL.

---

<a id="snippets"></a>
## 12. Snippets (Ctrl+Shift+P)

Reusable command snippets you fire into the active terminal - `sudo
apt update`, `tail -f /var/log/syslog`, `kubectl get pods -A`, etc.

- **Ctrl+Shift+P** (or Cmd+Shift+P) opens the snippet palette from
  any view (Connections / Credentials / Settings / Terminal).
- Type to fuzzy-search; ↑↓ navigates, **Enter** sends the snippet
  body into the active session and closes the palette.
- The body is written verbatim plus a trailing newline so single-line
  snippets execute immediately. Multi-line bodies are sent as-is -
  no extra paste guard, snippets are your own content.
- **Broadcast aware.** If the origin session is part of an active
  broadcast group, the snippet body is fanned out to every member
  (SSH or local PTY) instead of just the foreground tab.
- The Batch exec modal has its own inline **Load snippet…** picker
  (since the global palette would fight focus with the modal) -
  picks a snippet and loads its body into the command field for
  editing before Run.

Manage the library under **Settings → Snippets**:

- **Name** - short label shown in the palette.
- **Body** - command text. Supports multi-line.
- **Tags** - comma-separated; used as additional fuzzy haystacks.
- Per-connection scoping exists in the data model (`connection_id`)
  but the editor doesn't expose a picker yet - every snippet is
  global by default.

Use count + last-used time are tracked so the palette can order by
recency on an empty query.

---

<a id="tcpdump"></a>
## 13. Live tcpdump panel

For when you want to see traffic on a remote interface without
juggling a second terminal.

Open it via the **Activity** icon in the pane toolbar (pink,
between Broadcast and Reconnect).

What happens:

1. The app probes the remote host: `whoami` first, then
   `sudo -n true`. Header reflects the result:
   - **running as root** - tcpdump runs directly.
   - **sudo (no password)** - cached or NOPASSWD ticket is good.
   - **sudo (saved password)** - the connection's login password is
     auto-fed into sudo on Start.
   - **sudo will prompt** - you'll be asked for a password after Start.
2. `ls /sys/class/net` populates the interface dropdown plus
   `any` (the Linux pseudo-iface that captures across every
   device - not in sysfs, added by us). **`any` is pre-selected**
   so you start by seeing all traffic regardless of which
   interface it rides; narrow to a specific NIC once you know.
3. Pick an interface, optionally type a **BPF filter** (e.g.
   `host 10.0.0.1 and port 443`), set **max packets** (default 500,
   capped to 5000 server-side), or tick **Continuous** to run
   without a packet cap until you stop it (needed if you want a
   long-lived capture that survives detach - see below).
4. Optional: toggle **Verbose (decode)** to flip tcpdump from
   `-q` to `-v -X`. With Verbose on the **Decode** tab populates
   with per-protocol field dissection (see below) and the row
   cap drops to 800 to keep the renderer responsive on bigger
   packets.
5. **Insights** (on by default) runs a live network-health
   analyzer over the stream - see the Insights tab below.
   - The capture **excludes its own SSH control connection** by
     default. Capturing over the same SSH session would otherwise
     feed back on itself (every captured packet is sent back over
     SSH, generating more SSH packets to capture). This works on any
     SSH port. Add the SSH session's own traffic to your BPF filter
     only if you specifically need to see it.
   - A **snapshot length** is applied so tcpdump doesn't ship every
     packet's full payload over the link - headers in brief mode, a
     larger window in Verbose so DHCP / DNS / TLS-SNI still decode.
   - On a very high packet rate a banner suggests adding a BPF
     filter to narrow the capture. It's a hint, not a hard stop;
     the live view only renders the most recent rows anyway.
6. Optional (Verbose only): add **Custom port → proto** chips
   to teach the decoder about non-standard ports (HTTP on 9000,
   MQTT bridge on 1885, etc). Without this, the decoder only
   recognises traffic on the protocol's well-known port.
7. Click **Start**. Live rows stream in as tcpdump prints them in a
   plain scrollable list, newest at the bottom; it auto-follows the
   tail unless you scroll up to read history. A status line under the
   header shows the live capture context (interface · BPF ·
   verbose/insights/continuous); the packet count is in the footer.
8. The **Filter captured rows** input does substring matching on
   already-captured lines (client-side; doesn't affect what tcpdump
   is collecting).
9. **Minimise** (the `-` button) sends the capture to the
   background: the modal hides but the capture, Insights and
   counters keep running. The bottom status bar shows a pink
   activity segment with the running packet/insight totals; the
   pane's tcpdump icon grows a small green dot. Click either to
   restore. **Close** (`✕`) stops the capture and tears it down.
10. **Stop** sends SIGINT so the kernel flushes any buffered packets,
    then closes the auxiliary SSH session.

### Background captures, detach & re-attach

A capture is tied to its **session**, not the window or pane that
started it. That means:

- Switching tabs, splitting the pane, opening an SFTP split,
  closing one side of a split - none of these interrupt a running
  capture.
- **Detaching a tab** to its own window keeps the capture alive on
  the backend. The new window discovers it automatically (a pink
  segment appears in its status bar) and re-attaches: it pulls the
  last 2000 packets of history from the backend ring buffer and
  then continues the live stream. History captured before the
  detach is preserved up to that ring size.
- Re-docking the tab works the same way in reverse.

Because counts and history are surfaced in the status bar and the
modal header, you always see what's running and where - even after
moving the session between windows.

### Insights tab

With **Insights** on, a passive analyzer watches the parsed packet
stream and flags routing / wrong-interface problems live. Findings
appear in the **Insights** tab (the badge turns red as they arrive),
sorted by severity:

| Finding | Severity | What it means |
|---|---|---|
| **UDP reply from a different source IP** | error | A client sent to IP A but the reply came back from IP B. The classic 0.0.0.0-bound-service symptom: the kernel's return route egresses a different interface than the request arrived on, so the answer leaves with the wrong source IP and the client drops it. |
| **TCP SYN with no reply** | error | A SYN went out, no SYN-ACK came back within ~3s - server down/filtered, or the return path is broken. (Needs Verbose to see TCP flags.) |
| **ICMP unreachable** | warn | A router reported the destination unreachable - no route or a filter dropping it. |
| **ICMP redirect** | warn | The host is using a gateway that isn't the best next hop for that destination - routing table points at the wrong router. |
| **TTL exceeded** | warn | A packet's hop count ran out in transit - routing loop or a path far longer than expected. |
| **ARP for an off-subnet address** | warn | A host ARPing for an address outside every local subnet - wrong netmask or missing route. (Needs the host's interface CIDRs, probed automatically.) |
| **Repeated TCP resets** | warn | 5+ RSTs on one flow - a firewall/middlebox tearing connections down, or a broken return path. (Needs Verbose.) |

Findings that involve an egress decision carry a **Check route**
button. It runs `ip route get <dst> [from <src>]` on the host and
shows the interface (`dev`), source IP (`src`) and gateway (`via`)
the kernel would actually use - the ground truth for "is traffic
leaving the wrong interface / with the wrong source IP". No sudo
needed for the route check.

TCP flag-based checks (SYN-no-reply, RST storm) need **Verbose**
on, because brief `-q` output doesn't print the flags field.
UDP / ICMP / ARP checks work in either mode.

### Decode tab (Verbose mode only)

Built-in protocol decoders run on every captured packet whose
port matches a known protocol. Each match shows up as an
expandable row with a one-line summary plus a key/value table.

| Proto | Ports | What you get |
|---|---|---|
| **HTTP** | 80, 8000, 8080, 8081, 8888 | Method + path + Host + UA, or status code + reason + content-type |
| **TLS** | 443, 8443 | ClientHello SNI |
| **DNS** | 53 | Query: txid + qtype + qname. Reply: rrtype + first rdata |
| **DHCP** | 67, 68 | Direction, xid, msg type (DISCOVER/OFFER/REQUEST/ACK), assigned IP, MAC, gateway, lease |
| **ICMP** | - (proto field) | Echo request/reply seq, unreachable target, time-exceeded, redirect, IPv6 NDP |
| **ARP** | - (proto field) | Who-has + tell, is-at replies |
| **SSH** | 22 | Plaintext banner (client/server software version) on the first packet of each side |
| **NTP** | 123 | Mode (client/server/control), version, stratum |
| **SNMP** | 161, 162 | Version (v1/v2c/v3), community string, PDU tag (Get/Set/Trap) |
| **LDAP** | 389 | Op tag (bindRequest, searchRequest, …) + messageID |
| **SMB** | 445 | SMB1 vs SMB2/3 (or encrypted), command name |
| **MQTT** | 1883, 8883 | Packet type, PUBLISH topic, CONNECT protocol version |
| **CWMP / TR-069** | 7547 | RPC method (Inform, GetParameterValues(Response), SetParameterValues, Download, ...), Inform device identity (manufacturer, OUI, product class, serial) + event codes, and the first few ParameterValueStruct Name/Value pairs |

DHCP packets are additionally grouped into DORA transactions by
xid so a complete `DISCOVER · OFFER · REQUEST · ACK` cycle
collapses to one timeline row. Non-BOOTP traffic that tcpdump
mislabels as DHCP (PacketCable / DOCSIS provisioning rides UDP
port 67 too, with a non-Ethernet `htype` and an `unknown` op) is
detected and left as a plain UDP row rather than rendered as a
bogus transaction with scrambled IPs.

For non-standard ports, the **Custom port → proto** input on
the controls bar adds a per-capture override - e.g. enter
`9000` and pick `http` to dissect HTTP on that port.

Auth notes:

- The "Use saved password" toggle is on by default when a stored
  password exists. Turn it off if the sudo password differs from the
  login password.
- If sudo rejects the saved password, the modal falls through to a
  manual prompt.

Limits:

- Server-side cap: 5000 packets per run (or no cap with
  **Continuous**). The modal keeps the last 2000 rows in the DOM
  (sliding window) for responsiveness; the backend ring buffer
  used for detach re-attach is the same 2000.
- When a capped capture hits its limit it ends on its own and says
  so - that's not a crash; raise **Max packets** or use Continuous.
- For forensic-grade pcap capture, fall back to a real terminal -
  this panel is for "what's happening right now".

---

<a id="http-tool"></a>
## 14. HTTP / SOAP request tool

For when you need to hit an HTTP endpoint that's only reachable from
inside the remote network - or you just want a quick request panel
without leaving the app.

Open it via the **Globe** icon in the pane toolbar (blue, next to
tcpdump).

What you can do:

- Pick a method (GET / POST / PUT / PATCH / DELETE / HEAD / OPTIONS).
- Type a URL. **Ctrl+Enter** in the URL field fires the request
  too.
- Edit headers as a key/value list. The **JSON / SOAP/XML / Form**
  presets above the list set `Content-Type` to the matching value
  (replacing or appending).
- Paste the body. Raw text - pick your encoding via Content-Type.
- **Route via** dropdown - pre-filled with every running SOCKS5
  dynamic forward on the current session, plus "Direct (no proxy)".
  If a SOCKS5 is up, it's pre-selected.
- **Skip TLS verify** for self-signed test endpoints.
- **Timeout (s)** defaults to 60.

The response panel shows:

- **Status** - coloured for 2xx / 3xx / 4xx / 5xx.
- **Duration** in milliseconds.
- **Response headers** in a collapsible list.
- **Body** with **Pretty / Raw** toggle. JSON is parsed +
  reindented; XML / SOAP gets a small indenter; everything else is
  shown as-is. Copy button is in the toggle row.
- A "body truncated" pill if the response was bigger than the 4 MiB
  server-side cap.

Not implemented (yet): saved request presets, multi-request history,
WSDL parsing, file upload helpers. For now this panel is a quick
diagnostic tool - for proper API workflows fall back to Postman /
Insomnia via the SOCKS5 forward.

---

<a id="connect-feedback"></a>
## 15. Connect feedback

Two improvements over plain "connecting…" spinners:

- **Live stage label** - the Connect button updates with the current
  step: *Connecting to bastion1*, *TCP dial bastion1*, *SSH handshake
  bastion1*, *Tunnel dial target*, *SSH handshake target*, *Opening
  shell*. If a connect hangs, you see which hop is stuck on what.
- **Human-friendly error messages** - when a connect fails, the
  error banner shows a plain-English summary + hint, with the raw
  Go/SSH error hidden behind a *Show raw error* toggle. Recognised
  patterns include: host unreachable, connection refused, i/o
  timeout, DNS lookup failed, authentication failed, ssh handshake
  failed, host key mismatch, administratively prohibited, permission
  denied, context cancelled. If the failure happened at a specific
  hop, the summary reads "Failed at jump host (bastion1)" or
  "Failed at target host (api.example.com)".

The error banner clears when you select another connection.

---

<a id="color-tags"></a>
## 16. Color tags and visual cues

`color_tag` is a free-form colour string set on a folder or a
connection. Override semantics:

- Connection's `color_tag` wins if set.
- Otherwise resolved by walking up folder ancestors.

Picker accepts named palette colours (`red`, `orange`, `green`,
`blue`, `mauve`, etc. - full Catppuccin) or any hex value.

Visible in three places:

- **Sidebar tree** - 3 px inset left strip on the row.
- **Terminal tab strip** - 2 px top border in the active tab.
- **Terminal pane** - 3 px inset left shadow on the pane container.

Use this to make prod / staging / dev / per-customer at-a-glance
obvious without reading hostnames.

---

<a id="custom-icons"></a>
## 17. Custom icons

Both connections and folders accept a custom icon - PNG, SVG, JPG,
WebP, or GIF, up to 256 KB. Set via the **Icon** field in the editor:

- **Upload** - file picker, uploads + assigns in one step.
- **Clear** - reverts to the default icon (Lucide monitor / folder).

Image storage is content-addressed by MD5 in the `images` table; the
same icon used 600 times across an RDM import stores one row.

Default UI icons across the app use **Lucide** (server, folder, key,
key-round, lock, etc.) - emoji placeholders have been replaced. See
`iconMap.ts` in the frontend for the central mapping.

---

<a id="import-export"></a>
## 18. Import / Export

All import/export flows live in **Settings → Import / Export**.
The **Import** section is one page with a source picker at the top:
Devolutions RDM, OpenSSH config, MobaXterm, PuTTY / KiTTY, and
ssh-tool archive. Every import is additive - existing rows are
never modified, re-running is safe.

### Devolutions RDM JSON import

Paste or upload an RDM JSON export. Builds the folder hierarchy
from backslash-separated `Group` paths, stores inline base64 PNG
icons (deduped by MD5), and resolves VPN jump references
(`VPN.VPNGroupName` and `Terminal.SSHGateways[]`).

Optional **target folder** dropdown places all imported top-level
folders under an existing folder.

Summary shows folders / connections / images created, jump
resolution count, and a "needs attention" list for entries that
referenced external credentials or private-key files (which we
never auto-import for security).

### ssh_config import

Paste an OpenSSH client config (`~/.ssh/config`). Each non-wildcard
`Host` alias becomes a connection. `HostName` / `User` / `Port`
land in overrides; `ProxyJump` becomes a jump chain (resolved
against other hosts in the same paste, otherwise carried as a raw
hostname). `IdentityFile` paths are recorded in the connection
notes - **we do not auto-import private keys off disk**.

Summary shows connections created, jumps resolved/unresolved,
IdentityFile paths noted.

### MobaXterm import

In MobaXterm: right-click **User sessions → Export** to produce a
`.mxtsessions` file, then load it in **Settings → Import →
MobaXterm**. SSH sessions become connections; the bookmark folder
tree (`SubRep` paths) is rebuilt, reusing existing folders on
re-import. Host, port and username are read; RDP / telnet / VNC
sessions are counted and skipped. MobaXterm's export contains no
passwords - attach credentials afterwards.

### PuTTY / KiTTY import

PuTTY has no export UI - sessions live in the registry. Export them
first:

```
reg export "HKCU\Software\SimonTatham\PuTTY\Sessions" putty-sessions.reg
```

then load the `.reg` file in **Settings → Import → PuTTY / KiTTY**.
Only `Protocol=ssh` sessions import (serial / telnet are skipped);
`user@host` in the HostName box is split into username + hostname.
KiTTY registry exports parse identically. UTF-16 `.reg` files (the
`reg.exe` default) are handled. PuTTY never stores passwords, so
nothing is lost in the move.

### Export connections / archive

- Generate a TOML or JSON archive containing:
  - Selected folder subtrees (whole subtree included)
  - Plus any extra connections selected outside those folders
  - Optionally credentials referenced by the included connections,
    with secrets wrapped under a user-provided passphrase
    (argon2id + XChaCha20-Poly1305 - same crypto as the vault)
- **Copy** to clipboard or **Save as…** native dialog.
- Empty selection = export everything.

Single-connection export shortcut: **Right-click connection →
Export…** opens the same modal pre-loaded with just that connection
(no credentials included).

**Folder export shortcut**: **Right-click folder → Export folder…**
bundles every connection under that folder, recursively. The
archive's internal folder structure is preserved.

**Strip toggles** in the export modal control what gets included:

- **Notes** - drop per-connection free text (commonly holds
  internal docs / ticket numbers / owner contacts).
- **Tags** - clear connection + credential labels so the recipient
  doesn't inherit your local taxonomy.
- **Color tag** - clear folder + connection color overrides.
- **Custom icons** - drop the embedded icon images (they otherwise
  travel in the archive and are deduplicated by content on import);
  recipients get default icons.
- **Convert credential override to inherit** (on by default when
  credentials aren't included) - rewrites each connection's
  `auth_ref` override to `nil` so on import the connection falls
  back to its folder's credential. Keeps imports usable when the
  recipient supplies their own folder-level credentials.

### Import archive

Three ways to load an archive:

- **Paste** - drop TOML or JSON straight into the textarea (format
  is auto-detected).
- **Fetch** - pull from a URL (e.g. a sibling catalog's
  `/api/bundle?ids=…`).
- **Load file…** - native open dialog. Refuses files larger than
  32 MiB to keep stray binary picks from freezing the renderer.
  The chosen path is shown under the textarea so you can verify
  before applying.

Options:

- **Import into** - pick a target folder. Root folders and
  root-level connections from the archive land under the chosen
  folder; nested structure inside the archive is preserved
  relative to that root. Clear to import at root (default).
- **On conflict**: Skip (default, keep existing rows by id),
  Rename (append " (imported)" to the new row), or Overwrite
  (replace by id).
- **Passphrase** - required only if the archive carries an
  encrypted credentials block.
- **Dry-run** - see what would happen before applying.
- **Apply** - writes the changes; the connection / credential
  trees reload after.

Internal: the importer maintains OLD→NEW id maps for folders,
credentials, and `auth_ref` references. Folders are topologically
sorted so parents land before children. Missing parents fall back
to root with a warning.

---

<a id="settings"></a>
## 19. Settings

Side-nav with grouped panels (last-opened section persists as
`settings_active_section`):

### Appearance → Appearance

- **UI theme** - Mocha / Latte / High contrast.
  - *Mocha (default dark)* - Catppuccin Mocha. Same theme as
    earlier releases, now routed through CSS variables so the
    palette swap below is a one-class flip on `<html>`.
  - *Latte (light)* - Catppuccin Latte. Light background, dark
    text. Useful in bright rooms / projector demos. Repaints
    every panel, not just the readability-sensitive bits - the
    refactor that landed in this release reaches every Svelte
    component.
  - *High contrast (dark)* - Mocha with the muted text steps
    and borders pushed up for direct-sun readability on a laptop
    outdoors. Doesn't repaint the whole palette; just lifts the
    too-dim bits.
- **Density** - Compact / Comfortable / Cozy. Drives the row padding
  on the connection / credential trees. Compact is the default;
  Cozy works well on a 4K monitor.
- **UI font size (px)** - scales the whole app's rem-based sizes
  (tree, panels, modals). 11-18, default 13. Terminal has its own
  font size knob.
- **Color tag as row background** - opt-in. In addition to the
  3px left strip, tints the whole row with the connection /
  folder's colour tag (~14 % opacity, brighter on hover /
  selection).
- **Emphasise active session row** - opt-in. The tree row matching
  the currently focused terminal pane gets a bright cyan inset on
  the right edge so it stands out from other live (but not focused)
  connections.
- **Show session uptime in tab bar** - opt-in. Small "5m" / "2h" /
  "1d3h" indicator next to each connected tab's label showing how
  long the session has been up. Refreshes every 30 s.

### Appearance → Connection

- **Connect timeout (seconds)** - applies to both TCP dial and SSH
  handshake on every hop. Default 20s; raise for slow / unreliable
  links, lower to fail fast. Saved as `connect_timeout_seconds`.
- **In-app local shell** - which shell the top-bar **Local
  shell** button opens on plain click. Auto + per-platform list
  (Windows: WSL / PowerShell / cmd; macOS: zsh / bash; Linux:
  bash / zsh / sh). The dropdown chevron next to the button
  still lets you launch any of the others one-off, and contains
  a "Default for plain click" selector that mirrors this
  setting. The button label updates to show the current default
  (e.g. `Local: WSL`). Saved as `local_shell_kind`.
- **File manager integration** - adds **Open in ssh-tool** to the
  right-click menu on directories: Windows Explorer (folder and
  folder background), KDE Dolphin, and the GNOME Nautilus Scripts
  submenu. Picking it opens the default local shell (setting above)
  as a tab already cd'd into that directory - like "Open in
  Terminal", but inside the window that holds your SSH sessions.
  Per-user registration, no admin rights; the same card removes it.
  Not available on macOS yet. Under the hood it launches
  `ssh-tool --open-dir <path>`, which also works from scripts.
- **External terminal** - three radio cards (Windows Terminal /
  PowerShell / Command Prompt). Drives both the **Native
  terminal** top-bar button and the **Open in external terminal**
  connection right-click action. Only matters on Windows;
  macOS opens Terminal.app; Linux picks `$TERMINAL` first then a
  fallback list. Saved as `external_terminal_kind`.
- **Window → Minimise to tray** - when on, clicking the minimise
  button hides the window into the system tray instead of
  dropping it to the taskbar. Saved as `minimize_to_tray`.
- **Window → Close to tray** - when on, clicking the close (×)
  button hides the window into the tray instead of quitting.
  Saved as `close_to_tray`. With this on you have to use **Quit**
  from the tray menu (right-click) to actually exit. SSH sessions
  and port forwards keep running while hidden. If a session is
  alive and you click × *without* close-to-tray on, you'll get a
  warn-before-quit confirm prompting you to disconnect.

### Appearance → Terminal

- **Copy / paste mode** - Windows / Linux / Mac model.
- **Font size** - current size + Ctrl+wheel zoom hint.
- **Font family** - CSS font-family stack. Defaults to
  `ui-monospace, 'JetBrains Mono', Menlo, monospace`. Clear to reset.
- **Scrollback (lines)** - how many lines xterm keeps per session.
  Default 5000, range 500..100000. New sessions get the limit
  immediately; existing terminals adopt the option but don't grow
  their buffer retroactively.
- **Theme** - pick from the built-in list. Live preview.
- **Auto-close tab on clean exit** - toggle. When enabled, a tab
  whose shell exits cleanly (Ctrl+D or `exit 0`) auto-closes and
  removes the session. Non-zero exits and network drops still leave
  the tab around so you can read the reason.

### Appearance → Browser launcher

- **Browser path** - used by SOCKS5 forwards' "Open URL…" action.
  Detected engines: Chromium / Firefox families.
- The launcher always uses a temporary profile; your main browser's
  cookies / history stay untouched.

### Appearance → Snippets

CRUD for the command snippet library (see [Snippets](#snippets)).
Two-column layout - list on the left, editor on the right. Tags
input is comma-separated.

### Appearance → Workspaces

CRUD for workspace bundles (see [Workspaces](#workspaces)). Save
the current tab set as a new workspace, overwrite an existing one,
or open / delete. The same actions are available in the status
bar's Workspaces popover for quicker access.

### Security → Vault

- **Auto-lock after idle (minutes)** - 0 = off (default). Mouse
  movement / key press / scroll resets the idle timer; on timeout
  the credential tree gets re-protected (vault re-locked) while
  open SSH sessions + port forwards keep running. After a lock the
  next VaultGate prompt asks for the master passphrase even when
  the auto-unlock sidecar is present - sidecar bypass is suppressed
  until you unlock explicitly, otherwise the lock would be
  invisible.

### Security → Backup & restore

The vault, the SQLite store, and all settings are bundled into a
single encrypted file sealed with your vault master passphrase
(Argon2id 64 MiB / XChaCha20-Poly1305). Backups land in
`<DataDir>/backups/`. The SQLite snapshot is taken via
`VACUUM INTO`, so it's consistent even mid-write.

- **Automatic daily backup** - when on, a background scheduler
  takes a backup at app start and then once every 24h. It needs
  the auto-unlock sidecar to recover the passphrase without
  prompting; if the vault is locked and no sidecar is set up, the
  run is silently skipped.
- **Keep last N** - auto-backups (`ssh-tool-auto-*`) and
  pre-restore safety snapshots (`pre-restore-*` directories,
  written before every restore) are pruned to the same N (default
  7). Manual backups (`ssh-tool-backup-*`) are never auto-deleted.
- **Create backup now** - manual snapshot button.
- **Restore** - decrypts the chosen backup, verifies its embedded
  SHA-256 checksums, snapshots the current store + vault to
  `backups/pre-restore-<ts>/` as a safety undo, then **stages**
  the new files in `<DataDir>/pending-restore/`. The actual swap
  runs at the next app start, before SQLite reopens the store -
  in-process Windows file handles otherwise reject the overwrite.
  Quit and reopen the app to complete the restore. The auto-unlock
  sidecar is invalidated by the swap.
- **Delete** - removes a backup file from the list.

The backups directory is intentionally next to the live data so
crash-recovery doesn't depend on external storage. For off-site
safety, copy the `.sshtool-backup` files elsewhere - they are fully
self-contained and verified by the passphrase + checksums on
restore.

### Security → Sync (WebDAV)

Personal multi-machine sync over any WebDAV server you control
(Nextcloud, Apache mod_dav, `rclone serve webdav`, ...). The whole
profile travels: connections, folders, credentials, the vault,
custom icons, settings, snippets, workspaces. The section is laid
out as **Server** (connection details), **Status**, **Manual sync**,
and **Automatic**.

**Encrypted before upload.** The snapshot is the same sealed envelope
backups use (argon2id + XChaCha20-Poly1305), locked with a **sync
passphrase** independent of the vault passphrase. The server only
ever stores ciphertext plus a tiny meta file with a version counter -
a compromised WebDAV host learns nothing. HTTPS is required (plain
http only for localhost); certificates verify against the OS trust
store, so a private CA installed system-wide works.

**Server.** WebDAV URL, username, password, and the sync passphrase.
Both secrets live in this machine's vault, so sync needs an unlocked
vault. Use the same sync passphrase on every machine.

**Manual sync.** **Push** sends this machine's profile to the server.
**Pull** replaces this machine's profile with the server's - applied
**live, with no restart**: the connections appear immediately and
open SSH sessions stay up. (If the machines use different *vault*
passphrases, the connections still apply live and only the secrets
ask for one restart - never a passphrase prompt.) A version counter
keeps it safe: Push is refused when the server has changes this
machine hasn't pulled; **Force push** overwrites it deliberately.

**Automatic** (opt-in):

- **Auto sync** pushes your changes ~90 seconds after you stop
  editing (and on quit), and checks the server for newer versions
  every N minutes (default 5). When another machine pushed a newer
  version, a status-bar pill and a toast offer a one-tap pull.
  Auto-push is skipped while the server is ahead - conflicts are
  yours to resolve.
- **Apply incoming changes automatically** pulls newer versions in
  the background instead of just notifying - but only when this
  machine has no unsaved changes *and* you aren't mid-edit (no text
  field focused, no dialog open). Otherwise it waits for an idle
  moment or leaves the notification, so a pull never rearranges the
  tree out from under you.

**New machine setup**: fill in Server, hit Pull (applies live), and
if prompted, unlock once with the vault passphrase from the source
machine. Push and pull are recorded in the audit log.

### Import / Export → ssh_config

See [Import / Export](#import-export).

### Import / Export → Devolutions RDM

See above.

### Import / Export → Import archive

See above.

### Import / Export → Export connections

See above.

### Diagnostics → Logs

In-app live tail of backend log output. Buffer keeps the last 2000
lines. See [Logs](#logs).

---

<a id="logs"></a>
## 20. Logs

The backend's `log.Printf` output is teed three ways: a ring buffer
(cap 2000 lines) feeding the live in-app tail, stderr (visible in
dev / when launched from a terminal), and a rolling file on disk.

The file lives under `%APPDATA%\ssh-tool\logs\app.log` on Windows
(equivalent XDG dir on Linux) and is rotated at 5 MiB with three
historical files retained (`.1`, `.2`, `.3`). The current file path
is shown above the live tail in the Logs panel.

The Logs panel offers:

- **Filter** - substring filter against the lines.
- **Enabled** - toggle the ring + event emit. When off,
  `log.Printf` still writes to stdout but the in-app tail goes
  quiet. Persisted as `app_log_tail_enabled`.
- **Auto-scroll** - sticks to the bottom on new lines.
- **Clear** - empties the ring.
- **↻** - re-fetch snapshot from backend.

Useful for diagnosing failed SSH connects, slow RDM imports,
opkssh OIDC flow problems.

---

<a id="keyboard-shortcuts"></a>
## 21. Keyboard shortcuts

### Global

| Combo | Action |
|-------|--------|
| **Ctrl/Cmd+K** | Open Quick palette |
| **Ctrl/Cmd+Shift+P** | Open Snippet palette (fires into active terminal) |
| **Ctrl/Cmd+S** | Save the open editor (connection / folder / credential) |
| **Esc** | Close Quick palette / modal / context menu |
| **F12** | Open DevTools (development aid) |

A `?` button next to the top-nav Search opens an in-app cheat sheet
listing every shortcut and mouse gesture - same content as this
table but reachable without leaving the app.

### Terminal tab navigation

Active only while the Terminal view is showing - they don't hijack
combos elsewhere in the app.

| Combo | Action |
|-------|--------|
| **Ctrl+Tab** | Next tab (wraps around) |
| **Ctrl+Shift+Tab** | Previous tab (wraps around) |
| **Ctrl+1** … **Ctrl+8** | Jump to tab N |
| **Ctrl+9** | Jump to last tab (Chrome / VS Code parity) |
| **Ctrl+Shift+W** | Close active tab |
| **Ctrl+Shift+T** | Reopen most recently closed tab (32-deep stack, SSH only - local shells lose identity on close) |

The shifted Close / Reopen variants are intentional: plain
`Ctrl+W` and `Ctrl+T` map to `delete-word` and `transpose-chars`
in readline, and we don't want to fight the embedded shell. Tab
switches restore focus to the active pane's xterm so you can keep
typing without an extra click.

### Terminal

| Combo | Action |
|-------|--------|
| **Ctrl+F** | Open search bar |
| **F3** | Next search match |
| **Ctrl+wheel** | Zoom in/out |
| **Ctrl+Shift+L** | Force a clean redraw (clears the WebGL glyph cache - fixes the rare full-screen-TUI ghosting; plain Ctrl+L stays the shell's clear-screen) |
| **Ctrl+Shift+C / V** | Copy / paste (Windows mode) |
| **Cmd+C / V** | Copy / paste (Mac mode) |
| **Middle-click** | Paste (Linux mode, on selection) |
| **Right-click in pane** | Smart copy/paste toggle (Windows mode) |

### Tree

| Combo | Action |
|-------|--------|
| **Ctrl/Cmd+click** | Toggle into multi-selection |
| **Shift+click** | Range select (cross-folder) |
| **Arrow Up / Down** | Move focus + auto-select next/prev visible row |
| **Home / End** | Jump to first / last visible row |
| **Arrow Right / Left** | Expand / collapse folder |
| **Enter** | Connect (on a connection) - single or fan-out for multi-select |
| **Space** | Select without connecting |
| **Double-click connection** | Connect |
| **Delete** | Delete selected connection / folder (confirm modal - Enter confirms, Esc cancels) |
| **Right-click** | Open context menu (positions itself to stay on-screen) |
| **Right-click empty area** | New connection / New folder at root |

The Arrow Up / Down navigation walks rendered rows (collapsed
subtrees are skipped) and wraps at the edges. The selected row is
scrolled into view automatically; sidebar scroll position is
preserved across edits / reloads.

### Multi-line paste guard

| Combo | Action |
|-------|--------|
| **Enter** / **Y** | Confirm paste |
| **Escape** | Cancel paste |

### Snippet palette

| Combo | Action |
|-------|--------|
| **Ctrl/Cmd+Shift+P** | Open / close |
| **↑ / ↓** | Navigate matches |
| **Enter** | Send highlighted snippet to active terminal |
| **Esc** | Close |

---

<a id="android-mobile"></a>
## 22. Android / mobile

Shipped in v0.36.0. The same app runs on an Android phone (arm64,
DeX-ready) - it is the desktop Go core compiled for android via the
NDK, not a separate codebase. Built locally (`task android:package`);
not in CI. The installed app id is `app.sshtool`. What changes on a
phone:

### Vault unlock with biometrics

On first launch you set the master passphrase as on desktop. Tick
"Unlock with fingerprint / face next time" and the passphrase is
stored in the device's Keystore-backed encrypted store; the next
launch prompts the fingerprint / face sensor and unlocks without
typing. The passphrase never enters the WebView - the unlock happens
server-side in Go after a successful biometric prompt. Anyone who can
pass your device biometrics can open the vault (convenience trade-off,
same warning the UI shows). If the prompt can't show (e.g. launched
while the screen is locked) it falls back to the passphrase field
after a few seconds. There is no machine-bound auto-unlock sidecar on
mobile.

### Single-pane layout

Small screens use a single-pane layout instead of the desktop
tree + detail split: tapping a connection (or credential, or dynamic
host) slides in its detail view, and the system Back (button or
gesture) returns to the list - it steps detail -> list -> tab and only
exits the app at the root. A plain tap on a folder row expands or
collapses it; a long-press opens the folder menu (Rename, settings,
etc). The top nav collapses to icon-only tabs.

### On-screen key bar

A key bar sits above the keyboard in a terminal session with the keys
a phone keyboard lacks: Esc, Tab, Ctrl, Alt, arrows, Home/End,
PgUp/PgDn, and common control combos (^C ^D ^Z ^L ^R ...). Ctrl and
Alt latch - tap Ctrl then a letter to send the control byte. Tapping
the terminal focuses it and opens the keyboard. Two-finger pinch
changes the font size live.

### opkssh login

opkssh OIDC login opens your system browser via an Android intent;
after you authenticate, the loopback callback completes the login
in-app and the SSH session connects.

### Background sessions

A foreground service keeps a live SSH session (and its sockets) alive
when the app is backgrounded; a persistent notification shows while
sessions are open and clears when the last one closes. On Android 13+
the app asks for notification permission on first launch.

### What is desktop-only

Detach/redock multi-window, system tray, the SOCKS browser launcher,
"open in system terminal", the local-shell tab, the self-updater (no
in-app update on android yet - sideload a newer APK from
sshtool.app), and the SFTP native drag-out are desktop features and
are not present on mobile. VNC on mobile is untested.

## Storage locations

- **SQLite database** - connections, folders, credentials metadata,
  port forwards, known hosts, images, settings, snippets, workspaces.
  Path is OS-dependent (under your config directory). Schema at
  migration 10.
- **File vault** - encrypted credential secrets + per-connection
  passwords. Path shown in Settings (Vault section, when implemented
  - for now visible only in the file system).
- **Auto-unlock sidecar** - optional, in the OS keychain when
  available.
- **Rolling log file** - `%APPDATA%\ssh-tool\logs\app.log` (Windows)
  or the equivalent XDG dir on Linux. 5 MiB cap, three historical
  files kept (`.1`, `.2`, `.3`). Path shown in Settings → Logs.

## What's not in the app (yet)

- Smart command autocomplete (designs in TODO, discussion pending)
- Health probe / status dot per connection (deferred - scale concern
  for 300+ connections behind VPN; see TODO)
- Biometric vault unlock (Windows Hello / Touch ID - investigated,
  parked; auto-lock is wired)
- Change master passphrase (rotation flow not yet UI-exposed)
- Bulk credential rotation wizard
- SSH key deployment helper (ssh-copy-id-style)
- Multi-pane split restore inside a workspace (workspaces save the
  active leaf only for now)
- Save/restore window position + size between launches
- Multiple parallel broadcast groups
- Drag remote files OUT of SFTP to the OS (download via drag)
- Hardware key (FIDO2 / YubiKey) auth
- HashiCorp Vault / Vaultwarden sync (placeholder kind only)
- ssh_config auto-import of IdentityFile content (security gate)
- Git-as-sync (separate roadmap)
- Auto-update channel, packaging (.AppImage / .msi / NSIS)
- Android: in-app auto-update, signed release build / Play / F-Droid,
  android in CI (local sideload build only for now)
- iOS build (shares build tags with android, not produced)
- Onboarding tour, i18n, full a11y audit

See `TODO.md` for the full backlog.
