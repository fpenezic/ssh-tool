// Lazy image cache. Image IDs are content-addressed by MD5 server-side, so
// once we've fetched one we can serve every other reference to the same id
// without re-asking. Used by TreeNode + tab-bar + detail panes to render
// custom folder / connection icons.

import { api } from "./api";

class ImageCache {
  // Resolved data URLs keyed by image id. Reactive - components reading
  // through `peek()` in a $derived re-render when entries land.
  private urls = $state(new Map<string, string>());
  // Tracks in-flight / already-tried ids so we don't double-fetch.
  // Plain (non-reactive) so writing here doesn't trip state_unsafe_mutation
  // when the kickoff path is reached from inside a $derived.
  private pending = new Set<string>();

  /** Read-only lookup. Returns the data URL or null. */
  peek(id: string | null | undefined): string | null {
    if (!id) return null;
    return this.urls.get(id) ?? null;
  }

  /** Kick off a fetch if we haven't already. Safe to call from $effect. */
  ensure(id: string | null | undefined): void {
    if (!id) return;
    if (this.urls.has(id) || this.pending.has(id)) return;
    this.pending.add(id);
    (async () => {
      try {
        const { mime, b64 } = await api.imagesGet(id);
        const url = `data:${mime};base64,${b64}`;
        // Reassign so $state notifies.
        const next = new Map(this.urls);
        next.set(id, url);
        this.urls = next;
      } catch (e) {
        console.warn("imagesGet failed for", id, e);
      } finally {
        this.pending.delete(id);
      }
    })();
  }
}

export const imageCache = new ImageCache();
