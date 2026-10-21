# ssh-tool on macOS

There is no macOS release build: a distributed macOS app needs an Apple
Developer ID and notarisation, which the project does not have. The app
and the VPN plugins build from source and run on macOS. This page covers
building both and where the plugins go.

The in-app updater and the plugin downloads in Settings do not cover
macOS: an update is a rebuild, and a plugin is built and copied by hand.

## Requirements

- Xcode command line tools: `xcode-select --install` (the app needs CGO
  for WebKit).
- Go, at the version in `go.mod` or newer: https://go.dev/dl/
- Node 20+.
- The `wails3` CLI at the version in `go.mod`:
  `go install github.com/wailsapp/wails/v3/cmd/wails3@<version>`
- Task: `go install github.com/go-task/task/v3/cmd/task@latest`

`~/go/bin` has to be on `PATH` for `wails3` and `task`.

## Build the app

```bash
git clone https://github.com/fpenezic/ssh-tool.git
cd ssh-tool
git checkout v0.X.Y            # a release tag; main is the work in progress
task darwin:build              # bin/ssh-tool.app (and the bare bin/ssh-tool)
task darwin:package            # optional: bin/ssh-tool.dmg
```

The build is for the Mac it runs on (Apple Silicon or Intel); add
`ARCH=amd64` or `ARCH=arm64` to build for the other one.

Copy `bin/ssh-tool.app` to `/Applications` and start it from there.
Settings - About shows the version the build was made from.

A build made on the same Mac starts as it is. Copied to another Mac
(AirDrop, a download, a shared drive), macOS marks it as downloaded and
refuses to open an unsigned app. Remove the mark once:

```bash
xattr -dr com.apple.quarantine /Applications/ssh-tool.app
```

To update: pull, check out the new tag, build again and replace the app.
Your data stays where it is (see below).

## Build the VPN plugins

The plugins (OpenConnect, OpenVPN, Check Point, NetBird, Tailscale) live
in a separate repository, one Go module per plugin. Build only the ones
you use:

```bash
git clone https://github.com/fpenezic/ssh-tool-plugins.git
cd ssh-tool-plugins
git checkout openconnect-v1.3  # the plugin's newest <plugin>-vN tag
cd openconnect
go build -trimpath -ldflags="-X main.version=openconnect-v1.3" -o ssh-tool-openconnect .
```

The binary must be named `ssh-tool-<plugin>`, with no extension:
`ssh-tool-openconnect`, `ssh-tool-openvpn`, `ssh-tool-checkpoint`,
`ssh-tool-netbird`, `ssh-tool-tailscale`. Each plugin has its own tags
(`openvpn-v1.1`, `checkpoint-v1.1`, ...); the `-X main.version=` part is
what Settings shows as the plugin's version and can be left out (it then
says `dev`). Every plugin builds the same way from its own folder.

## Where the plugins go

The app keeps its data in `~/Library/Application Support/ssh-tool`, and
looks for plugins in its `plugins` folder:

```bash
mkdir -p ~/Library/Application\ Support/ssh-tool/plugins
cp ssh-tool-openconnect ~/Library/Application\ Support/ssh-tool/plugins/
chmod +x ~/Library/Application\ Support/ssh-tool/plugins/ssh-tool-openconnect
```

Settings - Network profiles - General then lists the plugin as
installed, with its version and path (reopen Settings if it was open).
A profile of that kind connects through it from then on; nothing else
needs restarting.

A plugin binary copied from another machine carries the same quarantine
mark as the app and is refused when the tunnel starts:

```bash
xattr -d com.apple.quarantine ~/Library/Application\ Support/ssh-tool/plugins/ssh-tool-openconnect
```

A plugin built on the Mac itself does not need this, and needs no
signing: the Go linker signs it ad hoc.

The app also finds a plugin placed next to its own binary
(`ssh-tool.app/Contents/MacOS/`), but replacing the app removes it
there; the plugins folder survives updates.

## Sync with other machines

Profile sync works the same as on Windows and Linux. Network profiles of
every kind come across; a plugin-backed profile (OpenConnect, OpenVPN,
Check Point, NetBird, Tailscale) only connects on the Mac once its
plugin is in the plugins folder.
