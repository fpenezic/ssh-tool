// Database tab state shared outside DbPane: the open handles, and the
// inner tabs (queries, tables) of every Database tab with their SQL and
// last results. It outlives the pane: PaneNode remounts DbPane on a split,
// a move, a detach and a reload, and the tabs must come back as they were.
//
// The handles and each query tab's server session live on the backend
// (app_db.go); the tab list is mirrored there (DbSetTabState) so a window
// that did not open the tab shows the same tabs. Results stay in the
// window that ran them.

import { api, dbEngineInfo, type Connection, type DbHandleInfo, type DbResult, type DbTarget } from "./api";
import { mergeTargets, type ListedTarget } from "./dbDraft";
import { tree, selection } from "./stores.svelte";

export type DbTabKind = "query" | "table" | "sessions" | "er";
export type DbTableView = "data" | "structure" | "ddl";

export interface DbTab {
  key: string; // "q1", "q2", ... / "t:<db>/<schema>/<name>"
  kind: DbTabKind;
  title: string;
  database: string;
  schema?: string; // table tabs
  name?: string; // table tabs
  sql?: string; // query tabs
  view?: DbTableView; // table tabs
  // Table tabs: the rows reached through foreign keys from the table, in
  // order, and the page each level was on (DbTableTab).
  trail?: DbTrailStep[];
  // ER tabs: where boxes were moved to (table -> [x, y]; none = arranged)
  // and whether boxes show only their key columns (unset = by size).
  layout?: Record<string, [number, number]>;
  keysOnly?: boolean;
}

export interface DbTrailStep {
  database: string;
  schema: string;
  name: string;
  where: { column: string; value: string | null; hex?: boolean }[];
  offset: number;
}

// What is saved and mirrored: the tabs and which one is in front.
export interface DbTabsSaved {
  tabs: DbTab[];
  active: string;
}

// A query tab's run state; not saved.
export interface DbRunState {
  running: boolean;
  results: DbResult[];
  shown: number; // index of the result in front
  inTx: boolean;
  error?: string; // a call error (no session, handle closed)
  ranAt?: number;
  // The last Explain of the tab, shown instead of the results until a run
  // or a close (kept here, like the results, so a tab switch keeps it).
  plan?: { statement: string; tree: unknown } | null;
  planErr?: string | null;
}

// Unsent cell edits of a table tab (sessionId + "\0" + key -> count): the
// tab's DbTableTab holds them, and the pane asks before a switch or a
// close drops them (the tab remounts per key).
export const dbTableEdits = $state<Record<string, number>>({});

const IDLE_RUN: DbRunState = Object.freeze({ running: false, results: [], shown: 0, inTx: false }) as DbRunState;

class DbStore {
  // sessionId (handle id) -> info
  infos = $state<Record<string, DbHandleInfo>>({});
  // sessionId -> its tabs; absent = this window has not shown the tab
  // yet (DbPane adopts the backend's copy).
  tabs = $state<Record<string, DbTabsSaved>>({});
  // sessionId + "\0" + tab key -> run state of a query tab.
  runs = $state<Record<string, DbRunState>>({});
  // A request from elsewhere (the tree's "Query" on a table) for a tab to
  // open; the pane with that sessionId consumes it.
  pending = $state<{ sessionId: string; tab: Omit<DbTab, "key"> } | null>(null);
  // The database row last clicked in the tree; drawn as selected while its
  // connection is.
  treeSelected = $state<{ connId: string; id: string } | null>(null);

  set(info: DbHandleInfo) {
    this.infos[info.id] = info;
  }
  get(sessionId: string): DbHandleInfo | undefined {
    return this.infos[sessionId];
  }
  // The run state of a query tab, created on first use. Only from an event
  // handler: creating it inside a $derived is an unsafe state mutation.
  run(sessionId: string, key: string): DbRunState {
    const k = sessionId + "\0" + key;
    // Read back through the store: `a ??= b` yields the raw object, not the
    // state proxy the store keeps, and writes to it would not show.
    if (!this.runs[k]) this.runs[k] = { running: false, results: [], shown: 0, inTx: false };
    return this.runs[k];
  }
  // The run state for reading (templates, $derived); an idle one when the
  // tab has not run yet.
  peek(sessionId: string, key: string): DbRunState {
    return this.runs[sessionId + "\0" + key] ?? IDLE_RUN;
  }
  dropRun(sessionId: string, key: string) {
    delete this.runs[sessionId + "\0" + key];
  }
  // Mirrors the tab list to the backend so another window can adopt it.
  save(sessionId: string) {
    const t = this.tabs[sessionId];
    if (t) api.dbSetTabState(sessionId, JSON.stringify(t)).catch(() => {});
  }
  // Takes over a saved / mirrored tab list ("" or junk = a fresh query tab).
  adopt(sessionId: string, json: string | undefined, database: string) {
    let saved: DbTabsSaved | null = null;
    try {
      const v = json ? JSON.parse(json) : null;
      if (v && Array.isArray(v.tabs)) saved = { tabs: v.tabs.filter((t: DbTab) => t && t.key && t.kind), active: String(v.active ?? "") };
    } catch {
      saved = null;
    }
    if (!saved || saved.tabs.length === 0) {
      saved = { tabs: [{ key: "q1", kind: "query", title: "Query 1", database, sql: "" }], active: "q1" };
    }
    if (!saved.tabs.some((t) => t.key === saved!.active)) saved.active = saved.tabs[0].key;
    this.tabs[sessionId] = saved;
  }
  forget(sessionId: string) {
    delete this.infos[sessionId];
    delete this.tabs[sessionId];
    for (const k of Object.keys(this.runs)) {
      if (k.startsWith(sessionId + "\0")) delete this.runs[k];
    }
  }
  async refresh(): Promise<DbHandleInfo[]> {
    const list = (await api.dbList()) ?? [];
    for (const d of list) this.infos[d.id] = d;
    return list;
  }
}

export const dbTabs = new DbStore();

// The key for another query tab: the lowest free "qN".
export function nextQueryKey(tabs: DbTab[]): string {
  for (let n = 1; ; n++) {
    const k = `q${n}`;
    if (!tabs.some((t) => t.key === k)) return k;
  }
}

export function tableKey(database: string, schema: string, name: string): string {
  return `t:${database}/${schema}/${name}`;
}

// Walks the folder chain the way the resolver does, so the tree's context
// menu reflects a folder-level db_enabled without a resolve round trip.
export function dbEnabledFor(conn: Connection | null | undefined): boolean {
  if (!conn) return false;
  const own = conn.overrides?.db_enabled;
  if (own !== undefined && own !== null) return !!own;
  return dbInheritedFrom(conn.folder_id, "db_enabled") === true;
}

// A db_* setting as the folder chain from folderId upwards sets it.
export function dbInheritedFrom<K extends "db_enabled" | "db_engine" | "db_read_only" | "db_query_timeout" | "kafka_read_only">(
  folderId: string | null | undefined,
  key: K,
): NonNullable<import("./api").InheritableSettings[K]> | undefined {
  for (let i = 0; i < 10000 && folderId; i++) {
    const f = tree.folderById(folderId);
    if (!f) break;
    const v = f.settings?.[key];
    if (v !== undefined && v !== null && v !== "") return v as NonNullable<import("./api").InheritableSettings[K]>;
    folderId = f.parent_id;
  }
  return undefined;
}

// The databases the folders from folderId upwards list, root folder's
// first, each with the folder it comes from.
export function dbFolderTargets(folderId: string | null | undefined): ListedTarget[] {
  const chain: { name: string; list: DbTarget[] | undefined }[] = [];
  for (let i = 0; i < 10000 && folderId; i++) {
    const f = tree.folderById(folderId);
    if (!f) break;
    chain.unshift({ name: f.name, list: f.settings?.db_targets });
    folderId = f.parent_id;
  }
  let out: ListedTarget[] = [];
  for (const c of chain) out = mergeTargets(out, c.list, c.name);
  return out;
}

// A connection's listed databases with its folders' applied (what the
// resolver gives the backend): the folders' first, then its own.
export function dbTargetsFor(conn: Connection | null | undefined): ListedTarget[] {
  if (!conn) return [];
  return mergeTargets(dbFolderTargets(conn.folder_id), conn.overrides?.db_targets, "");
}

// The default database id: the marked one while it is listed, else ""
// (the connection's own).
export function dbDefaultFor(conn: Connection | null | undefined): string {
  const d = conn?.overrides?.db_default_target;
  return d && dbTargetsFor(conn).some((t) => t.id === d) ? d : "";
}

// UTF-16 index <-> UTF-8 byte offset, for the backend's statement ranges.
export function byteOffset(s: string, index: number): number {
  return new TextEncoder().encode(s.slice(0, index)).length;
}
export function charIndex(s: string, bytes: number): number {
  const enc = new TextEncoder();
  let lo = 0;
  let hi = s.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (enc.encode(s.slice(0, mid)).length <= bytes) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

// A Database tab's title: "conn (Database)", or "conn / stage (Database)"
// for a database listed under the connection.
export function dbTabTitle(info: DbHandleInfo | undefined): string {
  if (!info) return "Database";
  return info.target_name ? `${info.name} / ${info.target_name} (Database)` : `${info.name} (Database)`;
}

// A database row under a connection in the tree: its own database (id "")
// and the listed ones, each unless switched off with "Show in tree".
export interface DbTreeRow {
  id: string;
  label: string;
  detail: string;
  isDefault: boolean;
}

export function dbTreeRows(conn: Connection | null | undefined): DbTreeRow[] {
  if (!conn || !dbEnabledFor(conn)) return [];
  const o = conn.overrides ?? {};
  const out: DbTreeRow[] = [];
  const listed = dbTargetsFor(conn);
  const def = dbDefaultFor(conn);
  if (o.db_in_tree !== false) {
    const engine = o.db_engine ?? dbInheritedFrom(conn.folder_id, "db_engine");
    out.push({
      id: "",
      label: o.db_label || o.db_name || "database",
      isDefault: def === "",
      detail: [o.db_label && o.db_name ? o.db_name : "", engine ? dbEngineInfo(engine).short : "", o.db_read_only ? "read-only" : ""]
        .filter(Boolean).join(" · "),
    });
  }
  for (const t of listed) {
    if (t.in_tree === false) continue;
    out.push({
      id: t.id,
      label: t.name || t.database || "database",
      isDefault: def === t.id,
      detail: [t.name && t.database ? t.database : "", t.engine ? dbEngineInfo(t.engine).short : "", t.read_only ? "read-only" : ""]
        .filter(Boolean).join(" · "),
    });
  }
  // The default first; it is what "Double-click opens: Database" opens.
  out.sort((a, b) => Number(b.isDefault) - Number(a.isDefault));
  return out;
}

// A database row under connId holds the selection, so the connection row
// itself should not look selected.
export function dbTreeSelectedUnder(connId: string): boolean {
  return dbTabs.treeSelected?.connId === connId
    && selection.current.kind === "connection" && selection.current.id === connId;
}
