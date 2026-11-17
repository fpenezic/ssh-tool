// Which catalog folders a user keeps out of their tree. A preference, not
// access: hidden folders still sync and come back the moment they are
// shown again. Stored by the folder's catalog uuid (the same on every
// machine), in a synced setting, so it follows the profile.

import { api, type Connection, type Folder } from "./api";

const KEY = "catalog_hidden_folders";

class CatalogVisibilityStore {
  // catalog id -> hidden folder uuids
  hidden = $state<Record<string, string[]>>({});
  private loaded = false;

  async load() {
    try {
      const raw = await api.settingsGet(KEY);
      const v = raw ? JSON.parse(raw) : {};
      this.hidden = v && typeof v === "object" ? v : {};
    } catch {
      this.hidden = {};
    }
    this.loaded = true;
  }

  isHidden(catalogId: string, uuid: string): boolean {
    return (this.hidden[catalogId] ?? []).includes(uuid);
  }

  count(catalogId: string): number {
    return (this.hidden[catalogId] ?? []).length;
  }

  async set(catalogId: string, uuid: string, hide: boolean) {
    if (!this.loaded) await this.load();
    const cur = new Set(this.hidden[catalogId] ?? []);
    if (hide) cur.add(uuid);
    else cur.delete(uuid);
    const next = { ...this.hidden };
    if (cur.size) next[catalogId] = [...cur];
    else delete next[catalogId];
    this.hidden = next;
    const empty = Object.keys(next).length === 0;
    await (empty ? api.settingsDelete(KEY) : api.settingsSet(KEY, JSON.stringify(next)));
  }

  async showAll(catalogId: string) {
    if (!this.loaded) await this.load();
    const next = { ...this.hidden };
    delete next[catalogId];
    this.hidden = next;
    await (Object.keys(next).length === 0 ? api.settingsDelete(KEY) : api.settingsSet(KEY, JSON.stringify(next)));
  }

  /** The folders and connections left once hidden catalog folders (and
   *  everything under them) are taken out. */
  filter(folders: Folder[], conns: Connection[]): { folders: Folder[]; connections: Connection[] } {
    if (Object.keys(this.hidden).length === 0) return { folders, connections: conns };
    const byId = new Map(folders.map((f) => [f.id, f]));
    const memo = new Map<string, boolean>();
    const gone = (id: string | null | undefined, depth = 0): boolean => {
      if (!id || depth > 64) return false;
      const m = memo.get(id);
      if (m !== undefined) return m;
      const f = byId.get(id);
      let h = false;
      if (f) {
        h = (!!f.catalog_id && !!f.remote_uuid && this.isHidden(f.catalog_id, f.remote_uuid)) || gone(f.parent_id, depth + 1);
      }
      memo.set(id, h);
      return h;
    };
    return {
      folders: folders.filter((f) => !gone(f.id)),
      connections: conns.filter((c) => !gone(c.folder_id)),
    };
  }
}

export const catalogVisibility = new CatalogVisibilityStore();
