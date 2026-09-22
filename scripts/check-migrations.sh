#!/usr/bin/env bash
# Warn when a staged change edits a migration that has already run.
#
# Migrations are applied once and recorded by version, so editing the SQL
# of an existing one is invisible to every database that is already past
# it - including the author's own dirty local build. The schemas then
# diverge silently. The fix is always a new migration.
#
# This warns rather than blocks: a legitimate edit exists (a comment, or
# repairing a migration that has genuinely never run anywhere), and the
# hook cannot tell those apart. It only needs to make the author look.

set -uo pipefail

[ -n "${SKIP_MIGRATION_CHECK:-}" ] && exit 0

file="internal/store/migrations.go"
git diff --cached --name-only | grep -qx "$file" || exit 0

# Versions whose line is touched by the staged diff. -U0 keeps the hunks
# tight so an unrelated edit elsewhere in the file does not trip this.
touched=$(git diff --cached --no-color -U0 -- "$file" \
  | awk '/^[+-]\t\t[0-9]+,$/ { gsub(/[^0-9]/,""); print }' | sort -un)

# The highest version in the file after this change. A new migration is
# expected to be exactly this, and editing it is fine - it has not shipped.
newest=$(awk '/^\t\t[0-9]+,$/ { gsub(/[^0-9]/,""); print }' "$file" \
  | sort -n | tail -1)

# Any hunk that is not adjacent to a version line still edits SQL; catch
# the broader case by checking whether any removal happened at all.
removed=$(git diff --cached --no-color -- "$file" \
  | awk '/^-/ && !/^---/ { print }' | grep -c . || true)

[ "$removed" -eq 0 ] && exit 0

echo "WARNING: $file has staged removals." >&2
echo "  A migration's SQL must never change once any build has run it -" >&2
echo "  databases past that version will never see the edit. Add a new" >&2
echo "  migration instead." >&2
if [ -n "$touched" ]; then
  echo "  Version lines touched: $(printf '%s ' $touched)" >&2
fi
echo "  Highest version in the file: ${newest:-unknown}" >&2
echo "  (warning only - the commit proceeds)" >&2
exit 0
