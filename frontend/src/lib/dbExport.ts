// Rows as a CSV or JSON file: the grid's export (the rows shown) and a
// whole table's export from the tree (read page by page).

import type { DbResultColumn } from "./api";

type Row = (string | null)[];

function csvCell(v: string | null): string {
  if (v === null) return "";
  return /[",\n\r]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
}

export function rowsCsv(cols: DbResultColumn[], rows: Row[]): string {
  return [cols.map((c) => csvCell(c.name)).join(","), ...rows.map((r) => r.map(csvCell).join(","))].join("\r\n") + "\r\n";
}

// A result can carry two columns of one name (a join's two ids): the
// later ones become "name (2)", "name (3)" so no value is lost; unique
// names stay as they are.
export function jsonKeys(cols: DbResultColumn[]): string[] {
  // Every name a column really has stays its own; a repeat takes the first
  // free suffix (so a column named "id (2)" keeps that name).
  const taken = new Set(cols.map((c) => c.name));
  const seen = new Set<string>();
  return cols.map((c) => {
    if (!seen.has(c.name)) {
      seen.add(c.name);
      return c.name;
    }
    let k = c.name;
    for (let n = 2; taken.has(k); n++) k = `${c.name} (${n})`;
    taken.add(k);
    return k;
  });
}

export function rowsJson(cols: DbResultColumn[], rows: Row[]): string {
  const keys = jsonKeys(cols);
  return JSON.stringify(rows.map((r) => Object.fromEntries(keys.map((k, i) => [k, r[i]]))), null, 2) + "\n";
}

export interface PageRead {
  columns: DbResultColumn[] | null;
  rows: Row[] | null;
  truncated: boolean;
  cut_cells: number;
}

// Every row of a table, a page at a time: read(offset, limit) is one
// page. A page the server cut short (its result size limit) is truncated
// and the next starts where it ended; an empty page, or a short one that
// was not cut, is the last. stopped() is asked before each page and once
// more after the last (null: stopped); progress(n) after each page.
export async function readAll(
  read: (offset: number, limit: number) => Promise<PageRead>,
  pageSize: number,
  stopped: () => boolean,
  progress: (n: number) => void,
): Promise<{ columns: DbResultColumn[]; rows: Row[]; cut: number; pages: number } | null> {
  let columns: DbResultColumn[] | null = null;
  const rows: Row[] = [];
  let cut = 0;
  let pages = 0;
  for (;;) {
    if (stopped()) return null;
    const p = await read(rows.length, pageSize);
    pages++;
    columns ??= p.columns ?? [];
    const got = p.rows ?? [];
    for (const r of got) rows.push(r);
    cut += p.cut_cells;
    progress(rows.length);
    if (!got.length || (got.length < pageSize && !p.truncated)) break;
  }
  if (stopped()) return null;
  return { columns: columns ?? [], rows, cut, pages };
}
