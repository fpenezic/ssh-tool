// Failed systemd units the user chose to ignore: a unit that has been
// broken on purpose for months should not keep the status bar's "N failed"
// chip lit, while a new failure on the same host still shows. A unit can be
// ignored on one host or on a folder, which covers every host below it
// (dynamic inventory hosts included) - one image-wide quirk across a cloud
// project is one click, not one per host. Stored in settings so it follows
// the profile like the connection does.

import { api } from "./api";
import { tree } from "./stores.svelte";

// Scope ids: a connection id for one host, "folder:<id>" for a folder,
// "local:<shell>" for a local shell (WSL distro or this machine).
const keyOf = (scope: string) => `failed_units_ignore:${scope}`;
export const folderScope = (folderId: string) => `folder:${folderId}`;

export interface IgnoreScope {
  scope: string;
  label: string;
  isFolder: boolean;
}

class FailedIgnoreStore {
  byScope = $state<Record<string, string[]>>({});
  private loading = new Set<string>();

  // Returns what is known now and loads the list once in the background.
  list(scope: string): string[] {
    if (!scope) return [];
    if (!(scope in this.byScope) && !this.loading.has(scope)) {
      this.loading.add(scope);
      api.settingsGet(keyOf(scope))
        .then((raw) => {
          const v = raw ? JSON.parse(raw) : [];
          this.byScope = { ...this.byScope, [scope]: Array.isArray(v) ? v : [] };
        })
        .catch(() => { this.byScope = { ...this.byScope, [scope]: [] }; })
        .finally(() => this.loading.delete(scope));
    }
    return this.byScope[scope] ?? [];
  }

  // The host itself, then its folders from the nearest up. A dynamic host
  // ("dyn:<entry>") is found through the entries loaded into the tree; one
  // restored before its folder was ever expanded only has the host scope.
  scopesOf(connectionId: string): IgnoreScope[] {
    if (!connectionId) return [];
    // "local:<shell>" is a local shell (no folders above it).
    const out: IgnoreScope[] = [{ scope: connectionId, label: connectionId.startsWith("local:") ? "this machine" : "this host", isFolder: false }];
    if (connectionId.startsWith("local:")) return out;
    let folderId: string | null = null;
    if (connectionId.startsWith("dyn:")) {
      const entryId = connectionId.slice(4);
      for (const [fid, list] of Object.entries(tree.dynamicEntries)) {
        if (list.some((e) => e.id === entryId)) { folderId = fid; break; }
      }
    } else {
      folderId = tree.connectionById(connectionId)?.folder_id ?? null;
    }
    let f = tree.folderById(folderId);
    let guard = 0;
    while (f && guard++ < 1000) {
      out.push({ scope: folderScope(f.id), label: f.name, isFolder: true });
      f = tree.folderById(f.parent_id ?? null);
    }
    return out;
  }

  // Where this unit is ignored for this host (empty = it counts).
  ignoredIn(connectionId: string, unit: string): IgnoreScope[] {
    return this.scopesOf(connectionId).filter((s) => this.list(s.scope).includes(unit));
  }

  isIgnored(connectionId: string, unit: string): boolean {
    return this.ignoredIn(connectionId, unit).length > 0;
  }

  async set(scope: string, unit: string, on: boolean) {
    const cur = this.list(scope);
    if (cur.includes(unit) === on) return;
    const next = on ? [...cur, unit].sort() : cur.filter((u) => u !== unit);
    this.byScope = { ...this.byScope, [scope]: next };
    await api.settingsSet(keyOf(scope), JSON.stringify(next)).catch(console.warn);
  }

  // Stop ignoring: clears the unit from every scope that covers this host.
  async unignore(connectionId: string, unit: string) {
    for (const s of this.ignoredIn(connectionId, unit)) await this.set(s.scope, unit, false);
  }

  // Failed units still worth a warning on this host.
  effective(connectionId: string, names: string[] | null | undefined): string[] {
    const ign = new Set(this.scopesOf(connectionId).flatMap((s) => this.list(s.scope)));
    return (names ?? []).filter((u) => !ign.has(u));
  }
}

export const failedIgnore = new FailedIgnoreStore();
