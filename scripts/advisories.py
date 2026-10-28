#!/usr/bin/env python3
"""Release gate: list GitHub advisories that affect our dependency versions.

Dependabot alerts come from the same GitHub Advisory Database, but they
are only recomputed when a manifest changes on the default branch or when
Dependabot gets round to it, so a new advisory can sit unreported for a
while (v1.0.0 was tagged with a go-jose advisory that only showed up as
an alert after a manual run). This asks the database directly.

Go: the modules required in go.mod (direct and indirect, what Dependabot
reads). npm: every package in package-lock.json, dev dependencies marked.
Unlike vulncheck.py this does not know whether the code reaches the
vulnerable function: a hit means "look at it", not "the app is exposed".

An advisory dismissed in the repository's Dependabot alerts counts as
checked and is shown as allowed with the dismissal reason. Others can be
accepted in `.advisories-allow` in the current directory (no fix yet, not
reachable, a build-time tool), one GHSA ID per line, with the reason
after a `#`:

    GHSA-xxxx-xxxx-xxxx  # deepmerge, build-time only, no fix released

Needs an authenticated `gh` CLI. Exit status 1 when anything not allowed
is found.

Usage: advisories.py [dir...]   (default: . frontend)
  Each dir is scanned for go.mod and package-lock.json. For the plugins
  repo, pass each plugin's module directory.
"""

import json
import os
import subprocess
import sys

CHUNK = 40  # packages per request; keeps the query string short


def go_modules(d: str) -> dict[str, str]:
    out = subprocess.run(["go", "mod", "edit", "-json"], cwd=d, capture_output=True, text=True, check=True)
    return {r["Path"]: r["Version"] for r in json.loads(out.stdout).get("Require") or []}


def npm_packages(d: str) -> dict[str, tuple[str, bool]]:
    lock = json.load(open(os.path.join(d, "package-lock.json")))
    pkgs = {}
    for path, p in (lock.get("packages") or {}).items():
        if not path or "version" not in p:
            continue
        name = path.rsplit("node_modules/", 1)[-1]
        # The same package can sit at several versions; keep each.
        pkgs[f"{name}@{p['version']}"] = (name, bool(p.get("dev")))
    return pkgs


def query(ecosystem: str, specs: list[str]) -> list[dict]:
    found = []
    for i in range(0, len(specs), CHUNK):
        part = ",".join(specs[i:i + CHUNK])
        out = subprocess.run(
            ["gh", "api", "--paginate", f"/advisories?ecosystem={ecosystem}&per_page=100&affects={part}"],
            capture_output=True, text=True, check=True)
        # --paginate concatenates JSON arrays page by page.
        found += json.loads(out.stdout.replace("][", ","))
    return [a for a in found if not a.get("withdrawn_at")]


def dismissed(d: str) -> dict[str, str]:
    """GHSA IDs dismissed in the Dependabot alerts of the repo holding d."""
    out = subprocess.run(["gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
                         cwd=d, capture_output=True, text=True)
    repo = out.stdout.strip()
    if out.returncode or not repo:
        return {}
    out = subprocess.run(["gh", "api", "--paginate", f"/repos/{repo}/dependabot/alerts?state=dismissed&per_page=100"],
                         capture_output=True, text=True)
    if out.returncode:
        return {}
    res = {}
    for a in json.loads(out.stdout.replace("][", ",")):
        why = a.get("dismissed_comment") or a.get("dismissed_reason") or "dismissed"
        res[a["security_advisory"]["ghsa_id"]] = f"dismissed in Dependabot: {why}"
    return res


def vkey(v: str) -> tuple:
    """Rough version order: numeric parts first, a pre-release sorts lower."""
    v = v.lstrip("v").split("+")[0]
    core, _, pre = v.partition("-")
    nums = tuple(int(x) if x.isdigit() else 0 for x in core.split("."))
    return nums + ((1,) if not pre else (0, pre))


def report(label: str, ecosystem: str, versions: dict[str, str], dev: dict[str, bool], allow: dict[str, str]) -> int:
    if not versions:
        return 0
    specs = sorted(f"{n}@{v}" for n, v in versions.items())
    bad = 0
    for a in query(ecosystem, specs):
        # An advisory can list one package several times, one entry per
        # affected range: the fix shown is the lowest patched version above
        # ours.
        fixes: dict[str, list[str]] = {}
        for v in a.get("vulnerabilities") or []:
            name = v["package"]["name"]
            if name not in versions:
                continue
            fix = v.get("first_patched_version")
            if isinstance(fix, dict):
                fix = fix.get("identifier")
            fixes.setdefault(name, [])
            if fix:
                fixes[name].append(fix)
        for name, fl in fixes.items():
            above = sorted((f for f in fl if vkey(f) > vkey(versions[name])), key=vkey)
            fix = above[0] if above else "none"
            tag = " (dev)" if dev.get(name) else ""
            if a["ghsa_id"] in allow:
                print(f"  allowed  {a['ghsa_id']} {a['severity']} {name}@{versions[name]}{tag} - {allow[a['ghsa_id']]}")
                continue
            bad += 1
            print(f"  {a['severity']:<8} {a['ghsa_id']} {name}@{versions[name]}{tag} fix: {fix} - {a['summary']}")
    print(f"{label}: {len(versions)} packages, {bad} advisories")
    return bad


def main() -> int:
    dirs = sys.argv[1:] or [".", "frontend"]
    allow = {}
    if os.path.exists(".advisories-allow"):
        for line in open(".advisories-allow"):
            gid, _, why = line.partition("#")
            if gid.strip():
                allow[gid.strip()] = why.strip()
    bad = 0
    for d in dirs:
        allow_d = {**dismissed(d), **allow}
        if os.path.exists(os.path.join(d, "go.mod")):
            bad += report(f"{d}/go.mod", "go", go_modules(d), {}, allow_d)
        if os.path.exists(os.path.join(d, "package-lock.json")):
            # One package can sit in the tree at several versions, and a
            # report maps name -> version: the first pass takes each name's
            # first version, the next pass the second, and so on.
            byname: dict[str, list[tuple[str, bool]]] = {}
            for spec, (name, isdev) in sorted(npm_packages(d).items()):
                byname.setdefault(name, []).append((spec.rsplit("@", 1)[1], isdev))
            rounds = max((len(v) for v in byname.values()), default=0)
            for r in range(rounds):
                versions = {n: v[r][0] for n, v in byname.items() if len(v) > r}
                dev = {n: v[r][1] for n, v in byname.items() if len(v) > r}
                label = f"{d}/package-lock.json" + (f" (version set {r + 1})" if r else "")
                bad += report(label, "npm", versions, dev, allow_d)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
