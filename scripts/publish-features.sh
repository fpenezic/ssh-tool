#!/usr/bin/env bash
# publish-features.sh - push the landing-page feature manifest to the
# release server. The manifest (docs/features.json) is the single
# source of truth for the sshtool.app feature list; this script POSTs
# it so the website tracks shipped features without a separate web
# deploy. Run it from a tagged commit, same as publish-release.sh,
# or from CI after a release tag.
#
# Idempotent: re-pushing the same manifest just overwrites it. Does
# NOT require a tag (the manifest isn't version-scoped), but CI runs
# it on tag so the website updates in lockstep with the release.
#
# Env:
#   RELEASE_API_URL   default https://sshtool.app
#   RELEASE_TOKEN     bearer token; alternatively a file at
#                     ~/.config/ssh-tool/release-token (mode 0600).
#   FEATURES_FILE     override the manifest path (default docs/features.json).

set -euo pipefail

API_URL="${RELEASE_API_URL:-https://sshtool.app}"
FEATURES_FILE="${FEATURES_FILE:-docs/features.json}"

if [[ ! -f "$FEATURES_FILE" ]]; then
  echo "manifest not found: $FEATURES_FILE" >&2
  exit 1
fi

# Token resolution: env first, file fallback (mirrors publish-release.sh).
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

echo "Pushing feature manifest"
echo "  file: $FEATURES_FILE"
echo "  api:  $API_URL/api/features"
echo

HTTP_CODE="$(curl -sS -o /tmp/publish-features-response.json -w "%{http_code}" \
  -X POST "$API_URL/api/features" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary "@$FEATURES_FILE")"

if [[ "$HTTP_CODE" != "200" ]]; then
  echo "feature push failed: HTTP $HTTP_CODE" >&2
  cat /tmp/publish-features-response.json >&2 || true
  echo >&2
  exit 1
fi

echo "OK - server response:"
cat /tmp/publish-features-response.json
echo
