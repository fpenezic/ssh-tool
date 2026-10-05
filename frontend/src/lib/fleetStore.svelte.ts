// Fleet tools state: which fleet dialog is open, over which hosts, and the
// last results worth keeping (a folder's facts snapshot, per-connection TLS
// expiry for the tree badges). Snapshots live in the settings table so a
// restart keeps them; they are what was measured, never refreshed on
// their own.

import { api, type FactsHostResult, type TLSCertResult } from "./api";
import { tree } from "./stores.svelte";

export type FleetTool = "facts" | "tls" | "compare" | "copykey";

export interface FleetOpen {
  tool: FleetTool;
  ids: string[];
  // Set when the hosts are "everything in this folder": the facts
  // snapshot is stored per folder.
  folderId?: string;
  label: string;
  // Facts: open straight on the stored report instead of the form.
  showSnapshot?: boolean;
}

export interface FactsSnapshot {
  at: number;
  facts: string[];
  results: FactsHostResult[];
}

export interface TlsMark {
  // Host name at check time, for cards on folders that hold no facts
  // snapshot to look it up in (older marks lack it).
  name?: string;
  days_left: number;
  not_after: number;
  port: number;
  at: number;
}

// The badge threshold: under this many days a cert shows in the tree.
export const TLS_WARN_DAYS = 14;

class FleetStore {
  open = $state<FleetOpen | null>(null);
  snapshots = $state<Record<string, FactsSnapshot>>({});
  tls = $state<Record<string, TlsMark>>({});
  private loaded = false;

  async init() {
    if (this.loaded) return;
    this.loaded = true;
    try {
      const raw = await api.settingsGet("fleet_tls");
      if (raw) this.tls = JSON.parse(raw);
    } catch { /* no marks yet */ }
  }

  show(o: FleetOpen) {
    this.open = o;
  }
  close() {
    this.open = null;
  }

  async snapshot(folderId: string): Promise<FactsSnapshot | null> {
    if (this.snapshots[folderId]) return this.snapshots[folderId];
    try {
      const raw = await api.settingsGet(`fleet_facts:${folderId}`);
      if (!raw) return null;
      const s = JSON.parse(raw) as FactsSnapshot;
      this.snapshots = { ...this.snapshots, [folderId]: s };
      return s;
    } catch {
      return null;
    }
  }

  async saveSnapshot(folderId: string, s: FactsSnapshot) {
    this.snapshots = { ...this.snapshots, [folderId]: s };
    await api.settingsSet(`fleet_facts:${folderId}`, JSON.stringify(s)).catch(console.warn);
  }

  // Keep the soonest-expiring port per connection; a host that answered on
  // no port loses its mark.
  async recordTls(results: TLSCertResult[]) {
    const next = { ...this.tls };
    const seen = new Set<string>();
    for (const r of results) {
      if (r.state === "skipped") continue;
      if (!seen.has(r.connection_id)) {
        delete next[r.connection_id];
        seen.add(r.connection_id);
      }
      if (r.state !== "ok") continue;
      const cur = next[r.connection_id];
      if (!cur || r.days_left < cur.days_left) {
        next[r.connection_id] = { name: r.name, days_left: r.days_left, not_after: r.not_after, port: r.port, at: Date.now() };
      }
    }
    this.tls = next;
    await api.settingsSet("fleet_tls", JSON.stringify(next)).catch(console.warn);
  }

  // Forget the marks of these hosts (card + tree badge) until the next
  // check - for certs nobody will renew (lab, self-signed, retired).
  async clearTls(ids: string[]) {
    const next = { ...this.tls };
    for (const id of ids) delete next[id];
    this.tls = next;
    await api.settingsSet("fleet_tls", JSON.stringify(next)).catch(console.warn);
  }

  // Days left as of now, from the stored expiry - the badge must not show
  // "9d" for weeks after the check.
  daysLeft(connectionId: string): number | null {
    const m = this.tls[connectionId];
    if (!m) return null;
    return Math.floor((m.not_after * 1000 - Date.now()) / 86_400_000);
  }
}

export const fleet = new FleetStore();

// Display name for a fleet host id: the saved connection, else the loaded
// dynamic entry ("dyn:<id>"), else what the caller last saw.
export function fleetHostName(id: string, fallback?: string): string {
  if (id.startsWith("dyn:")) {
    const eid = id.slice(4);
    for (const list of Object.values(tree.dynamicEntries)) {
      const e = list.find((x) => x.id === eid);
      if (e) return e.name || e.hostname || fallback || id;
    }
    return fallback || id;
  }
  return tree.connectionById(id)?.name ?? fallback ?? id;
}

// Dynamic (cloud inventory) folders under folderId, itself included, whose
// entries were never loaded into the tree - a subfolder nobody expanded.
// folderHostIds cannot see their hosts until they are.
export function unloadedDynamicUnder(folderId: string): string[] {
  const out: string[] = [];
  const walk = (fid: string) => {
    if (tree.isDynamic(fid) && tree.dynamicEntries[fid] === undefined) out.push(fid);
    for (const f of tree.folders.filter((x) => x.parent_id === fid)) walk(f.id);
  };
  walk(folderId);
  return out;
}

// folderHostIds after loading the cached entries of every dynamic subfolder
// that was never expanded. Reads what the last inventory refresh stored;
// it does not refresh the provider.
export async function folderHostIdsLoaded(folderIds: string[]): Promise<string[]> {
  const missing = [...new Set(folderIds.flatMap(unloadedDynamicUnder))];
  await Promise.all(missing.map((fid) => tree.loadDynamicEntries(fid)));
  return [...new Set(folderIds.flatMap(folderHostIds))];
}

// Every SSH host under a folder, subfolders included: saved connections
// plus the loaded entries of dynamic (cloud inventory) folders as
// "dyn:<id>". Local shells, RDP and VNC-only entries are left out - there
// is nothing to run a command on.
export function folderHostIds(folderId: string): string[] {
  const out: string[] = [];
  const walk = (fid: string) => {
    for (const c of tree.connectionsIn(fid)) {
      if ((c.protocol || "ssh") === "ssh") out.push(c.id);
    }
    for (const e of tree.dynamicEntries[fid] ?? []) out.push(`dyn:${e.id}`);
    for (const f of tree.folders.filter((x) => x.parent_id === fid)) walk(f.id);
  };
  walk(folderId);
  return out;
}
