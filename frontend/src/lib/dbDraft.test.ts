import { describe, expect, it } from "vitest";
import { DB_NONE, applyDbDraft, dbDraftError, dbDraftFrom, dbDraftSame, mergeTargets } from "./dbDraft";
import type { InheritableSettings } from "./api";

describe("dbDraft", () => {
  it("round-trips settings, empty fields inherit", () => {
    const s = { db_enabled: true, db_engine: "mysql", db_addr: "10.0.0.5:3307", db_read_only: false, db_credential_ref: "c1" };
    const d = dbDraftFrom(s);
    expect(d).toMatchObject({ enabled: "on", engine: "mysql", addr: "10.0.0.5:3307", readOnly: "off", cred: "c1", tunnel: "", tls: "" });
    expect(applyDbDraft({} as InheritableSettings, d)).toEqual({ ...s, db_name: undefined, db_use_tunnel: undefined, db_default: undefined, db_tls: undefined, db_targets: undefined });
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
  it("keeps listed databases, blank fields left to the connection", () => {
    const d = dbDraftFrom({ db_enabled: true });
    d.targets.push({ id: "a1", name: " stage ", database: "app_stage", addr: " ", credential_ref: "" , read_only: true });
    const s = applyDbDraft({} as InheritableSettings, d);
    expect(s.db_targets).toEqual([{ id: "a1", name: "stage", database: "app_stage", credential_ref: "", read_only: true }]);
    expect(dbDraftSame(dbDraftFrom(s), s)).toBe(true);
    expect(dbDraftSame({ ...dbDraftFrom(s), targets: [] }, s)).toBe(false);
  });
});

describe("query timeout", () => {
  it("is inherited when empty, a number of seconds otherwise", () => {
    const d = dbDraftFrom({ db_query_timeout: 30 });
    expect(d.timeout).toBe("30");
    const s: InheritableSettings = {};
    applyDbDraft(s, { ...d, timeout: "" });
    expect(s.db_query_timeout).toBeUndefined();
    applyDbDraft(s, { ...d, timeout: " 0 " });
    expect(s.db_query_timeout).toBe(0);
    applyDbDraft(s, { ...d, timeout: "30" });
    expect(s.db_query_timeout).toBe(30);
    expect(dbDraftError({ ...d, timeout: "30" })).toBe("");
    expect(dbDraftError({ ...d, timeout: "" })).toBe("");
    expect(dbDraftError({ ...d, timeout: "1.5" })).toMatch(/whole number/);
    expect(dbDraftError({ ...d, timeout: "4294967296" })).toMatch(/whole number/);
    expect(dbDraftError({ ...d, targets: [{ id: "a", name: " ", credential_ref: "c1" }] })).toMatch(/needs a name/);
    expect(dbDraftError({ ...d, targets: [{ id: "a", name: "stage" }] })).toBe("");
  });
});

describe("databases a folder lists", () => {
  const folder = mergeTargets([], [{ id: "p", name: "prod", database: "app" }, { id: "s", name: "stage", read_only: true }], "cluster");
  it("come first, adjusted by a connection's override, orphans dropped", () => {
    const m = mergeTargets(folder, [{ id: "own", name: "local" }, { id: "s", name: "", override: true, in_tree: false }, { id: "x", name: "", override: true, in_tree: false }], "");
    expect(m.map((t) => [t.id, t.from])).toEqual([["p", "cluster"], ["s", "cluster"], ["own", ""]]);
    expect(m[1]).toMatchObject({ name: "stage", read_only: true, in_tree: false });
    expect(folder[1].in_tree).toBeUndefined();
  });
  it("keep only overrides that change something and that a folder still lists", () => {
    const d = dbDraftFrom({ db_targets: [{ id: "s", name: "", override: true, in_tree: false }] });
    expect(d.targets).toEqual([]);
    d.overrides.push({ id: "p", name: "", override: true }, { id: "gone", name: "", override: true, credential_ref: "c1" });
    const s = applyDbDraft({} as InheritableSettings, { ...d, defaultTarget: "p" }, ["p", "s"]);
    expect(s.db_targets).toEqual([{ id: "s", name: "", override: true, in_tree: false }]);
    expect(s.db_default_target).toBe("p");
    expect(dbDraftSame(dbDraftFrom(s), s)).toBe(true);
    expect(applyDbDraft({} as InheritableSettings, { ...d, defaultTarget: "p" }).db_default_target).toBeUndefined();
  });
  it("keep every field an override sets, a shown override of a folder-hidden one too", () => {
    const d = dbDraftFrom({ db_targets: [{ id: "s", name: "", override: true, read_only: true, in_tree: true }] });
    const s = applyDbDraft({} as InheritableSettings, d, ["s"]);
    expect(s.db_targets).toEqual([{ id: "s", name: "", override: true, read_only: true, in_tree: true }]);
  });
  it("drop a blank entry an older app saved without its flag", () => {
    const m = mergeTargets(folder, [{ id: "gone", name: "", in_tree: false, credential_ref: "c" }], "");
    expect(m.map((t) => t.id)).toEqual(["p", "s"]);
  });
  it("read a flagless blank entry as an override, so the save is not blocked", () => {
    const d = dbDraftFrom({ db_targets: [{ id: "s", name: "", credential_ref: "c1" }] });
    expect(d.targets).toEqual([]);
    expect(dbDraftError(d)).toBe("");
    const s = applyDbDraft({} as InheritableSettings, d, ["s"]);
    expect(s.db_targets).toEqual([{ id: "s", name: "", override: true, credential_ref: "c1" }]);
  });
});
