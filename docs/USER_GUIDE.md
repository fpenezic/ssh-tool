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
6c. [Kafka](#kafka)
6d. [Databases](#databases)
7. [Port forwards](#port-forwards)
7b. [Network profiles (WireGuard / NetBird / Tailscale / OpenConnect / OpenVPN / Check Point)](#network-profiles)
8. [Broadcast input (multi-session)](#broadcast-input)
9. [Multi-window: detach + redock](#multi-window)
10. [Workspaces](#workspaces)
10b. [Dynamic inventory (Proxmox / Hetzner / DO / Linode / Vultr / Scaleway / EC2 / Ansible)](#dynamic-inventory)
11. [Quick palette (Ctrl+K)](#quick-palette)
12. [Snippets (Ctrl+Shift+P)](#snippets)
13. [Live tcpdump panel](#tcpdump)
13a. [Live log tail](#logtail)
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

Windows, Linux and Android have release downloads. macOS has none (it
would need an Apple Developer ID): there `scripts/macos-build.sh` builds
and installs the app and the VPN plugins you pick after a few
questions - see [docs/macos.md](https://github.com/fpenezic/ssh-tool/blob/main/docs/macos.md).

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
  right. With nothing selected the right side shows the home pane
  (see §3, Home pane).
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
- **Server status** readout (load / memory / disk / users) for the
  focused SSH session, when that optional feature is enabled in
  Settings. Click it for a **System status** popup: CPU load against
  core count, memory + swap, every real filesystem (pseudo/temp
  mounts like tmpfs and /boot/efi are hidden), uptime, kernel, and
  the logged-in users by name. A refresh button re-probes on demand.
  It reads the remote host of the focused session over a read-only
  side channel - the same probe the readout uses.
  - Values turn yellow, then red, near their limit (load per core,
    memory, disk). The disk figure is the **fullest** filesystem, not
    only `/`; inode use speaks up the same way when it runs out first.
  - **More** under a filesystem shows inode use and *Find largest
    directories* (`du -x`, two levels, only on click, capped at 20 s).
  - **Processes** tab: the top 5, 10 or 20 by CPU or memory; the row menu copies
    the PID or command line, or sends SIGTERM / SIGKILL after a
    confirmation.
  - **Services** tab: failed, running or all units. Open one for its
    last journal lines, restart / stop / start, a log tail on it, or
    `systemctl status` typed into the terminal (you press Enter).
  - Actions that need root run as the login user first, then through
    `sudo -n`, then with the connection's stored password; only if all
    of that fails does the app ask for a sudo password.
  - A red **N failed** chip appears next to the readout when a systemd
    unit has failed; it opens the Services tab. A failed unit there has
    *Ignore on* this host or one of its folders (a folder covers every
    host below it, dynamic inventory hosts too): it stays listed
    (greyed, "ignored") but stops counting toward the chip. *Warn about failed
    systemd units* in Settings turns the chip off everywhere.
  - **Local shells** show the machine they run on: WSL its distro,
    PowerShell / cmd read Windows (CPU as a busy percentage), Linux and
    macOS the machine itself. WSL and Linux shells also get read-only
    Processes and Services tabs (no kill / restart; ignore per distro).
- Update-available pill when a newer release is published. Settings ->
  Updates -> *Channel*: *Stable* (default) offers releases only;
  *Release candidates* also offers the test builds of the next release
  (`vX.Y.Z-rcN`), with that release's changelog in the update dialog, and
  moves on to the release itself when it ships. Leaving the channel keeps
  an installed candidate until a release overtakes it - there is no
  downgrade.
- A log tail segment shows while any log tail runs (open, minimised or in
  its own window), the way captures do: click it to open the tail, or
  pick one when several run.
- Right-click the status bar to pick which items it shows; warnings
  (locked vault, issues, updates, sharing, VPN) always show.
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
  chain, color tag, auto-reconnect, verbose connect log, initial
  command, sensitive.
- Connections live under a folder (or at root). They override any
  inherited setting per-connection.

### Quick access

A pinned **Quick access** panel above the tree lists:

- **Favourites** (any connection flagged with the star)
- **Recent** - last 10 connections you connected to on this machine.
  Excludes anything already in favourites.

Collapsible; collapsed state persists.

### Home pane

With nothing selected in the tree, the right side shows an overview
built from what the app already keeps. Nothing is fetched from a server
and no host is contacted for it, except the liveness probe below.

- **State line** - vault locked or unlocked, sync (with the time of the
  last push or pull, `(auto)` when auto sync is on), backup (the newest
  backup; amber when auto backup is on but the newest is older than 3
  days, "Auto backup off" otherwise) and an available update. Each
  opens its place in Settings.
- **Issues** - the same list as the status bar's issue counter, each
  with its fix button.
- **Quick connect** - type `user@host:port` and press Enter; the quick
  connect dialog opens with it filled in.
- **Recent** - the last connections you opened on this machine, with
  their folder path and how long ago. Click selects, double-click or
  Connect connects. With the liveness probe on, a dot on the icon
  shows reachable (green), unreachable (red) or connected (blue);
  these rows are probed like the rows of an open folder.
- **Workspaces** - one click opens a saved workspace.
- **Network profiles** - each profile's kind, mode and state, with
  Connect / Disconnect; the name opens it in Settings.
- **Fleet** - one line per folder that has a facts run: how many hosts
  need attention and how many did not answer, the first finding, the
  age of the run and a Report button. The same rules as the health
  report (thresholds, grace days, expected units). TLS certificates
  checked with the TLS tool and about to expire are listed under it.

An empty card says how to fill it.

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

Below the tags are pills derived from the connections themselves:
**Role** (`bastion` - other chains jump through it; `behind-bastion` -
it connects through a jump chain, its own or its folder's), **Auth**,
**User**, **Via** (first jump hop) and **Port**. Different groups
narrow each other; values within one group add up. Inventory hosts
only take part in `role:bastion`, and only while Role is the sole
active filter - they have no tags or saved auth to match on.

### Jump host marks

A host other connections jump through gets a small mark with how many
use it; hover for the list. The connection's detail ends with **Jump
host for**, each entry revealing that folder or connection in the
tree. A folder that sets its own jump chain, or an inventory folder
with a bastion route, carries a route mark. Settings → Connection
tree picks *Off*, *Bastions only* (default) or *Bastions and routed
connections*; the last also marks connections with a jump chain of
their own, but not ones that only inherit their folder's.

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

### Fleet tools

Selecting a folder (everything under it, subfolders and loaded cloud
inventory entries included) or several connections shows a **Fleet**
row in the right pane. The same entries are in the right-click menu.

- **Gather facts…** - tick the facts to collect or pick a preset
  (*Sizing*, *Patch day*, *Health check*, *Everything*). *Save as
  preset…* keeps the ticked facts, the custom column and the timeout
  under a name; a folder opens on the choice its last run used. All commands are read-only and
  run 8 hosts at a time. Update counts come from each host's cached
  package lists (`apt-get -s`, `dnf -C`), so they are as fresh as the
  host's last `apt update` / `dnf makecache`. The report groups hosts by
  size (cores / RAM rounded to the size you would order), by OS or by
  kernel; *Find similar* keeps the hosts built like the chosen one:
  same cores and RAM, and the same mount points with each size within
  5% (two "80 GB" disks never report the same byte count). Each mount
  point seen in the report gets its own column, empty where a host does
  not have it, plus *Disk total*; with more than six different mount
  points they fold into one list column. In exports sizes are whole
  numbers with the unit in the header (disks in GiB, memory in MiB), so
  a spreadsheet sorts and sums them whatever its decimal separator.
  For recurring reports to a customer the *Health check* preset adds
  what such a report usually asks for: use per filesystem (each mount
  cell reads "40 GiB · 72%", with inode use added when it is 80% or
  more; yellow from 80%, red from 90%; exports split it into size, use
  and inode columns per mount), *Last update* (the last package transaction that upgraded
  something, from `/var/log/apt/history.log*` or `dnf history`, both
  readable without root; without either, when the package database last
  changed), *Newer kernel* (a newer kernel image in `/boot`
  than the one running; unknown in containers and where image names
  carry no version) and *Boots 30d* (boots recorded in wtmp over the
  last 30 days, "-" where the host keeps no wtmp) and *Containers*
  (docker, else podman: name, state, health, exit code, restarts by the
  engine, image and compose project). Containers are read as the login
  user like every other fact - no sudo - so the user must be in the
  `docker` group; rootless podman lists only that user's own containers.
  Without access the column reads "no access" and the report says so,
  never "no containers". In the report a container that is unhealthy,
  restarting, dead or exited with an error is red; one the engine
  restarted (crash, out of memory) within the last 7 days is yellow - a
  redeploy recreates the container and does not count; one that exited
  with code 0 is listed for information. *Report settings…* takes
  containers stopped on purpose (by name), like expected failed units. Chips on top count
  hosts with a filesystem over 80%, not on the newest kernel, or not
  updated in 30 days or more, measured at collection time. Failed units
  are listed by name. Network shares (CIFS, NFS, sshfs) are marked
  "(net)" and do not count toward *Disk total* or *Find similar*.
  *Report* (next to *Table*) turns a run into a health report: a status
  per host (green, yellow, red), findings that need someone (failed
  services, filesystems over 80% with their trend and the date they
  would fill at that pace, pending reboots, security updates, hosts not
  updated in 30 days, hosts that did not answer), optionally the changes
  since an earlier run (new or missing hosts, kernel and OS changes, reboots,
  services that failed or recovered, new or closed ports, disk use that
  moved 5 points or more) and disk trends with a small chart, then the
  full table as an appendix. A host is red when it did not answer, has a
  failed unit or a filesystem at the critical level; yellow for a
  filesystem at the warning level, a reboot waiting longer than its grace
  period (14 days by default, counted from when the newest kernel was
  installed, else from the last upgrade), no update for the
  set number of days, or security updates while its last update is older
  than the grace period (7 days by default - updates released since the
  last patch day are listed, not flagged). *Report settings…* sets those
  thresholds and the *expected failed units* (a unit that fails by
  design, such as irqbalance on a small VM): they are listed apart and
  do not colour a host; *expected* next to a unit in the report adds it.
  Units ignored in Server status (for the host or a folder above it)
  count as expected too.
  Settings are kept per folder, except *Prepared by*, which signs every
  report (top and bottom, with the time the file was generated), and the
  *Logo* shown top right (PNG, JPEG, WebP or SVG; embedded in the saved
  file).
  *Save report…* writes it as one HTML file;
  open it in a browser to print or save as PDF. Runs on a folder are
  kept (the last 24) for those comparisons and trends; runs on a hand-
  picked set of hosts are not. *Changes since* appears only when you pick
  a run to compare with (*Compare with* above the report, not part of
  the saved file); *delete this run* there drops a test run from the
  history.
  *Copy table* puts the visible rows on the clipboard as an HTML table
  (Teams and Outlook paste a table, Excel splits it into cells) with
  tab-separated text as the plain fallback. *Export CSV* (UTF-8 with a
  BOM, so Excel reads accents right) and *Copy as Markdown* take the
  same rows. An optional
  custom column runs one command of your own and shows its first line;
  it has to pass the same read-only check as MCP commands. For a folder
  the last run is kept and shown as cards above the folder settings.
- **Check TLS certificates…** - reaches each host on the given ports
  the way a connect does (through its jump chain or network profile)
  and reads the certificate: expiry, subject, issuer, and whether it is trusted.
  Hosts you connect to by IP address are skipped. Under 14 days left, a
  **cert Nd** badge shows next to the host in the tree. One host at a
  time: *Tools → TLS certificate* in the pane header.
- **Compare file…** (two or more hosts) - reads one path on every host
  as the login user, counts how many different versions exist, and
  diffs any two side by side. Read-only; up to 1 MiB per file.
- **Copy SSH key…** - pick a credential that holds an SSH key, *Check
  hosts* to see where it is already present, then add it where it is
  missing. The line gets a comment (prefilled with the key's own comment
  or the credential's name) so whoever reads the file later knows whose
  key it is. Presence is judged by the key itself, so a host that has it
  under another comment counts as already present. It only appends to
  `~/.ssh/authorized_keys` and never removes or rewrites a line. Connections that log in as root are
  skipped unless you tick the option.
- **Download files…** - a remote file, a folder (with everything in it)
  or a pattern such as `/var/log/nginx/*.log` from every host, into one
  local folder with a subfolder per host named after the connection -
  collecting logs is the typical use. Patterns are matched over SFTP,
  not by a shell: `*`, `?` and `[..]` work, `**` and `{a,b}` do not.
  *Skip compressed files* leaves out rotated `.gz` / `.xz` / `.zst`
  archives. Download first lists what each host has (nothing is fetched
  yet) and starts right away when the total is under 500 MB; above that
  it shows the sizes per host and asks once more. *Check sizes* shows
  them without downloading, *Skip size check* starts at once. A file
  that fails (rotated away, unreadable) does not stop the rest of that
  host. Files already in the local folder are replaced.
- **Upload file…** - one local file or folder to every host, into a
  remote directory (`~/` by default; created when missing). When a file
  is already there: *Skip it* (default), *Replace if changed* (size
  differs or yours is newer; uploaded files keep your file's time, so a
  second run skips what the first sent) or *Always replace*. Optional
  permissions (`0755`).

Upload and download run as each host's login user, 8 hosts at a time,
with progress per host, *Cancel* and *Retry N failed*. There is no sudo
step: a path the login user cannot read or write fails for that host,
and anything after the copy is done by hand. The minimise button (or a
click outside) puts a running transfer in the status bar - progress
while it runs, *done* or *N failed* once it ends; click it to bring the
dialog back. The x button stops the transfer (after asking) and closes.

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
- **New local shell here…** - a local-shell connection (see below)
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

### Liveness probe

Optional reachability dots on connection rows, so opening a folder
shows you at a glance which hosts are up.

- Enable it under **Settings → Network → Liveness probe** (off by
  default). The re-probe interval (default 60s, minimum 10s) is
  configurable there.
- With it on, a hollow ○ appears on connection rows inside folders you
  have **expanded**: **green** = a TCP connect to the SSH port
  succeeded, **red** = it was refused or timed out. Rows that aren't
  probed show nothing. (A filled green ● still means an open session,
  which takes precedence over the probe dot.)
- Only connections in currently-expanded folders are probed, and they
  are re-checked every ~30 seconds while the folder stays open. Collapse
  the folder and probing stops. This keeps it cheap even on a tree of
  hundreds of connections.
- The probe uses the same path a real connect would: it routes through a
  connection's WireGuard profile or an already-open bastion when there is
  one. It never brings a tunnel or bastion **up** just to probe - a host
  behind a sleeping VPN, or behind a bastion you have no live session to,
  simply shows no dot (unknown) rather than a false red.
- A folder can opt out entirely: set **Liveness probe → Off** in the
  folder's settings (tri-state, inherited by children) for hosts you
  never want probed (for example anything that alarms on port scans).
- **Dynamic inventory hosts** are covered by a separate toggle in the
  same Settings section ("Also probe dynamic inventory hosts"). With it
  on, dynamic entries (Proxmox, Hetzner, Ansible, …) get the same dot,
  but only for entries the provider reports as **running** - a stopped
  VM keeps its provider status and isn't probed. Each entry probes with
  its folder's SSH port, network profile and jump host.

### Connection editor

The header shows the connection's name, `user@host:port` and folder
path, and what is running for it: "connected" (or how many sessions are
open), or for a tunnel connection its state ("tunnel up, 2/2
listening", yellow while a forward that should listen does not). Next
to it: the favourite toggle, **Save**, the main action (**Connect**, or
**Start tunnel** / **Stop tunnel** and **Terminal** for a tunnel
connection), **VNC** when the console is on, and **...** with **Use a
different credential...** and **Delete connection**. The settings sit
in tabs under the header; the editor reopens on the tab you used last.

**General**

- **Name**
- **Double-click opens** - Terminal / Tunnel / VNC console / Kafka /
  Inherit.
  What a double-click or Enter does; the other actions stay in the
  header and the right-click menu. **Tunnel** runs the connection
  without a shell, see [Tunnel connections](#tunnel-connections).
  **VNC console** is offered once VNC is on for the connection, **Kafka**
  once [Kafka](#kafka) is.
  *Inherit* names the value it gets and the folder it comes from.
- **Sensitive** - Yes / No / Inherit. Marks production or otherwise
  critical hosts. The LLM (MCP) cannot list, open, change or route
  through them; a Database tab lists the statements that change data
  and asks before running them; Kafka asks for the topic or group name
  before a delete. Set it on a folder (customer, prod) and it covers
  every connection and subfolder there; **No** on a connection opts it
  out.
- **Hostname / IP address** and **Port**, **Username**
- **Credential** - pick from your credential list, or leave blank
  to inherit from a folder. **None** (first in the list) stops the
  folder's credential from being inherited: the connection's own
  password below is used, or the username and password are asked when
  you connect.
- **One-time code from** - a Bitwarden or KeePass item whose TOTP
  answers the server's verification-code question.
- **Password** - per-connection override stored in the vault under
  `conn_pass:<connID>`. Set / Clear buttons; an inline hint shows
  when a password is stored. When the credential (own or inherited) is
  also a password, this one is tried first and the credential's only if
  it fails - the editor marks it as **Password overridden**.
- **Color tag** - picker; resolves through folder ancestors if
  unset on the connection.
- **Icon** - Upload PNG/SVG/JPG/WebP/GIF (≤256 KB). Clears to the
  default monitor icon.
- **Tags** - chip editor with type-and-enter.
- **Notes** - markdown textarea with an **Edit / Preview** toggle.
  Supports headings (`#` through `######`), `**bold**`, `*italic*`,
  `` `inline code` ``, fenced code blocks, `-`/`*`/`+` unordered
  lists, `1.` ordered lists, `---` horizontal rules, and
  `[link](https://…)` links. HTTP/HTTPS links open in the system
  browser. Use it as a mini runbook per host: what it does, who
  owns it, restart command, gotchas.
- **Quick actions** - copy buttons for Host / User / Password /
  ssh command, plus **Launch in system terminal** which opens the
  OS terminal preloaded with the equivalent `ssh ...` invocation.
  Password copy auto-clears the clipboard after 30 seconds. The
  system-terminal launcher prefers Windows Terminal on Windows,
  Terminal.app on macOS, and falls through gnome-terminal /
  konsole / xfce4-terminal / xterm / alacritty on Linux.

**Connection**

- **Path** - the route as it will be dialled: this computer, the
  network profile the first hop goes through (a dashed card in the VPN
  colour, with its kind, up / down / paused and "auto" when it is used
  only if the host is not reachable directly), each jump hop with its
  connection's icon, then this host. The line under it says where the
  chain and the network come from: this connection, a folder, or - when
  the connection sets no network itself - the bastion that supplies the
  first hop, whose network profile the first hop then uses. Clicking
  the network card jumps to the **Network** setting. **Edit chain** opens the editor: one row per hop, in connect
  order. A hop is either **Manual** (hostname, port, user, credential)
  or a **Saved connection**: any saved connection or any host of an
  inventory folder. A saved-connection hop reads the bastion's address,
  user, credential and its own jump chain at connect time, so changing
  the bastion once changes every chain through it; an inventory host
  follows its address from the last refresh. A bastion inside the
  folder it is the jump host for connects directly. If the bastion is
  deleted, or drops out of its inventory, the connect says so.
- **Network** - route the first SSH hop through a network profile
  (VPN), or Direct.
- **Auto-reconnect** - On / Off / Inherit. When on, sessions that
  drop without a manual Disconnect retry with exponential backoff.
- **Keepalive / anti-idle (s)** - seconds between SSH keepalive global
  requests. Empty = inherit, 0 = off. Useful for connections behind
  NAT that drop idle TCP sessions after 1-5 minutes.

**Terminal**

- **Initial command** - a command run in the shell right after connect,
  e.g. `cd /var/www`, `tmux new -A -s main`, or
  `source venv/bin/activate`. Set it on a folder to give every
  connection under it the same landing spot; a connection can override
  or (with an empty value) opt out. It runs on the target host only (not
  jump hosts) and shows in your scrollback like anything you'd type.
  Multi-line values are sent one line at a time.
- **Delay between lines (ms)** - a pause between the lines of a
  multi-line initial command. Empty = inherit, 0 = no pause (what
  every connection did before this existed). Raise it when a line
  hands the terminal to a different shell - `sudo su - deploy` is the
  usual case: without a wait, the lines after it are read by the shell
  on its way out instead of the one that just started. A few hundred
  milliseconds is normally enough. With a delay set, the terminal is
  usable immediately and the remaining lines land as they are sent.
- **Open in the background** - the session connects but its tab stays
  out of the tab bar.

**Forwards** (**Forwards & tunnel** for a tunnel connection) - the
connection's [port forwards](#port-forwards). For a tunnel connection
the tab starts with the tunnel itself: its state, **Start tunnel** /
**Stop tunnel**, and **Start with the app**. The tab is marked **!**
when the tunnel failed or a forward is not listening.

**VNC** - the [VNC console](#generic-vnc-your-own-hosts) settings.

**Kafka** - the [Kafka](#kafka) settings.

**Advanced**

- **Verbose connect log** - On / Off / Inherit. Emits TCP / handshake
  / auth diagnostics to the Connect diagnostics block below the
  header during connect.
- **Jump host for** - the connections and folders that use this one
  as a bastion.
- **Resolved & inherited** - the merged settings with where each value
  comes from (collapsed; for debugging).

A folder's editor has the same tabs (General, Connection, Terminal,
Tunnels, Kafka, Advanced) with the settings its connections inherit, each
in the same place as on a connection.

### Local-shell connections

A connection is normally an SSH host, but it can also be a saved
**local shell**: a terminal that opens on *this* machine and runs a
command, instead of dialling SSH. This turns any "a shell that runs
one thing" target into a first-class tree entry you can name, tag,
icon, favourite, and open with a double-click:

- **telnet** to a console server or old switch - command `telnet 10.0.0.5`
- **serial console** - `screen /dev/ttyUSB0`, `wsl -e picocom -b 115200 /dev/ttyUSB0`
- **Claude Code** on a keystroke - command `claude` (or
  `cd ~/project && claude -c`)
- a REPL, `docker exec -it db psql`, `kubectl exec`, mosh, anything

Create one from **New local shell…** in the tree toolbar (the `+`
terminal button), the right-click menu, or **New local shell here…**
under a folder. A local connection's editor is trimmed to what
applies:

- **Shell** - which shell to launch: *Auto* (a sensible per-OS
  default - WSL then PowerShell on Windows, `$SHELL` then bash/zsh
  elsewhere) or a specific one (bash / zsh / sh / PowerShell / cmd /
  WSL).
- **Command** - what the shell runs on open (the point of the
  connection). Blank just gives you an interactive shell. It is
  written to the shell and runs with a trailing newline, landing in
  your scrollback like anything you'd type.

Host, port, credential, jump host, VNC, port-forwards, keepalive and
the SSH quick-actions are hidden - none apply to a local shell.
Exiting the shell (`exit` / Ctrl-D, or the command finishing) closes
the tab, same as an SSH session.

Local connections sync like any other tree entry (they ride the
whole-profile snapshot). A machine where a given shell or command
doesn't exist simply fails to launch it - nothing special is done to
scope them per-device.

### One-shot credential / username / password override

**Use a different credential...** in the editor header's **...** menu
opens an override block with three independent fields:

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

Rows are one line, like connection rows: the kind (or the external
backend) sits on the right, the hint shows in the row's tooltip.

**Search.** The box above the tree filters by name, default username,
hint, tags, kind, backend and folder name. Every word you type must
match somewhere, so `root prod` finds root credentials under a prod
folder. Folders with hits open while you search. Enter selects the first
hit, Escape clears the box.

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
- **client certificate** - a TLS client certificate and its private
  key, for VPN gateways that log in with a certificate (OpenConnect
  profiles). Imported from a `.pfx` / `.p12`, a PEM with certificate and
  key, or a certificate + key pair; stored in the vault, so it syncs and
  backs up with everything else. Not offered as SSH auth.

### Storage modes

- **managed** - full secret material lives in the app vault.
- **file_ref** - credential row holds a path; secret stays on disk.
- **external** - for delegated kinds (agent, opkssh, vault).

### Tree operations

- **Drag and drop** works like the connections tree. Every row is a
  drop target: the middle of a folder row drops into that folder, its
  top or bottom edge next to it, and a credential row into that
  credential's folder. Only the empty area below the rows moves to the
  top level. The folder that receives the drop is highlighted; a drop
  that would change nothing shows the no-drop cursor. A multi-selection
  (credentials, or folders) moves as one. There is no manual order:
  credentials and their folders sort by name.
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
- **F2 renames in place.** The name turns into a field in the row
  itself: Enter saves, Escape abandons, and the text starts selected so
  typing replaces it. Press F2 on the next row to keep going. It acts on
  a single selection only - a rename is one name, so with several rows
  selected the key does nothing rather than guessing which one you
  meant.

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
- **PuTTY .ppk keys** - the key importer accepts a PuTTY `.ppk` (v2 or
  v3) as well as OpenSSH/PEM: paste it into the private-key box, or use
  **Browse .ppk** on the file-reference kind. It is converted to OpenSSH
  and stored in the vault (an encrypted `.ppk` asks for its passphrase);
  no manual `puttygen` step. A picked `.ppk` is absorbed into the vault
  rather than left as a file link, since the key is only usable once
  converted.
- **Used by** - every place that references this credential: folders
  and connections (login, one-time code, jump hosts), inventory folders
  (API token, VNC login), network profiles (password, client
  certificate, setup key, auth key), Infisical servers and the sync
  target, each with what it uses the credential for. Click a folder or
  connection to reveal it in the Connections view; a network profile
  opens on its Settings tab, the others open their Settings section.
- **Delete** - refused while anything in **Used by** still points at
  the credential, so nothing is left referencing a deleted one.
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
- **Client certificate (VPN)** - pick the certificate file (`.pfx`,
  `.p12`, `.pem`, `.crt`), a separate key file if the key is not inside
  it, and the file's password if it has one. Both AES-encrypted `.pfx`
  files (current OpenSSL and Windows exports) and older ones import.
  The password only opens the file and is not kept; the vault holds the
  certificate chain and the key. The certificate's own expiry date
  becomes the credential's expiry, so the list badge and the status bar
  warn before it lapses. The detail panel shows subject, issuer,
  SHA-256 fingerprint and chain, and **Replace certificate** imports a
  renewed one - every profile using the credential picks it up.

**Expiry.** Passwords, keys and API tokens take an optional expiry: pick
a date, type **in N days**, or click 30d / 90d / 365d; the field shows
how many days a picked date is away. Expired and soon-to-expire
credentials (14 days) get a badge in the list and a status bar warning.

### One-time codes (TOTP)

A Bitwarden / Vaultwarden item or a KeePass entry that carries a TOTP
(an `otpauth://` URI, a bare base32 secret, KeePassXC's `otp` field or
KeePass 2's TimeOtp fields) can answer verification-code questions:

- **Copy code** - the credential detail panel of such an item shows the
  current code and the seconds left.
- **SSH** - set **One-time code from** on a folder or connection (it
  inherits like the credential, and has its own **From Bitwarden** /
  **From KeePass** pickers). When the server asks for a password and a
  verification code (keyboard-interactive), the stored password answers
  the password question and the item's TOTP answers the code question;
  only what is left is asked. The credential and the code can come from
  different places - a key from the vault plus a code from Vaultwarden
  is one connection. Each answer is sent once per connect: if the server
  rejects it, you are asked next time instead of the app retrying into
  a lockout.
- **VPN** - an OpenConnect profile's second password can be the TOTP of
  an item (see Network profiles).

Picking an item for the code always uses its TOTP, whichever field you
click in the picker; an item with a password and a TOTP can be both the
credential and the code source. If the item has no TOTP or the server
cannot be reached, you are simply asked for the code.

Keeping the password and the TOTP in the same item puts both factors in
one place - convenient, but a leak of that vault gives both. ssh-tool
itself does not store TOTP secrets for that reason; hardware and app
tokens (MobilePASS and the like) are answered in the login prompt.

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

### Infisical servers

ssh-tool can also read secrets straight out of an **Infisical** server.
It is the simplest of the three external backends: Infisical decrypts
**server-side**, so there is **no master password** and **no vault sync**
- the value comes back over TLS and is read fresh on every connect,
never copied into ssh-tool's own store.

**Register a server** in Settings → Infisical:

- **Server URL** - e.g. `https://infisical.example.com`.
- **API key** - sign-in uses a **machine identity** (Universal Auth). On
  the server, create a machine identity and a Universal Auth key (a
  client id + client secret), and add the identity as a **member of the
  project** whose secrets you want to read. In ssh-tool, pick an existing
  API-key credential or click **Create** to add one inline.
- **Network profile** (optional) - if the server is only reachable over a
  VPN, choose a **WireGuard** profile to dial through.
- **Test login** verifies the API key works.

**Reference a secret** the same two ways as the other backends:

- Fastest: on a connection or folder, click **From Infisical** next to
  the Credential picker (shown once a server is registered, live - no
  restart). The picker opens a **Project → Environment → secret** tree
  (nested folder paths shown as a prefix on the key) with a search box.
  Choose a secret, and ssh-tool creates and assigns a credential for it,
  filed under an "Infisical" credential folder. Infisical-backed
  credentials show a **lock icon** and an "infisical" label.
- Or from the credential editor: **From Infisical server**, pick the
  server and secret.

A secret whose value is a PEM private key is offered as an **SSH key**
(authenticates as a key); anything else is used as a password. Secrets
are referenced by project + environment + path + key.

**Freshness and offline**: every connect reads the secret fresh (a single
request). If the server is briefly unreachable, the **last value seen**
for that secret - sealed with your vault on disk - is used and marked
**stale** rather than breaking a connect. The access token lives only in
memory and is dropped the moment the vault locks.

Sign-in is Universal-Auth (API key) only, and the server needs a
certificate your OS trusts.

---

<a id="terminal-sessions"></a>
## 5. Terminal sessions

### Connecting

- Double-click a connection in the tree, click **Connect**, or hit
  Enter while a connection is anchored.
- Multi-select + **Connect all** opens N tabs in parallel. Connections
  that share a jump host reuse ONE connection to that bastion (a channel
  per target through it), so a large batch behind a single bastion comes
  up fast without tripping its connection-rate limit. Connecting to more
  than five at once asks for confirmation first. If several targets have
  host keys you have not seen, the host-key prompt offers a single
  **Trust & remember all** for every unknown host in the batch.
- Each session lives in the **Terminal** view; the top nav grows a
  Terminal tab once at least one session exists.

### Quick connect

For a host you only need once. Open it from the tab bar's **+** menu
(**Quick connect...**), or type an address such as `root@203.0.113.10` or
`admin@host.example.com:2222` into Ctrl+K and pick **Quick connect to ...**.

- **Host** - `[user@]host[:port]`; an IPv6 address takes a port only in
  brackets (`ops@[2001:db8::1]:2200`).
- **Inherit from folder** (optional) - the connection behaves as if it sat
  in that folder: its jump host, credential, network profile and other
  inherited settings apply. Handy for a one-off box behind a customer's
  bastion.
- **Credential** (optional) - a vault credential. Without one, the agent
  and default keys are tried, then you are asked for a password.

Nothing is written to the profile and nothing syncs. The tab carries a
**quick** marker; reconnect and splitting work as usual, but quick tabs are
left out of reopen-last-session, workspaces and Ctrl+Shift+T. Right-click
the tab for **Save as connection...** to keep it: a saved connection with
the same host, port, user, folder and credential is created and opened in
the editor, and the live tab now belongs to it.

The last ten targets you typed are suggested in the Host field, on this
machine only (address, folder and credential choice - no passwords). The x
on a suggestion forgets it.

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
- With several tabs selected (Ctrl/Cmd+click, Shift+click), right-click
  one of them for actions on all of them, among them **Upload file to N
  tabs…**, **Download files from N tabs…** and **Reconnect N tabs**.
  Upload and download are the Fleet tools above, but over each tab's
  open session - no new login, so hosts behind a password prompt or 2FA
  work too. Reconnect follows the bulk-connect limits in Settings.

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
- **Copy** menu: host, username, password (clears the clipboard after
  30 s) or the ssh command.
- **Snippets** and **SFTP** (the SFTP browser on the same session as a
  split-right pane, only on a terminal pane).
- **Tools** menu, each entry with its icon: Log tail, Packet capture,
  HTTP / SOAP request, TLS certificate, Port forwards. A green dot on
  Tools means a capture, tail or forward is running for that pane.
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
modal shows "N more queued" so you know what's coming. When several
queued hosts are unknown, a **Trust & remember all** button accepts
every queued unknown host at once. A changed key is never part of that -
it always requires an individual decision.

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

- Sortable columns: Name, Size, Modified time. Owner and Mode are
  shown too.
- Owner reads as `user:group`. SFTP itself only carries numeric ids,
  so the names are resolved from the host's `/etc/passwd` and
  `/etc/group` (read once per session). Accounts that live only in
  LDAP/SSSD are not in those files and stay numeric.
- Click a file / folder to select it; Ctrl/Cmd-click toggles into a
  multi-selection.
- Double-click a directory enters it.
- Breadcrumbs at the top - click any segment to jump.
- ↑ button = go to parent directory.
- Refresh button (↻) re-lists the current directory.

### Path bar

The breadcrumbs double as an editable field. Click the pencil (or the
empty strip beside the crumbs), paste a path, press Enter. Escape puts
the breadcrumbs back.

Absolute paths, paths relative to your login directory, `~` and `~/sub`
all work.

### Quick view and editing

Press Enter, double-click a file, or use the **View** button to open a
preview with syntax highlighting (YAML, INI/conf, JSON, shell, Python,
Dockerfile, and log output). Binary files are detected and refused
rather than rendered as garbage; files over 256 KB open truncated,
with a banner saying so.

The preview **docks inside the SFTP pane**, under the listing, rather
than covering the window. Since the browser opens as a split beside
the terminal, this is what lets you read a README or a config while
you type in the shell next to it. Drag the divider between the listing
and the preview to resize; double-click it to reset to half. Escape
does not close a docked preview - it only leaves edit mode - so the
file stays put while you work beside it.

- **Expand** moves the file to a full-window view for when the split
  is too short. **Dock** in that window puts it back.

- **{ } Format** appears for a JSON document and pretty-prints it. It
  is a view toggle - it never rewrites the file.
- **Wrap** toggles line wrapping, **Copy** copies what is on screen.
- A **CRLF** badge marks a file with Windows line endings. **MIXED**
  (yellow) means the file has both, and the offending lines are marked
  in the gutter - that is usually a defect worth fixing.
- **Edit** switches to an editable buffer. Ctrl+S saves, Escape leaves
  edit mode, and closing with unsaved changes asks first.

Saving preserves the file's permission bits and its original line
endings. If the file changed on the server since you opened it, the
save is refused rather than overwriting someone else's work - reopen
it and redo the change. Truncated files cannot be edited at all, since
saving would drop everything past the first 256 KB.

The write is not atomic: an interrupted save leaves a truncated file,
the same as any editor writing in place over SFTP. For anything
critical, keep a copy.

### Follow the shell

The **⇄** button makes the browser follow the directory your shell is
in. It is off by default and set per pane.

This relies on the shell announcing its directory with OSC 7. Many
distributions ship it only for their own terminal, so it usually needs
enabling. Add to `~/.bashrc` on the server:

```bash
PROMPT_COMMAND='printf "\033]7;file://%s%s\033\\" "$HOSTNAME" "$PWD"'
```

zsh:

```zsh
precmd() { printf "\033]7;file://%s%s\033\\" "$HOST" "$PWD"; }
```

Until the shell reports something, the button stays disabled.

The other direction is deliberately manual: **cd here** types
`cd '<current directory>'` into the terminal and moves focus there,
but does not press Enter - you confirm it. It clears the input line
first, so clicking it twice does not concatenate two commands.

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

On Windows the taskbar button fills behind the app icon while
transfers run, the way a browser shows a download - so you can watch a
big upload finish without keeping the window in front. It shows all
running transfers combined, turns red for a few seconds if one fails,
and clears when the last one is done. Downloads an LLM starts (see
Share with an LLM below) fill the same bar.

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
the **VNC** tab has the on/off switch and:

- **RFB port** - default 5900 (inheritable from the folder).
- **Reach the port** - *Direct* dials `host:port`; *Through SSH* dials
  `127.0.0.1:port` on the remote's loopback through the connection's
  SSH session (for a localhost-bound x11vnc / TigerVNC / macOS Screen
  Sharing). Tunnelling reuses the connection's SSH credentials and jump
  chain.
- **VNC password** - optional, stored in the vault. If empty and the
  server demands auth, noVNC prompts in the panel. Tunnelled
  localhost-bound servers often have no password.

Open it from the editor's **VNC** button or the connection's
right-click menu. To open it on a double-click instead of a terminal
(a host you only reach over VNC), set **Double-click opens** to **VNC
console** in the General tab.

### How it works

Pixels flow over a loopback websocket bridge inside the app: noVNC
connects to `127.0.0.1` with a single-use token (no secrets in the
URL), and the app relays RFB to Proxmox or the tunnelled/direct VNC
port. The webview never needs custom headers or TLS-skip. Detaching a
console tab to its own window and redocking works like any other tab.

---

<a id="kafka"></a>
## 6c. Kafka

Browse and work with a Kafka cluster from the connection that reaches it.
Kafka is a service on a connection, like the VNC console: you switch it on
in the connection (or once on a folder of a cluster's hosts) and the
connection opens a **Kafka tab** next to its terminals. No separate tool, no
local port per broker, no `/etc/hosts` entries and no sshuttle.

### Turning it on

In the connection editor (or a folder's), the **Services** tab, below the
database:

- **Bootstrap servers** - the seed brokers, comma separated; a missing port
  means 9092. Write them as they are seen from where the brokers are
  dialed: from the SSH host when Kafka runs on it or behind it (often
  `127.0.0.1:9092` on a broker host).
- **Reach the brokers** - *Through SSH* (the default): one SSH session to
  this host carries every broker connection, and the broker names the
  cluster advertises are resolved on that host, so a cluster that only
  exists on the far side works as is. *Direct / from the jump host*: the
  brokers are dialed from the connection's last jump host, or from this
  machine over its network profile - the host itself is not SSHed.
- **Writes** - *Read-only* refuses every write (produce, topic and group
  changes) in the app's backend, not only in the UI. Meant for production.
- **TLS** (end to end, inside the tunnel), with an optional *skip
  certificate check* for test clusters, and **SASL** (PLAIN, SCRAM-SHA-256,
  SCRAM-SHA-512) with a **SASL credential** - a password credential whose
  default username and password are the login, so vault, KeePass,
  Bitwarden and Infisical credentials all work. **+ New login...** in the
  list creates one on the spot (saved in the vault).

To have double-click / Enter open the Kafka tab instead of a terminal (a
connection that exists for its cluster), set **Double-click opens** to
**Kafka** on the General tab.

Every setting inherits through folders like the SSH ones; **Resolved &
inherited** on the General tab shows what applies.

### Opening it

**Open** next to Connect in the connection header (Kafka when the host has
no database, else under its arrow), **Open Kafka** in the
connection's right-click menu, on a terminal tab's right-click menu (for a
host with Kafka on), or the default action as above. A second open of the
same connection brings its tab forward. The SSH session opens with the
first request, with the usual host-key and password prompts; the tab says
where it is waiting. If the session drops, the next request reconnects it.

The Kafka tab is a normal tab: it detaches to its own window and back,
splits (a split opens a terminal on the same host - watch a topic, tail the
broker log), comes back with *Reopen last session* and with workspaces, and
can be shared with an LLM.

### Topics

The left column lists the topics with their partitions and retained message
count, flags under-replicated and offline partitions, and filters as you
type (internal topics on request). **+** creates a topic (partitions,
replication factor, config overrides).

Clicking a topic opens it in a tab above the view, or switches to its tab if
it is open already; **Groups** and **Brokers** are pinned at the start of the
row. A topic tab stays alive while another is in front - its consumer keeps
reading and its filters, selected record and subtab are where you left
them - until you close it (the x, or middle-click), which ends the consumer
and frees its buffer. To read one topic twice - say a key filter for one
value in one tab and for another in the next - middle-click the topic in
the list or use **+ Tab** in the topic's header: the second tab is named
`topic (2)` and has its own consumer, filters and buffer share.
Splitting or moving the Kafka tab, detaching it to
another window or reloading the window keeps the topic tabs and their
consumers too. A green dot with a count marks a topic that is being
listened to, in the list, on its tab and, as a number, on the connection's
tab at the top (the tooltip lists the topics); it turns red when a consumer
fails. Reopen last session and workspaces bring the topic tabs back open,
without starting their consumers. A topic opens on its own subtabs:

- **Messages** - the consumer, below.
- **Produce** - key, value and headers as text or base64, null key / value
  (a tombstone), a partition or the key-hash default, JSON formatting.
- **Partitions** - leader, replicas, ISR and offsets per partition.
- **Config** - the overrides set on the topic (or every value with the
  broker defaults), edited as `key=value` lines.
- **Metrics** - messages, rate, throughput, average stored size and size on
  disk (leaders and all replicas), sampled every 15 s while open.
- **Actions** - add partitions, change the replication factor (partitions
  reassigned round robin, the brokers copy in the background), truncate
  (delete every record, keep the topic) and delete. Delete and truncate ask
  first; on a connection marked *sensitive* you type the topic name.

### Consuming

Pick where to start - the last N records of every partition, the
beginning, new records only, an offset, or a time - a partition or all, an
optional record limit, and whether to **stop at end** (the end offsets as
they were when you started). The consumer never joins a consumer group and
never commits an offset: reading a topic here cannot move anyone's real
consumers.

Records arrive in a list (partition, offset, time, key, value); a record
opens below with its value pretty-printed when it is JSON, a hex dump when
it is binary, its key and headers. The filter matches key, value and
headers as you type; **export** saves the shown records as NDJSON. The app
keeps the newest records of a consumer and says how many older ones it
dropped: up to 10,000, within a memory budget of 256 MiB per Kafka tab that
its consumers share equally - running ones and finished ones that still
hold records (Clear or closing the topic tab gives the share back), so
listening to more topics keeps fewer records of each.

**Keep only** (key contains / value contains / header contains) is a match
done before storing: a record is kept only if it contains every filled text,
ignoring case, anywhere in the full key, value or a header name or value.
The rest are counted but not stored, so the status line reads "3 matched of
48k read", and a limit counts matches (a limit of 1 stops at the first
hit). It applies when Consume starts; the filter above the list still
searches what is already buffered. Texts are plain substrings, no regex. A
binary payload matches on the text inside it (strings in Protobuf or Avro)
and on the base64 the list shows for it.

### Consumer groups and brokers

**Groups** lists the consumer groups with state, members and total lag. A
group shows its members and the committed offset, end offset and lag per
partition; with the group stopped you can **reset its offsets** on a topic
(to the beginning, the end, back N records, an offset or a time) or delete
it. **Brokers** shows the cluster id, the controller and every broker's
advertised address.

### LLM access (MCP)

Share a Kafka tab from its robot button like any session, or let the LLM
open one with `open_kafka` (you approve). It can then list and describe
topics and groups and read records - capped in count and bytes, never
committing - and, with read-run, produce records you approve one by one.
Topic and group administration is not available to it. See
[Share a session with an LLM](#llm-mcp) for the bridge itself.

---

<a id="databases"></a>
## 6d. Databases

Browse and query a PostgreSQL, MySQL / MariaDB, Oracle, SQL Server or
ClickHouse database, an OpenSearch / Elasticsearch cluster or a Redis
server from the connection
that reaches it. Like Kafka, the database is a service on a
connection: switch it on in the connection (or once on a folder of
database hosts) and the connection opens a **Database tab** next to its
terminals. Through SSH (the default), every database connection rides the
connection's SSH session, so a server that only listens on the host's
`127.0.0.1` works with no port forward.

### Turning it on

In the connection editor (or a folder's), the **Services** tab, which holds
the database and Kafka:

- **Engine** - PostgreSQL, MySQL / MariaDB, Oracle (see [Oracle](#oracle)),
  SQL Server (see [SQL Server](#mssql)), ClickHouse (over its HTTP
  interface, see [ClickHouse](#clickhouse) below), OpenSearch /
  Elasticsearch (see [OpenSearch / Elasticsearch](#opensearch)) or Redis /
  Valkey (see [Redis](#redis)).
- **Address** - host and port as seen from where the database is dialed:
  from the SSH host when it runs there or behind it. Empty means
  `127.0.0.1` and the engine's port (5432 / 3306 / 1521 / 1433 / 8123 /
  9200 / 6379; ClickHouse with TLS on takes 8443).
- **Database** - the one the tab opens first; empty takes the server's
  default for the login. On Oracle the field is **Service**: the service
  name to log in to.
- **Login** - a password credential whose default username and password are
  the database login, so vault, KeePass, Bitwarden and Infisical
  credentials all work. A credential without a password works for trust
  authentication. **+ New login...** in the list asks for a username and
  password and saves them as a vault credential on the spot. With no login
  set (*None (ask)*), opening the database asks for one: saved as a
  credential, or used for that tab only. **Credentials -> Used by** lists it
  as *Database login*.
- **Reach the database** - *Through SSH* (the default): one SSH session to
  this host carries every connection, and the address resolves there.
  *Direct / from the jump host*: dialed from the connection's last jump
  host, or from this machine over its network profile.
- **Writes** - *Read-only*: every statement runs in a READ ONLY transaction
  that is rolled back, and only `SELECT`, `WITH`, `SHOW`, `EXPLAIN`,
  `VALUES`, `TABLE` and `DESCRIBE` are sent at all (on MySQL a DDL statement
  would otherwise commit on its own and escape the transaction;
  `SELECT ... INTO OUTFILE` is refused too). It is a strong guard, but the
  real guarantee is a login with read-only grants - use one for production.
  Oracle and SQL Server differ, see their sections.
- **TLS** - *Require* (encrypted, certificate not checked) or *Verify
  certificate* (it must match the address's host name), end to end inside
  the tunnel.

To have double-click / Enter open the Database tab instead of a terminal,
set **Double-click opens** to **Database** on the General tab (with
databases listed, the first of them opens). Every setting above inherits
through folders: N servers with the same DBA login set it once on their
folder.

### In the tree

Each database shows as a row under its connection - the connection's own
and any listed below - so the database browser is at hand next to the
terminal: double-click (or Enter) on the connection still opens the
terminal, on the database row it opens the Database tab. **Name** in the
Database settings names the row and the tab (default: the database name),
and **Show in tree** takes a row out. The badge with the database icon on
the connection row folds the rows away and back, like pinned bookmarks. With more than one, one is the
**default** - the connection's own unless you pick another (right-click ->
**Set as default**, or **Default** in the settings): it is listed first and
is what **Double-click opens -> Database** opens.

### Several databases on one host

One Database tab already shows every database of its server, and each
query tab picks the one it runs on. When a host needs more than one
login or server - *prod* and *stage* with different users, read-only for
one of them, or PostgreSQL and MySQL side by side - list them under
**Databases on this host** in the connection's Services tab: a name, and
only what differs (database, login, writes, engine, address); empty
fields take the values above. Each is a row under the connection in the
tree (unless its **Show in tree** is off), opens its own tab
(`host / stage`) on double-click or Enter, and is in the right-click menu
as **Open database stage**. The list belongs to
the connection; folders hold the defaults.

### Opening it

**Open** next to Connect in the connection header (the default database;
its arrow lists every database and Kafka), **Open database** in
the connection's right-click menu, on a terminal tab's right-click menu, or
the default action as above. A second open of the same connection brings
its tab forward. The SSH session opens with the first request, with the
usual host-key and password prompts.

The Database tab is a normal tab: it detaches to its own window and back,
splits (a split opens a terminal on the same host), comes back with *Reopen
last session* and with workspaces - with its query tabs and their SQL;
nothing is run again - and can be shared with an LLM.

### The tree

The left column lists the databases (system ones behind the **system**
box) with their size. Expanding one lists its tables and views - grouped by
schema on PostgreSQL - with an estimated row count; the filter box narrows
them. A click on a table opens it in a tab; the icon next to a database
opens a new query tab on it.

### Queries

A query tab has an SQL editor with highlighting for the engine and
completion of the table names listed so far.

- **Ctrl+Enter** (or **Run**) runs the statement under the cursor - or the
  selection - and **Ctrl+Shift+Enter** (**Run all**) the whole script,
  statement by statement, stopping at the first error. Semicolons inside
  strings, comments and PostgreSQL `$$` bodies do not split.
- **Limit** caps the rows a statement brings back (500 by default, up to
  100 000); a read cut at the limit is stopped on the server, so a `SELECT`
  without `LIMIT` on a huge table is not read to the end. Two exceptions:
  inside a PostgreSQL transaction you opened (stopping the statement would
  abort the transaction), and a statement that changes data while
  returning rows (`DELETE ... RETURNING`) - those are read to the end and
  only the limit is shown.
- **Stop** cancels the running statement on the server.
- Each query tab has its own server session: `SET`, temporary tables and an
  open transaction carry from one run to the next. On PostgreSQL the tab
  shows **in transaction** with **Commit** and **Roll back** while one is
  open, and closing the tab asks first (closing rolls it back).
- With several statements, each result is a chip above the grid. Result
  sets show in a grid that stays fast with many rows: click a cell and
  **Ctrl+C** copies it, double-click (or Enter) opens the full value below -
  JSON pretty-printed. A click on a column header sorts the rows (again for
  descending, a third time back to the server's order): numbers as
  numbers, `NULL` last. **Ctrl+F** (or the magnifier under the grid) keeps
  only the rows with the text in any column and marks the cells; **Esc**
  closes it. Both work on the rows already fetched - on a table's Data
  view that is the page shown - and `#` keeps each row's number from the
  server. **CSV** and **JSON** save the rows as shown.
- On a connection marked *sensitive*, statements that change data are
  listed for confirmation before they run.

Values are shown the way `psql`, the `mysql` client and `clickhouse-client`
print them: numbers keep their precision, `NULL` is marked, binary columns
show as hex.

<a id="clickhouse"></a>
### ClickHouse

ClickHouse is reached over its HTTP interface (port 8123, 8443 with TLS),
through the SSH session like the others. What differs:

- **No transactions.** Commit and Roll back never show. Read-only rests on
  the statement check (`SELECT`, `WITH`, `SHOW`, `EXPLAIN`, `DESCRIBE`,
  `EXISTS`) and on the server: every statement of a read-only tab runs with
  `readonly = 2`, which ClickHouse itself enforces - a query cannot lift it
  with its own `SETTINGS`. A login whose profile is already read-only is
  left as it is.
- **Sessions.** Each query tab is a ClickHouse session: `SET` and `USE`
  carry from one run to the next. The server ends a session idle for an
  hour (its `max_session_timeout`); the next run starts a fresh one with
  the defaults.
- **Stop** closes the request and sends `KILL QUERY`. A login without the
  right to kill queries only gets the close: a query that has not started
  returning rows (a long aggregation) then runs on until it finishes, and
  the tab waits for it.
- **Results.** Arrays, maps and tuples show as ClickHouse prints them
  (`['a','b']`, `{'k':'v'}`). A statement with its own `FORMAT` clause
  (`FORMAT JSONEachRow`) comes back as one **output** column, a row per
  line. `INSERT` reports the rows written.
- **The tree** lists databases with their size on disk (from
  `system.parts`, when the login may read it), tables with their engine's
  row count, views and materialized views. **Data** reads in the table's
  `ORDER BY` key, **Structure** shows the primary key, the sorting key and
  the data-skipping indexes, and **DDL** is the server's own
  `CREATE TABLE`.

<a id="oracle"></a>
### Oracle

Oracle Database 12c and newer, with a pure-Go driver: no Oracle client or
Instant Client to install. What differs:

- **Service, then schemas.** The **Service** field is the service name to
  log in to (`ORCLPDB1`, `FREEPDB1`), or `sid:NAME` for an old-style SID.
  The tree lists the schemas that own tables or views, and the login's own;
  Oracle's own schemas (`SYS`, `SYSTEM`, ...) show with **system**. The tab
  starts on the login's schema, and a query tab on another one runs with
  `ALTER SESSION SET CURRENT_SCHEMA`.
- **Statements commit on their own.** Oracle has no `BEGIN` statement: put
  what has to commit together in a PL/SQL block.
- **PL/SQL blocks** (`BEGIN`, `DECLARE`, `CREATE PROCEDURE / FUNCTION /
  PACKAGE / TRIGGER / TYPE`) keep their semicolons and end at a line holding
  only `/`, as in SQL*Plus and SQL Developer; a `/` line also ends a plain
  statement. What a block prints with `DBMS_OUTPUT.PUT_LINE` shows under its
  result.
- **Read-only** sends only `SELECT` and `WITH`, each in a `SET TRANSACTION
  READ ONLY` that is rolled back; PL/SQL and `WITH FUNCTION` are refused. A
  function that runs as an autonomous transaction can still write - a
  login with read-only grants is the real guarantee.
- **Stop** sends a break in band (no TCP urgent data through a tunnel): a
  running query stops, a `DBMS_SESSION.SLEEP` runs to its end.
- **The tree and tables.** Row counts are the optimizer's statistics.
  **DDL** comes from `DBMS_METADATA`; for another schema's table that needs
  `SELECT_CATALOG_ROLE`, and without it the DDL is rebuilt from the columns.
  Dates show as `2006-01-02 15:04:05`, whatever the session's NLS settings.

<a id="mssql"></a>
### SQL Server

Microsoft SQL Server and Azure SQL. What differs:

- **The address names the port.** Named instances are not looked up (the
  SQL Browser service is UDP, which no tunnel carries): give the
  instance's port.
- **Azure SQL Database** reaches only the database you log in to (no `USE`
  of another), so that is the one the tree lists: put it in **Database**.
- **GO ends a batch.** A line holding only `GO` separates statements like
  `;` does; `GO 5` runs the batch five times, as sqlcmd does (up to 100000). A `DECLARE` (its variables last the batch), a `BEGIN ... END`
  block, an `IF` / `WHILE` and a `CREATE PROCEDURE / FUNCTION / TRIGGER` run
  to the next `GO` as one batch. A batch with several result sets shows the
  first and counts the rest; `PRINT` output shows under the result.
- **Transactions** work as on PostgreSQL: `BEGIN TRAN` holds one across
  runs, the tab shows it, and closing the tab rolls it back.
- **Read-only.** SQL Server has no read-only transaction, and T-SQL needs
  no semicolon between two statements (`SELECT 1 DELETE FROM t` is two), so
  a read-only tab sends a plain `SELECT` or `WITH` only: no `EXEC`, `INTO`,
  `OPENQUERY` / `OPENROWSET` or a second statement's keyword, inside a
  transaction that is rolled back. A login with only `db_datareader` is the
  real guarantee.
- **TLS** *Off* still encrypts the login (the password), as SSMS does,
  without checking the certificate.
- **The tree** groups tables by schema; row counts and sizes come from the
  partition statistics. SQL Server has no `SHOW CREATE TABLE`: **DDL** is
  rebuilt from the catalog (views show their own definition).

<a id="opensearch"></a>
### OpenSearch / Elasticsearch

One engine covers both (and forks such as the Wazuh indexer); which one
answers is read from the server. Port 9200; a cluster with its security
plugin on usually wants TLS **Require** (its certificate is self-signed).
The login is a user and password; a password with no username is sent as
an Elasticsearch API key. **Database** is not used: the tree shows the
cluster as the one database.

- **The tree** lists indices with their document count and size, aliases
  (as views, with the indices behind them) and data streams. Indices
  starting with a dot show with **system**. **Data** reads 200 documents at
  a time (`_search` with `from` / `size`, up to the index's 10 000 window),
  one column per field with nested objects flattened (`meta.color`) and
  arrays as JSON. **Structure** is the mapping, multi-fields included, and
  **DDL** is `GET /index`: settings, mappings and aliases.
- **Query tabs** take Kibana console requests and SQL, mixed in one
  script. A request is a line with the method and path, its JSON body on
  the lines below; `#` and `//` lines are comments, and `"""` strings
  hold raw text (a SQL query, a script). `_bulk` and `_msearch` bodies may
  be pretty-printed: they are sent one document per line.

  ```
  GET /logs-*/_search
  {
    "query": { "match": { "message": "timeout" } },
    "size": 50
  }

  SELECT host, count(*) FROM logs GROUP BY host;
  ```

  **Ctrl+Enter** runs the request or statement under the cursor.
- **Results.** Search hits come back as a grid (`_index`, `_id`, `_score`,
  then the document's fields) with the total hit count below it; `_cat`
  requests as a grid too (sent with `format=json` unless you ask for
  another); SQL as a grid, paged through the cursor up to the row limit.
  Anything else, aggregations included, is the response's JSON,
  pretty-printed a line per row. An error shows the cluster's type and reason.
- **Read-only** sends only GET and HEAD requests, POST to the search
  endpoints (`_search`, `_count`, `_msearch`, `_mget`, `_field_caps`,
  `_validate`, `_explain`, `_analyze`, `_termvectors`, `_rank_eval`) and
  SQL that reads. The cluster has no read-only session, so this check is
  all the tab can do: a login with a read-only role is the real guard.
- **Stop** closes the request; the cluster cancels a search whose client
  went away. There are no sessions or transactions.

<a id="redis"></a>
### Redis

Redis and the servers that speak its protocol (Valkey, KeyDB), port 6379,
TLS as for the others. The login is an ACL user and password; a password
with no username is the `requirepass` password. **Database** is the
database number (`3` or `db3`; empty is 0).

- **The tree** lists the numbered databases (all of them when the login
  may read `CONFIG GET databases`, else the ones with keys) and, in each,
  the keys grouped by the part before their first `:` (`user:*`,
  `session:*`; keys without one under *(no prefix)*). Groups come from a
  `SCAN` of up to 10 000 keys; on a bigger keyspace the counts say they
  are from a sample.
- **Data** lists a group's keys, sorted, with type, TTL (`-1`: none),
  size (bytes of a string, elements of a collection) and the value: a
  string's start, a collection's first 20 elements as JSON. **DDL** shows
  the pattern and the types seen.
- **Query tabs** run one command per line, quoted as in `redis-cli`
  (`SET k "two words"`, `"\x00"` escapes); `#` and `//` lines are comments.
  `HGETALL`, `CONFIG GET` and `... WITHSCORES` come back as two columns,
  `INFO` as section / name / value, `SCAN` as its keys with the next cursor
  below the grid, other arrays a row per element; `OK` and integer replies
  show as the result's message. `SELECT` carries to the next run.
  `MONITOR`, `SUBSCRIBE`, `SYNC`, `HELLO`, `RESET`, `QUIT` and
  `CLIENT REPLY` are refused: they would take the connection over.
- **Stop** closes the tab's connection, which also breaks off a blocking
  command (`BLPOP`, `XREAD BLOCK`); the next run connects again on the
  same database.
- **Read-only** sends only read commands (an allowlist that checks
  subcommands: `CONFIG GET` reads, `CONFIG SET` does not; `EVAL_RO` and
  `FCALL_RO` read, `EVAL` does not). Redis has no read-only session, so an
  ACL user limited to `+@read` is the real guard.

### Tables

A table tab has three views: **Data** (200 rows at a time in primary-key
order, **Prev** / **Next**), **Structure** (columns with type, nullability,
default and key; indexes; foreign keys) and **DDL** (MySQL's own `SHOW
CREATE`; on PostgreSQL rebuilt from the catalog, without partitioning,
storage options, ownership and grants). **Query** opens a query tab on the
table.

Statements that change data are recorded in the audit log (the statement,
cut to 300 characters, and the rows affected or the error).

### LLM access

A Database tab can be shared with an LLM over MCP like a terminal, or the
LLM can open one with `open_db` (you approve). It can list databases and
tables, describe a table and run read queries - always read-only, whatever
the connection allows, capped in rows and bytes. With read-run it can also
run statements that change data - each call approved by you unless the
share is auto-run - never on a read-only connection and without `BEGIN` / `COMMIT`. It works on its own
session, never on your query tabs. See
[Share a session with an LLM](#llm-mcp) for the bridge itself.

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
  for that connection is connected - a terminal tab, or the
  connection's tunnel (see [Tunnel connections](#tunnel-connections)).
- A forward runs in one session at a time. With two sessions on the
  same connection (two terminals, or a terminal next to its tunnel)
  the second does not start the forwards the first already runs, and
  the Port forwards section shows each forward's state whichever
  session holds it.
- Each forward has a Start / Stop toggle on the row.
- A local port that is already taken says by what: another tunnel (by
  name and connection), another program, or a Windows reserved port
  range. **Start on a free port** runs it once on a free port and keeps
  the saved one. The editor warns while you type a port that is in use
  or set on another saved tunnel. Tunnels that start on their own
  (auto-start, after a reconnect) report a failed start as a
  notification, and an auto-port tunnel says when it could not get its
  previous port back.
- The Forwards header shows running counts; rows show live byte
  counters (in / out).

### Opening things in a browser

Dynamic and local forwards both have an **Open URL…** button and
per-forward **bookmarks** - the URLs you reach through that tunnel,
as quick-launch buttons. They appear in the forwards list, in the
tunnel popover on a pane header, and in the quick palette.

**Bookmarks in the connection tree.** The bookmark button on a
bookmark's chip (or **In tree** when adding one) shows it as a row under
its connection, with a mauve bookmark icon and the URL's host on the
right. It behaves like a connection row: click selects, double-click or
Enter opens it - connecting and starting the tunnel first if they are
not up - and right-click offers **Open** and **Hide from tree**. The
connection row gets a small badge with the count; click it to fold the
bookmarks away (remembered, like open folders).

For a local forward the prompt starts filled in with the forward's own
address, so opening it is one click rather than retyping
`http://127.0.0.1:8080` every time.

**Tunnels that choose their own port.** A forward set to port 0 gets its
port from the OS at start, and a different one on the next run - there
is no number to write down. Bookmarks on such a forward use a
placeholder instead:

    http://127.0.0.1:{port}/grafana

`{port}` is replaced with the real port each time the bookmark is
opened, so it keeps working across restarts. `{host}` works the same way
for the listen address. Both are filled in for you when you add a
bookmark on an auto-port forward, and both are optional: a URL with a
literal port is left exactly as you typed it.

The placeholder is worth using on a fixed-port forward too - it means
changing the port later does not silently break every bookmark that
referred to it.

A dynamic forward routes the browser through its SOCKS5 port; a local
forward is reached directly, because it already listens on your
machine.

**Prefer one SOCKS proxy over many local forwards.** A single dynamic
forward reaches every host the server can reach, by its real name, with
no port mapping to invent and no collisions to avoid when two services
both want 8080. Local forwards are the better fit when something can
only speak to a fixed local port, or when you want exactly one service
exposed and nothing else.

#### Which browser a bookmark opens in

Each local and dynamic forward chooses this for itself, in the forward's
edit form:

- **Your browser** - the normal one, with your logins and extensions.
  The default for local forwards, and the same thing that happens when
  you paste `localhost:<port>` into a browser by hand.
- **Isolated profile** - a fresh throwaway profile every time. The
  default for SOCKS proxies, where separation is the point.
- **Dedicated profile** - separate from your everyday browser, but it
  remembers logins between launches.

The dedicated profile is worth choosing when two tunnels reach the same
software on different servers: a shared cookie jar means signing into
one signs you out of the other.

Configure which browser binary is launched in **Settings → Browser
launcher** (Chrome, Chromium, Firefox and Edge are all supported - pick
the binary path). **Use a persistent browser profile** in the same
section sets the default for SOCKS forwards that have not chosen for
themselves. Works with both Chromium- and Firefox-family browsers (on
WSL a persistent Firefox profile falls back to isolated).

### Grouping bookmarks: one forward per customer

Each launched browser process gets exactly one proxy - that is a
browser limitation, not an ssh-tool one. `--proxy-server` is a
process-wide switch, and a second launch against the same profile
directory is handed to the already-running process, which keeps the
proxy it started with. So a profile shared across two SOCKS forwards
would send everything through whichever one came up first.

That is why every forward gets its own isolated profile, and why two
forwards mean two browser windows rather than two tabs.

The practical way to get tabs back: **define one SOCKS forward per
customer (or per environment), on a jump host that can reach all of
that customer's services, and hang every bookmark for that customer
off it.** One forward means one browser window, so those bookmarks
open as tabs and share a session. Isolation stays where it matters -
between customers, not between services belonging to the same one.

This also avoids address collisions. Two customers both using
`10.0.0.0/8` internally are only unambiguous if each is reached
through its own forward.

If a jump host genuinely cannot see part of a customer's estate, give
that segment its own forward. The extra window is correct there, not
a workaround.

<a id="tunnel-connections"></a>
### Tunnel connections (no tab)

A connection that exists only for its forwards - a jump into a
customer network that carries a Kafka broker, a database port and a
SOCKS proxy for the web GUIs - does not need a terminal. Set **Double-click
opens** to **Tunnel** on it, or on a whole folder of them, and it runs
like `ssh -N`:
the session connects without a shell or PTY, holds the forwards, and
opens no tab. Accounts without a login shell work too, since no shell
is ever asked for.

- **Starting.** Double-click or Enter on a tunnel connection starts
  the tunnel instead of opening a terminal. It never stops one that is
  already up. Right-click offers **Start tunnel** / **Stop tunnel**,
  **Show in Tunnels panel** and **Open terminal**, for when you want a
  shell on the same host. Selecting several and picking Connect all
  starts the tunnels among them. In the connection editor the header
  offers **Start tunnel** / **Stop tunnel** first and **Terminal**
  next to it; with **Use a different credential...** set, the button is
  a terminal connect as for any connection.
- **Which forwards come up.** All of them: a tunnel connection exists
  for its forwards, so starting the tunnel starts every saved forward,
  whatever its auto-start flag says. You can stop single forwards in
  the Tunnels panel or in the connection's Port forwards section (which
  for a tunnel connection acts on the tunnel, and offers **Start
  tunnel** while it is down); one you stopped stays stopped across a
  reconnect, and the next start of the tunnel brings it back.
- **Feedback.** A start by hand ends in a notification: "Tunnel X up,
  3/3 forwards listening", or why it failed, in the window you started
  it from. A launch start or a reconnect stays quiet unless the tunnel
  fails or comes up with a forward down; several at once share one
  notification.
- **On app launch.** **Start with the app** (On / Off / Inherit, in
  the editor's Forwards & tunnel tab, or the folder's Tunnels tab)
  starts the tunnel once
  the vault gate is passed. A launch start keeps retrying while the
  network or the server is not there yet, so starting the app before
  Wi-Fi or a VPN is fine. If you choose **Skip** at the vault gate,
  tunnels that need a vault credential wait in error and start on
  their own after the next unlock. A tunnel you stopped by hand is not
  restarted by a UI reload.
- **After a drop.** A tunnel that was up and lost its connection
  reconnects until it succeeds or you stop it, waiting 1, 2, 4 ... up
  to 60 seconds between tries, and brings every forward up again except
  the ones you stopped - a forward that failed before gets another try. Only network failures
  are retried: a rejected host key or a failed login stops the tunnel
  in error rather than asking again every minute. A start by hand
  tries once and reports what went wrong. Tunnels reconnect whatever
  the connection's Auto-reconnect setting says.
- **The Tunnels panel.** The status bar shows **Tunnels n/m** (up of
  all) whenever a tunnel connection exists - green while tunnels are
  up, yellow while one is connecting or reconnecting, red with a count
  when one failed. A connected tunnel with a forward that should be
  listening and is not (it failed to start; one you stopped yourself
  does not count) also shows yellow, in the bar, the panel and the
  tree, and the panel says why on the forward's line. Its tooltip and
  the panel header also give the
  number of listening port forwards in all sessions, which the separate
  forwards segment shows when there are no tunnel connections. Click it
  for the panel: tunnels grouped by folder,
  each with its state and uptime, the reconnect countdown, the last
  error, and Start / Stop; **Start all** / **Stop all** and a filter at
  the top. Click a tunnel to see its forwards (state, traffic, Start /
  Stop each), **Show in tree** and **Open terminal**. A failed tunnel
  offers **Try again** and **Clear error** (leaves it stopped). The
  item can be hidden from the status bar by right-clicking the bar.
- **In the tree** a tunnel connection carries a small cable icon,
  green while up, yellow while connecting, red when it failed.
- **Deleting** a tunnel connection stops its tunnel.
- **Quitting** the app disconnects tunnels like any other session; the
  quit prompt says how many of the open sessions are tunnels. With
  close-to-tray on, closing the window keeps them running.

Both settings are part of the profile, so they sync to your other
machines like every connection setting: a tunnel set to start on
launch starts on each of them.

### Tunnels from the terminal view

You don't have to scroll back to the Connections view to toggle a
tunnel or open a bookmark - every pane header has **Tools → Port
forwards** (the Tools menu in the SSH-only toolbar group):

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
matter which window or pane they belong to. Hidden at zero. Once you
have tunnel connections, the **Tunnels** segment takes its place and
carries the same count in its tooltip and panel header.

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

<a id="llm-mcp"></a>
### Share a session with an LLM (MCP)

You can attach an external LLM client (Claude Code, etc.) to a live
SSH session so it can help you debug - read what's on screen, pull
logs, propose and run commands. It is **off by default** and
desktop-only.

Connections marked **Sensitive** (on them or on a folder above them,
see [Connection editor](#connection-editor)) are off limits in every
LLM tool: not listed, not opened, not changed, not used as a jump host.
The LLM cannot change a folder that is sensitive or holds a sensitive
connection either, since its settings flow down to those hosts.

Setup, once:

1. **Settings -> LLM (MCP) access** -> tick *Allow LLM (MCP) access
   to shared sessions*. This starts a local-only bridge (a unix
   socket on Linux/macOS, a loopback pipe on Windows). Nothing is
   exposed to the network.
2. Register ssh-tool with your LLM client. The Settings page shows
   the exact command with your binary's path. For **Claude Code**:
   `claude mcp add ssh-tool -- /path/to/ssh-tool --mcp-bridge`. For
   **Claude Desktop** there is an **Add to Claude Desktop** button -
   it merges an `ssh-tool` entry into Claude Desktop's config file,
   leaves any other MCP servers you have there alone, and keeps a
   `.bak` of the original. Claude Desktop only reads that file at
   startup, so restart it afterwards. If the app later moves to a
   different path the button says *Update the path* - the registered
   command still points at the old binary until you press it. For
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
   pane toolbar (the robot-icon button next to tunnels). Three levels:
   - *Read only* - the LLM can read the session's scrollback, nothing
     else. No commands, no files.
   - *Read + run* - adds running commands and typing into the terminal
     (read-only commands go straight through, anything else is
     **gated** by an approval prompt), plus the file tools: browsing
     and reading files over SFTP, and fetching one to your machine
     with your approval. There is no separate file-sharing level - if
     the LLM can run `cat`, it can read the same file over SFTP.
   - *Auto-run (YOLO)* - runs the LLM's commands **without asking** each
     time, for hands-off work. Catastrophic commands (recursive delete of
     a system path, disk wipe, shutdown, ...) **still** raise a prompt -
     a built-in safety net the mode can't bypass. Everything is recorded
     either way. Use it deliberately; it is per-session and never a
     default.

   The button turns blue while shared (red for YOLO). The LLM only ever
   sees sessions you have shared.

What the LLM can do:

- **read_terminal** - the recent scrollback. This is handed to the
  LLM as untrusted data; a log line that says "run X" is not a
  command, only a tool call is.
- **run** - runs a command on a side channel and returns the output.
  Read-only commands (ls, cat, journalctl, systemctl status, ...)
  run immediately; anything that could change state pops an approval
  prompt where you **Run** it or **Deny**. You can extend the
  auto-run allowlist in Settings; mutating commands (sudo, rm, ...)
  always prompt. Arguments count too: `find -delete`, `sort -o`,
  `ip link set`, `git branch NAME` and a `VAR=value` prefix other than
  the locale ones prompt, and so does a command spread over several
  lines. `awk`, `sed`, `less` and `more` are not on the built-in list
  (they can edit files or run commands); add them to the allowlist if
  you want them auto-run. The Gather facts custom column uses the same
  list.
- **type_into_terminal** - on approval, types text into your live
  terminal **without pressing Enter**, so you review it and submit it
  yourself.
- **list_files / read_file** - browses directories and reads files over
  SFTP instead of shelling out to ls and cat. Same gate as those
  commands (read-run, no prompt), but exact sizes and binary files
  survive the trip - a file header comes back base64 rather than
  mangled. read_file is capped at 64 KB by default, 1 MB absolute.
- **download_file** - copies a remote file **to your machine** and hands
  the LLM only the local path, so a 200 MB log or a core dump never
  travels through the conversation. This one **always asks you**
  (except on a YOLO session), because it is the only tool that writes
  to your local disk. The prompt names the file and its size. The
  destination is fixed - `mcp-downloads/<session>/` inside the app's
  data directory - so the LLM cannot aim a write anywhere else, and a
  second copy of the same filename lands as `name-2.log` rather than
  overwriting the first.
- **list_connections / connect** - the LLM can also search your saved
  connections (by name or folder only - hostnames aren't exposed
  until a connect) and open one. Opening a session always asks you to
  approve first, and the new session is then shared with the LLM
  automatically so it can start working.
- **list_facts / gather_facts / facts_history / facts_snapshot** - for a
  status or health report over many servers ("write the monthly report
  for the Hetzner folder"). `gather_facts` runs Gather facts on a folder
  (or listed hosts) with the fixed, read-only fact snippets - never the
  custom column. Before anything connects you get one approval listing
  every host (untick the ones to leave out), every fact and the exact
  script. A folder run is stored in that folder's report history, so it
  also shows under Gather facts > Report. The LLM gets the folder's
  report settings with the results (thresholds, expected failed units)
  and is told to apply them. `facts_history` and `facts_snapshot` read
  stored runs without connecting, e.g. to compare with last month.

**Create connections in bulk (Allow manage).** The Share-with-LLM
popover has an *Allow manage (create connections)* toggle. It is off by
default and **ends when you quit ssh-tool** - a grant that can write to
your whole connection tree never comes back on its own, so turn it on
again on the next launch. With it on, you can paste a list of servers
into your LLM with instructions - "these go through bastion 1.2.3.4 as
admin, on credential 'prod-key', each with a SOCKS proxy and a bookmark
to the internal wiki" - and it will build the whole tree of folders,
connections, port forwards and SOCKS bookmarks for you. Nothing is
written as the LLM works: it stages a plan, then calls **commit_plan**,
which pops a single approval modal showing the full structure (each
host, user, via which bastion, network profile, and credential **name**).
Approve and it is all written at once (all-or-nothing); reject and
nothing changes. The LLM **never sets or sees a password** - a
connection or its bastion can only reference a credential that already
exists in your vault, by name.

The same grant also lets the LLM **change things that already exist**:
rename a connection or folder, correct a hostname, user or port, swap a
credential or network profile, or move a connection into another
folder. Only the fields it names are touched. It can also take a
setting *off* a connection so the connection inherits the folder's
instead - that is how you say "these two should just use the folder's
credential". Edits stage into the same plan and are shown separately in
the approval modal, as `field: old -> new` above the list of new items,
so you can see what is being overwritten before you approve. There is
deliberately **no delete tool**: removing things stays yours to do in
the app.

If every connection the LLM is about to create in a folder carries the
same credential or network profile, the approval modal says so and
suggests putting it on the folder instead. It only points this out - it
never rewrites the plan behind you - but taking the hint means a later
credential rotation is one edit instead of one per host.

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
output as untrusted, respect approvals, be extra careful in auto-run
mode), click **Copy system prompt** in Settings -> LLM or the
Share-with-LLM popover, then paste it where your client keeps
instructions: **Claude Desktop** -> Project -> *Instructions* (and
then chat inside that project), **Claude Code** -> your `CLAUDE.md`,
**LM Studio** and others -> the system-prompt field. It is the same text
ssh-tool already sends to clients that read MCP server instructions; see
[`docs/MCP_SYSTEM_PROMPT.md`](https://github.com/fpenezic/ssh-tool/blob/main/docs/MCP_SYSTEM_PROMPT.md).

Pasting it as an ordinary chat message part-way through a conversation
often will **not** work, and that is the model behaving correctly: a
model that accepted "here is your new system prompt" from a chat turn
would also accept it from a log line or a filename it read through
these tools. Put it where instructions belong and it is trusted; send
it as chat and it is treated as text, which is the whole point.

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
## 7b. Network profiles (WireGuard / NetBird / Tailscale / OpenConnect / OpenVPN / Check Point)

A network profile routes a connection's **first SSH hop** through an
overlay VPN, so you can reach hosts that only live on a private
network (a client's WireGuard, OpenVPN or Check Point VPN, a Hetzner internal network, a
NetBird tailnet) without setting up a system-wide VPN. Everything runs in
**userspace**: no TUN adapter, no admin rights, no system routes, and
several profiles can be up at once toward different networks.

Manage profiles in **Settings -> Network profiles**: the list on the
left holds **Plugins & proxy** (plugins, defaults, the app proxy), your profiles
(a green dot when the tunnel is up, **via proxy** when it is reachable
through the app proxy) and **New profile**. A profile opens on four tabs:
**Overview** (mode, idle stop, how many connections and web bookmarks use
it, when the session ends), **Settings** (the profile itself, and Delete),
**Proxy** and **Details**. Above them: **Connect** brings the tunnel
up now (it turns into **Disconnect** while the tunnel runs), **Pause** /
**Resume** shows the pause switch's state (yellow while paused; Pause
stops the tunnel, Resume connects it again), and
**Browser**, after a divider, opens a browser through the
profile. Assign a profile to a
folder or a connection via its **Network** setting (in the detail
pane); the setting inherits down the tree like the others, so a whole
"client-X" folder can go through one tunnel. A pane whose first hop
went through a tunnel shows a small VPN badge with the profile name,
and the status bar shows which tunnels are up.

Tunnels start on demand (first connection through them) and stop on
their own after the last session using them closes. **Stop an idle
tunnel** (under General) sets how long they linger: right away,
1 minute to 4 hours, or never (default 2 minutes). A profile's Overview
has **Stop when idle** to override it for that profile; **Global** follows
the General setting. A change applies to idle tunnels at once.
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
  show no VPN badge because they really went direct. While the tunnel
  is up, connects use it straight away without the probe.
- **Pause** - a per-profile kill switch: every connection using it
  dials direct and the tunnel is stopped immediately. For "I'm on the
  network, leave the VPN alone."

### Profiles on several machines

With profile sync on, a profile's tunnel can only be up on one machine
at a time: two machines with the same identity degrade a WireGuard
tunnel on both, and most VPN gateways drop the older login. A running
tunnel is recorded in the synced storage, and connecting through the
same profile on another machine opens **Network profile in use**:
**Take over** asks the other machine to disconnect and connects here
once it has (a countdown shows the wait), **Connect anyway** ignores
it, **Cancel** leaves both alone. Quitting the app releases its
records (it waits two seconds at most); a machine that quit before
that, or crashed, counts as gone after about 100 seconds.

### Plugins

NetBird, Tailscale, OpenConnect, OpenVPN and Check Point run as optional **plugins**: small
helper programs, one per profile, kept out of the app to keep it small
(and, for OpenConnect and Check Point, because their engines are GPL /
AGPL licensed). Install them
under **Plugins & proxy** on this page: each is downloaded from the
[ssh-tool-plugins](https://github.com/fpenezic/ssh-tool-plugins)
releases and checked against its sha256 digest before install. The card
shows the plugin release and the engine version inside it, and an
update is offered there (and as a status bar pill) when a newer plugin
release is out. Downloads exist for Windows and Linux; on macOS build
the plugin from the ssh-tool-plugins repository and put it in the
plugins folder (`~/Library/Application Support/ssh-tool/plugins`), where
the app finds it. The macOS build script (`scripts/macos-build.sh`)
builds and copies the plugins you pick - see [docs/macos.md](https://github.com/fpenezic/ssh-tool/blob/main/docs/macos.md) for it and for the steps by hand.

Plugins are desktop only: Android cannot start helper processes.
Profiles still sync to the phone, and there a plugin profile is marked
**not on mobile**. On **Auto** it dials directly (no probe), which
works when the official VPN app is connected as the system VPN; on
**Always** the connect is refused with that explanation. WireGuard
profiles are built in and work on Android as on the desktop.

### Browser through a profile

**Browser** on a profile opens an isolated browser (its own
profile per VPN, kept between launches, apart from your everyday
browser) that reaches the customer's intranet through that VPN: the
profile's networks, the host names of connections that use it and names
only the customer's DNS knows go through the tunnel; everything else
(the internet) goes out directly, so a split VPN behaves as usual. The
tunnel comes up with the first page that needs it.

**Web bookmarks** live in the connection tree: **New web bookmark** (the
tree's + menu, or a folder's right-click menu) takes a name and an
address. A bookmark has a **Network** setting like any connection,
inherited from its folders, so a bookmark in a customer's folder opens
in that customer's VPN browser; with no profile (or Direct) it opens in
your normal browser. Double-click or **Open** opens it. The browser reaches the app
through a proxy on `127.0.0.1` without a password (browsers cannot send
SOCKS credentials, the same as the SSH dynamic forwards); it closes after
30 minutes without traffic and reopens on the same port.

### App proxy (programs and a CI runner through the VPNs)

The **app proxy** (Plugins & proxy -> App proxy) is one SOCKS5 proxy for things
outside the app - a CI runner, `curl`, a script, a database tool - that
reaches every profile you mark **Reachable through the app proxy** (a
profile's Proxy tab). Each address goes through the VPN whose networks
contain it; anything no marked profile covers is refused, so it is not a
way out to the internet. It always asks for a username and password
(`ssh-tool` and a generated password, kept in the vault); **Copy proxy
address** gives `socks5://ssh-tool:<password>@127.0.0.1:<port>`. A
tunnel comes up with the first connection through it (a login or
one-time code is asked as usual) and the idle stop applies after the last
one closes. The app has to be running; the listener starts with it.

A profile's networks are the routes the server pushed (from the last
connect; a full-tunnel default route is left out), the addresses of
connections in the tree that use it, and any **Extra networks** on its
Proxy tab, which lists them all. Host names of those connections go to
their profile as well and are resolved inside the VPN. When two profiles
carry the same network, the more specific one wins, then the one that is
up; **When networks overlap, use** picks one profile to take every
address its networks contain. The summary under it names each overlap
and which profile wins it.

For a **GitLab runner in Docker**, without touching `.gitlab-ci.yml`:

1. Turn the app proxy on. **Copy gateway command** (the "A CI runner in
   Docker" card) gives a `docker run` for a small gateway container
   (tun2socks) that sends only the marked profiles' networks into the
   proxy (**Show networks** lists them). Everything else - your GitLab,
   cache, registry - stays on the normal network. After adding a profile
   or a network, recreate the gateway with a freshly copied command.
2. In the runner's `config.toml`, under `[runners.docker]`:
   `network_mode = "container:sshtool-gw"`. Every job container then
   shares the gateway's network. Docker refuses `extra_hosts` together
   with this mode; move those entries to the gateway (`--add-host` on
   its `docker run`), the jobs see its hosts file.

A profile can also have a **Separate proxy for this VPN only** (its Proxy
tab): its own port and password and its own gateway command
(`sshtool-gw-<profile>`), for one runner per customer that must stay
apart from the others, overlapping address ranges included.

Docker Desktop reaches a proxy on `127.0.0.1` through
`host.docker.internal`; a Linux Docker host needs the proxy on the
docker bridge address (or `0.0.0.0`, firewalled). Only TCP goes through
(SSH, HTTPS, registries); a job resolves names with its own DNS, so a
name only the customer's DNS knows needs an address (or an `--add-host`
on the gateway).

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
> process, which Android can't spawn - so NetBird profiles work on
> the desktop only (see Plugins above for Android). WireGuard profiles, by contrast, are
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

### OpenConnect profiles (AnyConnect, Fortinet, F5, GlobalProtect, Pulse)

SSL VPN gateways through the optional **OpenConnect plugin**: Cisco
AnyConnect and ocserv, Fortinet SSL VPN, F5 BIG-IP, Palo Alto
GlobalProtect, Pulse / Ivanti and Juniper Network Connect. Like the
others it runs in userspace, so two gateways can be up at the same time,
even with overlapping address ranges. Desktop only.

A profile needs:

- **VPN type** and **Gateway** - the address or URL, with the group path
  if the gateway uses one (`vpn.example.com/staff`). **Group** picks a
  login group when the gateway offers several.
- **Backup gateways** (optional) - further gateways of the same VPN, one
  per line, as FortiClient's list of remote gateways. Connect tries the
  gateway, then each backup in order, moving on only when one does not
  answer at all (no TLS handshake within 10 seconds). A gateway that
  answers and rejects the login is not retried on the next one, so a
  wrong password is sent once. A session kept open by the idle stop
  resumes on the gateway that holds it, tried first. Needs OpenConnect
  plugin v1.3 or newer.
- **Username** and **Password** - type the password on the form (it is
  saved in the vault as a credential named after the profile), pick a
  stored password (vault, Bitwarden or KeePass), or leave it blank to be
  asked at login. A blank username falls back to the password
  credential's username, else it is asked.
- **Second password** - for gateways with a second login or a one-time
  code: **Ask at login** (hardware or app tokens such as MobilePASS),
  **One-time code from** a Bitwarden / KeePass item's TOTP, or a
  **Stored password**. **Second username** is asked when blank.
- **Client certificate** (optional) - a stored certificate credential,
  or import one right here from a file or pasted PEM.
- **MTU** (optional, caps the tunnel MTU) and **TLS only** (no DTLS /
  UDP, for networks that block it).

**Login prompts** show the gateway's message and banner and name each
field: `[1st login - password]`, `[2nd login - password / one-time
code]`. What the profile can answer is filled in, the rest is asked.
If the gateway rejects a login, the next form is asked in full rather
than resending stored answers, so a wrong value cannot lock the account
through retries. The app log records each login round (field names and
where each answer came from, never the values) for troubleshooting.

**Password change.** When the gateway asks for a new password at login
(expired, or Cisco ASA offering a change before it expires), the form is
asked as `[password change - new password]` / `[password change -
confirm new password]`. Nothing stored is filled into it, and the two
entries must match before anything is sent. Leave both blank to skip an
optional change. Once the tunnel is up, the new password replaces the
old one in the vault credential it came from (the old one stays in the
credential's history). A typed password, or one from Bitwarden or
KeePass, has to be updated there by hand; the log says so.

**Session reuse (AnyConnect).** The idle stop takes the tunnel down
without logging out of the gateway: the session stays open there until
the gateway's own idle timeout, and the next connect resumes it with no
login and no one-time code. If the gateway no longer knows the session,
the normal login follows. The session cookie is held in memory only,
never in the vault or sync, so quitting the app or stopping the profile
by hand logs out for good. **Log out of the gateway** under Idle stop
turns this off for a profile. A dropped network inside a running tunnel
reconnects on its own with the same session either way.

**Gateway certificate.** A certificate the system trusts is accepted.
Otherwise (self-signed, private CA) the first connect shows **Untrusted
VPN gateway certificate** with its subject and fingerprint; **Trust &
remember** pins it on the profile. A changed certificate asks again.
Each gateway keeps its own pin, by address: changing an address forgets
its pin, reordering the gateways keeps them.

**Details.** A running profile's **Details** tab shows the tunnel address,
gateway, transport (DTLS or TLS), MTU, routes, DNS servers and search
domains and the gateway banner, with **Copy** for a support ticket. When
the gateway announces them, it also shows when the session ends (the
gateway disconnects then and asks for a new login) and its idle timeout.

**One login per user.** Most gateways allow one session per user and
drop the older one. A synced OpenConnect profile that is up on another
machine therefore gets the same take-over prompt as WireGuard: take it
over, connect anyway, or cancel.

SAML / browser (single sign-on) login is not supported yet; such a
gateway reports it clearly.

### OpenVPN profiles

OpenVPN 2.x servers (OpenVPN Community and Access Server, pfSense,
OPNsense, router firmware) through the optional **OpenVPN plugin**, an
OpenVPN client written for ssh-tool. Like the others it runs in
userspace: no TAP / TUN driver, no admin rights, several servers at once.
Desktop only.

**Import** the server's `.ovpn` (or paste it). Files it references must
be inline (`<ca>`, `<cert>`, `<key>`, `<tls-auth>` / `<tls-crypt>`), as
exported client profiles usually are. The configuration is checked when
the profile is saved. Its secrets do not stay in the profile: the client
certificate and key become a **certificate credential** (vault, synced,
expiry warning like any credential; renew it there), the tls-auth /
tls-crypt key goes to the vault. **Import again** on an existing profile
to replace the configuration; a certificate in the new file renews the
profile's certificate credential (the old one stays in its history).

When the file has `auth-user-pass`, the form asks for:

- **Username** and **Password** - typed (saved as a credential), a
  stored password (vault, Bitwarden or KeePass), or blank to be asked.
- **One-time code** - the answer to the server's challenge: a
  `static-challenge` in the file, or a question the server asks at login
  (dynamic challenge, or a pending login with a question, as OpenVPN
  Access Server asks for its OTP):
  **Ask at login**, a **TOTP** from a Bitwarden / KeePass item, or a
  **Stored password**.

A refused login is asked again in full (no stored value is retried), and
the app log records each login round without the values. When the server
hands out an auth-token, renegotiations and reconnects use it, so a
one-time code is not asked again for the life of the session.

What works: UDP and TCP with several `remote` lines tried in order,
tls-auth and tls-crypt, AES-GCM, ChaCha20-Poly1305 and AES-CBC (with
cipher negotiation), key renegotiation from either side, reconnects
after a network loss or a server restart, `remote-cert-tls server` and
`verify-x509-name`, and servers that compress (`comp-lzo`, `compress
lzo` / `lz4` / `lz4-v2`: what the server compresses is unpacked, what the
app sends is never compressed). Not supported: TAP (layer 2),
tls-crypt-v2, PKCS#12 / smart cards (import the key as PEM). **Details**
shows the address, routes, DNS, transport and cipher of a running
profile. The take-over prompt for a profile up on another synced machine
applies as for OpenConnect.

**Login in the browser.** A server that finishes the login on a web page
(SAML / SSO, or Access Server's web login) opens that page in your
browser during the connect; the tunnel comes up once you are done there.
A server that waits for an approval on your phone just holds the connect
open until you approve.

### Check Point profiles

Check Point remote access gateways (the ones Endpoint Security VPN, the
Check Point Mobile client or `snx` connect to) through the optional
**Check Point plugin**, a Go client based on
[snx-rs](https://github.com/ancwrd1/snx-rs) (AGPL-3.0). It speaks the
SSL Network Extender tunnel (TLS on the gateway's port, usually 443) in
userspace: no TUN adapter, no admin rights, several gateways at once.
Desktop only.

A profile needs:

- **Gateway** - the address you would give the Check Point client (host,
  or host:port); a pasted portal URL is cut to its host.
- **Username** and **Password** - typed (saved as a credential), a
  stored password (vault, Bitwarden or KeePass), or blank to be asked.
- **One-time code** - for a gateway that asks for a further factor after
  the password: **Ask at login**, a **TOTP** from a Bitwarden / KeePass
  item, or a **Stored password**.
- **Login option** (optional) - the gateway's login option id when it
  offers several; blank picks its username + password option
  (`vpn_Username_Password`).

A refused login is asked again in full (no stored value is retried), and
after three refusals the connect stops, since gateways may lock the
account. **Gateway certificate**: Check Point gateways usually present a
certificate from their own internal CA, so the first connect asks to
trust it (**Trust & remember** pins it on the profile), exactly as for
OpenConnect.

**Details** shows the office-mode address, the networks the gateway
routes into the tunnel (also what the app proxy sends to this profile),
the DNS servers and search domains, and when the session ends (the
gateway disconnects then and asks for a new login). A dropped network
reconnects on its own with the same session. The take-over prompt for a
profile up on another synced machine applies as for OpenConnect.

Coming from `snx-rs`: its configuration file keeps the password base64
encoded (`password=` line). Decode it before typing or storing it here;
the encoded text is refused like any wrong password and counts toward
the gateway's lockout.

Not supported yet: certificate logins, browser (SAML) logins, Mobile
Access portals and the IPsec transport; the connect names the login
option that needs one of them.

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
   active session into the **default** group, or out of every group
   it is in. Orange when any group contains this session. The hover tooltip lists
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

- **New workspace from N tabs** - select tabs (Ctrl/Shift+click), then
  right-click one of them: only the selected tabs are saved and framed,
  the rest stay as they are. A single tab's menu has the same as **New
  workspace from this tab**.
- **Save current as workspace** - snapshots every open tab: pane tree
  (splits, directions, ratios), connection, title and group metadata.
  Those tabs become the workspace's frame.
- **Open** - connects every pane in the workspace and rebuilds its tabs
  next to the ones already open; nothing you are working on is closed.
  Opening a workspace that is already open just focuses it.
- **The frame** - a workspace's tabs sit together in the tab bar inside a
  coloured border, with the workspace name in front. Click the name for
  **Save changes** (writes exactly the tabs in the frame) and **Close
  workspace** (disconnects only those tabs). Several workspaces can be
  open at once, each in its own frame.
- **Save changes** in the status bar menu targets the workspace of the
  focused tab. The save icon on a workspace that is not open overwrites
  it with all open tabs, which then become its frame.
- **Delete** - remove the workspace; its tabs stay open, unframed.

Frame membership:

- Drag a tab onto a tab inside a frame (or onto the frame's name) to add
  it; drag it onto a tab outside the frame, or the empty end of the bar,
  to take it out. Dropped on the left edge of the frame's name, a tab
  goes in front of the frame, outside it - the way to put a tab first
  when a workspace starts the bar. Right-click a tab for **Add to workspace "…"** and
  **Remove from workspace "…"**, which also act on a multi-selection.
- A dot on the frame's name means its tabs changed since the last save.
  Nothing is saved on its own - **Save changes** writes the frame.
- A tab opened later lands outside the frame until you add it.
- Drag the frame's name (the icon, or the full name) to move the whole
  workspace: its tabs move as one block, in order. Dropped on another
  workspace it lands before or after that workspace, never inside it.
- **Compact labels** in the name's menu shows only the icon, for a
  narrower bar. It applies to all frames on this machine.

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
- **Ctrl+Enter** or **Ctrl+click** - mark a connection without
  connecting. See below.
- **Esc** - close.

Connections rank slightly above folders at similar scores; tunnels
and bookmarks rank slightly below so a query that matches both a
host and a tunnel surfaces the host first. The empty query view
shows connections / dynamic entries / folders only; tunnels and
bookmarks appear once you start typing so they don't swamp the
list.

### Opening several hosts at once

Mark connections with **Ctrl+Enter** (or Ctrl+click) and they get a
coloured stripe; the footer counts them. **Enter** then connects
every marked host instead of the highlighted row, and `clear` in the
footer drops the set.

Marks survive retyping the query, so hosts that no single search
matches can still be collected: find the first, mark it, search
again, mark the next. Only connections can be marked - tunnels,
bookmarks and commands are not things you open five of at once.

Hosts are dialled one after another rather than all at once, because
a connection may need the vault unlocked, a host key accepted or a
password typed, and those prompts are modal - firing them in
parallel would stack prompts from different hosts with no way to
tell them apart. If some hosts fail the rest still open, and the
failures are reported together in one message.

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
- **Variables.** A body may contain `${var}` or `${var:default}`
  placeholders. Firing a snippet that has them opens a small inline
  form in the palette - one field per variable, defaults pre-filled -
  and **Enter** substitutes the values and sends. Example:
  `journalctl -u ${svc} --since ${since:-1h}` prompts for `svc` and
  `since` (the latter defaulting to `-1h`). Values are ad-hoc, typed
  each time, never stored; snippets with no placeholders fire
  immediately as before.
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

**Default snippets.** A fresh install seeds a small starter set (below).
An existing install is never touched - click **Load defaults** in
Settings → Snippets to add the ones you don't already have (it skips
duplicates). The set:

| Name | Body |
|------|------|
| Listening ports | `ss -tlnp` |
| Service status | `systemctl status ${svc}` |
| Service logs | `journalctl -u ${svc} --since ${since:-1h} --no-pager` |
| Tail a log | `tail -f ${path:-/var/log/syslog}` |
| Kernel messages | `dmesg -T \| tail -40` |
| Top by memory | `ps aux --sort=-%mem \| head -12` |
| Compose up | `docker compose up -d` |
| Compose pull + up | `docker compose pull && docker compose up -d` |
| Compose logs | `docker compose logs -f --tail=100 ${service}` |
| Docker containers | `docker ps -a` |
| Docker disk usage | `docker system df` |

---

<a id="tcpdump"></a>
## 13. Live tcpdump panel

For when you want to see traffic on a remote interface without
juggling a second terminal.

Open it via **Tools → Packet capture** in the pane header.

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
   pane's Tools button grows a small green dot. Click either to
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
- **Detach the capture itself** to a standalone window with the
  **detach** button (`⧉`) in the tcpdump header. This is the way to
  watch packets while typing in the terminal: the capture pops into
  its own window and keeps running; closing that window brings it
  back as a background chip in the main window (nothing is stopped).
  It re-attaches through the same ring snapshot as a tab detach.

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

<a id="logtail"></a>
## 13a. Live log tail

Stream a remote log while you keep working in the terminal. Open it
via **Tools → Log tail** in the pane header (next to Packet capture).

Pick a source in the start form:

- **journalctl** - follow the whole journal or a specific unit
  (e.g. `nginx`). Runs `journalctl -f`.
- **tail file** - follow a path (e.g. `/var/log/syslog`). Runs
  `tail -F`, which survives log rotation.
- **container** - follow one running container's logs. Appears only
  when a container engine is detected on the host (Docker preferred,
  else Podman). A dropdown lists running containers (name - image);
  runs `<engine> logs -f`. A refresh button re-lists.
- **compose** - follow a whole Compose stack at once. A dropdown lists
  Compose projects; runs `<engine> compose -p <project> logs -f`, so
  every service streams together, each line prefixed with its service
  name.
- **Seed lines** - how many existing lines to show before following
  (default 200).

Auth uses the same root/sudo probe as tcpdump (many logs need root);
the connection's saved password is auto-fed to sudo when available,
with a manual prompt as fallback.

Once running, the view understands the logs, it doesn't just scroll
them:

- **Parsed / Raw toggle.** *Parsed* (default) detects the format -
  JSON logs, journald/syslog, nginx/apache access, or a level keyword
  in plain lines - and colours each row by severity with a level tag.
  *Raw* shows the exact bytes the host sent, no colours or tags, for
  copy-paste fidelity or when a format guess is off. The filter works
  in both.
- **Smart filter.** The filter box still greps (a bare word is a
  case-insensitive substring), but also understands a small query
  language, terms combined with AND:
  - `level>=warn` / `level=error` - severity compare.
  - `status=500`, `method=POST` - match a parsed field.
  - `-noise` / `!noise` - exclude a substring.
  - `"quoted phrase"` - substring with spaces.
  Typing offers **autocomplete** for keys, level values, and real
  field values seen in the current lines (Tab / Enter accepts). A
  malformed query falls back to a plain substring match - it never
  blanks the view. The filter is client-side and never touches the
  stream.
- **Group similar** (Parsed view) collapses repeated lines that differ
  only by variables - numbers, IPs, ids - into one row with an `xN`
  count, so a flapping message stops drowning out a lone new error.
- The stream **auto-reconnects** if the remote command drops while
  the SSH session is still up (you'll see a brief "reconnecting"
  note), so a flaky link doesn't end the tail.
- **Detach** (`⧉`) pops the tail into its own window so you can
  watch it beside the terminal; closing that window brings it back
  as a background chip. **Minimise** (`-`) keeps it running in the
  background. Both re-attach through a 2000-line backend ring, same
  model as tcpdump.

---

<a id="http-tool"></a>
## 14. HTTP / SOAP request tool

For when you need to hit an HTTP endpoint that's only reachable from
inside the remote network - or you just want a quick request panel
without leaving the app.

Open it via **Tools → HTTP / SOAP request** in the pane header.

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

Connections, folders, credentials and credential folders accept a
custom icon - PNG, SVG, JPG, WebP, or GIF, up to 256 KB - or a built-in
icon with a colour. Set via the **Icon** field in the editor:

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
Devolutions RDM, OpenSSH config, MobaXterm, PuTTY / KiTTY, SuperPuTTY,
and ssh-tool archive. Every import is additive - existing rows are
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

### SuperPuTTY import

SuperPuTTY keeps its sessions in a `Sessions.xml` file (usually
`%APPDATA%\SuperPuTTY\Sessions.xml`). Load it in **Settings → Import →
SuperPuTTY**. SSH sessions become connections and the SessionId folder
tree is rebuilt; RDP / telnet / other protocols are counted and skipped.
SuperPuTTY stores no passwords, so attach credentials afterwards.

### kafka-tool import

The kafka-tool Kafka GUI keeps its cluster profiles in
`~/.config/kafka-tool/profiles.json`. Load it in **Settings → Import →
kafka-tool**. Every profile becomes a connection with [Kafka](#kafka)
enabled, in a `kafka-tool` folder: the SSH host (with its proxy jump) and
the bootstrap as that host sees it when the profile used a tunnel, else the
brokers directly with the Kafka tab as the default action. SSH passwords go
to the vault (unlock it first); key files are not read, so attach a key
credential afterwards - the import lists which connections need one.

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
- **Show session uptime in tab bar** - opt-in. Small "5m" / "2h" /
  "1d3h" indicator next to each connected tab's label showing how
  long the session has been up. Refreshes every 30 s.

### Appearance → Connection tree

- **Jump host marks** - Off / Bastions only (default) / Bastions and
  routed connections. See *Jump host marks* in section 3.
- **Color tag as row background** - opt-in. In addition to the
  3px left strip, tints the whole row with the connection /
  folder's colour tag (~14 % opacity, brighter on hover /
  selection).
- **Emphasise active session row** - opt-in. The tree row matching
  the currently focused terminal pane gets a bright cyan inset on
  the right edge so it stands out from other live (but not focused)
  connections.

### Appearance → Connection

- **Connect timeout (seconds)** - applies to both TCP dial and SSH
  handshake on every hop, and to opening a channel through a bastion.
  Default 20s; raise for slow / unreliable links, lower to fail fast.
  Saved as `connect_timeout_seconds`.
- **Auto-reconnect** - number of attempts (default 5) and the longest
  wait between them (default 16 s); the wait doubles from 1 s.
- **Connect all** - handshakes at once (default 4, at most 10), pause
  between starts (default 150 ms, at least 50) and the host count above
  which it asks first (default 5). The limits exist because every
  handshake in progress counts against a bastion's sshd `MaxStartups`
  (default 10); past it new ones are dropped as "handshake failed:
  EOF".
- **Shared bastion** - how long the shared connection to a jump host
  stays open after the last session through it closes (default 10 s,
  0 closes it at once).
- **In-app local shell** - which shell the top-bar **Local
  shell** button opens on plain click. Auto + per-platform list
  (Windows: WSL / PowerShell / cmd; macOS: zsh / bash; Linux:
  bash / zsh / sh). The dropdown chevron next to the button
  still lets you launch any of the others one-off, and contains
  a "Default for plain click" selector that mirrors this
  setting. The button label updates to show the current default
  (e.g. `Local: WSL`). Saved as `local_shell_kind`.
- **Start in directory** - where a local shell opens. Empty means your
  home directory, which is what every other terminal on the system
  does. A saved local-shell connection can override it with its own
  directory, in the connection editor. For **WSL** this takes a Linux
  path (`/srv/app`); a Windows path works too and is mapped to
  `/mnt/...` the way WSL always does. Saved as `local_shell_dir`.
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
- **Tray icon** - a click shows or hides the window. The right-click
  menu has **Show window**; **VPN profiles** and **Tunnels**, each
  ticked while it runs, where a click starts or stops it without
  opening the window (a failure comes back as a notification);
  **Favorites** and **Recent** (the last 10 connected, favorites left
  out - the same lists as Quick access above the tree), where a click
  opens the window and connects like a double-click in the tree;
  **Lock vault**; and **Quit**, which asks
  first when sessions are open, the same as closing the window. Empty
  submenus are left out.
- **Startup → Reopen last session** - **Ask** (default) offers to
  reconnect the tabs that were open at the last quit once the vault
  unlocks, **Always** does it without asking, **Never** starts empty
  (the last session is still remembered). SSH connections, inventory
  hosts and local shells come back with their titles, tab groups and
  split panes; quick connects and VNC consoles do not. A pane that
  cannot reconnect is dropped and its split closed.

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

### Security → Sync

Personal multi-machine sync. The whole profile travels: connections,
folders, credentials, the vault, custom icons, settings, snippets,
workspaces. Pick a **Transport** at the top of the section, set the
**sync passphrase** once (it applies to every backend), then use
**Status**, **Manual sync**, and **Automatic** below.

**Transports.** Five backends, all interchangeable - the snapshot and
the push/pull logic are identical, only where it lands differs:

- **WebDAV** - any server you control (Nextcloud, Apache mod_dav,
  `rclone serve webdav`, ...). URL, username, password. HTTPS required
  (plain http only for localhost).
- **SSH / SFTP** - a directory on any SSH server; auth reuses a vault
  credential or a key/password you type in.
- **Dropbox**, **OneDrive**, **Google Drive** - your own cloud
  account. See [Cloud sync](#cloud-sync) below.

**Encrypted before upload.** The snapshot is the same sealed envelope
backups use (argon2id + XChaCha20-Poly1305), locked with a **sync
passphrase** independent of the vault passphrase. The remote only ever
stores ciphertext plus a tiny meta file with a version counter - a
compromised host learns nothing.

**Sync passphrase - one place.** The passphrase seals the snapshot on
whichever backend is active, so it is a single field at the top of the
section, not per-backend. It lives in this machine's vault (sync needs
an unlocked vault). Use the same one on every machine. Changing an
existing passphrase asks for confirmation: it invalidates pulls of any
snapshot sealed with the old value until you push a fresh one over it.

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

**New machine setup**: fill in the transport, hit Pull (applies live),
and if prompted, unlock once with the vault passphrase from the source
machine. Push and pull are recorded in the audit log.

<a id="cloud-sync"></a>

#### Cloud sync (Dropbox, OneDrive, Google Drive)

Sync to your own consumer cloud account instead of a server. Sign-in
is OAuth with PKCE and a loopback redirect: you click **Connect**, a
browser tab opens, you authorize, and a refresh token is stored in
this machine's vault. As with every backend the provider only ever
holds the sealed snapshot - it never sees your connections or secrets.

You supply your **own app credentials** (a one-time setup per
provider), so the sync runs entirely through your own cloud account -
there is no shared ssh-tool app and no shared rate limit. Each panel
shows the exact **redirect URI** to register, with a Copy button.

- **Dropbox.** In the [Dropbox App Console](https://www.dropbox.com/developers/apps),
  create an app with **Scoped access** and **App folder** access.
  Under Permissions enable `files.content.read` and
  `files.content.write` (then Submit). Under Settings - OAuth 2, keep
  **Allow public clients (PKCE)** on and add the redirect URI the
  panel shows. Paste the **App key**. The sync folder inside the app
  folder is configurable.
- **OneDrive.** In the Azure Portal (Entra ID) register an app,
  add a **Mobile and desktop applications** platform with the redirect
  URI the panel shows, and grant the `Files.ReadWrite.AppFolder` +
  `offline_access` delegated permissions. Paste the **Application
  (client) ID**. Pick an **account type** - personal (`/consumers`),
  work / school (`/organizations`), or either (`/common`) - to match
  your account and the app's "Supported account types". A business-only
  OneDrive works with the work / school option.
  > OneDrive is not yet verified against a live account. It shares the
  > tested Dropbox / Google sign-in and transport plumbing, but treat
  > it as beta.
- **Google Drive.** In the Google Cloud Console create an OAuth client
  of type **Desktop app**, and add the `drive.appdata` scope on the
  OAuth consent screen. Paste the **client ID** and **client secret**.
  Google requires a secret even for a Desktop app - it is not truly
  confidential for an installed app, but is kept in this machine's
  vault. Google accepts any loopback port, so no redirect URI needs
  registering.

Each backend tracks its own version counter, so switching backends
never triggers a false "this machine is ahead". To connect a fresh
machine to the same cloud account, register the same app credentials
there and Connect; the first Pull brings the profile down.

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
| **F2** | Rename the selected connection or folder in the tree, in place |
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

Everything lives in the app's data folder: `%APPDATA%\ssh-tool`
(Windows) or `$XDG_DATA_HOME/ssh-tool`, by default
`~/.local/share/ssh-tool` (Linux).

- **SQLite database** (`store.db`) - connections, folders,
  credential metadata, network profiles, port forwards, known hosts,
  icons, settings, snippets, workspaces. The schema migrates itself on
  start; Settings -> About shows its version.
- **File vault** (`vault.enc`) - encrypted credential secrets,
  per-connection passwords, client certificates.
- **Auto-unlock sidecar** (`vault.enc.local.key`) - optional, machine-
  bound (DPAPI on Windows, machine id on Linux); no OS keychain is used.
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
- Packaging (.AppImage / .msi / NSIS)
- Android: in-app auto-update, signed release build / Play / F-Droid,
  android in CI (local sideload build only for now)
- iOS build (shares build tags with android, not produced)
- Onboarding tour, i18n, full a11y audit

See `TODO.md` for the full backlog.
