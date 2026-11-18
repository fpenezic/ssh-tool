import { describe, it, expect } from "vitest";
import { catalogStateLabel, parseMarker, parseSshToolLink } from "./catalogMarks";
import type { CatalogInfo } from "./api";

const cat = (over: Partial<CatalogInfo>): CatalogInfo => ({
  id: "c", name: "Team", base_url: "", server_name: "", account_name: "", root_folder_id: "r",
  last_sync_at: 1, last_error: "", sync_interval_sec: 300, state: "ok", slots: 0, unbound: 0, removed: 0, network_profile_id: "", ...over,
});

describe("parseMarker", () => {
  it("reads both marker kinds", () => {
    expect(parseMarker("cat-unbound:c1/s1")).toEqual({ unbound: true, catalogId: "c1", uuid: "s1" });
    expect(parseMarker("cat-missing:c1/r1")).toEqual({ unbound: false, catalogId: "c1", uuid: "r1" });
  });
  it("leaves ordinary ids alone", () => {
    expect(parseMarker("8f3c")).toBeNull();
    expect(parseMarker("")).toBeNull();
    expect(parseMarker(null)).toBeNull();
  });
});

describe("catalogStateLabel", () => {
  it("gives every state its action", () => {
    expect(catalogStateLabel(cat({ state: "no_login" })).action).toBe("signin");
    expect(catalogStateLabel(cat({ state: "signed_out" })).action).toBe("signin");
    expect(catalogStateLabel(cat({ state: "error" })).action).toBe("sync");
    expect(catalogStateLabel(cat({ state: "locked" })).action).toBe("unlock");
    expect(catalogStateLabel(cat({ state: "signing_in" })).action).toBe("cancel");
    expect(catalogStateLabel(cat({ state: "syncing" })).tone).toBe("busy");
  });
  it("leads to unbound slots, and says nothing when all is well", () => {
    const l = catalogStateLabel(cat({ unbound: 2 }));
    expect(l.text).toBe("2 slots to bind");
    expect(l.action).toBe("slots");
    expect(catalogStateLabel(cat({})).text).toBe("");
  });
});

describe("parseSshToolLink", () => {
  it("reads a catalog enrollment link", () => {
    expect(parseSshToolLink("ssh-tool://catalog/add?url=https%3A%2F%2Fcatalog.example.com&code=SDKGM-F02SJ-6CM90-CPKSX")).toEqual({
      kind: "catalog", url: "https://catalog.example.com", code: "SDKGM-F02SJ-6CM90-CPKSX",
    });
  });
  it("keeps a look-alike URL exactly as sent", () => {
    const l = parseSshToolLink("ssh-tool://catalog/add?url=https%3A%2F%2Fcata1og.example.com.evil.test&code=X");
    expect(l).toEqual({ kind: "catalog", url: "https://cata1og.example.com.evil.test", code: "X" });
  });
  it("reads an import link", () => {
    expect(parseSshToolLink("ssh-tool://import?source=https%3A%2F%2Fx.example.com%2Fa.toml")).toEqual({ kind: "import", source: "https://x.example.com/a.toml" });
  });
  it("reads a catalog open link and refuses a malformed one", () => {
    const u = "3f2b8c1e-9a4d-4e6f-8b2a-1c3d5e7f9a0b";
    expect(parseSshToolLink(`ssh-tool://catalog/open?url=https%3A%2F%2Fcatalog.example.com&kind=connections&uuid=${u.toUpperCase()}`)).toEqual({
      kind: "catalog_open", url: "https://catalog.example.com", recordKind: "connections", uuid: u,
    });
    expect(parseSshToolLink(`ssh-tool://catalog/open?url=https%3A%2F%2Fc.example.com&kind=forwards&uuid=${u}`)).toBeNull();
    expect(parseSshToolLink("ssh-tool://catalog/open?url=https%3A%2F%2Fc.example.com&kind=folders&uuid=../../x")).toBeNull();
    expect(parseSshToolLink(`ssh-tool://catalog/open?kind=slots&uuid=${u}`)).toBeNull();
  });
  it("ignores other links and incomplete ones", () => {
    expect(parseSshToolLink("https://example.com")).toBeNull();
    expect(parseSshToolLink("ssh-tool://catalog/add?url=https%3A%2F%2Fa.example.com")).toBeNull();
    expect(parseSshToolLink("ssh-tool://other")).toBeNull();
  });
});
