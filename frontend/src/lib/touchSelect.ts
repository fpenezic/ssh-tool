// Geometry for touch text selection in the terminal.
//
// Extracted from Terminal.svelte so the arithmetic - which is easy to
// get subtly wrong at the edges, and invisible when it is - can be
// tested without a DOM or a live xterm.

export interface Cell {
  col: number;
  row: number;
}

export interface CellBox {
  width: number;
  height: number;
}

/**
 * Map a viewport point onto a terminal cell, clamped to the grid.
 *
 * A finger regularly lands outside the screen rect - on the padding,
 * or past the last row while dragging - and an unclamped result would
 * hand xterm an out-of-range row.
 */
export function cellFromPoint(
  clientX: number,
  clientY: number,
  rect: { left: number; top: number; width: number; height: number },
  cell: CellBox,
  cols: number,
  rows: number,
): Cell {
  const cw = cell.width > 0 ? cell.width : rect.width / Math.max(1, cols);
  const ch = cell.height > 0 ? cell.height : rect.height / Math.max(1, rows);
  const col = Math.floor((clientX - rect.left) / cw);
  const row = Math.floor((clientY - rect.top) / ch);
  return {
    col: Math.max(0, Math.min(cols - 1, col)),
    row: Math.max(0, Math.min(rows - 1, row)),
  };
}

/** Order two points so the earlier one comes first. */
export function orderCells(a: Cell, b: Cell): [Cell, Cell] {
  if (b.row < a.row || (b.row === a.row && b.col < a.col)) return [b, a];
  return [a, b];
}

/**
 * Length, in cells, of the range between two points on a `cols`-wide
 * grid - what xterm's selection API wants instead of an end point.
 *
 * Both ends are inclusive: selecting one character yields 1, not 0.
 */
export function selectionLength(a: Cell, b: Cell, cols: number): number {
  const [start, end] = orderCells(a, b);
  if (start.row === end.row) {
    return Math.max(1, end.col - start.col + 1);
  }
  return (
    (cols - start.col) +
    (end.row - start.row - 1) * cols +
    (end.col + 1)
  );
}

/**
 * Whether a movement is far enough to mean "scrolling", cancelling a
 * pending long-press.
 */
export function exceedsSlop(
  from: { x: number; y: number },
  to: { x: number; y: number },
  slopPx: number,
): boolean {
  return Math.hypot(to.x - from.x, to.y - from.y) > slopPx;
}
