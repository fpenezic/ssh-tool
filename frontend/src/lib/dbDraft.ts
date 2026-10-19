// The Database settings of a connection or folder as the editor holds
// them: every field a string, "" = inherit. Tri-state booleans are "on" /
// "off"; DB_NONE is an explicit empty override (stops an inherited
// credential). Pure, unit-tested.

import type { InheritableSettings } from "./api";

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
  return s;
}

export function dbDraftSame(d: DbDraft, s: InheritableSettings | undefined | null): boolean {
  const o = dbDraftFrom(s);
  return (Object.keys(o) as (keyof DbDraft)[]).every((k) =>
    k === "addr" || k === "name" ? d[k].trim() === o[k].trim() : d[k] === o[k],
  );
}
