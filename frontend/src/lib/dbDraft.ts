// The Database settings of a connection or folder as the editor holds
// them: every field a string, "" = inherit. Tri-state booleans are "on" /
// "off"; DB_NONE is an explicit empty override (stops an inherited
// credential). Pure, unit-tested.

import type { DbTarget, InheritableSettings } from "./api";

export const DB_NONE = "__none__";

export interface DbDraft {
  enabled: string;
  engine: string;
  addr: string;
  name: string;
  tunnel: string;
  def: string;
  readOnly: string;
  // Seconds a query tab's statement may run ("" = inherit, "0" = no limit).
  timeout: string;
  tls: string;
  cred: string;
  // The connection's own database in the tree (connections only).
  label: string;
  inTree: string;
  // The default database: a target id, "" = the connection's own.
  defaultTarget: string;
  // The databases this level lists (a folder's go to every connection
  // under it).
  targets: DbTarget[];
  // On a connection: what it changes on databases a folder lists (hidden
  // here, another login), by id. Only set fields are kept.
  overrides: DbTarget[];
}

const enc = (v: boolean | undefined) => (v === true ? "on" : v === false ? "off" : "");
const dec = (s: string) => (s === "on" ? true : s === "off" ? false : undefined);
const encRef = (v: string | undefined) => (v === "" ? DB_NONE : v ?? "");
const decRef = (s: string) => (s === DB_NONE ? "" : s || undefined);

export function dbDraftFrom(s: InheritableSettings | undefined | null): DbDraft {
  const o = s ?? {};
  return {
    enabled: enc(o.db_enabled),
    engine: o.db_engine ?? "",
    addr: o.db_addr ?? "",
    name: o.db_name ?? "",
    tunnel: enc(o.db_use_tunnel),
    def: enc(o.db_default),
    readOnly: enc(o.db_read_only),
    timeout: o.db_query_timeout === undefined || o.db_query_timeout === null ? "" : String(o.db_query_timeout),
    tls: o.db_tls ?? "",
    cred: encRef(o.db_credential_ref),
    label: o.db_label ?? "",
    inTree: enc(o.db_in_tree),
    defaultTarget: o.db_default_target ?? "",
    targets: (o.db_targets ?? []).filter((t) => !t.override).map((t) => ({ ...t })),
    overrides: (o.db_targets ?? []).filter((t) => t.override).map((t) => ({ ...t })),
  };
}

// Writes the draft into settings (in place) and returns them. inherited
// are the ids of the databases the folders above list (null = not known):
// the default may be one of them, and overrides of others are dropped.
export function applyDbDraft<T extends InheritableSettings>(s: T, d: DbDraft, inherited: string[] | null = null): T {
  s.db_enabled = dec(d.enabled);
  s.db_engine = d.engine || undefined;
  s.db_addr = d.addr.trim() || undefined;
  s.db_name = d.name.trim() || undefined;
  s.db_use_tunnel = dec(d.tunnel);
  s.db_default = dec(d.def);
  s.db_read_only = dec(d.readOnly);
  // dbDraftError refuses anything else before a save gets here.
  const secs = d.timeout.trim();
  s.db_query_timeout = secs === "" ? undefined : Number(secs);
  s.db_tls = d.tls || undefined;
  s.db_credential_ref = decRef(d.cred);
  s.db_label = d.label.trim() || undefined;
  s.db_in_tree = dec(d.inTree);
  s.db_default_target = d.targets.some((t) => t.id === d.defaultTarget) || (inherited ?? []).includes(d.defaultTarget) ? d.defaultTarget : undefined;
  const targets = [...cleanTargets(d.targets), ...cleanOverrides(d.overrides, inherited)];
  s.db_targets = targets.length ? targets : undefined;
  return s;
}

// A listed database as stored: trimmed, blank fields left out (they take
// the connection's value), "" kept only for an explicit "no login".
function cleanTargets(list: DbTarget[]): DbTarget[] {
  return list.map((t) => {
    const out: DbTarget = { id: t.id, name: (t.name ?? "").trim() };
    const str = (v: string | undefined) => (v ?? "").trim() || undefined;
    if (str(t.engine)) out.engine = str(t.engine);
    if (str(t.addr)) out.addr = str(t.addr);
    if (str(t.database)) out.database = str(t.database);
    if (t.credential_ref !== undefined && t.credential_ref !== null) out.credential_ref = t.credential_ref;
    if (t.read_only !== undefined && t.read_only !== null) out.read_only = t.read_only;
    if (str(t.tls)) out.tls = str(t.tls);
    if (t.use_tunnel !== undefined && t.use_tunnel !== null) out.use_tunnel = t.use_tunnel;
    if (t.in_tree === false) out.in_tree = false;
    return out;
  });
}

// The overrides worth storing: ones that change something. While the
// inherited ids are known (the editor), also only ones a folder still
// lists.
function cleanOverrides(list: DbTarget[], inherited: string[] | null): DbTarget[] {
  const out: DbTarget[] = [];
  for (const t of list) {
    if (inherited && !inherited.includes(t.id)) continue;
    // Every field it sets is kept (the editor sets the login and the tree
    // row; another app or the MCP may set the rest).
    const o: DbTarget = { id: t.id, name: (t.name ?? "").trim(), override: true };
    let set = !!o.name;
    for (const k of ["engine", "addr", "database", "credential_ref", "read_only", "tls", "use_tunnel", "in_tree"] as const) {
      const v = t[k];
      if (v === undefined || v === null || (typeof v === "string" && k !== "credential_ref" && !v.trim())) continue;
      (o as any)[k] = typeof v === "string" && k !== "credential_ref" ? v.trim() : v;
      set = true;
    }
    if (set) out.push(o);
  }
  return out;
}

// What makes the draft unsavable ("" = nothing): a query timeout that is
// not whole seconds within the backend's uint32. Saving it as "inherit"
// would silently drop a limit that was set.
export function dbDraftError(d: DbDraft): string {
  const secs = d.timeout.trim();
  if (secs !== "" && (!/^\d+$/.test(secs) || Number(secs) > 4294967295)) {
    return `Query timeout: "${secs}" is not a whole number of seconds (0 for no limit, empty to inherit)`;
  }
  // A listed database needs something of its own: one with only a login
  // would read as a leftover override (mergeTargets drops those) and
  // vanish after the save.
  const blank = (v: string | undefined) => !(v ?? "").trim();
  if (d.targets.some((t) => blank(t.name) && blank(t.database) && blank(t.engine) && blank(t.addr))) {
    return "A listed database needs a name (or its own database, engine or address)";
  }
  return "";
}

// A listed database with where it comes from: the folder that lists it
// ("" = this level's own).
export type ListedTarget = DbTarget & { from: string };

// The resolver's mergeTargets (internal/resolver): the list from above
// first, then this level's; an entry with an id already listed adjusts it
// (set fields win, a name renames), an override with nothing to adjust is
// dropped.
export function mergeTargets(base: ListedTarget[], over: DbTarget[] | null | undefined, from: string): ListedTarget[] {
  const out = base.filter((t) => !t.override).map((t) => ({ ...t }));
  for (const t of over ?? []) {
    const i = out.findIndex((b) => b.id === t.id);
    if (i < 0) {
      // A blank entry is an override an older app saved without its flag.
      const blank = !(t.name ?? "").trim() && !(t.database ?? "").trim() && !(t.engine ?? "").trim() && !(t.addr ?? "").trim();
      if (!t.override && !blank) out.push({ ...t, from });
      continue;
    }
    const m = out[i];
    for (const k of ["engine", "addr", "database", "credential_ref", "read_only", "tls", "use_tunnel", "in_tree"] as const) {
      if (t[k] !== undefined && t[k] !== null) (m as any)[k] = t[k];
    }
    if (t.name) m.name = t.name;
  }
  return out;
}

export function newTargetId(): string {
  return Math.random().toString(36).slice(2, 10);
}

export function dbDraftSame(d: DbDraft, s: InheritableSettings | undefined | null): boolean {
  const o = dbDraftFrom(s);
  return (Object.keys(o) as (keyof DbDraft)[]).every((k) => {
    if (k === "targets") return JSON.stringify(cleanTargets(d.targets)) === JSON.stringify(cleanTargets(o.targets));
    if (k === "overrides") return JSON.stringify(cleanOverrides(d.overrides, null)) === JSON.stringify(cleanOverrides(o.overrides, null));
    return k === "addr" || k === "name" || k === "label" || k === "timeout" ? d[k].trim() === o[k].trim() : d[k] === o[k];
  });
}
