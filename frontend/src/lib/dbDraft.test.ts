import { describe, expect, it } from "vitest";
import { DB_NONE, applyDbDraft, dbDraftFrom, dbDraftSame } from "./dbDraft";
import type { InheritableSettings } from "./api";

describe("dbDraft", () => {
  it("round-trips settings, empty fields inherit", () => {
    const s = { db_enabled: true, db_engine: "mysql", db_addr: "10.0.0.5:3307", db_read_only: false, db_credential_ref: "c1" };
    const d = dbDraftFrom(s);
    expect(d).toMatchObject({ enabled: "on", engine: "mysql", addr: "10.0.0.5:3307", readOnly: "off", cred: "c1", tunnel: "", tls: "" });
    expect(applyDbDraft({} as InheritableSettings, d)).toEqual({ ...s, db_name: undefined, db_use_tunnel: undefined, db_default: undefined, db_tls: undefined });
    expect(dbDraftSame(d, s)).toBe(true);
  });
  it("an explicit none stops an inherited credential", () => {
    const d = dbDraftFrom({ db_credential_ref: "" });
    expect(d.cred).toBe(DB_NONE);
    expect(applyDbDraft({} as InheritableSettings, d).db_credential_ref).toBe("");
    expect(applyDbDraft({} as InheritableSettings, { ...d, cred: "" }).db_credential_ref).toBeUndefined();
  });
  it("trims the address and name, blank ones inherit", () => {
    const d = { ...dbDraftFrom(null), addr: "  db1 ", name: " " };
    const s = applyDbDraft({} as InheritableSettings, d);
    expect(s.db_addr).toBe("db1");
    expect(s.db_name).toBeUndefined();
    expect(dbDraftSame({ ...d, addr: "db1" }, s)).toBe(true);
  });
});
