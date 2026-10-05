import { describe, it, expect } from "vitest";
import { jumpReferrers } from "./jumpRefs";

const ref = (id: string, via?: any) => ({ kind: "chain", chain: { hostname: "", connection_id: id, via } });

describe("jumpReferrers", () => {
  it("finds folders and connections that jump through a connection, on any hop", () => {
    const folders = [
      { name: "Customer A", settings: { jump_host: ref("b1") } },
      { name: "Other", settings: { jump_host: { kind: "none" } } },
    ];
    const connections = [
      { id: "b1", name: "bastion", overrides: {} },
      { id: "c1", name: "db-01", overrides: { jump_host: { kind: "chain", chain: { hostname: "edge.example.com", via: { connection_id: "b1" } } } } },
      { id: "c2", name: "web-01", overrides: {} },
    ];
    expect(jumpReferrers("b1", folders, connections)).toEqual(["folder Customer A", "db-01"]);
    expect(jumpReferrers("c2", folders, connections)).toEqual([]);
  });

  it("ignores a connection that references itself", () => {
    expect(jumpReferrers("b1", [], [{ id: "b1", name: "bastion", overrides: { jump_host: ref("b1") } }])).toEqual([]);
  });
});
