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
  tls: string;
  cred: string;
  // The databases listed under a connection (not on folders).
  targets: DbTarget[];
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
    tls: o.db_tls ?? "",
    cred: encRef(o.db_credential_ref),
    targets: (o.db_targets ?? []).map((t) => ({ ...t })),
  };
}

// Writes the draft into settings (in place) and returns them.
export function applyDbDraft<T extends InheritableSettings>(s: T, d: DbDraft): T {
  s.db_enabled = dec(d.enabled);
  s.db_engine = d.engine || undefined;
  s.db_addr = d.addr.trim() || undefined;
  s.db_name = d.name.trim() || undefined;
  s.db_use_tunnel = dec(d.tunnel);
  s.db_default = dec(d.def);
  s.db_read_only = dec(d.readOnly);
  s.db_tls = d.tls || undefined;
  s.db_credential_ref = decRef(d.cred);
  const targets = cleanTargets(d.targets);
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
    return out;
  });
}

export function newTargetId(): string {
  return Math.random().toString(36).slice(2, 10);
}

export function dbDraftSame(d: DbDraft, s: InheritableSettings | undefined | null): boolean {
  const o = dbDraftFrom(s);
  return (Object.keys(o) as (keyof DbDraft)[]).every((k) => {
    if (k === "targets") return JSON.stringify(cleanTargets(d.targets)) === JSON.stringify(cleanTargets(o.targets));
    return k === "addr" || k === "name" ? d[k].trim() === o[k].trim() : d[k] === o[k];
  });
}
