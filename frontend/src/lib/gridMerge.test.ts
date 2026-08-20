import { describe, it, expect } from "vitest";

// Mirror of mergeTabsIntoGrid's layout maths, so the shape can be asserted
// without instantiating the store (which pulls in the Wails runtime).
type N = { kind: "pane"; id: string } | { kind: "split"; direction: "horizontal" | "vertical"; ratio: number; a: N; b: N };

const leaf = (id: string): N => ({ kind: "pane", id });

function chain(nodes: N[], direction: "horizontal" | "vertical"): N {
  if (nodes.length === 1) return nodes[0];
  const [head, ...tail] = nodes;
  return { kind: "split", direction, ratio: 1 / nodes.length, a: head, b: chain(tail, direction) };
}

// Balanced column distribution - remainder to the leading columns, matching
// mergeTabsIntoGrid.
function columnSizes(n: number, cols: number): number[] {
  const base = Math.floor(n / cols);
  const extra = n % cols;
  return Array.from({ length: cols }, (_, i) => base + (i < extra ? 1 : 0));
}

function build(n: number, colsWanted?: number): N {
  const roots = Array.from({ length: n }, (_, i) => leaf("p" + i));
  const cols = colsWanted ?? Math.ceil(Math.sqrt(roots.length));
  const columns: N[][] = [];
  let taken = 0;
  for (const size of columnSizes(n, cols)) {
    if (size <= 0) continue;
    columns.push(roots.slice(taken, taken + size));
    taken += size;
  }
  return chain(columns.map((col) => chain(col, "vertical")), "horizontal");
}

function leaves(n: N): string[] {
  return n.kind === "pane" ? [n.id] : [...leaves(n.a), ...leaves(n.b)];
}

describe("grid merge layout", () => {
  it("keeps every pane, none duplicated or lost", () => {
    for (const n of [2, 3, 4, 5, 6, 9, 12]) {
      const got = leaves(build(n));
      expect(got.length).toBe(n);
      expect(new Set(got).size).toBe(n);
    }
  });

  it("4 panes form a 2x2: two columns of two", () => {
    const root = build(4);
    if (root.kind !== "split") throw new Error("expected split");
    expect(root.direction).toBe("horizontal");
    expect(root.ratio).toBeCloseTo(0.5, 6);
    // each side is a vertical pair
    for (const side of [root.a, root.b]) {
      if (side.kind !== "split") throw new Error("expected a column split");
      expect(side.direction).toBe("vertical");
      expect(side.ratio).toBeCloseTo(0.5, 6);
    }
  });

  it("6 panes form 3 columns of 2, outer ratio 1/3", () => {
    const root = build(6);
    if (root.kind !== "split") throw new Error("expected split");
    expect(root.direction).toBe("horizontal");
    expect(root.ratio).toBeCloseTo(1 / 3, 6);
  });

  it("2 panes are a single horizontal split, not a nested grid", () => {
    const root = build(2);
    if (root.kind !== "split") throw new Error("expected split");
    expect(root.direction).toBe("horizontal");
    expect(root.a.kind).toBe("pane");
    expect(root.b.kind).toBe("pane");
  });

  it("column-major order preserves tab-bar order down each column", () => {
    // 4 panes -> col0 = p0,p1 ; col1 = p2,p3
    expect(leaves(build(4))).toEqual(["p0", "p1", "p2", "p3"]);
  });

  it("spreads the remainder across leading columns, not the last one", () => {
    // 7 in 3 columns must be 3+2+2, never 3+3+1 (a lone full-height pane).
    expect(columnSizes(7, 3)).toEqual([3, 2, 2]);
    expect(columnSizes(10, 4)).toEqual([3, 3, 2, 2]);
    expect(columnSizes(5, 3)).toEqual([2, 2, 1]);
  });

  it("keeps all 5 panes and never leaves a column empty", () => {
    for (const cols of [2, 3, 4, 5]) {
      const sizes = columnSizes(5, cols);
      expect(sizes.reduce((a, b) => a + b, 0)).toBe(5);
      expect(sizes.every((n) => n > 0)).toBe(true);
      expect(leaves(build(5, cols)).length).toBe(5);
    }
  });
});
