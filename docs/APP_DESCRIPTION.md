# ssh-tool - app description

Reusable copy for README / website / store listings / release notes.
Pick the length that fits the surface.

---

## One-liner (≤ 200 chars)

> Daily-driver SSH manager for sysadmins with 300+ hosts: folder-
> inherited tree, encrypted vault with password history + audit log,
> multi-tab split-pane terminal, native multi-window, dynamic
> inventory (Proxmox, Hetzner, DO, Linode, Vultr, Scaleway, EC2,
> Ansible), SOCKS5 forwards, batch exec, RDM import.

---

## Short (≈ 100 words)

**ssh-tool** is a desktop SSH connection manager built for sysadmins
who maintain hundreds of hosts. Cross-platform (Wails v3 + Svelte 5),
single binary, native multi-window with tab detach/redock.

Tree of connections with folder-level inherited settings, encrypted
credential vault (Argon2id + XChaCha20-Poly1305) with idle auto-lock,
multi-tab terminal with split panes, SFTP browser with native OS
drag-and-drop upload, port forwards (L / R / D-SOCKS) with isolated-
browser launcher, opkssh certificate auth, broadcast input across
sessions and windows, dynamic inventory folders that auto-populate
from Proxmox VE, Hetzner Cloud, DigitalOcean, Linode, Vultr,
Scaleway, AWS EC2, and Ansible static inventory files.

---

## Medium (≈ 250 words)

**ssh-tool** is a desktop SSH connection manager built for sysadmins
who maintain hundreds of hosts. Cross-platform (Wails v3 + Svelte 5),
single binary, native multi-window with tab detach/redock.

**Connection tree** - folder hierarchy with inherited settings
(username, port, credential, jump host chain, color tag, auto-
reconnect, keepalive). Tags + auto-facets for filtering. Multi-select
with tri-state batch editor.

**Credential vault** - Argon2id-derived key + XChaCha20-Poly1305
authenticated encryption, optional OS-keychain auto-unlock sidecar,
idle auto-lock. Password / private key / agent / opkssh kinds, with
custom icons and folder grouping.

**Terminal** - xterm.js with WebGL, multi-tab + binary pane splits,
detachable to a second OS window, broadcast input across selected
sessions (multi-window aware), per-session paste guard, configurable
themes / fonts / scrollback.

**SFTP** - file browser per session, native OS drag-and-drop upload
(files and directories).

**Port forwards** - local / remote / SOCKS5 dynamic, with an
isolated-browser launcher that uses a temporary profile so your
main browser stays clean.

**Dynamic inventory** - folders that auto-populate from external
sources. Proxmox VE (whole-cluster fetch via `/cluster/resources`,
tag filter, hide-stopped, live CPU/RAM/disk bars in the detail
pane) and Hetzner Cloud (paginated `/v1/servers`, hostname source
picker for name vs public/private IPv4, label filter). API tokens
stored in the vault.

**Power tools for sysadmins** - live tcpdump panel with smart sudo
(uses the connection's saved password automatically when possible),
HTTP / SOAP request modal routed through an active SOCKS5 forward,
parallel batch command exec across a multi-selection, command snippet
library (Ctrl+Shift+P), workspaces for one-click "open my client-A
production set", command-history-style markdown notes per host.

**Import / export** - Devolutions RDM JSON, ssh_config, encrypted
archive bundle. Exports never include credential secrets by default.

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
- **Optional OS keychain auto-unlock**, **idle auto-lock**,
  **password strength meter** on create / rotate.
- **Per-connection password override** stored separately for
  one-off cases where the credential doesn't apply.
- **opkssh** native (no external binary) certificate flow.

### Multi-window, multi-tab, multi-pane

- **Native multi-window** with drag-out detach + drag-back redock
  (Wails v3 alpha).
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

### File transfer + forwarding

- **SFTP browser** per session with native OS drag-and-drop upload.
- **Port forwards** - local / remote / SOCKS5 dynamic with byte
  counters and an **isolated-browser launcher** (Chromium / Firefox
  families, temporary profile, your main browser stays clean).

### Import / export

- Devolutions **RDM JSON** import (folders, connections, jump
  chains, icons).
- **ssh_config** import (ProxyJump chains; key paths recorded in
  notes - we never auto-copy private keys off disk).
- **Encrypted archive** export (Argon2id + XChaCha20) for backup /
  team distribution. Credentials excluded by default.

### Observability

- In-app live log tail (Settings → Logs) plus a 5 MiB rolling log
  file on disk.
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
- **Don't oversell**: features that are deferred (health probe,
  smart autocomplete, biometric unlock) stay off the page.
