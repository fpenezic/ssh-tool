# Prerequisites

What to install for hacking on ssh-tool.

## Toolchain

```bash
# Go 1.26
# https://go.dev/dl - use the official installer or rely on
# your distro's package manager if it ships 1.26+
go version    # → go1.26.x

# Node 20 LTS
# https://nodejs.org or use fnm/nvm

# Wails CLI v3
go install -v github.com/wailsapp/wails/v3/cmd/wails3@latest
wails3 --version
```

Add `$HOME/go/bin` to `PATH` so `wails3` and `task` resolve.

```bash
# Task (build runner, see Taskfile.yml)
go install github.com/go-task/task/v3/cmd/task@latest
task --version
```

## Per-OS system packages

### Linux (Ubuntu / Debian / WSL2)

```bash
sudo apt update
sudo apt install -y \
    build-essential \
    pkg-config \
    libgtk-4-dev \
    libwebkit2gtk-4.1-dev
```

For machine-bound vault auto-unlock to work natively on Linux you
need a running Secret Service implementation
(gnome-keyring-daemon or kwallet). On headless WSL it's easiest to
disable auto-unlock and use the passphrase on every launch.

### macOS

```bash
xcode-select --install
```

### Windows

- Visual Studio Build Tools 2022 with the "Desktop development
  with C++" workload (needed for ConPTY headers).
- WebView2 runtime - already installed on Windows 11; manual
  install on older Windows 10.
- Go from `https://go.dev/dl` and Node 20 LTS.

## First run

Clone, then:

```bash
cd frontend && npm install && cd ..
go mod download
wails3 generate bindings ./...     # populates frontend/bindings/
```

For native Linux dev:

```bash
# Terminal 1
cd frontend && npm run dev

# Terminal 2 (env workarounds required on WSL - see below)
go run .
```

WSL/WebKit env workarounds. Without these the webview either blanks
or hangs at startup:

```bash
export GDK_BACKEND=x11
export WEBKIT_DISABLE_DMABUF_RENDERER=1
export WEBKIT_DISABLE_COMPOSITING_MODE=1
export LIBGL_ALWAYS_SOFTWARE=1
export FRONTEND_DEVSERVER_URL=http://localhost:5173
```

Native Windows build doesn't hit these.

## Cross-build from WSL → Windows

```bash
CGO_ENABLED=0 task windows:build    # → bin/ssh-tool.exe
```

Copy the resulting `bin/ssh-tool.exe` to the Windows host via
`\\wsl$\Ubuntu\...` and run natively. CGO is disabled because
`modernc.org/sqlite` is pure Go - no cross-compile toolchain
dance needed.

## Linux native build

```bash
task linux:build                    # → bin/ssh-tool
```

## Release flow

```bash
git tag -a v0.X.Y -m "v0.X.Y - short description"
git push origin HEAD v0.X.Y
# releases are built + published by GitHub Actions on tag push
```

## Verifying tooling

```bash
go version
node --version
wails3 --version
task --version
git config user.email     # confirm it matches what you expect
```

## Recommended editor extensions

- VS Code: gopls, Svelte for VS Code, Vue/Svelte language tools.
- The repo has no `.vscode/` checked in - bring your own settings.
