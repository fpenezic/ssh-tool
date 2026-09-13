#!/bin/bash
# Refresh build/aur/PKGBUILD for a published release and print what to
# push to the AUR.
#
# Run this AFTER the GitHub release exists - the checksums are computed
# from the published artefacts, so there is nothing to hash before then.
#
#   ./scripts/aur-release.sh v0.95.0
#
# The AUR repo itself is a separate git remote:
#   git clone ssh://aur@aur.archlinux.org/ssh-tool-bin.git
# Copy the refreshed PKGBUILD in, regenerate .SRCINFO, commit, push.
set -euo pipefail

tag="${1:-}"
if [ -z "$tag" ]; then
    echo "usage: $0 <tag>   e.g. $0 v0.95.0" >&2
    exit 2
fi
ver="${tag#v}"

repo="fpenezic/ssh-tool"
base="https://github.com/$repo/releases/download/$tag"
raw="https://raw.githubusercontent.com/$repo/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch_sha() {
    local url="$1" name="$2"
    if ! curl -fsSL "$url" -o "$tmp/$name"; then
        echo "cannot fetch $url" >&2
        echo "(is the release published, and does it carry that asset?)" >&2
        exit 1
    fi
    sha256sum "$tmp/$name" | cut -d' ' -f1
}

echo "hashing $tag artefacts..." >&2
sha_amd64=$(fetch_sha "$base/ssh-tool-linux-amd64" amd64)
sha_arm64=$(fetch_sha "$base/ssh-tool-linux-arm64" arm64)
sha_desktop=$(fetch_sha "$raw/build/linux/ssh-tool.desktop.in" desktop)
sha_png=$(fetch_sha "$raw/build/appicon.png" png)
sha_svg=$(fetch_sha "$raw/build/appicon.svg" svg)

pkgbuild="$(dirname "$0")/../build/aur/PKGBUILD"

sed -i "s/^pkgver=.*/pkgver=$ver/" "$pkgbuild"
sed -i "s/^pkgrel=.*/pkgrel=1/" "$pkgbuild"
sed -i "s|^sha256sums_x86_64=.*|sha256sums_x86_64=('$sha_amd64'\n                   '$sha_desktop'\n                   '$sha_png'\n                   '$sha_svg')|" "$pkgbuild"
sed -i "s|^sha256sums_aarch64=.*|sha256sums_aarch64=('$sha_arm64'\n                    '$sha_desktop'\n                    '$sha_png'\n                    '$sha_svg')|" "$pkgbuild"

echo
echo "build/aur/PKGBUILD updated to $ver"
echo
echo "To publish:"
echo "  git clone ssh://aur@aur.archlinux.org/ssh-tool-bin.git /tmp/aur-ssh-tool"
echo "  cp build/aur/PKGBUILD /tmp/aur-ssh-tool/"
echo "  cd /tmp/aur-ssh-tool && makepkg --printsrcinfo > .SRCINFO"
echo "  git commit -am '$ver' && git push"
echo
echo "Check it builds first:  cd /tmp/aur-ssh-tool && makepkg -si"
