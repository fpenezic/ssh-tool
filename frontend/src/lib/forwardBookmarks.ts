import type { PortForward, ProxyBookmark } from "./api";

/** A forward's bookmarks to offer: the user's own, then the ones its
 *  catalog record shares (revision 6) that no own bookmark already has. */
export function forwardBookmarks(f: PortForward): ProxyBookmark[] {
  const own = f.bookmarks ?? [];
  const seen = new Set(own.map((b) => b.url));
  return [...own, ...(f.catalog_bookmarks ?? []).filter((b) => !seen.has(b.url))];
}
