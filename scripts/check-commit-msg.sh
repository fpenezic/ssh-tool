#!/usr/bin/env bash
# Enforce the two commit-message rules that a reader cannot un-see.
#
# The pre-commit check guards file contents; this guards the message,
# which nothing else looks at. Both rules are in CLAUDE.md, and both have
# been broken in practice - the Co-Authored-By trailer in particular got
# through and had to be stripped from several commits by hand.
#
# Author email is deliberately NOT checked: GitHub's email privacy
# protection already rejects a push carrying the real address, so a local
# copy would be a third layer over a solved problem.
#
# Usage:  check-commit-msg.sh <file>    (a commit-msg hook argument)
# Bypass: git commit --no-verify        (or SKIP_MSG_CHECK=1)

set -uo pipefail

[ -n "${SKIP_MSG_CHECK:-}" ] && exit 0
[ $# -ge 1 ] || exit 0
msg="$1"
[ -r "$msg" ] || exit 0

# Comments are stripped by git after this hook runs, so ignore them here
# too - a rule mentioned in the commented template is not a violation.
body=$(grep -v '^#' "$msg")

fail=0

if printf '%s\n' "$body" | grep -qi '^[[:space:]]*co-authored-by:'; then
  echo "commit-msg: Co-Authored-By trailer is not used in this project." >&2
  echo "  GitHub counts the trailer as a contributor on a personal repo." >&2
  fail=1
fi

# grep -P is not everywhere; the dashes are matched as literal UTF-8 bytes
# so this works with BRE on any grep, ugrep included.
if printf '%s\n' "$body" | grep -q $'—\|–'; then
  echo "commit-msg: em-dash or en-dash found. Use a plain ASCII hyphen." >&2
  printf '%s\n' "$body" | grep -n $'—\|–' | sed 's/^/  /' >&2
  fail=1
fi

[ "$fail" -eq 0 ] && exit 0
echo >&2
echo "Fix the message, or SKIP_MSG_CHECK=1 / --no-verify to override." >&2
exit 1
