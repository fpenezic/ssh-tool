// Live name/hostname filter for the connections tree.
//
// Sidebar's search input writes to `nameFilter.query`. Tree rows
// fall out when neither the connection's name nor hostname contains
// the query (case-insensitive substring). Folders stay visible iff
// any descendant connection matches - same shape as `tagFilter`.
//
// Not persisted: this is a transient narrowing aid, not a saved view.

import { tree } from "./stores.svelte";

class NameFilterStore {
  query = $state<string>("");

  set(q: string) {
    const wasInactive = !this.isActive();
    this.query = q;
    // Eagerly load dynamic entries on first keystroke so the filter
    // can match against VMs / containers in dynamic folders that
    // haven't been expanded yet. Lazy expand-load alone would mean
    // searching for "prod" returns nothing until the user opens the
    // dynamic folder manually - defeats the search-from-anywhere UX.
    if (wasInactive && this.isActive()) {
      for (const id of Object.keys(tree.dynamicFolders)) {
        if (!tree.dynamicEntries[id]) tree.loadDynamicEntries(id);
      }
    }
  }
  clear() { this.query = ""; }

  isActive(): boolean {
    return this.query.trim().length > 0;
  }

  private needle(): string {
    return this.query.trim().toLowerCase();
  }

  connectionMatches(connId: string): boolean {
    if (!this.isActive()) return true;
    const c = tree.connectionById(connId);
    if (!c) return false;
    const n = this.needle();
    if (c.name?.toLowerCase().includes(n)) return true;
    if (c.hostname?.toLowerCase().includes(n)) return true;
    // Tag match: typing the tag string narrows to hosts carrying it.
    // Cheap because connection tag arrays are tiny.
    for (const t of c.tags ?? []) {
      if (t.toLowerCase().includes(n)) return true;
    }
    return false;
  }

  folderHasMatch(folderId: string): boolean {
    if (!this.isActive()) return true;
    for (const c of tree.connectionsIn(folderId)) {
      if (this.connectionMatches(c.id)) return true;
    }
    // Dynamic-inventory entries - cached children pulled from
    // proxmox / hetzner / … the entries list is populated lazily on
    // first expand, so a filter typed before that returns "no match"
    // here. The store auto-loads on expand (see TreeNode $effect),
    // and the filter re-evaluates once entries land.
    const dynEntries = tree.dynamicEntries[folderId];
    if (dynEntries && dynEntries.length > 0) {
      const n = this.needle();
      for (const e of dynEntries) {
        if (e.name.toLowerCase().includes(n) || e.hostname.toLowerCase().includes(n)) {
          return true;
        }
        for (const t of e.tags ?? []) {
          if (t.toLowerCase().includes(n)) return true;
        }
      }
    }
    for (const sub of tree.childrenOf(folderId)) {
      if (this.folderHasMatch(sub.id)) return true;
    }
    return false;
  }

  // dynamicEntryMatches mirrors connectionMatches for dynamic
  // entries. TreeNode uses it to hide individual rows under an
  // expanded dynamic folder when only a subset matches the filter -
  // without this every dynamic entry stays visible as long as the
  // folder has at least one match.
  dynamicEntryMatches(folderId: string, entryId: string): boolean {
    if (!this.isActive()) return true;
    const entries = tree.dynamicEntries[folderId];
    if (!entries) return true;
    const e = entries.find((x) => x.id === entryId);
    if (!e) return false;
    const n = this.needle();
    if (e.name.toLowerCase().includes(n)) return true;
    if (e.hostname.toLowerCase().includes(n)) return true;
    for (const t of e.tags ?? []) {
      if (t.toLowerCase().includes(n)) return true;
    }
    return false;
  }
}

export const nameFilter = new NameFilterStore();
