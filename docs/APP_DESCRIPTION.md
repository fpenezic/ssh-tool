# ssh-tool - app description

Reusable copy for README / website / store listings / release notes.
Pick the length that fits the surface.

---

## One-liner (200 chars or less)

> SSH manager for sysadmins with hundreds of hosts: inherited folder tree, encrypted vault, split-pane terminal, VPN profiles, dynamic inventory, Kafka and Database tabs, RDM import.

---

## Short (about 100 words)

**ssh-tool** is a desktop SSH connection manager built for sysadmins who maintain hundreds of hosts. Cross-platform (Wails v3 + Svelte 5), a single binary, native multi-window with tab detach / redock, and an Android app on the same core.

A tree of connections with folder-level inherited settings, an encrypted credential vault (Argon2id + XChaCha20-Poly1305) that can also read KeePass, Bitwarden / Vaultwarden and Infisical, a multi-tab terminal with split panes, SFTP, port forwards, opkssh certificate auth, userspace VPN profiles (WireGuard built in, NetBird, Tailscale, OpenConnect, OpenVPN and Check Point as plugins), dynamic inventory from eight providers, and Kafka, Database and VNC tabs that ride the connection's SSH session.

---

## Medium (about 250 words)

**ssh-tool** is a desktop SSH connection manager built for sysadmins who maintain hundreds of hosts. Cross-platform (Wails v3 + Svelte 5), a single binary, native multi-window with tab detach / redock; Android on the same Go core.

**Connection tree** - folder hierarchy with inherited settings (username, port, credential, jump host chain, network profile, colour tag, auto-reconnect, keepalive). Tags, a fast filter, a fuzzy palette, multi-select with a tri-state batch editor.

**Credentials** - Argon2id + XChaCha20-Poly1305 vault with an optional machine-bound auto-unlock and idle auto-lock; password, key, agent and opkssh kinds; secrets read from KeePass, Bitwarden / Vaultwarden or Infisical at connect time, and TOTP codes answering SSH and VPN prompts.

**Terminal** - xterm.js, multi-tab with split panes, detachable windows, broadcast input, session recording, command timestamps, SFTP with drag-and-drop.

**Networking** - local / remote / SOCKS5 forwards with an isolated-browser launcher; userspace VPN profiles (no TUN adapter, no admin rights, several at once); an app proxy and a GitLab CI runner that reach customer networks through them.

**Kafka, Database, VNC** - per-connection tabs riding the SSH session: Kafka topics, consumer (last N per partition or of the whole topic), produce and groups; PostgreSQL, MySQL, Oracle, SQL Server, Db2, ClickHouse, OpenSearch / Elasticsearch and Redis with read-only guards, a spreadsheet-like grid (in-place editing in a table's Data view) and whole-table export; a noVNC console.

**Dynamic inventory** - Proxmox VE, Hetzner, DigitalOcean, Linode, Vultr, Scaleway, AWS EC2 and Ansible inventories as auto-populated folders.

**Import / sync** - Devolutions RDM, ssh_config, MobaXterm, PuTTY / KiTTY, SuperPuTTY and kafka-tool imports; encrypted profile sync over WebDAV, SFTP, Dropbox, OneDrive or Google Drive; encrypted backups.

---

## Long-form bullet list (release-notes / marketing page)

### Built for daily use at scale

- **300+ connections** - tag filter, fuzzy palette (Ctrl+K),
  tree keyboard navigation, configurable density.
- **Folder inheritance** - set username / port / credential / jump
  host / colour on a folder, every connection inside picks it up
  unless overridden.
- **Multi-select + batch editor** - tri-state per field
  (leave / inherit / set). Connect-all, run-command-on-all, move-to,
  bulk delete.

### Secure credential vault

- **Argon2id + XChaCha20-Poly1305** at rest.
- **Optional machine-bound auto-unlock**, **idle auto-lock**,
  **password strength meter** on create / rotate.
- **Per-connection password override** stored separately for
  one-off cases where the credential doesn't apply.
- **opkssh** native (no external binary) certificate flow.

### Multi-window, multi-tab, multi-pane

- **Native multi-window** with drag-out detach + drag-back redock.
- **Binary pane splits** inside a tab (right / down / close).
- **Broadcast input** across selected sessions, shared between
  windows. Multiple named groups in parallel - a session can sit
  in several groups at once and keystrokes from a member fan out
  to the union; default group preserves the legacy single-set
  flow.
- **Workspaces** - named tab bundles you can switch between with
  one click; tabs carry optional group chips that round-trip
  through save/restore.

### Dynamic inventory

- **Folders that auto-populate** from external sources: Proxmox VE
  (whole-cluster via `/cluster/resources`), Hetzner Cloud,
  DigitalOcean, Linode, Vultr, Scaleway, AWS EC2, and Ansible
  static inventory (`.ini` / `.yml` files). Each entry inherits
  the folder's credentials + jump host + SSH options.
- **Ansible-specific lifts** - `ansible_user`, `ansible_port`,
  `ansible_host` map to per-host SSH overrides. Jump chain comes
  from `ansible_ssh_common_args` / `_extra_args` (`-J`,
  `ProxyJump=`, `ProxyCommand=ssh -W` all recognised).
  Per-folder jump host credential picker handles the bastion
  credential that Ansible inventories don't carry. Per-connect
  override for jump host + jump credential too.
- **Detail pane on click** - name, status, provider chip, facts
  grid (resource type / node / VMID / vCPUs / RAM / disk / uptime
  for Proxmox; cloud-specific IPs and regions; "Jump via" line
  for Ansible), tags or labels, live CPU/RAM/disk bars (Proxmox),
  collapsible raw provider payload.
- **Kind-aware icons** - VM uses a Monitor glyph, LXC a Box,
  plain host a Server tower. Consistent across tree, palette,
  detail header.
- **Pin one host as a permanent connection** - single click in
  the detail pane promotes a dynamic host into a real
  connection (with overrides, port forwards, notes), and the
  refresh path skips its external ID so the host doesn't
  appear twice. Deleting the pinned connection unpins.
- **Convert whole folder to static** - snapshots every host
  into regular connections and drops the provider link, for
  one-off inventories or when retiring an API token.
- **API tokens in the vault** - never touch disk in plaintext.
- **Visual cues** - globe icon, teal name tint, provider pill,
  red error dot when last refresh failed.

### SSH features

- Password / private key / agent / **opkssh certificate** auth.
- Per-hop credentials in jump chains.
- TOFU known-hosts with trust-once / trust-and-remember prompts.
- Auto-reconnect with exponential backoff.
- Configurable connect timeout + keepalive.

### Power tools

- **Live tcpdump panel** with smart sudo (auto-feeds the
  connection's saved password when relevant; prompts only when
  necessary), BPF filter, client-side row filter.
- **HTTP / SOAP request modal** routed through active SOCKS5
  forwards - hit endpoints inside the remote network without
  leaving the app.
- **Batch command exec** across a multi-selection, parallel,
  with aggregated output and save-as-snippet.
- **Snippet library** (Ctrl+Shift+P) for command bodies you fire
  into the active terminal.
- **Markdown notes** per connection (mini-runbook).

### Sharing a session

- **Share a live session to a plain web browser** - a colleague opens
  a link and watches your terminal, or (with your explicit approval)
  types into it. You pick the tabs, the network interface, and
  read-only vs full control; you approve every guest and compare a
  short word-code with them so a leaked link is useless. Encrypted
  over a self-signed cert, no cloud relay (LAN or your own VPN).
  Splits, tab switches and added tabs follow through live.
- **Share a session with an external LLM (MCP)** - a local-only bridge
  lets an MCP client (Claude Code, LM Studio) read the terminal, pull
  logs, and propose commands. Off by default, per-session grants,
  read-only auto-runs and state-changing commands gated on approval;
  terminal output is treated as untrusted and every action is logged.

### VPN profiles and the app proxy

- **Userspace VPN profiles** - WireGuard built in; NetBird, Tailscale, OpenConnect (AnyConnect, Fortinet, F5, GlobalProtect, Pulse), OpenVPN and Check Point as optional plugins. No TUN adapter, no admin rights, several gateways at once; a connection's first hop and provider APIs dial through its profile.
- **App proxy** - one local SOCKS5 proxy (with a password) for other programs, reaching every marked profile, routing by networks and by the VPN's DNS domains (pushed by the gateway or added by hand), so `db1.prod.example.com` resolves on the far side for a client that passes the name to the proxy (`socks5h://`, `curl --socks5-hostname`).
- **CI runner** - a GitLab runner whose jobs reach customer networks through the profiles and jump hosts.

### Secrets, sync and backup

- **External secrets** - KeePass, Bitwarden / Vaultwarden and Infisical read at connect time; TOTP codes from Bitwarden or KeePass items answer SSH and VPN prompts.
- **Profile sync** - the whole profile, sealed with its own passphrase, over WebDAV, SFTP, Dropbox, OneDrive or Google Drive; pulls apply live.
- **Encrypted backups** - manual or daily, verified on restore.

### Android

- The same Go core as the desktop app, with biometric vault unlock and sessions kept alive in the background; a signed APK with every release.

### File transfer + forwarding

- **SFTP browser** per session with native OS drag-and-drop upload.
- **Port forwards** - local / remote / SOCKS5 dynamic with byte
  counters and an **isolated-browser launcher** (Chromium / Firefox
  families, temporary profile, your main browser stays clean).
- **Tunnel connections** - connections that run without a shell or
  tab (`ssh -N`), start with the app, reconnect after a drop and are
  managed from a Tunnels panel in the status bar.
- **Kafka** - a connection with Kafka enabled opens a Kafka tab: topics,
  a consumer that never commits, produce, topic config and metrics,
  consumer groups with lag and offset reset. The consumer starts from the last N records (offsets) of every partition or of the whole topic, a time, an offset, or new records only. Broker connections ride the
  connection's SSH session, so advertised broker names resolve on the far
  side - no local tunnels, no hosts-file entries.
- **Databases** - a connection with the database enabled opens a Database tab on PostgreSQL, MySQL / MariaDB, Oracle, SQL Server, IBM Db2, ClickHouse, OpenSearch / Elasticsearch or Redis: databases, tables and their structure, query tabs with their own sessions, and a results grid that works like a spreadsheet (edit a table's cells in place in its Data view, select and copy blocks straight into Excel or Teams, right-click menus) with whole-table CSV / JSON export. Foreign keys can be followed both ways from a row, a schema has an ER diagram, filters and sorting run on the server, edited cells become an UPDATE script to read and run, Explain shows the plan (PostgreSQL, MySQL), a Sessions view stops a statement or a session, and a query timeout stops long statements. A folder can list the databases (prod, stage) every host under it has. Connections ride the connection's SSH session, so a server on the host's loopback needs no port forward; read-only mode is enforced in the backend.

### Import / export

- Devolutions **RDM JSON** import (folders, connections, jump chains, icons), plus MobaXterm `.mxtsessions`, PuTTY / KiTTY `.reg`, SuperPuTTY `Sessions.xml` and kafka-tool profiles.
- **ssh_config** import (ProxyJump chains; key paths recorded in
  notes - we never auto-copy private keys off disk).
- **Encrypted archive** export (Argon2id + XChaCha20) for backup /
  team distribution. Credentials excluded by default.

### Observability

- In-app live log tail (Settings > Logs) plus a 5 MiB rolling log file on disk.
- **Liveness probe** - reachability dots on the rows of open folders.
- Local **audit log** with insights (connects, time connected, top hosts).
- Friendly connect-error messages with raw error behind a toggle.
- Live "TCP dial / SSH handshake / opening shell" stage hint while
  a connect is in flight.

---

## Voice / tone notes

- **Audience**: sysadmins / DevOps / network engineers managing
  fleets, not casual SSH users.
- **Tone**: factual, dense, no marketing fluff. The user already
  knows what SSH is and what RDM was.
- **Lead with the differentiators**: scale (300+ connections),
  multi-window, batch tools, RDM replacement story.
- **Don't oversell**: features that are deferred (smart autocomplete, desktop biometric unlock, FIDO2 keys) stay off the page.
