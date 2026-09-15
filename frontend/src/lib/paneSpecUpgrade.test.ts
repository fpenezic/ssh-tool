import { describe, it, expect } from "vitest";
import { upgradeWorkspaceTabs, PANE_SPEC_VERSION, type TabSpec } from "./paneSpec";

// Version 1 workspaces (one connectionId per tab, splits collapsed) must keep
// opening after the pane-tree upgrade. These cover the read path only - the
// store class itself needs the Wails runtime.

const v2Tab: TabSpec = {
  title: "web01",
  sessions: [{ kind: "ssh", connectionId: "c1" }],
  root: { kind: "pane", ref: 0 },
};

describe("upgradeWorkspaceTabs", () => {
  it("passes a v2 layout through untouched", () => {
    const out = upgradeWorkspaceTabs({ version: 2, tabs: [v2Tab] });
    expect(out).toEqual([v2Tab]);
  });

  it("lifts a v1 tab to a single-pane spec", () => {
    const out = upgradeWorkspaceTabs({
      version: 1,
      tabs: [{ connectionId: "c1", title: "web01", groupName: "prod", groupColor: "#f00" }] as any,
    });
    expect(out).toHaveLength(1);
    expect(out[0].root).toEqual({ kind: "pane", ref: 0 });
    expect(out[0].sessions).toEqual([{ kind: "ssh", connectionId: "c1" }]);
    expect(out[0].title).toBe("web01");
    expect(out[0].groupName).toBe("prod");
    expect(out[0].groupColor).toBe("#f00");
  });

  it("lifts every tab of a multi-tab v1 layout", () => {
    const out = upgradeWorkspaceTabs({
      version: 1,
      tabs: [{ connectionId: "c1" }, { connectionId: "c2" }, { connectionId: "c3" }] as any,
    });
    expect(out).toHaveLength(3);
    expect(out.map((t) => t.sessions[0].connectionId)).toEqual(["c1", "c2", "c3"]);
  });

  it("reads rows by shape, not by the version field", () => {
    // A layout mislabelled v1 but holding v2 rows (hand-edited, or written by
    // a newer build) still restores its trees.
    const out = upgradeWorkspaceTabs({ version: 1, tabs: [v2Tab] });
    expect(out[0].root).toEqual({ kind: "pane", ref: 0 });
    expect(out[0].sessions).toHaveLength(1);
  });

  it("keeps a mixed layout's good rows from both versions", () => {
    const out = upgradeWorkspaceTabs({
      version: 2,
      tabs: [v2Tab, { connectionId: "c9", title: "old" } as any],
    });
    expect(out).toHaveLength(2);
    expect(out[1].sessions[0].connectionId).toBe("c9");
  });

  it("drops rows that are neither shape", () => {
    const out = upgradeWorkspaceTabs({
      version: 1,
      tabs: [{ title: "no connection at all" } as any, { connectionId: "c1" } as any],
    });
    expect(out).toHaveLength(1);
    expect(out[0].sessions[0].connectionId).toBe("c1");
  });

  it("survives null entries in the array", () => {
    const out = upgradeWorkspaceTabs({ version: 1, tabs: [null as any, { connectionId: "c1" } as any] });
    expect(out).toHaveLength(1);
  });

  it("returns empty for an empty, missing or null layout", () => {
    expect(upgradeWorkspaceTabs({ version: 2, tabs: [] })).toEqual([]);
    expect(upgradeWorkspaceTabs(null)).toEqual([]);
    expect(upgradeWorkspaceTabs({ version: 2, tabs: undefined as any })).toEqual([]);
  });

  it("keeps a v2 tab that has a split tree intact", () => {
    const split: TabSpec = {
      title: "pair",
      sessions: [
        { kind: "ssh", connectionId: "c1" },
        { kind: "local", shellKind: "wsl" },
      ],
      root: {
        kind: "split", direction: "vertical", ratio: 0.7,
        a: { kind: "pane", ref: 0 },
        b: { kind: "pane", ref: 1 },
      },
    };
    const out = upgradeWorkspaceTabs({ version: 2, tabs: [split] });
    expect(out[0].root).toMatchObject({ kind: "split", direction: "vertical", ratio: 0.7 });
    expect(out[0].sessions).toHaveLength(2);
  });
});

describe("PANE_SPEC_VERSION", () => {
  it("is 2 - the version that added pane trees", () => {
    expect(PANE_SPEC_VERSION).toBe(2);
  });
});
