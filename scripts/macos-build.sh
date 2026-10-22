#!/usr/bin/env bash
# Build ssh-tool and, optionally, its VPN plugins from source on macOS, and
# install them. Asks before every step that changes something; Enter takes
# the default shown in brackets.
#
#   curl -fsSLO https://raw.githubusercontent.com/fpenezic/ssh-tool/main/scripts/macos-build.sh
#   bash macos-build.sh
#
# Written for the bash 3.2 that ships with macOS: no associative arrays,
# no ${var,,}, no mapfile. docs/macos.md describes the same steps by hand.
set -euo pipefail

APP_REPO="https://github.com/fpenezic/ssh-tool.git"
PLUGIN_REPO="https://github.com/fpenezic/ssh-tool-plugins.git"
PLUGINS="openconnect openvpn checkpoint netbird tailscale"
PLUGIN_DIR="$HOME/Library/Application Support/ssh-tool/plugins"

say()  { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
die()  { printf '\nerror: %s\n' "$*" >&2; exit 1; }

# Questions read from the terminal, so the script also works when piped.
ask() { # ask "Question" "default" -> answer on stdout
  local q="$1" def="${2:-}" ans
  if [ -n "$def" ]; then printf '%s [%s]: ' "$q" "$def" >/dev/tty; else printf '%s: ' "$q" >/dev/tty; fi
  IFS= read -r ans </dev/tty || ans=""
  printf '%s' "${ans:-$def}"
}
yes_no() { # yes_no "Question" y|n -> exit status 0 for yes
  local def="$2" ans
  ans="$(ask "$1 (y/n)" "$def")"
  case "$ans" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

[ "$(uname -s)" = Darwin ] || die "this script is for macOS"

# ---------------------------------------------------------------- tools
say "Checking the tools"

xcode-select -p >/dev/null 2>&1 || {
  info "The Xcode command line tools are missing (the app needs CGO for WebKit)."
  if yes_no "Install them now? macOS opens its own installer" y; then
    xcode-select --install || true
    die "finish the Xcode tools installer, then run this script again"
  fi
  die "the Xcode command line tools are required"
}
info "Xcode command line tools: ok"

have() { command -v "$1" >/dev/null 2>&1; }

# shellcheck disable=SC2016 # printed for the user, not run here
BREW_INSTALL_CMD='/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'

# Homebrew installed but not on PATH (a fresh install only adds it to new
# shells): /opt/homebrew on Apple Silicon, /usr/local on Intel.
load_brew() {
  local b
  for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do
    if [ -x "$b" ]; then eval "$("$b" shellenv)"; return 0; fi
  done
  return 1
}

# Go and Node come from Homebrew; without it, offer the official installer
# or say how to get the tools by hand.
ensure_brew() { # ensure_brew <what is missing>
  have brew && return 0
  load_brew && return 0
  info "$1 is missing, and Homebrew (which installs it) is not installed."
  if yes_no "Install Homebrew now? It runs the official installer and asks for your password" y; then
    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)" </dev/tty \
      || die "the Homebrew installer failed"
    load_brew || die "Homebrew was installed but brew was not found; open a new terminal and run this script again"
    return 0
  fi
  printf '\nInstall the tools one of these ways, then run this script again:\n' >&2
  printf '  - Homebrew:  %s\n' "$BREW_INSTALL_CMD" >&2
  printf '               then: brew install go node\n' >&2
  printf '  - by hand:   Go from https://go.dev/dl/ and Node.js LTS from https://nodejs.org/\n' >&2
  printf '               (the macOS .pkg installers; open a new terminal afterwards)\n' >&2
  exit 1
}

brew_install() { # brew_install <formula> <what>
  ensure_brew "$2"
  if yes_no "$2 is missing. Install it with Homebrew (brew install $1)?" y; then
    brew install "$1"
  else
    die "$2 is required - install it and run this script again"
  fi
}

have git || die "git is missing (it comes with the Xcode command line tools)"
have go || brew_install go Go
have node || brew_install node "Node.js 20 or newer"
node_major="$(node -v | sed 's/^v//; s/\..*//')"
[ "$node_major" -ge 20 ] || die "Node.js 20 or newer is required (found $(node -v))"
info "Go $(go env GOVERSION), Node $(node -v): ok"

# go install puts tools in GOBIN when it is set, else GOPATH/bin.
GOBIN_DIR="$(go env GOBIN)"
[ -n "$GOBIN_DIR" ] || GOBIN_DIR="$(go env GOPATH)/bin"
case ":$PATH:" in *":$GOBIN_DIR:"*) ;; *) export PATH="$GOBIN_DIR:$PATH" ;; esac

# ---------------------------------------------------------------- source
say "Source"
SRC="$(ask "Where to keep the source" "$HOME/ssh-tool-src")"
mkdir -p "$SRC"
APP_SRC="$SRC/ssh-tool"

latest_stable() { # newest vX.Y.Z tag of a remote, release candidates left out
  git ls-remote --tags --refs "$1" 'v*' \
    | sed 's#.*refs/tags/##' \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
    | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1 | sed 's/^/v/'
}
LATEST="$(latest_stable "$APP_REPO")"
[ -n "$LATEST" ] || die "could not read the release tags from $APP_REPO"
info "The latest release is $LATEST. 'main' is the work in progress."
REF="$(ask "Version to build" "$LATEST")"

if [ -d "$APP_SRC/.git" ]; then
  info "Updating $APP_SRC"
  git -C "$APP_SRC" fetch --quiet --tags origin
else
  info "Cloning into $APP_SRC"
  git clone --quiet "$APP_REPO" "$APP_SRC"
fi
if [ "$REF" = main ]; then
  git -C "$APP_SRC" checkout --quiet main
  git -C "$APP_SRC" pull --quiet --ff-only origin main
else
  git -C "$APP_SRC" -c advice.detachedHead=false checkout --quiet "$REF"
fi

# wails3 and task at the versions the build expects.
WAILS_VERSION="$(awk '$1 == "github.com/wailsapp/wails/v3" { print $2; exit }' "$APP_SRC/go.mod")"
[ -n "$WAILS_VERSION" ] || die "could not read the Wails version from go.mod"
if ! have wails3 || ! wails3 version 2>/dev/null | grep -q -- "$WAILS_VERSION"; then
  info "Installing the wails3 CLI $WAILS_VERSION"
  go install "github.com/wailsapp/wails/v3/cmd/wails3@$WAILS_VERSION"
fi
if ! have task; then
  info "Installing Task"
  go install github.com/go-task/task/v3/cmd/task@latest
fi

# ---------------------------------------------------------------- app
say "Building ssh-tool $REF (this takes a few minutes the first time)"
(cd "$APP_SRC" && task darwin:build)
APP_BUNDLE="$APP_SRC/bin/ssh-tool.app"
[ -d "$APP_BUNDLE" ] || die "the build did not produce $APP_BUNDLE"

APP_DEST=""
if yes_no "Install ssh-tool.app into /Applications?" y; then
  APP_DEST="/Applications"
  [ -w "$APP_DEST" ] || { info "/Applications is not writable for you; using ~/Applications"; APP_DEST="$HOME/Applications"; }
  mkdir -p "$APP_DEST"
  if pgrep -xq ssh-tool; then
    yes_no "ssh-tool is running. Quit it first, then answer y to replace it" y || die "stopped before replacing the app"
  fi
  # Copy next to the installed app first and swap only once the copy is
  # complete: a failed copy leaves the working install in place.
  rm -rf "$APP_DEST/.ssh-tool.app.new"
  # A .old left by an interrupted run is the only copy when no app is
  # installed; keep it then.
  if [ -e "$APP_DEST/ssh-tool.app" ]; then rm -rf "$APP_DEST/.ssh-tool.app.old"; fi
  cp -R "$APP_BUNDLE" "$APP_DEST/.ssh-tool.app.new" || die "copying the app failed; the installed app is unchanged"
  if [ -e "$APP_DEST/ssh-tool.app" ] && ! mv "$APP_DEST/ssh-tool.app" "$APP_DEST/.ssh-tool.app.old"; then
    rm -rf "$APP_DEST/.ssh-tool.app.new"
    die "could not move the installed app aside; it is unchanged"
  fi
  if mv "$APP_DEST/.ssh-tool.app.new" "$APP_DEST/ssh-tool.app"; then
    rm -rf "$APP_DEST/.ssh-tool.app.old"
  else
    if [ ! -e "$APP_DEST/.ssh-tool.app.old" ]; then
      die "installing the app failed"
    elif mv "$APP_DEST/.ssh-tool.app.old" "$APP_DEST/ssh-tool.app"; then
      die "installing the app failed; the previous app was put back"
    else
      die "installing the app failed and the previous app could not be put back: it is at $APP_DEST/.ssh-tool.app.old"
    fi
  fi
  info "Installed $APP_DEST/ssh-tool.app"
else
  info "Left at $APP_BUNDLE"
fi

# ---------------------------------------------------------------- plugins
say "VPN plugins"
info "WireGuard is built in. These need a plugin each:"
info "  openconnect (AnyConnect, Fortinet, F5, GlobalProtect, Pulse), openvpn,"
info "  checkpoint, netbird, tailscale"
CHOSEN=""
for p in $PLUGINS; do
  def=n
  [ -x "$PLUGIN_DIR/ssh-tool-$p" ] && def=y # already installed: offer the update
  if yes_no "Build the $p plugin?" "$def"; then CHOSEN="$CHOSEN $p"; fi
done

BUILT=""
if [ -n "$CHOSEN" ]; then
  PLUGIN_SRC="$SRC/ssh-tool-plugins"
  if [ -d "$PLUGIN_SRC/.git" ]; then
    git -C "$PLUGIN_SRC" fetch --quiet --tags origin
  else
    git clone --quiet "$PLUGIN_REPO" "$PLUGIN_SRC"
  fi
  mkdir -p "$PLUGIN_DIR"
  for p in $CHOSEN; do
    # Each plugin is released on its own <plugin>-vN[.P] tags.
    tag="$(git -C "$PLUGIN_SRC" tag -l "$p-v*" --sort=-v:refname | head -1)"
    [ -n "$tag" ] || { info "$p: no release tag found, skipped"; continue; }
    say "Building the $p plugin ($tag)"
    git -C "$PLUGIN_SRC" -c advice.detachedHead=false checkout --quiet "$tag"
    (
      cd "$PLUGIN_SRC/$p"
      extra=""
      if [ "$p" = netbird ]; then
        # NetBird needs CGO; the Xcode tools provide the compiler. The
        # embedded version stamp makes the management console show it.
        export CGO_ENABLED=1
        nbver="$(go list -m -f '{{.Version}}' github.com/netbirdio/netbird | sed 's/^v//')"
        extra="-X github.com/netbirdio/netbird/version.version=$nbver"
      else
        export CGO_ENABLED=0
      fi
      go build -trimpath -ldflags="-w -s -X main.version=$tag $extra" -o "ssh-tool-$p" .
    ) || { info "$p: the build failed, skipped"; continue; }
    # Stage inside the plugins folder, then rename: the rename is atomic
    # there, so a failed copy never leaves the plugin missing.
    staged="$PLUGIN_DIR/.ssh-tool-$p.new"
    if ! { cp "$PLUGIN_SRC/$p/ssh-tool-$p" "$staged" && chmod +x "$staged"; }; then
      rm -f "$staged"
      info "$p: copying into the plugins folder failed, the installed one is unchanged"
      continue
    fi
    mv -f "$staged" "$PLUGIN_DIR/ssh-tool-$p"
    rm -f "$PLUGIN_SRC/$p/ssh-tool-$p"
    BUILT="$BUILT $p"
  done
fi

# ---------------------------------------------------------------- done
say "Done"
if [ -n "$APP_DEST" ]; then info "ssh-tool $REF: $APP_DEST/ssh-tool.app"; else info "ssh-tool $REF: $APP_BUNDLE"; fi
if [ -n "$BUILT" ]; then
  info "Plugins in $PLUGIN_DIR:$BUILT"
  info "Settings - Network profiles - General lists them (reopen Settings if it was open)."
fi
info "To update later, run this script again: it reuses $SRC."
