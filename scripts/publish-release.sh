#!/usr/bin/env bash
# publish-release.sh - upload a built ssh-tool binary to the release
# server. Run after a tagged build (`task windows:build` etc.):
#
#   scripts/publish-release.sh bin/ssh-tool.exe
#
# The binary path is required. Everything else is auto-derived:
#
#   - version: from `git describe --tags --exact-match HEAD`
#     (refuses to publish from a non-tag commit).
#   - sha256:  computed locally; the server re-verifies.
#   - changelog hunk: the `## [vX.Y.Z]` block in CHANGELOG.md.
#   - platform: filename suffix or env override $PLATFORM.
#   - token: read from $RELEASE_TOKEN or ~/.config/ssh-tool/release-token.
#
# Env:
#   RELEASE_API_URL   default https://sshtool.app
#   RELEASE_TOKEN     bearer token; alternatively a file at the path
#                     above (mode 0600).
#   PLATFORM          override the auto-derived "windows-amd64" etc.
#   CHANNEL           default "stable".

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <binary-path>" >&2
  exit 1
fi

BIN="$1"
if [[ ! -f "$BIN" ]]; then
  echo "binary not found: $BIN" >&2
  exit 1
fi

VERSION="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
if [[ -z "$VERSION" ]]; then
  echo "HEAD is not on a tagged commit - refusing to publish." >&2
  echo "Tag it first with 'git tag -a vX.Y.Z -m \"…\"'." >&2
  exit 1
fi

API_URL="${RELEASE_API_URL:-https://sshtool.app}"
CHANNEL="${CHANNEL:-stable}"

# Token resolution: env first, file fallback.
TOKEN="${RELEASE_TOKEN:-}"
if [[ -z "$TOKEN" ]]; then
  TOKEN_FILE="${HOME}/.config/ssh-tool/release-token"
  if [[ -f "$TOKEN_FILE" ]]; then
    TOKEN="$(<"$TOKEN_FILE")"
    TOKEN="$(echo -n "$TOKEN" | tr -d '[:space:]')"
  fi
fi
if [[ -z "$TOKEN" ]]; then
  echo "no token - set RELEASE_TOKEN or write one to ~/.config/ssh-tool/release-token" >&2
  exit 1
fi

# Platform auto-derive from filename.
PLATFORM="${PLATFORM:-}"
if [[ -z "$PLATFORM" ]]; then
  case "$(basename "$BIN")" in
    *windows-amd64*|*-win.exe|*.exe) PLATFORM="windows-amd64" ;;
    *linux-amd64*)                   PLATFORM="linux-amd64" ;;
    *linux-arm64*)                   PLATFORM="linux-arm64" ;;
    *darwin-amd64*)                  PLATFORM="darwin-amd64" ;;
    *darwin-arm64*)                  PLATFORM="darwin-arm64" ;;
    *)
      echo "couldn't infer platform from filename; set PLATFORM=" >&2
      exit 1
      ;;
  esac
fi

SHA="$(sha256sum "$BIN" | awk '{print $1}')"

# Extract the changelog hunk for this version. Stops at the next
# top-level "## " header (or EOF).
CHANGELOG=""
if [[ -f CHANGELOG.md ]]; then
  CHANGELOG="$(awk -v v="$VERSION" '
    BEGIN { vbare=v; sub(/^v/,"",vbare) }
    /^## \[/ {
      if (in_section) exit
      if (index($0, "[" v "]") || index($0, "[" vbare "]")) {
        in_section=1
        print
        next
      }
    }
    in_section { print }
  ' CHANGELOG.md)"
fi

CHANGELOG_FILE="$(mktemp)"
trap 'rm -f "$CHANGELOG_FILE"' EXIT
printf "%s" "$CHANGELOG" > "$CHANGELOG_FILE"

echo "Publishing $VERSION ($PLATFORM)"
echo "  binary:    $BIN"
echo "  sha256:    $SHA"
echo "  size:      $(stat -c%s "$BIN" 2>/dev/null || stat -f%z "$BIN") bytes"
echo "  api:       $API_URL/api/upload"
echo

HTTP_CODE="$(curl -sS -o /tmp/publish-response.json -w "%{http_code}" \
  -X POST "$API_URL/api/upload" \
  -H "Authorization: Bearer $TOKEN" \
  -F "version=$VERSION" \
  -F "channel=$CHANNEL" \
  -F "platform=$PLATFORM" \
  -F "sha256=$SHA" \
  -F "binary=@$BIN" \
  -F "changelog=@$CHANGELOG_FILE")"

if [[ "$HTTP_CODE" == "409" ]]; then
  # The server enforces immutability per version+platform, so a 409
  # means this exact release slot is already occupied. Treat it as
  # success so re-running a partially-failed publish job (or a
  # workflow re-trigger on an unchanged tag) is idempotent instead of
  # dying on the first platform that already made it up.
  echo "already published ($VERSION $PLATFORM) - skipping"
  exit 0
fi

if [[ "$HTTP_CODE" != "200" ]]; then
  echo "upload failed: HTTP $HTTP_CODE" >&2
  cat /tmp/publish-response.json >&2 || true
  echo >&2
  exit 1
fi

echo "OK - server response:"
cat /tmp/publish-response.json
echo
echo
echo "Public download URL:"
echo "  $API_URL/download/$VERSION/$(basename "$BIN")"
