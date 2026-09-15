import { describe, it, expect } from "vitest";
import {
  serializePaneSpec,
  restorePaneSpec,
  specFromFlat,
  isSplit,
  type SessionSpec,
  type SpecNode,
} from "./paneSpec";
import type { PaneNode } from "./panetypes";

const ssh = (id: string): SessionSpec => ({ kind: "ssh", connectionId: id });

function leaf(sessionId: string, view?: "terminal" | "sftp" | "vnc"): PaneNode {
  return { kind: "pane", id: "p-" + sessionId + (view ?? ""), sessionId, view };
}

function split(a: PaneNode, b: PaneNode, ratio = 0.5, direction: "horizontal" | "vertical" = "horizontal"): PaneNode {
  return { kind: "split", id: "s", direction, ratio, a, b };
}

/** specOf that maps sessionId -> connectionId of the same name. */
const mapAll = (sid: string) => ssh("conn-" + sid);

describe("serializePaneSpec", () => {
  it("keeps a single pane", () => {
    const out = serializePaneSpec(leaf("s1"), mapAll)!;
    expect(out.sessions).toEqual([ssh("conn-s1")]);
    expect(out.root).toEqual({ kind: "pane", ref: 0 });
  });

  it("keeps both sides of a split, with direction and ratio", () => {
    const tree = split(leaf("s1"), leaf("s2"), 0.3, "vertical");
    const out = serializePaneSpec(tree, mapAll)!;
    expect(out.sessions).toHaveLength(2);
    expect(out.root).toEqual({
      kind: "split",
      direction: "vertical",
      ratio: 0.3,
      a: { kind: "pane", ref: 0 },
      b: { kind: "pane", ref: 1 },
    });
  });

  it("keeps a nested three-pane tree", () => {
    const tree = split(leaf("s1"), split(leaf("s2"), leaf("s3")));
    const out = serializePaneSpec(tree, mapAll)!;
    expect(out.sessions).toHaveLength(3);
    const root = out.root as any;
    expect(root.b.kind).toBe("split");
    expect(root.b.b.ref).toBe(2);
  });

  it("gives two leaves on one session a single shared ref", () => {
    // splitPaneShareSession: terminal + SFTP on the same ssh.Client. This must
    // not connect twice on restore.
    const tree = split(leaf("s1"), leaf("s1", "sftp"));
    const out = serializePaneSpec(tree, mapAll)!;
    expect(out.sessions).toHaveLength(1);
    const root = out.root as any;
    expect(root.a.ref).toBe(0);
    expect(root.b.ref).toBe(0);
    expect(root.b.view).toBe("sftp");
  });

  it("preserves the sftp view on a leaf", () => {
    const out = serializePaneSpec(leaf("s1", "sftp"), mapAll)!;
    expect((out.root as any).view).toBe("sftp");
  });

  it("does not store a view for a plain terminal", () => {
    const out = serializePaneSpec(leaf("s1", "terminal"), mapAll)!;
    expect((out.root as any).view).toBeUndefined();
  });

  it("drops a vnc leaf and collapses its split", () => {
    const tree = split(leaf("s1"), leaf("s2", "vnc"));
    const out = serializePaneSpec(tree, mapAll)!;
    expect(out.sessions).toEqual([ssh("conn-s1")]);
    expect(out.root).toEqual({ kind: "pane", ref: 0 });
  });

  it("drops a leaf whose session cannot be described", () => {
    const tree = split(leaf("s1"), leaf("gone"));
    const out = serializePaneSpec(tree, (sid) => (sid === "gone" ? null : mapAll(sid)))!;
    expect(out.sessions).toHaveLength(1);
    expect(out.root.kind).toBe("pane");
  });

  it("returns null when every leaf is dropped", () => {
    const tree = split(leaf("a", "vnc"), leaf("b", "vnc"));
    expect(serializePaneSpec(tree, mapAll)).toBeNull();
  });

  it("renumbers refs after a drop so they stay contiguous", () => {
    const tree = split(leaf("dead"), split(leaf("s2"), leaf("s3")));
    const out = serializePaneSpec(tree, (sid) => (sid === "dead" ? null : mapAll(sid)))!;
    expect(out.sessions).toHaveLength(2);
    const root = out.root as any;
    expect(root.a.ref).toBe(0);
    expect(root.b.ref).toBe(1);
    expect(out.sessions[root.a.ref]).toEqual(ssh("conn-s2"));
  });
});

describe("restorePaneSpec", () => {
  const connectOk = async (s: SessionSpec) => "live-" + s.connectionId;

  it("rebuilds a single pane", async () => {
    const spec = { sessions: [ssh("c1")], root: { kind: "pane", ref: 0 } as SpecNode };
    const out = (await restorePaneSpec(spec, connectOk))!;
    expect(out.root).toMatchObject({ kind: "pane", sessionId: "live-c1" });
    expect(out.sessionIds).toEqual(["live-c1"]);
  });

  it("rebuilds a split with its ratio and direction intact", async () => {
    const spec = {
      sessions: [ssh("c1"), ssh("c2")],
      root: {
        kind: "split", direction: "vertical", ratio: 0.25,
        a: { kind: "pane", ref: 0 }, b: { kind: "pane", ref: 1 },
      } as SpecNode,
    };
    const out = (await restorePaneSpec(spec, connectOk))!;
    expect(out.root).toMatchObject({ kind: "split", direction: "vertical", ratio: 0.25 });
    expect((out.root as any).a.sessionId).toBe("live-c1");
    expect((out.root as any).b.sessionId).toBe("live-c2");
  });

  it("connects each session exactly once, including a shared ref", async () => {
    let calls = 0;
    const spec = {
      sessions: [ssh("c1")],
      root: {
        kind: "split", direction: "horizontal", ratio: 0.5,
        a: { kind: "pane", ref: 0 },
        b: { kind: "pane", ref: 0, view: "sftp" },
      } as SpecNode,
    };
    const out = (await restorePaneSpec(spec, async (s) => { calls++; return "live-" + s.connectionId; }))!;
    expect(calls).toBe(1);
    const root = out.root as any;
    expect(root.a.sessionId).toBe("live-c1");
    expect(root.b.sessionId).toBe("live-c1");
    expect(root.b.view).toBe("sftp");
  });

  it("keeps the surviving panes when one leaf fails to connect", async () => {
    const spec = {
      sessions: [ssh("dead"), ssh("c2")],
      root: {
        kind: "split", direction: "horizontal", ratio: 0.5,
        a: { kind: "pane", ref: 0 }, b: { kind: "pane", ref: 1 },
      } as SpecNode,
    };
    const out = (await restorePaneSpec(spec, async (s) =>
      s.connectionId === "dead" ? null : "live-" + s.connectionId))!;
    expect(out.root).toMatchObject({ kind: "pane", sessionId: "live-c2" });
    expect(out.sessionIds).toEqual(["live-c2"]);
  });

  it("treats a throwing connect as a failed leaf, not a failed tab", async () => {
    const spec = {
      sessions: [ssh("boom"), ssh("c2")],
      root: {
        kind: "split", direction: "horizontal", ratio: 0.5,
        a: { kind: "pane", ref: 0 }, b: { kind: "pane", ref: 1 },
      } as SpecNode,
    };
    const out = (await restorePaneSpec(spec, async (s) => {
      if (s.connectionId === "boom") throw new Error("refused");
      return "live-" + s.connectionId;
    }))!;
    expect(out.root).toMatchObject({ kind: "pane", sessionId: "live-c2" });
  });

  it("returns null when every leaf fails", async () => {
    const spec = {
      sessions: [ssh("a"), ssh("b")],
      root: {
        kind: "split", direction: "horizontal", ratio: 0.5,
        a: { kind: "pane", ref: 0 }, b: { kind: "pane", ref: 1 },
      } as SpecNode,
    };
    expect(await restorePaneSpec(spec, async () => null)).toBeNull();
  });

  it("gives every node a distinct id", async () => {
    const spec = {
      sessions: [ssh("c1"), ssh("c2"), ssh("c3")],
      root: {
        kind: "split", direction: "horizontal", ratio: 0.5,
        a: { kind: "pane", ref: 0 },
        b: {
          kind: "split", direction: "vertical", ratio: 0.5,
          a: { kind: "pane", ref: 1 }, b: { kind: "pane", ref: 2 },
        },
      } as SpecNode,
    };
    const out = (await restorePaneSpec(spec, connectOk))!;
    const ids: string[] = [];
    const walk = (n: any) => { ids.push(n.id); if (n.kind === "split") { walk(n.a); walk(n.b); } };
    walk(out.root);
    expect(ids).toHaveLength(5);
    expect(new Set(ids).size).toBe(5);
  });

  it("tolerates a spec with no sessions array", async () => {
    const spec = { sessions: undefined as any, root: { kind: "pane", ref: 0 } as SpecNode };
    expect(await restorePaneSpec(spec, connectOk)).toBeNull();
  });
});

describe("round trip", () => {
  it("preserves a four-pane layout through serialize and restore", async () => {
    const tree = split(
      split(leaf("s1"), leaf("s2"), 0.4, "vertical"),
      split(leaf("s3"), leaf("s4"), 0.6, "vertical"),
      0.35,
      "horizontal",
    );
    const saved = serializePaneSpec(tree, mapAll)!;
    const out = (await restorePaneSpec(
      { sessions: saved.sessions, root: saved.root },
      async (s) => "live-" + s.connectionId,
    ))!;
    const r = out.root as any;
    expect(r.direction).toBe("horizontal");
    expect(r.ratio).toBe(0.35);
    expect(r.a.direction).toBe("vertical");
    expect(r.a.ratio).toBe(0.4);
    expect(r.b.ratio).toBe(0.6);
    expect(r.a.a.sessionId).toBe("live-conn-s1");
    expect(r.b.b.sessionId).toBe("live-conn-s4");
  });
});

describe("specFromFlat", () => {
  it("wraps a legacy single-session entry", () => {
    const spec = specFromFlat(ssh("c1"), { title: "web01", groupName: "prod" });
    expect(spec.root).toEqual({ kind: "pane", ref: 0 });
    expect(spec.sessions).toEqual([ssh("c1")]);
    expect(spec.title).toBe("web01");
    expect(spec.groupName).toBe("prod");
  });

  it("produces something restorePaneSpec accepts", async () => {
    const spec = specFromFlat(ssh("c1"), {});
    const out = (await restorePaneSpec(spec, async (s) => "live-" + s.connectionId))!;
    expect(out.root).toMatchObject({ kind: "pane", sessionId: "live-c1" });
  });
});

describe("isSplit", () => {
  it("is false for a lone pane and true for a split", () => {
    expect(isSplit({ kind: "pane", ref: 0 })).toBe(false);
    expect(isSplit({
      kind: "split", direction: "horizontal", ratio: 0.5,
      a: { kind: "pane", ref: 0 }, b: { kind: "pane", ref: 1 },
    })).toBe(true);
  });
});
