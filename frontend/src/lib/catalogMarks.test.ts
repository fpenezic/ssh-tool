import { describe, it, expect } from "vitest";
import { catalogStateLabel, parseMarker } from "./catalogMarks";
import type { CatalogInfo } from "./api";

const cat = (over: Partial<CatalogInfo>): CatalogInfo => ({
  id: "c", name: "Team", base_url: "", server_name: "", account_name: "", root_folder_id: "r",
  last_sync_at: 1, last_error: "", sync_interval_sec: 300, state: "ok", slots: 0, unbound: 0, removed: 0, ...over,
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
