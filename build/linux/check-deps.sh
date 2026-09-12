#!/bin/sh
# Runtime dependency check for the Linux desktop build.
#
# ssh-tool links GTK4 + WebKitGTK 6.0 dynamically through CGO, so a missing
# library kills the process in the dynamic loader - before main() runs, and
# before any Go code could print something a user can act on. What they see
# instead is:
#
#   error while loading shared libraries: libwebkitgtk-6.0.so.4:
#   cannot open shared object file: No such file or directory
#
# which is accurate and useless. Distro packages (.deb/.rpm/AUR) declare the
# dependency so this never fires there; this script is for the people who
# download the bare binary from the releases page, which is most of them.
#
# Run it standalone, or source the hint function from a wrapper.

set -eu

missing=""

for lib in libwebkitgtk-6.0.so.4 libgtk-4.so.1; do
    if ! ldconfig -p 2>/dev/null | grep -q "$lib"; then
        missing="$missing $lib"
    fi
done

[ -z "$missing" ] && exit 0

# Name the package rather than the library. Nobody knows which package ships
# libwebkitgtk-6.0.so.4, and the answer differs per distro.
if [ -r /etc/os-release ]; then
    . /etc/os-release
fi

case "${ID:-}${ID_LIKE:-}" in
    *arch*)
        install_cmd="sudo pacman -S webkitgtk-6.0 gtk4" ;;
    *debian*|*ubuntu*)
        install_cmd="sudo apt install libwebkitgtk-6.0-4 libgtk-4-1" ;;
    *fedora*|*rhel*|*centos*)
        install_cmd="sudo dnf install webkitgtk6.0 gtk4" ;;
    *suse*)
        install_cmd="sudo zypper install libwebkitgtk-6_0-4 libgtk-4-1" ;;
    *)
        install_cmd="install the WebKitGTK 6.0 and GTK 4 runtime packages for your distribution" ;;
esac

cat >&2 <<EOF
ssh-tool cannot start: missing system libraries.

Missing:$missing

ssh-tool draws its window with GTK 4 and WebKitGTK 6.0, which are not
bundled. Install them with:

    $install_cmd

Then run ssh-tool again. Installing the .deb, .rpm or AUR package instead
pulls these in automatically.
EOF

exit 1
