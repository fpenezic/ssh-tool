# ssh-tool

Daily-driver SSH manager for sysadmins with 300+ hosts: folder-
inherited tree, encrypted vault, multi-tab split-pane terminal,
native multi-window, SOCKS5 forwards, batch exec, RDM import.

Built on Wails v3 (alpha) + Svelte 5 + Go. Single binary, cross-
platform (Windows / Linux native; macOS scaffolding exists but
test status varies).

> Looking for a longer pitch? See [`docs/APP_DESCRIPTION.md`](docs/APP_DESCRIPTION.md).

## Highlights

- **Connection tree** with folder-level inherited settings, tags,
  multi-select batch editor.
- **Encrypted credential vault** - Argon2id + XChaCha20-Poly1305,
  optional OS keychain auto-unlock, idle auto-lock, password
  strength meter.
- **Multi-window, multi-tab, multi-pane** terminal with native
  detach / redock and broadcast input across windows.
- **Workspaces** - named tab bundles you switch between with one
  click; tabs carry group chips.
- **Dynamic inventory** - auto-populated folders from Proxmox VE,
  Hetzner Cloud, DigitalOcean, Linode, Vultr, Scaleway, AWS EC2 and
  Ansible static inventory, with tag/label filters, hide-stopped,
  live load bars + Raw payload in the detail pane.
- **Power tools** - live tcpdump panel (smart sudo), HTTP/SOAP
  request modal over SOCKS5, parallel batch command exec across a
  multi-selection, snippet library (Ctrl+Shift+P), markdown notes.
- **opkssh** native (no external binary) certificate flow.
- **SFTP** browser with native OS drag-and-drop upload.
- **Port forwards** - local / remote / SOCKS5 dynamic with
  isolated-browser launcher.
- **Imports** from Devolutions RDM JSON and ssh_config; encrypted
  archive export.

## Documentation

- [User guide](docs/USER_GUIDE.md) - every shipped feature, indexed.
- [App description](docs/APP_DESCRIPTION.md) - copy for
  README / web / store at four lengths.
- [TODO / backlog](docs/TODO.md) - open items grouped by area.
- [CHANGELOG](CHANGELOG.md) - release notes.
- [Architecture](docs/02-architecture.md), [data model](docs/03-data-model.md),
  [security](docs/06-security.md) - design references.
- [Gotchas](docs/gotchas.md) - implementation traps archive
  (subsystem-grouped).

## Build

### Requirements

- Go 1.26+
- Node 20+
- [`wails3` CLI](https://wails.io) (alpha) - `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111`
- [Task](https://taskfile.dev) - `go install github.com/go-task/task/v3/cmd/task@latest`

### Build for your platform

```bash
task build
```

Output lands in `bin/`. On Windows the binary is `ssh-tool.exe`.

### Cross-build Windows from Linux

```bash
CGO_ENABLED=0 task windows:build
```

### Dev mode (hot reload)

```bash
# Terminal 1
cd frontend && npm install && npm run dev

# Terminal 2
FRONTEND_DEVSERVER_URL=http://localhost:5173 go run .
```

WSL? Add the GTK4 workarounds (see `CLAUDE.md`):

```bash
GDK_BACKEND=x11 WEBKIT_DISABLE_DMABUF_RENDERER=1 \
  WEBKIT_DISABLE_COMPOSITING_MODE=1 LIBGL_ALWAYS_SOFTWARE=1 \
  FRONTEND_DEVSERVER_URL=http://localhost:5173 go run .
```

## Project layout

```
ssh-tool/
├─ main.go            entrypoint (Wails v3 application.New)
├─ app.go             IPC service exposed to the frontend
├─ internal/
│  ├─ store/          SQLite + migrations + CRUD (+ audit.db)
│  ├─ creds/          vault crypto + lifecycle
│  ├─ ssh/            session, jump chain, forwards, tcpdump, batch
│  ├─ inventory/      dynamic providers (Proxmox, Hetzner, DO, AWS, ...)
│  ├─ httpc/          HTTP / SOAP request runner with SOCKS5 dialer
│  ├─ resolver/       inheritable-settings resolver (folder → conn)
│  ├─ local/          in-app local PTY (Win/Mac/Linux shells)
│  ├─ exporter/       encrypted archive export / import
│  └─ importer/       RDM JSON + ssh_config parsers
├─ frontend/          Svelte 5 + xterm.js
└─ docs/              user guide, app description, TODO, etc.
```

See [`CLAUDE.md`](CLAUDE.md) for an exhaustive handoff document
intended for new contributors (or future Claude Code instances).

## Status

Active development. Wails v3 is still alpha upstream, so expect
occasional breakage when bumping. The project started as a Rust +
Tauri app and was ported to Go - do **not** reintroduce russh-based
code; we moved to Go specifically because `russh`'s forked `ssh-key`
crate rejects opkssh "forever" certs (`valid_before=u64::MAX`) and
rewriting them breaks the CA signature.

Current release: see the latest `v*` tag on `main` and
[CHANGELOG.md](CHANGELOG.md). Builds are published at
[sshtool.app](https://sshtool.app).

## License

[Apache License 2.0](LICENSE). Copyright 2026 Filip Penezic.
