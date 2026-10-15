#!/usr/bin/env python3
"""Release gate: fail when the code reaches a known Go vulnerability.

Runs govulncheck (pinned) in a module directory and fails on every
vulnerability the code actually calls (a symbol-level finding), the
standard library included - which is how a stale Go toolchain shows up.
Vulnerabilities only present in required modules but never called do not
fail it.

An ID that cannot be fixed yet (no patched release, or an upstream
dependency that has not moved) can be listed in `.vulncheck-allow` in the
module directory, one per line, with the reason after a `#`:

    GO-2026-4479  # pion/dtls via the NetBird client, no fix upstream

Usage: vulncheck.py [module-dir] [packages...]   (default: . ./...)
"""

import json
import os
import subprocess
import sys

GOVULNCHECK = "golang.org/x/vuln/cmd/govulncheck@v1.9.0"


def main() -> int:
    moddir = sys.argv[1] if len(sys.argv) > 1 else "."
    pkgs = sys.argv[2:] or ["./..."]
    allow = {}
    path = os.path.join(moddir, ".vulncheck-allow")
    if os.path.exists(path):
        for line in open(path):
            vid, _, why = line.partition("#")
            if vid.strip():
                allow[vid.strip()] = why.strip()

    out = subprocess.run(
        ["go", "run", GOVULNCHECK, "-format", "json", *pkgs],
        cwd=moddir, capture_output=True, text=True,
    )
    if out.returncode != 0 and not out.stdout:
        print(out.stderr, file=sys.stderr)
        return 2

    reached, summary = set(), {}
    dec, s, i = json.JSONDecoder(), out.stdout, 0
    while i < len(s):
        while i < len(s) and s[i].isspace():
            i += 1
        if i >= len(s):
            break
        obj, i = dec.raw_decode(s, i)
        if "osv" in obj:
            summary[obj["osv"]["id"]] = obj["osv"].get("summary", "")
        f = obj.get("finding")
        if f and f.get("trace") and f["trace"][0].get("function"):
            reached.add(f["osv"])

    failing = sorted(reached - allow.keys())
    for vid in sorted(reached & allow.keys()):
        print(f"allowed  {vid}: {allow[vid]}")
    for vid in failing:
        print(f"REACHED  {vid}: {summary.get(vid, '')}")
    for vid in sorted(allow.keys() - reached):
        print(f"stale allow entry (no longer reached): {vid}")
    if failing:
        print(f"\n{len(failing)} reachable vulnerabilit{'y' if len(failing) == 1 else 'ies'} "
              f"in {moddir}. Fix (go.mod toolchain / module bumps) or, if no fix exists, "
              f"list in .vulncheck-allow with the reason.", file=sys.stderr)
        return 1
    print(f"vulncheck {moddir}: no reachable vulnerabilities")
    return 0


if __name__ == "__main__":
    sys.exit(main())
