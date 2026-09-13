#!/bin/bash
# Build apt and dnf repository metadata from a set of .deb / .rpm files.
#
# Hosting is deliberately not decided here: this writes a self-contained
# tree that can be served by anything static (GitHub Pages, a directory
# on sshtool.app, S3). See docs/TODO.md "apt / dnf repositories".
#
#   ./scripts/build-repo.sh <packages-dir> <output-dir> [gpg-key-id]
#
# Without a key id the metadata is written unsigned, which is useful for
# testing the layout but NOT for publishing: apt refuses unsigned repos
# by default, and telling users to pass [trusted=yes] trains them to
# accept unsigned packages.
set -euo pipefail

pkgdir="${1:-}"
outdir="${2:-}"
keyid="${3:-}"

if [ -z "$pkgdir" ] || [ -z "$outdir" ]; then
    echo "usage: $0 <packages-dir> <output-dir> [gpg-key-id]" >&2
    exit 2
fi
[ -d "$pkgdir" ] || { echo "no such directory: $pkgdir" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

missing=""
have apt-ftparchive || missing="$missing apt-utils(apt-ftparchive)"
have createrepo_c   || missing="$missing createrepo_c"
if [ -n "$keyid" ]; then have gpg || missing="$missing gnupg"; fi
if [ -n "$missing" ]; then
    echo "missing tools:$missing" >&2
    echo "on Debian/Ubuntu: sudo apt install apt-utils createrepo-c gnupg" >&2
    exit 1
fi

# --- apt ------------------------------------------------------------
# Flat-ish layout with one suite ("stable") and one component ("main").
# Users add it with:
#   deb [signed-by=/usr/share/keyrings/ssh-tool.gpg] <url>/deb stable main
aptroot="$outdir/deb"
pool="$aptroot/pool/main"
mkdir -p "$pool"
cp "$pkgdir"/*.deb "$pool"/ 2>/dev/null || { echo "no .deb files in $pkgdir" >&2; exit 1; }

# apt-ftparchive's --arch does NOT filter by the package's own
# Architecture field the way it reads: pointed at a pool it emitted an
# empty Packages file for a .deb that was plainly amd64. Generate once
# and split the stanzas ourselves, which is also less to get wrong.
all_stanzas=$(cd "$aptroot" && apt-ftparchive packages pool/main)
if [ -z "$all_stanzas" ]; then
    echo "apt-ftparchive found no packages in $pool" >&2
    exit 1
fi

for arch in amd64 arm64; do
    d="$aptroot/dists/stable/main/binary-$arch"
    mkdir -p "$d"
    # Stanzas are separated by a blank line; keep the ones whose
    # Architecture matches (plus "all", which suits any).
    printf '%s\n' "$all_stanzas" | awk -v want="$arch" '
        BEGIN { RS = ""; ORS = "\n\n" }
        {
            arch = ""
            n = split($0, lines, "\n")
            for (i = 1; i <= n; i++) {
                if (lines[i] ~ /^Architecture: /) {
                    arch = substr(lines[i], 15)
                    break
                }
            }
            if (arch == want || arch == "all") print
        }
    ' > "$d/Packages"
    gzip -9kf "$d/Packages"
    cat > "$d/Release" <<EOF
Archive: stable
Component: main
Origin: ssh-tool
Label: ssh-tool
Architecture: $arch
EOF
done

cat > "$aptroot/apt-release.conf" <<'EOF'
APT::FTPArchive::Release::Origin "ssh-tool";
APT::FTPArchive::Release::Label "ssh-tool";
APT::FTPArchive::Release::Suite "stable";
APT::FTPArchive::Release::Codename "stable";
APT::FTPArchive::Release::Architectures "amd64 arm64";
APT::FTPArchive::Release::Components "main";
APT::FTPArchive::Release::Description "ssh-tool releases";
EOF
(cd "$aptroot" && apt-ftparchive -c apt-release.conf release dists/stable) > "$aptroot/dists/stable/Release"
rm -f "$aptroot/apt-release.conf"

if [ -n "$keyid" ]; then
    # InRelease (inline signature) is what modern apt prefers;
    # Release.gpg is kept for older clients.
    gpg --default-key "$keyid" --batch --yes --clearsign \
        -o "$aptroot/dists/stable/InRelease" "$aptroot/dists/stable/Release"
    gpg --default-key "$keyid" --batch --yes --detach-sign --armor \
        -o "$aptroot/dists/stable/Release.gpg" "$aptroot/dists/stable/Release"
    gpg --export --armor "$keyid" > "$outdir/ssh-tool.asc"
fi

# --- dnf ------------------------------------------------------------
rpmroot="$outdir/rpm"
mkdir -p "$rpmroot"
cp "$pkgdir"/*.rpm "$rpmroot"/ 2>/dev/null || { echo "no .rpm files in $pkgdir" >&2; exit 1; }
createrepo_c --quiet "$rpmroot"

if [ -n "$keyid" ]; then
    gpg --default-key "$keyid" --batch --yes --detach-sign --armor \
        "$rpmroot/repodata/repomd.xml"
fi

echo
echo "repository written to $outdir"
[ -z "$keyid" ] && echo "WARNING: unsigned - for layout testing only, do not publish"
echo
echo "apt users:"
echo "  curl -fsSL <url>/ssh-tool.asc | sudo gpg --dearmor -o /usr/share/keyrings/ssh-tool.gpg"
echo "  echo 'deb [signed-by=/usr/share/keyrings/ssh-tool.gpg] <url>/deb stable main' | sudo tee /etc/apt/sources.list.d/ssh-tool.list"
echo
echo "dnf users: a .repo file pointing at <url>/rpm with gpgkey=<url>/ssh-tool.asc"
