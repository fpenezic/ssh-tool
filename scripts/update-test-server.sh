#!/usr/bin/env bash
#
# Local update-flow test harness.
#
# Testing the updater normally needs a published release AND an older
# build to run it from - which makes every fix to the update path
# untestable until after it ships. This builds both sides locally and
# serves a fake /api/latest, so the whole flow (check -> download ->
# staged -> apply -> relaunch) can be exercised against code that is
# not released anywhere.
#
# It works because resolveLatestRelease skips GitHub entirely when the
# `update_check_base_url` setting is set, and falls back to
# <base>/api/latest with the same JSON shape the release server uses.
#
# Usage:
#   scripts/update-test-server.sh windows    # cross-build both, serve
#   scripts/update-test-server.sh linux      # native build, serve
#
# Then, in the app you want to update FROM:
#   sqlite3 <datadir>/store.db \
#     "INSERT INTO settings(key,value) VALUES('update_check_base_url','http://<host>:877')
#      ON CONFLICT(key) DO UPDATE SET value=excluded.value;"
#
# and remove that row when you are done, or the app keeps checking a
# server that is no longer running.
set -euo pipefail

TARGET="${1:-linux}"
PORT="${PORT:-8877}"
OLD_VERSION="${OLD_VERSION:-v0.50.0-updatetest}"
NEW_VERSION="${NEW_VERSION:-v9.99.0-updatetest}"

cd "$(dirname "$0")/.."
OUT="$(mktemp -d)"
SERVE="$OUT/serve"
mkdir -p "$SERVE"

# Linux needs CGO for GTK/WebKit; the Windows build is pure Go and
# cross-compiles from here. A linux target therefore only builds when
# run ON Linux with a working toolchain.
case "$TARGET" in
  windows) EXT=".exe"; ASSET_KEY="windows-amd64"; GOOS=windows; GOARCH=amd64; CGO=0 ;;
  linux)   EXT="";     ASSET_KEY="linux-amd64";   GOOS=linux;   GOARCH=amd64; CGO=1 ;;
  *) echo "usage: $0 [windows|linux]" >&2; exit 2 ;;
esac

# The two builds are the SAME code; only the stamped version differs.
# That is the point: the update path is what is under test, not any
# behavioural difference between the versions.
build() {
  local version="$1" dest="$2"
  echo "building $version -> $dest"
  CGO_ENABLED="$CGO" GOOS="$GOOS" GOARCH="$GOARCH" go build \
    -tags production -trimpath -buildvcs=false \
    -ldflags "-w -s -X main.appVersion=$version -X main.appCommit=updatetest" \
    -o "$dest" .
}

build "$OLD_VERSION" "$OUT/ssh-tool-old$EXT"
build "$NEW_VERSION" "$SERVE/ssh-tool$EXT"

SHA="$(sha256sum "$SERVE/ssh-tool$EXT" | cut -d' ' -f1)"
SIZE="$(stat -c%s "$SERVE/ssh-tool$EXT")"

# Match the release server's /api/latest shape exactly - the client
# unmarshals into a fixed struct and silently gets an empty asset if
# the key does not match platformAssetKey() (GOOS-GOARCH).
cat > "$SERVE/api-latest.json" <<JSON
{
  "version": "$NEW_VERSION",
  "released_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "changelog_url": "http://localhost:$PORT/",
  "assets": {
    "$ASSET_KEY": {
      "url": "http://HOSTPLACEHOLDER:$PORT/ssh-tool$EXT",
      "sha256": "$SHA",
      "size": $SIZE
    }
  }
}
JSON

HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
HOST_IP="${HOST_IP:-localhost}"
sed -i "s/HOSTPLACEHOLDER/$HOST_IP/" "$SERVE/api-latest.json"

cat <<INFO

  old build:  $OUT/ssh-tool-old$EXT   ($OLD_VERSION)
  new build:  $SERVE/ssh-tool$EXT     ($NEW_VERSION)
  sha256:     $SHA

  point the app at:  http://$HOST_IP:$PORT

  sqlite3 store.db "INSERT INTO settings(key,value) VALUES('update_check_base_url','http://$HOST_IP:$PORT') ON CONFLICT(key) DO UPDATE SET value=excluded.value;"

  serving on :$PORT - Ctrl+C to stop

INFO

# python's handler serves files by path; /api/latest has no extension
# and must return the JSON, so rewrite that one path onto the file.
cd "$SERVE"
exec python3 - "$PORT" <<'PY'
import sys, http.server, socketserver

PORT = int(sys.argv[1])

class H(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path.rstrip("/") == "/api/latest":
            self.path = "/api-latest.json"
        return super().do_GET()

    def end_headers(self):
        # No caching, so editing api-latest.json between runs takes
        # effect on the next check instead of serving a stale version.
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("0.0.0.0", PORT), H) as httpd:
    httpd.serve_forever()
PY
