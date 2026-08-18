import { describe, it, expect } from "vitest";
import { spineCount, spineIsEven, evenSpine, rebalanceEven } from "./paneSplit";

// Structural copy of the pane tree shape, so this suite stays free of
// stores.svelte.ts (which pulls in the Wails runtime and needs `window`).
type PaneNode =
  | { kind: "pane"; id: string; sessionId: string }
  | { kind: "split"; id: string; direction: "horizontal" | "vertical"; ratio: number; a: PaneNode; b: PaneNode };

const leaf = (id: string): PaneNode => ({ kind: "pane", id, sessionId: "s-" + id });
const hsplit = (id: string, ratio: number, a: PaneNode, b: PaneNode): PaneNode =>
  ({ kind: "split", id, direction: "horizontal", ratio, a, b });
const vsplit = (id: string, ratio: number, a: PaneNode, b: PaneNode): PaneNode =>
  ({ kind: "split", id, direction: "vertical", ratio, a, b });

describe("spineCount", () => {
  it("counts leaves along a same-direction chain", () => {
    expect(spineCount(leaf("x"), "horizontal")).toBe(1);
    expect(spineCount(hsplit("s1", 0.5, leaf("a"), leaf("b")), "horizontal")).toBe(2);
    expect(
      spineCount(hsplit("s1", 0.5, leaf("a"), hsplit("s2", 0.5, leaf("b"), leaf("c"))), "horizontal")
    ).toBe(3);
  });

  it("stops at a split of the other direction", () => {
    const mixed = hsplit("s1", 0.5, leaf("a"), vsplit("s2", 0.5, leaf("b"), leaf("c")));
    expect(spineCount(mixed, "horizontal")).toBe(2);
  });
});

describe("spineIsEven", () => {
  it("accepts an untouched 2-pane split", () => {
    expect(spineIsEven(hsplit("s1", 0.5, leaf("a"), leaf("b")), "horizontal")).toBe(true);
  });

  it("accepts exact thirds", () => {
    const thirds = hsplit("s1", 1 / 3, leaf("a"), hsplit("s2", 0.5, leaf("b"), leaf("c")));
    expect(spineIsEven(thirds, "horizontal")).toBe(true);
  });

  it("rejects a hand-resized split", () => {
    expect(spineIsEven(hsplit("s1", 0.72, leaf("a"), leaf("b")), "horizontal")).toBe(false);
  });

  it("rejects when an inner split was resized", () => {
    const inner = hsplit("s1", 1 / 3, leaf("a"), hsplit("s2", 0.8, leaf("b"), leaf("c")));
    expect(spineIsEven(inner, "horizontal")).toBe(false);
  });
});

describe("evenSpine", () => {
  it("turns a fresh 3-chain into exact thirds", () => {
    // What splitPane builds before rebalancing: 0.5 at both levels.
    const raw = hsplit("s1", 0.5, leaf("a"), hsplit("s2", 0.5, leaf("b"), leaf("c")));
    const out = evenSpine(raw, "horizontal");
    expect(out.kind).toBe("split");
    if (out.kind !== "split") return;
    expect(out.ratio).toBeCloseTo(1 / 3, 6);
    expect(out.b.kind).toBe("split");
    if (out.b.kind !== "split") return;
    expect(out.b.ratio).toBeCloseTo(0.5, 6);
  });

  it("produces quarters for a 4-chain", () => {
    const raw = hsplit("s1", 0.5, leaf("a"),
      hsplit("s2", 0.5, leaf("b"), hsplit("s3", 0.5, leaf("c"), leaf("d"))));
    const out = evenSpine(raw, "horizontal");
    if (out.kind !== "split") throw new Error("expected split");
    expect(out.ratio).toBeCloseTo(0.25, 6);
    if (out.b.kind !== "split") throw new Error("expected split");
    expect(out.b.ratio).toBeCloseTo(1 / 3, 6);
    if (out.b.b.kind !== "split") throw new Error("expected split");
    expect(out.b.b.ratio).toBeCloseTo(0.5, 6);
  });
});

describe("rebalanceEven", () => {
  it("fixes the reported 1/2 + 1/4 + 1/4 case", () => {
    const raw = hsplit("s1", 0.5, leaf("a"), hsplit("s2", 0.5, leaf("b"), leaf("c")));
    const out = rebalanceEven(raw);
    if (out.kind !== "split") throw new Error("expected split");
    expect(out.ratio).toBeCloseTo(1 / 3, 6);
  });

  it("leaves a hand-resized row completely alone", () => {
    const raw = hsplit("s1", 0.72, leaf("a"), hsplit("s2", 0.5, leaf("b"), leaf("c")));
    const out = rebalanceEven(raw);
    if (out.kind !== "split") throw new Error("expected split");
    expect(out.ratio).toBeCloseTo(0.72, 6);
  });

  it("rebalances a nested column without touching the outer resized row", () => {
    const col = vsplit("v1", 0.5, leaf("b"), vsplit("v2", 0.5, leaf("c"), leaf("d")));
    const raw = hsplit("h1", 0.8, leaf("a"), col);
    const out = rebalanceEven(raw);
    if (out.kind !== "split") throw new Error("expected split");
    expect(out.ratio).toBeCloseTo(0.8, 6);          // outer stays hand-set
    if (out.b.kind !== "split") throw new Error("expected split");
    expect(out.b.ratio).toBeCloseTo(1 / 3, 6);      // inner column evened out
  });

  it("leaves a hand-resized nested column alone", () => {
    const col = vsplit("v1", 0.65, leaf("b"), vsplit("v2", 0.5, leaf("c"), leaf("d")));
    const raw = hsplit("h1", 0.8, leaf("a"), col);
    const out = rebalanceEven(raw);
    if (out.kind !== "split") throw new Error("expected split");
    if (out.b.kind !== "split") throw new Error("expected split");
    expect(out.b.ratio).toBeCloseTo(0.65, 6);
  });

  it("converges on equal columns across sequential splits", () => {
    // Mirrors what splitPane does: graft a fresh 0.5 split, then rebalance.
    let t: PaneNode = rebalanceEven(hsplit("s1", 0.5, leaf("a"), leaf("b")));
    if (t.kind !== "split") throw new Error("expected split");
    expect(t.ratio).toBeCloseTo(0.5, 6);

    t = rebalanceEven(hsplit("s1", t.ratio, leaf("a"), hsplit("s2", 0.5, leaf("b"), leaf("c"))));
    if (t.kind !== "split" || t.b.kind !== "split") throw new Error("expected splits");
    expect(t.ratio).toBeCloseTo(1 / 3, 6);
    expect(t.b.ratio).toBeCloseTo(0.5, 6);

    t = rebalanceEven(
      hsplit("s1", t.ratio, leaf("a"), hsplit("s2", t.b.ratio, leaf("b"), hsplit("s3", 0.5, leaf("c"), leaf("d"))))
    );
    if (t.kind !== "split" || t.b.kind !== "split" || t.b.b.kind !== "split") throw new Error("expected splits");
    expect(t.ratio).toBeCloseTo(0.25, 6);
    expect(t.b.ratio).toBeCloseTo(1 / 3, 6);
    expect(t.b.b.ratio).toBeCloseTo(0.5, 6);
  });
});
