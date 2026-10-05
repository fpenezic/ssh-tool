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

import { bastionUsage, dynRef } from "./jumpRefs";

describe("bastionUsage", () => {
  const folders = [
    { id: "f1", name: "Customer A", settings: { jump_host: ref("b1") } },
    { id: "f2", name: "Lab", settings: { jump_host: { kind: "chain", chain: { hostname: "10.0.0.5" } } } },
    { id: "pv", name: "pxmx", settings: {} },
  ];
  const connections = [
    { id: "b1", name: "bastion", hostname: "bastion.example.com", overrides: { jump_host: ref("b1") } },
    { id: "c1", name: "db-01", hostname: "db.example.com", overrides: { jump_host: { kind: "chain", chain: { hostname: "BASTION.example.com", port: 22 } } } },
    { id: "c2", name: "edge", hostname: "edge.example.com", overrides: { port: 2222 } },
    { id: "c3", name: "app", hostname: "app.example.com", overrides: { jump_host: { kind: "chain", chain: { hostname: "edge.example.com" } } } },
  ];
  const entries = { pv: [{ external_id: "qemu/101", name: "vpn-01", hostname: "10.0.0.5" }] };
  const dynFolders = { pv: { config: { bastion_external_id: "qemu/101" } } };
  const raw = bastionUsage(folders, connections, entries, dynFolders);
  const m = new Map([...raw].map(([k, v]) => [k, v.map((u) => u.label)]));

  it("counts references and typed-in hops by address, never the bastion itself", () => {
    expect(m.get("b1")).toEqual(["folder Customer A", "db-01"]);
  });
  it("matches a typed hop's port, not just the host", () => {
    expect(m.get("c2")).toBeUndefined();
  });
  it("marks inventory hosts by hostname and by the folder's bastion route", () => {
    expect(m.get(dynRef("pv", "qemu/101"))).toEqual(["folder Lab", "inventory folder pxmx"]);
  });
});

describe("bastionUsage ids", () => {
  it("carries kind and id for revealing the user in the tree", () => {
    const m = bastionUsage(
      [{ id: "f1", name: "A", settings: { jump_host: ref("b1") } }],
      [{ id: "b1", name: "bastion", hostname: "b.example.com" }],
      {}, {},
    );
    expect(m.get("b1")).toEqual([{ kind: "folder", id: "f1", label: "folder A" }]);
  });
});
