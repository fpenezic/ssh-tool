#!/usr/bin/env bash
# Refuse to commit strings that must not become public.
#
# The patterns themselves are the sensitive part, so they are NOT in this
# file: it reads .git/sensitive-patterns, which is outside the work tree
# and therefore unpublishable by construction. No list, no check - the
# hook is a maintainer convenience, not a gate contributors must satisfy.
#
# One extended-regex pattern per line. Blank lines and # comments ignored.
# Case-insensitive.
#
# Install: scripts/install-hooks.sh
# Bypass:  git commit --no-verify   (or SKIP_SENSITIVE_CHECK=1)

set -uo pipefail

[ -n "${SKIP_SENSITIVE_CHECK:-}" ] && exit 0

git_dir=$(git rev-parse --git-dir) || exit 0
patterns="${git_dir}/sensitive-patterns"

[ -r "$patterns" ] || exit 0

# Strip comments and blanks. An all-comment file would otherwise collapse
# to an empty -E pattern, which matches every line of every file.
res=$(grep -vE '^\s*(#|$)' "$patterns")
[ -n "$res" ] || exit 0

# Diff-cached, not the work tree: only what is actually being committed,
# and only added lines (a removal is the fix, not the offence).
#
# awk, not grep, to select those lines: "+++" is a valid -E pattern to GNU
# grep but invalid repetition to ugrep, which some distros install as grep.
# That difference made an earlier version fail open - the pipeline errored,
# and without -e the script ran on to exit 0.
added=$(git diff --cached --no-color -U0 -- . \
  | awk '/^\+/ && !/^\+\+\+/ { print }')
[ -n "$added" ] || exit 0

hits=$(printf '%s\n' "$added" | grep -inE -f <(printf '%s\n' "$res")) || true
[ -z "$hits" ] && exit 0

echo "commit blocked: staged changes contain a sensitive pattern" >&2
echo >&2
printf '%s\n' "$hits" | sed 's/^/  /' | head -20 >&2
echo >&2
echo "Files staged:" >&2
git diff --cached --name-only | sed 's/^/  /' >&2
echo >&2
echo "Edit the lines, or SKIP_SENSITIVE_CHECK=1 git commit ... to override." >&2
exit 1
