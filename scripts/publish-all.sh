#!/usr/bin/env bash
# publish-all.sh - local release-build escape hatch.
#
# The default release path is CI: tagging triggers
# .github/workflows/release.yml which builds amd64 + arm64 on
# hosted runners and publishes them in parallel. This script
# exists for the case where CI is unavailable (GitHub outage,
# hotfix while travelling) and you need to push a release from
# your laptop.
#
# Refuses to run if HEAD isn't on a tag, mirroring the per-
# platform script. Builds happen in `task <os>:build`; binaries
# land in `bin/`. linux-arm64 / windows-arm64 require the local
# wails-cross docker image which doesn't reliably build them
# (the whole reason CI exists) - prefer CI for those.
#
# Usage:
#   scripts/publish-all.sh                                         # default subset (amd64 only)
#   scripts/publish-all.sh windows linux                           # explicit subset
#   scripts/publish-all.sh windows linux linux-arm64 windows-arm64 # opt-in arm64
#
# arm64 builds are UNTESTED - author has no native arm64 hardware
# to validate against. Default keeps to amd64; opt in by listing
# linux-arm64 / windows-arm64 explicitly.

set -euo pipefail

VERSION="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
if [[ -z "$VERSION" ]]; then
  echo "HEAD is not on a tagged commit - refusing to publish." >&2
  exit 1
fi

PLATFORMS=("$@")
if [[ ${#PLATFORMS[@]} -eq 0 ]]; then
  PLATFORMS=(windows linux)
fi

for os in "${PLATFORMS[@]}"; do
  case "$os" in
    windows|windows-amd64)
      echo "==> Building windows-amd64"
      task windows:build
      cp bin/ssh-tool.exe bin/ssh-tool-windows-amd64.exe
      PLATFORM=windows-amd64 scripts/publish-release.sh bin/ssh-tool-windows-amd64.exe
      ;;
    windows-arm64)
      echo "==> Building windows-arm64 (UNTESTED)"
      task windows:build ARCH=arm64
      cp bin/ssh-tool.exe bin/ssh-tool-windows-arm64.exe
      PLATFORM=windows-arm64 scripts/publish-release.sh bin/ssh-tool-windows-arm64.exe
      ;;
    linux|linux-amd64)
      echo "==> Building linux-amd64"
      task linux:build
      cp bin/ssh-tool bin/ssh-tool-linux-amd64
      PLATFORM=linux-amd64 scripts/publish-release.sh bin/ssh-tool-linux-amd64
      ;;
    linux-arm64)
      echo "==> Building linux-arm64 (UNTESTED)"
      task linux:build ARCH=arm64
      cp bin/ssh-tool bin/ssh-tool-linux-arm64
      PLATFORM=linux-arm64 scripts/publish-release.sh bin/ssh-tool-linux-arm64
      ;;
    darwin|darwin-arm64)
      echo "==> Building darwin-arm64"
      task darwin:build
      cp bin/ssh-tool bin/ssh-tool-darwin-arm64
      PLATFORM=darwin-arm64 scripts/publish-release.sh bin/ssh-tool-darwin-arm64
      ;;
    *)
      echo "unknown platform: $os" >&2
      exit 1
      ;;
  esac
done

echo
echo "==> Pushing feature manifest to landing page"
scripts/publish-features.sh || echo "WARN: feature manifest push failed (non-fatal)" >&2

echo
echo "All requested platforms published for $VERSION."
