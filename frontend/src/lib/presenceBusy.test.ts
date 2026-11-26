import { describe, it, expect, vi } from "vitest";
vi.mock("./api", () => ({ api: {} }));
import { isBusyElsewhere } from "./presenceTakeover.svelte.ts";

describe("isBusyElsewhere", () => {
  const P = "network profile is active on another machine: ";
  it("finds the payload inside a wrapped dial error", () => {
    expect(isBusyElsewhere(new Error(`target h: dial: direct dial failed (i/o timeout) and tunnel failed: ${P}pid-1|pc-1`))).toEqual({ profileId: "pid-1", owner: "pc-1" });
  });
  it("cuts the owner at a trailing wrapped clause", () => {
    expect(isBusyElsewhere(`via tunnel wg: ${P}pid-1|pc-1 (direct dial failed too: timeout)`)).toEqual({ profileId: "pid-1", owner: "pc-1" });
  });
  it("reads the Wails JSON envelope", () => {
    expect(isBusyElsewhere(new Error(JSON.stringify({ message: `${P}pid-2|pc-2`, kind: "RuntimeError" })))).toEqual({ profileId: "pid-2", owner: "pc-2" });
  });
  it("is null for other errors", () => {
    expect(isBusyElsewhere(new Error("dial tcp: i/o timeout"))).toBeNull();
  });
});
