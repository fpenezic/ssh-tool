#!/bin/sh
# Install the standalone ssh-tool binary into the user's own prefix.
#
# This is the middle ground between dropping the binary on the desktop
# and installing a distro package:
#
#   ~/.local/bin/ssh-tool                       on $PATH, user-owned
#   ~/.local/share/applications/ssh-tool.desktop launcher entry
#   ~/.local/share/icons/...                     the app icon
#
# No root, no package manager, and - because the user owns the binary -
# the in-app updater keeps working. A distro package gives you the same
# desktop integration plus automatic dependency handling, but hands
# updates to pacman/apt; pick whichever trade you prefer.
#
# Usage:  ./install-user.sh ./ssh-tool-linux-amd64
#         ./install-user.sh --uninstall

set -eu

# ~/.local/bin is fixed by the XDG user-dirs spec; the data root moves
# with XDG_DATA_HOME.
BIN_DIR="$HOME/.local/bin"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}"
APP_DIR="$DATA_DIR/applications"
ICON_DIR="$DATA_DIR/icons/hicolor"

if [ "${1:-}" = "--uninstall" ]; then
    # The ssh-tool.* names are from builds before the desktop entry was
    # renamed to match the Wayland app-id; remove both so an uninstall
    # after an upgrade leaves nothing behind.
    rm -f "$BIN_DIR/ssh-tool" \
          "$APP_DIR/org.wails.ssh-tool.desktop" \
          "$APP_DIR/ssh-tool.desktop" \
          "$ICON_DIR/128x128/apps/org.wails.ssh-tool.png" \
          "$ICON_DIR/scalable/apps/org.wails.ssh-tool.svg" \
          "$ICON_DIR/128x128/apps/ssh-tool.png" \
          "$ICON_DIR/scalable/apps/ssh-tool.svg"
    command -v update-desktop-database >/dev/null 2>&1 &&
        update-desktop-database -q "$APP_DIR" || true
    echo "Removed ssh-tool from $BIN_DIR and $APP_DIR."
    echo "Your connections and vault in ~/.local/share/ssh-tool were kept."
    exit 0
fi

SRC="${1:-}"
if [ -z "$SRC" ] || [ ! -f "$SRC" ]; then
    echo "usage: $0 <path-to-ssh-tool-binary>" >&2
    echo "       $0 --uninstall" >&2
    exit 2
fi

# The runtime libraries are not bundled, so say so before the user
# discovers it as a loader error.
here=$(dirname "$0")
[ -x "$here/check-deps.sh" ] && "$here/check-deps.sh" || true

mkdir -p "$BIN_DIR" "$APP_DIR" "$ICON_DIR/128x128/apps" "$ICON_DIR/scalable/apps"

install -m 0755 "$SRC" "$BIN_DIR/ssh-tool"

# Exec must be absolute: a .desktop launched by the session does not
# necessarily inherit a PATH containing ~/.local/bin.
cat > "$APP_DIR/org.wails.ssh-tool.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=ssh-tool
GenericName=SSH connection manager
Comment=Cross-platform SSH connection manager
Exec=$BIN_DIR/ssh-tool %U
Icon=org.wails.ssh-tool
Categories=Network;Development;RemoteAccess;
Terminal=false
Keywords=ssh;terminal;sftp;tunnel;wireguard;
Version=1.0
StartupNotify=true
StartupWMClass=ssh-tool
MimeType=x-scheme-handler/ssh-tool;
EOF

# Icons ship next to this script in the source tree; when it is run from
# an extracted release tarball they sit beside the binary instead.
for cand in "$here/../appicon.png" "$here/appicon.png" "$(dirname "$SRC")/appicon.png"; do
    [ -f "$cand" ] && cp "$cand" "$ICON_DIR/128x128/apps/org.wails.ssh-tool.png" && break
done
for cand in "$here/../appicon.svg" "$here/appicon.svg" "$(dirname "$SRC")/appicon.svg"; do
    [ -f "$cand" ] && cp "$cand" "$ICON_DIR/scalable/apps/org.wails.ssh-tool.svg" && break
done

rm -f "$APP_DIR/ssh-tool.desktop" \
      "$ICON_DIR/128x128/apps/ssh-tool.png" \
      "$ICON_DIR/scalable/apps/ssh-tool.svg"

command -v update-desktop-database >/dev/null 2>&1 &&
    update-desktop-database -q "$APP_DIR" || true
command -v gtk-update-icon-cache >/dev/null 2>&1 &&
    gtk-update-icon-cache -q -t -f "$ICON_DIR" 2>/dev/null || true

echo "Installed to $BIN_DIR/ssh-tool"

case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *)
        echo
        echo "Note: $BIN_DIR is not on your PATH. Add it with:"
        echo "    echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc"
        echo "The launcher entry works either way (it uses the full path)."
        ;;
esac
