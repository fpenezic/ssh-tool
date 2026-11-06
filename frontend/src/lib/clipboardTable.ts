// Rows as a table on the clipboard: HTML, which Teams and Outlook paste as
// a real table and Excel splits into cells, with tab-separated text as the
// plain flavour (Excel splits that too, whatever the locale's list
// separator). Used by Gather facts' "Copy table" and the Database grid.

import { api } from "./api";

export type Cell = string | null;

// head: the column names, or null for the rows alone. A null cell is
// empty in TSV and HTML (a spreadsheet has no NULL), "NULL" in Markdown.
export function tableTsv(head: string[] | null, rows: Cell[][]): string {
  const cell = (v: Cell) => (v ?? "").replace(/[\t\r\n]+/g, " ");
  return [...(head ? [head] : []), ...rows].map((r) => r.map(cell).join("\t")).join("\n") + "\n";
}

function esc(v: string): string {
  return v.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export function tableHtml(head: string[] | null, rows: Cell[][]): string {
  const th = head ? `<thead><tr>${head.map((h) => `<th style="text-align:left;border:1px solid #999;padding:2px 6px">${esc(h)}</th>`).join("")}</tr></thead>` : "";
  const body = rows.map((r) => `<tr>${r.map((v) => `<td style="border:1px solid #999;padding:2px 6px">${esc(v ?? "")}</td>`).join("")}</tr>`).join("");
  return `<table style="border-collapse:collapse">${th}<tbody>${body}</tbody></table>`;
}

export function tableMarkdown(head: string[], rows: Cell[][]): string {
  // The backslash first: a value ending in one would otherwise escape the
  // pipe that closes its cell.
  const cell = (v: Cell) => (v === null ? "NULL" : v.replace(/\\/g, "\\\\").replace(/\|/g, "\\|").replace(/\r?\n/g, " "));
  return [
    `| ${head.map(cell).join(" | ")} |`,
    `| ${head.map(() => "---").join(" | ")} |`,
    ...rows.map((r) => `| ${r.map(cell).join(" | ")} |`),
  ].join("\n") + "\n";
}

// Both flavours in one write; the Go-side plain text when the rich write
// is missing or refused (permissions, an unfocused document). Throws only
// when the plain write fails too.
export async function copyTable(head: string[] | null, rows: Cell[][]): Promise<void> {
  const text = tableTsv(head, rows);
  try {
    if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
      await navigator.clipboard.write([
        new ClipboardItem({
          "text/html": new Blob([tableHtml(head, rows)], { type: "text/html" }),
          "text/plain": new Blob([text], { type: "text/plain" }),
        }),
      ]);
      return;
    }
  } catch {
    /* the plain write below */
  }
  await api.clipboardSetText(text);
}
