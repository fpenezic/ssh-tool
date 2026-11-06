// Layout of a schema's ER diagram (DbErTab). Pure, unit-tested.
//
// Tables that others point at go left, the ones pointing at them to their
// right (rank = the longest chain of foreign keys below a table, cycles
// cut), each rank a column ordered by where its neighbours sit, a column
// wrapping into more when it grows much taller than the diagram is wide.
// Tables with no key to or from another one fill a grid under the rest.

import type { DbForeignKey, DbGraphColumn, DbGraphTable } from "./api";

export const HEAD = 24; // header height
export const ROW = 18; // one column line
export const PAD = 6; // under the last line
const GAP_Y = 28;
const GAP_X = 90;

export interface ErBox {
  name: string;
  x: number;
  y: number;
  w: number;
  h: number;
  cols: DbGraphColumn[];
}

// The keys between two tables of this schema, one per constraint.
export interface ErEdge {
  fk: DbForeignKey;
  from: string;
  to: string;
}

// The edges inside the schema (a key to another schema's table has no box
// to land on; the column says where it goes instead).
export function edgesOf(tables: DbGraphTable[], fks: DbForeignKey[]): ErEdge[] {
  const names = new Set(tables.map((t) => t.name));
  const out: ErEdge[] = [];
  for (const fk of fks) {
    const to = fk.ref_table ?? "";
    const here = (fk.ref_schema ?? "") === (fk.schema ?? "") && (fk.ref_database ?? "") === (fk.database ?? "");
    if (here && fk.table && names.has(fk.table) && names.has(to)) out.push({ fk, from: fk.table, to });
  }
  return out;
}

// The columns a box shows: all, or with keysOnly the primary key and the
// foreign key columns (a table with neither shows its first column, so the
// box is not empty).
export function shownColumns(t: DbGraphTable, fkCols: Set<string>, keysOnly: boolean): DbGraphColumn[] {
  if (!keysOnly) return t.columns;
  const keep = t.columns.filter((c) => c.key || fkCols.has(c.name));
  return keep.length ? keep : t.columns.slice(0, 1);
}

// The foreign key columns of each table.
export function fkColumns(fks: DbForeignKey[]): Map<string, Set<string>> {
  const m = new Map<string, Set<string>>();
  for (const fk of fks) {
    const s = m.get(fk.table ?? "") ?? new Set<string>();
    for (const c of fk.columns ?? []) s.add(c);
    m.set(fk.table ?? "", s);
  }
  return m;
}

// measure: the width of a line of text in the diagram's font.
export function boxes(
  tables: DbGraphTable[],
  fks: DbForeignKey[],
  keysOnly: boolean,
  measure: (s: string) => number,
): ErBox[] {
  const fkc = fkColumns(fks);
  return tables.map((t) => {
    const cols = shownColumns(t, fkc.get(t.name) ?? new Set(), keysOnly);
    let w = measure(t.name) + 24;
    for (const c of cols) w = Math.max(w, measure(c.name) + measure(c.type) + 56);
    w = Math.min(Math.max(Math.ceil(w), 140), 360);
    return { name: t.name, x: 0, y: 0, w, h: HEAD + cols.length * ROW + PAD, cols };
  });
}

// Places the boxes (in place) and returns them.
export function layout(list: ErBox[], edges: ErEdge[]): ErBox[] {
  const by = new Map(list.map((b) => [b.name, b]));
  const out = new Map<string, Set<string>>(); // table -> tables it points at
  const linked = new Set<string>();
  for (const e of edges) {
    if (e.from === e.to) continue;
    if (!out.has(e.from)) out.set(e.from, new Set());
    out.get(e.from)!.add(e.to);
    linked.add(e.from);
    linked.add(e.to);
  }
  // Rank: 0 for a table pointing at nothing, else 1 + its targets' highest;
  // a target still on the stack closes a cycle and does not count.
  const rank = new Map<string, number>();
  const onStack = new Set<string>();
  const rankOf = (n: string): number => {
    const r = rank.get(n);
    if (r !== undefined) return r;
    onStack.add(n);
    let v = 0;
    for (const t of out.get(n) ?? []) if (!onStack.has(t)) v = Math.max(v, rankOf(t) + 1);
    onStack.delete(n);
    rank.set(n, v);
    return v;
  };
  const graphNames = list.map((b) => b.name).filter((n) => linked.has(n)).sort();
  for (const n of graphNames) rankOf(n);

  const ranks: string[][] = [];
  for (const n of graphNames) (ranks[rank.get(n)!] ??= []).push(n);
  for (let i = 0; i < ranks.length; i++) ranks[i] ??= [];

  // Neighbours both ways, for the ordering sweeps.
  const nb = new Map<string, string[]>();
  for (const e of edges) {
    if (e.from === e.to) continue;
    (nb.get(e.from) ?? nb.set(e.from, []).get(e.from)!).push(e.to);
    (nb.get(e.to) ?? nb.set(e.to, []).get(e.to)!).push(e.from);
  }
  const order = new Map<string, number>();
  const setOrder = () => ranks.forEach((r) => r.forEach((n, i) => order.set(n, i / Math.max(1, r.length - 1))));
  setOrder();
  for (let sweep = 0; sweep < 6; sweep++) {
    const seq = sweep % 2 === 0 ? ranks.keys() : [...ranks.keys()].reverse();
    for (const i of seq) {
      const bc = new Map<string, number>();
      for (const n of ranks[i]) {
        const ns = (nb.get(n) ?? []).filter((m) => rank.get(m) !== i);
        bc.set(n, ns.length ? ns.reduce((s, m) => s + order.get(m)!, 0) / ns.length : order.get(n)!);
      }
      ranks[i].sort((a, b) => bc.get(a)! - bc.get(b)! || a.localeCompare(b));
      ranks[i].forEach((n, j) => order.set(n, j / Math.max(1, ranks[i].length - 1)));
    }
  }

  // Columns, wrapping a rank that would be far taller than wide.
  const area = graphNames.reduce((s, n) => s + (by.get(n)!.w + GAP_X) * (by.get(n)!.h + GAP_Y), 0);
  const maxH = Math.max(900, Math.sqrt(area) * 1.1);
  let x = 0;
  let bottom = 0;
  for (const r of ranks) {
    if (!r.length) continue;
    let y = 0;
    let colW = 0;
    for (const n of r) {
      const b = by.get(n)!;
      if (y > 0 && y + b.h > maxH) {
        x += colW + GAP_X / 2;
        y = 0;
        colW = 0;
      }
      b.x = x;
      b.y = y;
      y += b.h + GAP_Y;
      colW = Math.max(colW, b.w);
      bottom = Math.max(bottom, b.y + b.h);
    }
    x += colW + GAP_X;
  }

  // The unlinked tables: a grid under the diagram, as wide as it (or a
  // square-ish one when nothing is linked).
  const rest = list.filter((b) => !linked.has(b.name)).sort((a, b) => a.name.localeCompare(b.name));
  if (rest.length) {
    const restArea = rest.reduce((s, b) => s + (b.w + GAP_Y) * (b.h + GAP_Y), 0);
    const width = Math.max(x - GAP_X, Math.sqrt(restArea) * 1.4, 600);
    let cx = 0;
    let cy = graphNames.length ? bottom + GAP_X : 0;
    let rowH = 0;
    for (const b of rest) {
      if (cx > 0 && cx + b.w > width) {
        cx = 0;
        cy += rowH + GAP_Y;
        rowH = 0;
      }
      b.x = cx;
      b.y = cy;
      cx += b.w + GAP_Y;
      rowH = Math.max(rowH, b.h);
    }
  }
  return list;
}

// The y of a column's line in a box (its middle), or the header's when the
// column is not shown.
export function columnY(b: ErBox, col: string | undefined): number {
  const i = col === undefined ? -1 : b.cols.findIndex((c) => c.name === col);
  return i < 0 ? b.y + HEAD / 2 : b.y + HEAD + i * ROW + ROW / 2;
}

// An edge's path: out of the side of the referencing box that faces the
// referenced one, into the facing side of that one; a key to its own table
// loops on the right.
export function edgePath(from: ErBox, to: ErBox, fromCol: string | undefined, toCol: string | undefined): string {
  const y1 = columnY(from, fromCol);
  const y2 = columnY(to, toCol);
  if (from === to) {
    const x = from.x + from.w;
    return `M ${x} ${y1} C ${x + 40} ${y1}, ${x + 40} ${y2}, ${x} ${y2}`;
  }
  const right = to.x + to.w / 2 >= from.x + from.w / 2;
  const x1 = right ? from.x + from.w : from.x;
  // Overlapping columns: both sides the same way, around.
  const overlap = right ? to.x < x1 : to.x + to.w > x1;
  const x2 = overlap ? (right ? to.x + to.w : to.x) : right ? to.x : to.x + to.w;
  const d1 = right ? 1 : -1;
  const d2 = overlap ? d1 : -d1;
  const c = Math.max(40, Math.abs(x2 - x1) / 2);
  return `M ${x1} ${y1} C ${x1 + d1 * c} ${y1}, ${x2 + d2 * c} ${y2}, ${x2} ${y2}`;
}

// The bounding box of all boxes.
export function bounds(list: ErBox[]): { x: number; y: number; w: number; h: number } {
  if (!list.length) return { x: 0, y: 0, w: 0, h: 0 };
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const b of list) {
    x0 = Math.min(x0, b.x);
    y0 = Math.min(y0, b.y);
    x1 = Math.max(x1, b.x + b.w);
    y1 = Math.max(y1, b.y + b.h);
  }
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}
