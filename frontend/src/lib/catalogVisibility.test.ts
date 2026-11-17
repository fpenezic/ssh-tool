import { describe, it, expect, vi } from "vitest";

vi.mock("./api", () => ({ api: {} }));

import { catalogVisibility } from "./catalogVisibility.svelte";

const f = (id: string, parent: string | null, uuid: string | null, cat = "c1") =>
  ({ id, parent_id: parent, remote_uuid: uuid, catalog_id: uuid || id === "root" ? cat : null, name: id }) as never;
const c = (id: string, folder: string | null) => ({ id, folder_id: folder, name: id }) as never;

describe("catalogVisibility.filter", () => {
  const folders = [f("root", null, null), f("a", "root", "ua"), f("a1", "a", "ua1"), f("b", "root", "ub"), f("mine", null, null, "")];
  const conns = [c("ca", "a"), c("ca1", "a1"), c("cb", "b"), c("cm", "mine"), c("top", null)];

  it("passes everything through with nothing hidden", () => {
    catalogVisibility.hidden = {};
    const r = catalogVisibility.filter(folders, conns);
    expect(r.folders.length).toBe(5);
    expect(r.connections.length).toBe(5);
  });

  it("hides a folder with everything under it, nothing else", () => {
    catalogVisibility.hidden = { c1: ["ua"] };
    const r = catalogVisibility.filter(folders, conns);
    expect(r.folders.map((x: { id: string }) => x.id).sort()).toEqual(["b", "mine", "root"]);
    expect(r.connections.map((x: { id: string }) => x.id).sort()).toEqual(["cb", "cm", "top"]);
  });

  it("matches by catalog: the same uuid in another catalog stays", () => {
    catalogVisibility.hidden = { other: ["ua"] };
    expect(catalogVisibility.filter(folders, conns).folders.length).toBe(5);
  });
});

describe("catalogVisibility changes", () => {
  it("a read that started before a change does not undo it", async () => {
    const { api } = await import("./api");
    const a = api as unknown as Record<string, unknown>;
    const writes: string[] = [];
    a.settingsSet = async (_k: string, v: string) => { writes.push(v); };
    a.settingsDelete = async () => { writes.push("{}"); };
    a.settingsGet = async () => "{}";
    catalogVisibility.hidden = {};
    await catalogVisibility.load();

    let release: (v: string) => void = () => {};
    a.settingsGet = () => new Promise<string>((r) => { release = r; });
    const stale = catalogVisibility.load();
    await catalogVisibility.set("c1", "A", true);
    release("{}");
    await stale;
    expect(catalogVisibility.isHidden("c1", "A")).toBe(true);
    await catalogVisibility.set("c1", "B", true);
    expect(JSON.parse(writes.at(-1)!)).toEqual({ c1: ["A", "B"] });
  });
});
