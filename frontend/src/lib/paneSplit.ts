import type { PaneNode } from "./stores.svelte";

// ---- Even-split rebalancing -------------------------------------------
//
// The pane tree is binary (see gotcha "Pane tree is a binary tree"), so N
// panes in a row are nested splits, not one N-way container. Three equal
// columns are NOT ratio 0.5 everywhere: the outer split must sit at 1/3
// with the inner one at 1/2. Splitting a leaf always inserted ratio 0.5,
// which halves ONE pane instead of re-dividing the row - hence the
// 1/2 + 1/4 + 1/4 the user reported instead of thirds.
//
// A "spine" is the maximal chain of same-direction splits hanging off a
// node's `b` side - the shape our own splits build when you keep adding
// to the same row. Nested splits of the OTHER direction end a spine and
// are treated as opaque leaves, so a column inside a row keeps its own
// geometry.

// Count the leaves along a spine: splits of `dir` chained through `b`.
export function spineCount(node: PaneNode, dir: "horizontal" | "vertical"): number {
  if (node.kind === "pane" || node.direction !== dir) return 1;
  return 1 + spineCount(node.b, dir);
}

// True when every split along the spine already sits at the exact ratio an
// even division would give it. That is the "untouched" signal: a user who
// dragged a resize handle leaves a ratio that does not match, and we then
// leave the whole spine alone rather than undoing their sizing.
export function spineIsEven(node: PaneNode, dir: "horizontal" | "vertical"): boolean {
  if (node.kind === "pane" || node.direction !== dir) return true;
  const n = spineCount(node, dir);
  // Two shapes count as untouched, and both must be accepted:
  //
  //   1/n            already evened out by a previous pass, and
  //   1/(n-1)        the row as it was BEFORE this split inserted a pane
  //
  // The second case is the whole point: splitPane grafts a fresh 0.5 split
  // onto the row and calls us immediately, so the outer ratios still describe
  // the shorter row. Testing only against 1/n would reject every freshly
  // split row as "hand-resized" and never rebalance anything. A genuinely
  // dragged handle matches neither, and 1/(n-1) vs 1/n stay far enough apart
  // (0.5 vs 0.333, 0.333 vs 0.25) that the tolerance cannot confuse them.
  const evenNow = Math.abs(node.ratio - 1 / n) <= 0.001;
  const evenBefore = n > 2 && Math.abs(node.ratio - 1 / (n - 1)) <= 0.001;
  if (!evenNow && !evenBefore) return false;
  return spineIsEven(node.b, dir);
}

// Rewrite a spine so its leaves divide the space equally: the outermost
// split gets 1/n, the next 1/(n-1), down to 1/2 at the innermost.
export function evenSpine(node: PaneNode, dir: "horizontal" | "vertical"): PaneNode {
  if (node.kind === "pane" || node.direction !== dir) return node;
  const n = spineCount(node, dir);
  return { ...node, ratio: 1 / n, b: evenSpine(node.b, dir) };
}

// Rebalance every same-direction spine that is still evenly divided,
// leaving hand-resized ones untouched. Applied after a split inserts a
// new pane.
export function rebalanceEven(node: PaneNode): PaneNode {
  if (node.kind === "pane") return node;
  // Decide about THIS spine before touching anything inside it. Recursing
  // first would rewrite the spine's own inner splits (each looks like a
  // shorter spine of the same direction), and the evenness test above would
  // then be reading ratios this function had just written - a resized row
  // would rebalance itself. So: test the spine as the user left it, rewrite
  // its ratios if it is untouched, and only then recurse into the branches
  // that hang OFF the spine (each `a`, plus the leaf that ends it), where
  // nested splits of the other direction live.
  const evened = spineIsEven(node, node.direction)
    ? (evenSpine(node, node.direction) as Extract<PaneNode, { kind: "split" }>)
    : node;
  return rebalanceOffSpine(evened, evened.direction);
}

// Walk a spine and rebalance only what hangs off it, leaving the spine's own
// ratios (just decided by rebalanceEven) alone.
function rebalanceOffSpine(node: PaneNode, dir: "horizontal" | "vertical"): PaneNode {
  if (node.kind === "pane") return node;
  if (node.direction !== dir) return rebalanceEven(node);
  return { ...node, a: rebalanceEven(node.a), b: rebalanceOffSpine(node.b, dir) };
}

