// Persisted expanded-folder state for the connections + credentials
// trees. Default: every folder is collapsed; only those the user
// explicitly opened are in the set. Stored as a comma-separated id
// list in the SQLite settings table (one row per tree).
//
// Why SQLite and not localStorage: the user picked this so the state
// follows the data file rather than the browser. If they ever sync
// the DB across machines, expansion state goes with it.

import { api } from "./api";

const CONN_KEY = "tree_expanded_connections";
const CRED_KEY = "tree_expanded_credentials";

class ExpandedSet {
  private storageKey: string;
  ids = $state<Set<string>>(new Set());
  private loaded = false;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(storageKey: string) {
    this.storageKey = storageKey;
  }

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(this.storageKey);
      if (raw) {
        this.ids = new Set(raw.split(",").filter(Boolean));
      }
    } catch {
      // missing key - keep empty
    }
    this.loaded = true;
  }

  isExpanded(id: string): boolean {
    return this.ids.has(id);
  }

  toggle(id: string) {
    const next = new Set(this.ids);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.ids = next;
    this.scheduleSave();
  }

  set(id: string, expanded: boolean) {
    if (this.ids.has(id) === expanded) return;
    const next = new Set(this.ids);
    if (expanded) next.add(id);
    else next.delete(id);
    this.ids = next;
    this.scheduleSave();
  }

  // Expand-all / collapse-all. Replaces the whole set: expand gets
  // every id the caller passes (all live folders), collapse empties it.
  setAll(ids: string[], expanded: boolean) {
    this.ids = expanded ? new Set(ids) : new Set();
    this.scheduleSave();
  }

  // Drop ids that no longer exist in the live tree. Called after a load
  // so the stored set doesn't grow forever after deletes.
  prune(liveIds: Set<string>) {
    let changed = false;
    const next = new Set<string>();
    for (const id of this.ids) {
      if (liveIds.has(id)) next.add(id);
      else changed = true;
    }
    if (changed) {
      this.ids = next;
      this.scheduleSave();
    }
  }

  // Debounce so a quick burst of toggles only writes once.
  private scheduleSave() {
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      const v = [...this.ids].join(",");
      // settingsDelete when empty keeps the row absent in the common case
      // (fresh install, never expanded anything).
      const p = v
        ? api.settingsSet(this.storageKey, v)
        : api.settingsDelete(this.storageKey);
      p.catch(console.warn);
      this.saveTimer = null;
    }, 300);
  }
}

export const expandedConnections = new ExpandedSet(CONN_KEY);
export const expandedCredentials = new ExpandedSet(CRED_KEY);
