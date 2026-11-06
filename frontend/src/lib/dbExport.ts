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

export function rowsJson(cols: DbResultColumn[], rows: Row[]): string {
  return JSON.stringify(rows.map((r) => Object.fromEntries(cols.map((c, i) => [c.name, r[i]]))), null, 2) + "\n";
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
// was not cut, is the last. stopped() is asked before each page;
// progress(n) after it.
export async function readAll(
  read: (offset: number, limit: number) => Promise<PageRead>,
  pageSize: number,
  stopped: () => boolean,
  progress: (n: number) => void,
): Promise<{ columns: DbResultColumn[]; rows: Row[]; cut: number } | null> {
  let columns: DbResultColumn[] | null = null;
  const rows: Row[] = [];
  let cut = 0;
  for (;;) {
    if (stopped()) return null;
    const p = await read(rows.length, pageSize);
    columns ??= p.columns ?? [];
    const got = p.rows ?? [];
    for (const r of got) rows.push(r);
    cut += p.cut_cells;
    progress(rows.length);
    if (!got.length || (got.length < pageSize && !p.truncated)) break;
  }
  return { columns: columns ?? [], rows, cut };
}
