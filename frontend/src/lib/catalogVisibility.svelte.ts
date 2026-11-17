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
  // Bumped by every change: a read that started before one is stale and
  // must not put the older preference back.
  private version = 0;
  // Changes run one after another, each building on the one before.
  private queue: Promise<void> = Promise.resolve();

  async load() {
    const v = this.version;
    let next: Record<string, string[]> = {};
    try {
      const raw = await api.settingsGet(KEY);
      const parsed = raw ? JSON.parse(raw) : {};
      next = parsed && typeof parsed === "object" ? parsed : {};
    } catch {
      next = {};
    }
    if (v !== this.version) return;
    this.hidden = next;
    this.loaded = true;
  }

  isHidden(catalogId: string, uuid: string): boolean {
    return (this.hidden[catalogId] ?? []).includes(uuid);
  }

  count(catalogId: string): number {
    return (this.hidden[catalogId] ?? []).length;
  }

  private change(mut: (cur: Record<string, string[]>) => Record<string, string[]>): Promise<void> {
    const run = async () => {
      if (!this.loaded) await this.load();
      this.version++;
      const next = mut({ ...this.hidden });
      this.hidden = next;
      this.loaded = true;
      await (Object.keys(next).length === 0 ? api.settingsDelete(KEY) : api.settingsSet(KEY, JSON.stringify(next)));
    };
    const p = this.queue.then(run, run);
    this.queue = p.catch(() => {});
    return p;
  }

  set(catalogId: string, uuid: string, hide: boolean): Promise<void> {
    return this.change((next) => {
      const cur = new Set(next[catalogId] ?? []);
      if (hide) cur.add(uuid);
      else cur.delete(uuid);
      if (cur.size) next[catalogId] = [...cur];
      else delete next[catalogId];
      return next;
    });
  }

  showAll(catalogId: string): Promise<void> {
    return this.change((next) => {
      delete next[catalogId];
      return next;
    });
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
