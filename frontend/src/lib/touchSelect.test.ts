import { describe, it, expect } from "vitest";
import {
  cellFromPoint,
  orderCells,
  selectionLength,
  exceedsSlop,
} from "./touchSelect";

const rect = { left: 10, top: 20, width: 800, height: 400 };
const cell = { width: 10, height: 20 };
const COLS = 80;
const ROWS = 20;

describe("cellFromPoint", () => {
  it("maps a point to the cell containing it", () => {
    // 35px right of the left edge = column 3 (cells are 10px wide).
    expect(cellFromPoint(10 + 35, 20 + 50, rect, cell, COLS, ROWS)).toEqual({
      col: 3,
      row: 2,
    });
  });

  it("maps the very first pixel to cell 0,0", () => {
    expect(cellFromPoint(10, 20, rect, cell, COLS, ROWS)).toEqual({ col: 0, row: 0 });
  });

  // A finger on the padding to the left of the grid, or dragged above
  // the first row, must not produce a negative index.
  it("clamps points before the grid", () => {
    expect(cellFromPoint(0, 0, rect, cell, COLS, ROWS)).toEqual({ col: 0, row: 0 });
  });

  // Dragging past the last row is the normal way to select to the
  // bottom; it must clamp rather than run off the grid.
  it("clamps points past the grid", () => {
    expect(cellFromPoint(9999, 9999, rect, cell, COLS, ROWS)).toEqual({
      col: COLS - 1,
      row: ROWS - 1,
    });
  });

  // The renderer's cell box is read from a private xterm field; if an
  // upgrade changes its shape we get zeros and must still work.
  it("falls back to rect/grid when the cell box is unavailable", () => {
    const got = cellFromPoint(10 + 400, 20 + 200, rect, { width: 0, height: 0 }, COLS, ROWS);
    expect(got).toEqual({ col: 40, row: 10 });
  });
});

describe("orderCells", () => {
  it("leaves a forward range alone", () => {
    const a = { col: 2, row: 1 };
    const b = { col: 5, row: 3 };
    expect(orderCells(a, b)).toEqual([a, b]);
  });

  // Dragging up-and-left is as natural as down-and-right on a touch
  // screen, so the range has to normalise.
  it("swaps a backward range", () => {
    const a = { col: 5, row: 3 };
    const b = { col: 2, row: 1 };
    expect(orderCells(a, b)).toEqual([b, a]);
  });

  it("orders by column within one row", () => {
    const a = { col: 9, row: 4 };
    const b = { col: 1, row: 4 };
    expect(orderCells(a, b)).toEqual([b, a]);
  });
});

describe("selectionLength", () => {
  it("counts a single cell as 1", () => {
    expect(selectionLength({ col: 3, row: 0 }, { col: 3, row: 0 }, COLS)).toBe(1);
  });

  it("is inclusive of both ends on one row", () => {
    // Columns 3..7 is five characters, not four.
    expect(selectionLength({ col: 3, row: 0 }, { col: 7, row: 0 }, COLS)).toBe(5);
  });

  it("spans two rows", () => {
    // 78 cells left on row 0 (cols 2..79), plus cols 0..4 on row 1.
    expect(selectionLength({ col: 2, row: 0 }, { col: 4, row: 1 }, COLS)).toBe(78 + 5);
  });

  it("counts whole rows in between", () => {
    // row 0 from col 0 = 80, row 1 entire = 80, row 2 up to col 0 = 1.
    expect(selectionLength({ col: 0, row: 0 }, { col: 0, row: 2 }, COLS)).toBe(161);
  });

  it("gives the same length regardless of drag direction", () => {
    const a = { col: 12, row: 1 };
    const b = { col: 4, row: 5 };
    expect(selectionLength(a, b, COLS)).toBe(selectionLength(b, a, COLS));
  });
});

describe("exceedsSlop", () => {
  it("treats a still finger as a hold", () => {
    expect(exceedsSlop({ x: 100, y: 100 }, { x: 103, y: 98 }, 12)).toBe(false);
  });

  it("treats a real drag as a scroll", () => {
    expect(exceedsSlop({ x: 100, y: 100 }, { x: 100, y: 140 }, 12)).toBe(true);
  });

  // Diagonal movement has to count too - measuring only one axis
  // would let a diagonal scroll trigger the long-press.
  it("measures diagonal distance", () => {
    expect(exceedsSlop({ x: 0, y: 0 }, { x: 10, y: 10 }, 12)).toBe(true);
  });
});
