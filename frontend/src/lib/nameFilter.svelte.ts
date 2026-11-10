// Live name/hostname filter for the connections tree.
//
// Sidebar's search input writes to `nameFilter.query`. Tree rows
// fall out when neither the connection's name nor hostname contains
// the query (case-insensitive substring). Folders stay visible iff
// any descendant connection matches - same shape as `tagFilter`.
//
// Not persisted: this is a transient narrowing aid, not a saved view.

import { tree } from "./stores.svelte";

// How long typing pauses before the tree applies the filter: a typed word
// remounts the rows once, not once per letter (each pass mounts or drops
// every matching row; ~1 ms a row).
const APPLY_DELAY_MS = 120;

class NameFilterStore {
  // What the search box shows, at once.
  text = $state<string>("");
  // What the tree filters by: text, after the typing pause.
  query = $state<string>("");
  private timer: ReturnType<typeof setTimeout> | null = null;

  set(q: string) {
    this.text = q;
    if (this.timer) clearTimeout(this.timer);
    // Clearing applies at once: the full tree is what the user expects
    // back, and there is nothing more to type.
    if (!q.trim()) {
      this.apply();
      return;
    }
    this.timer = setTimeout(() => this.apply(), APPLY_DELAY_MS);
  }

  // Apply what is typed now (Enter / ArrowDown into the tree, Escape).
  flush() {
    if (this.timer) this.apply();
  }

  private apply() {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    const wasInactive = !this.query.trim();
    this.query = this.text;
    // Eagerly load dynamic entries on first keystroke so the filter
    // can match against VMs / containers in dynamic folders that
    // haven't been expanded yet. Lazy expand-load alone would mean
    // searching for "prod" returns nothing until the user opens the
    // dynamic folder manually - defeats the search-from-anywhere UX.
    if (wasInactive && this.query.trim()) {
      for (const id of Object.keys(tree.dynamicFolders)) {
        if (!tree.dynamicEntries[id]) tree.loadDynamicEntries(id);
      }
    }
  }

  clear() {
    this.text = "";
    this.apply();
  }

  // Something is typed (the box's clear button, Escape handling), even
  // if the tree has not applied it yet.
  isActive(): boolean {
    return this.text.trim().length > 0;
  }

  // The tree is filtered (the typed text has been applied).
  isApplied(): boolean {
    return this.query.trim().length > 0;
  }

  private needle(): string {
    return this.query.trim().toLowerCase();
  }

  // What the query matches, computed once per query (and tree change)
  // instead of per row: the matching connections, the folders with a
  // match anywhere below them (inventory entries included), and the
  // matching inventory entries as "folderId/entryId". Rows only look up.
  private matches = $derived.by(() => {
    const n = this.query.trim().toLowerCase();
    if (!n) return null;
    const hit = (name: string | undefined, host: string | undefined, tags: string[] | undefined | null) => {
      if (name?.toLowerCase().includes(n) || host?.toLowerCase().includes(n)) return true;
      for (const t of tags ?? []) {
        if (t.toLowerCase().includes(n)) return true;
      }
      return false;
    };
    const conns = new Set<string>();
    const folders = new Set<string>();
    const entries = new Set<string>();
    const markUp = (folderId: string | null | undefined) => {
      let id = folderId ?? null;
      while (id && !folders.has(id)) {
        folders.add(id);
        id = tree.folderById(id)?.parent_id ?? null;
      }
    };
    for (const c of tree.connections) {
      if (hit(c.name, c.hostname, c.tags)) {
        conns.add(c.id);
        markUp(c.folder_id);
      }
    }
    for (const [fid, list] of Object.entries(tree.dynamicEntries)) {
      for (const e of list ?? []) {
        if (hit(e.name, e.hostname, e.tags)) {
          entries.add(fid + "/" + e.id);
          markUp(fid);
        }
      }
    }
    return { conns, folders, entries };
  });

  connectionMatches(connId: string): boolean {
    const m = this.matches;
    return !m || m.conns.has(connId);
  }

  // A folder stays visible iff a connection or loaded inventory entry
  // somewhere below it matches. Entries are pulled lazily on first
  // expand (set() starts that for every dynamic folder), and the filter
  // re-evaluates once they land.
  folderHasMatch(folderId: string): boolean {
    const m = this.matches;
    return !m || m.folders.has(folderId);
  }

  // dynamicEntryMatches mirrors connectionMatches for dynamic
  // entries. TreeNode uses it to hide individual rows under an
  // expanded dynamic folder when only a subset matches the filter -
  // without this every dynamic entry stays visible as long as the
  // folder has at least one match.
  dynamicEntryMatches(folderId: string, entryId: string): boolean {
    const m = this.matches;
    if (!m) return true;
    if (!tree.dynamicEntries[folderId]) return true;
    return m.entries.has(folderId + "/" + entryId);
  }
}

export const nameFilter = new NameFilterStore();
