#!/usr/bin/env python3
"""Regenerate the GitHub wiki pages that mirror docs/USER_GUIDE.md.

Usage: scripts/gen-wiki.py <path to a clone of ssh-tool.wiki>

Each generated page is a set of top-level guide sections ("## N. Title"),
headings kept, the "N. " numbering dropped. A page's preamble (its title,
screenshot and caption - everything before its first "## ") is kept from
the existing wiki page, so screenshots stay curated in the wiki. Links to
guide anchors ("](#credentials)") point at the page that now holds that
anchor. Hand-written pages (Home, FAQ, MCP, Session sharing) are not
touched. Review with git diff in the wiki clone, then commit and push.
"""
import re
import sys
from pathlib import Path

GUIDE = Path(__file__).resolve().parent.parent / "docs" / "USER_GUIDE.md"

# page -> guide section keys ("7b", "Storage locations", ...), in order
PAGES = {
    "Getting-Started": ["1", "2"],
    "Connections-and-Credentials": ["3", "4", "16", "17"],
    "Sessions-and-Transfer": ["5", "6", "6b", "6c", "6d", "8", "9", "10", "12", "15"],
    "Networking-and-Tunnels": ["7", "7b", "13", "13a", "14"],
    "Dynamic-Inventory": ["10b"],
    "Quick-Access-and-Shortcuts": ["11", "21"],
    "Import-Export-and-Settings": ["18", "19", "20", "Storage locations"],
    "Mobile": ["22"],
}

# guide sections that deliberately have no wiki page
SKIP = {"Table of contents", "What's not in the app (yet)"}

HEAD = re.compile(r"^## (?:(\d+[a-z]?)\. )?(.+)$")
ANCHOR = re.compile(r'^<a id="([^"]+)"></a>\s*$')


def sections(text):
    """Split the guide into {key: lines}; an <a id> line right before a
    heading belongs to that heading's section."""
    out, key, cur, pending = {}, None, [], []
    for line in text.splitlines():
        if ANCHOR.match(line):
            pending.append(line)
            continue
        m = HEAD.match(line)
        if m:
            if key is not None:
                out[key] = cur
            key = m.group(1) or m.group(2).strip()
            cur = pending + ["## " + m.group(2).strip()]
            pending = []
            continue
        if pending:
            cur += pending
            pending = []
        cur.append(line)
    if key is not None:
        out[key] = cur
    return out


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    wiki = Path(sys.argv[1])
    secs = sections(GUIDE.read_text(encoding="utf-8"))
    missing = [k for keys in PAGES.values() for k in keys if k not in secs]
    if missing:
        sys.exit(f"guide sections not found: {missing}")
    mapped = {k for keys in PAGES.values() for k in keys}
    unmapped = [k for k in secs if k not in mapped and k not in SKIP]
    if unmapped:
        sys.exit(f"guide sections with no wiki page (add them to PAGES or SKIP): {unmapped}")
    # anchor -> page, for cross-page links
    where = {}
    for page, keys in PAGES.items():
        for k in keys:
            for line in secs[k]:
                for a in re.findall(r'<a id="([^"]+)"></a>', line):
                    where[a] = page

    def relink(page, line):
        def sub(m):
            target = where.get(m.group(1))
            if target is None or target == page:
                return m.group(0)
            return f"]({target}#{m.group(1)})"
        return re.sub(r"\]\(#([A-Za-z0-9_-]+)\)", sub, line)

    for page, keys in PAGES.items():
        path = wiki / f"{page}.md"
        old = path.read_text(encoding="utf-8") if path.exists() else f"# {page.replace('-', ' ')}\n\n"
        # The preamble ends at the first section; a section's own <a id>
        # line sits right above its heading and belongs to the section.
        pre = old.split("\n## ", 1)[0].rstrip().split("\n")
        while pre and ANCHOR.match(pre[-1]) or pre and not pre[-1].strip():
            pre.pop()
        pre = "\n".join(pre) + "\n"
        body = []
        for k in keys:
            lines = [relink(page, l) for l in secs[k]]
            while lines and lines[-1].strip() in ("", "---"):
                lines.pop()
            body.append("\n".join(lines))
        path.write_text(pre + "\n" + "\n\n".join(body) + "\n", encoding="utf-8")
        print(f"{page}: {', '.join(keys)}")


if __name__ == "__main__":
    main()
