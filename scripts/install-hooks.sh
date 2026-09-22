#!/usr/bin/env bash
# Install the repo's git hooks. Hooks live in .git/, which is never
# cloned, so this is run once per checkout - by whoever wants them.

set -euo pipefail

root=$(git rev-parse --show-toplevel)
git_dir=$(git rev-parse --git-dir)
hooks="${git_dir}/hooks"
mkdir -p "$hooks"

# Chained rather than written inline so scripts/ stays the single source
# of truth: editing the checked-in script changes behaviour without a
# reinstall.
cat > "${hooks}/pre-commit" <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
[ -x "${root}/scripts/check-sensitive.sh" ] && "${root}/scripts/check-sensitive.sh"
HOOK
chmod +x "${hooks}/pre-commit"

echo "installed: ${hooks}/pre-commit"

patterns="${git_dir}/sensitive-patterns"
if [ ! -e "$patterns" ]; then
  cat > "$patterns" <<'PATTERNS'
# Extended regex, one per line, case-insensitive. Matched against ADDED
# lines in staged changes only.
#
# This file sits in .git/ on purpose: the patterns name the things that
# must not be published, so they must not be committed either.
#
# Examples - replace with the real ones:
#   acmecorp
#   internal\.example\.com
PATTERNS
  echo "created:   ${patterns} (empty - add patterns to arm the hook)"
else
  n=$(grep -cvE '^\s*(#|$)' "$patterns" || true)
  echo "kept:      ${patterns} (${n} pattern(s))"
fi
