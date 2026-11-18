import type { PortForward, ProxyBookmark } from "./api";

/** A forward's bookmarks to offer: the user's own, then the ones its
 *  catalog record shares (revision 6) that no own bookmark already has.
 *  A shared URL listed twice is offered once: lists key rows by URL. */
export function forwardBookmarks(f: PortForward): ProxyBookmark[] {
  const own = f.bookmarks ?? [];
  return [...own, ...sharedBookmarks(f)];
}

/** The shared bookmarks not already among the user's own, first one per URL. */
export function sharedBookmarks(f: PortForward): ProxyBookmark[] {
  const seen = new Set((f.bookmarks ?? []).map((b) => b.url));
  const out: ProxyBookmark[] = [];
  for (const b of f.catalog_bookmarks ?? []) {
    if (seen.has(b.url)) continue;
    seen.add(b.url);
    out.push(b);
  }
  return out;
}
