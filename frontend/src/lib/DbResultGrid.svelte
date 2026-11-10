<script lang="ts">
  // A result set as a grid: only the rows in view are in the DOM, so the
  // 100k-row ceiling stays smooth. Click picks a cell, Shift / a drag / a
  // # click pick more; Ctrl+C copies one value, or more cells as a table
  // Excel and Teams paste as one. Double-click edits in place on an
  // editable grid and opens the value in full below the grid otherwise
  // (JSON pretty-printed); right-click has the rest. A header click sorts
  // and Ctrl+F filters the rows that were fetched, here in the window; #
  // keeps the server's row number. Export writes the rows as shown.

  import { untrack } from "svelte";
  import { api, type DbResult } from "./api";
  import { toast } from "./toast.svelte.ts";
  import { IconDownload, IconX, IconCopy, IconArrowUp, IconArrowDown, IconSearch, IconLink, IconPencil, IconRotateCw, IconEye, IconTable, IconClipboard, IconSql } from "./iconMap";
  import { contextMenu, type MenuItem } from "./contextMenu.svelte.ts";
  import { copyTable, setPlain, tableMarkdown } from "./clipboardTable";
  import { cellsWhere, type SqlEngine } from "./dbSql";
  import { rowsCsv, rowsJson } from "./dbExport";
  import { selectAllWithin } from "./selectWithin";
  import { sortedOrder } from "./dbSort";

  interface Props {
    result: DbResult;
    // File name stem for exports.
    exportName: string;
    // The rows are one page of more (a table's Data view): sorting and
    // filtering say they cover this page only.
    paged?: boolean;
    // Cells that lead somewhere (a foreign key): canLink says which, and
    // such a cell shows a link that calls onLink with its row.
    canLink?: (row: (string | null)[], ci: number) => boolean;
    linkTitle?: (ci: number) => string;
    onLink?: (row: (string | null)[], ci: number) => void;
    // The rows are sorted on the server (a table's Data view): a header
    // click asks for that sort instead of sorting the page here.
    serverSort?: { column: string; desc: boolean } | null;
    onServerSort?: (s: { column: string; desc: boolean } | null) => void;
    // The row of the picked cell, as the server sent it (null: none), and
    // the rows the selection spans.
    onSelect?: (row: (string | null)[] | null, rows: (string | null)[][]) => void;
    // Cells can be changed in place or in the value pane; edits (keyed
    // "row:col" by the server's row index) show in place, onEdit records
    // changes in one go (a cell set to the server's own value drops its
    // edit).
    editable?: boolean;
    edits?: Map<string, string | null>;
    onEdit?: (changes: { row: number; ci: number; value: string | null }[]) => void;
    // The engine's SQL, for "Copy as WHERE" (none: not offered).
    sqlEngine?: SqlEngine | null;
    // More right-click items for the selected rows (Copy as INSERT ...).
    rowMenu?: (rows: (string | null)[][]) => MenuItem[];
  }
  let { result, exportName, paged = false, canLink, linkTitle, onLink, serverSort = null, onServerSort, onSelect, editable = false, edits, onEdit, sqlEngine = null, rowMenu }: Props = $props();

  const ROW_H = 24;
  const cols = $derived(result.columns ?? []);
  // Output shown a line per row (pretty JSON, a ClickHouse FORMAT) keeps its
  // indentation.
  const lines = $derived(cols.length === 1 && cols[0].name === "output");
  const base = $derived(result.rows ?? []);

  // Header click: ascending, descending, off.
  let sort = $state<{ c: number; name: string; desc: boolean } | null>(null);
  function toggleSort(c: number) {
    const name = cols[c]?.name ?? "";
    if (onServerSort) {
      if (serverSort?.column !== name) onServerSort({ column: name, desc: false });
      else if (!serverSort.desc) onServerSort({ column: name, desc: true });
      else onServerSort(null);
      return;
    }
    if (sort?.c !== c) sort = { c, name, desc: false };
    else if (!sort.desc) sort = { c, name, desc: true };
    else sort = null;
  }
  const order = $derived(sort && cols[sort.c] ? sortedOrder(base, sort.c, cols[sort.c].type, sort.desc) : null);

  let findOpen = $state(false);
  let findText = $state("");
  let needle = $state("");
  let findInput = $state<HTMLInputElement | null>(null);
  let findTimer: ReturnType<typeof setTimeout> | undefined;
  // Typing re-filters a beat later: 100k rows per keystroke is felt.
  $effect(() => {
    const t = findText;
    clearTimeout(findTimer);
    findTimer = setTimeout(() => (needle = t.trim().toLowerCase()), t ? 150 : 0);
    return () => clearTimeout(findTimer);
  });
  function openFind() {
    findOpen = true;
    queueMicrotask(() => findInput?.select());
  }
  function closeFind() {
    findOpen = false;
    findText = "";
    needle = "";
    scroller?.focus();
  }

  // The server's row index of each row shown, in display order.
  const view = $derived.by(() => {
    const idx = order ?? base.map((_, i) => i);
    if (!needle) return idx;
    return idx.filter((i) => base[i].some((v) => v !== null && v.toLowerCase().includes(needle)));
  });
  const rows = $derived(view.map((i) => base[i]));

  // Column widths from the header and a sample of values.
  const widths = $derived.by(() => {
    const sample = rows.slice(0, 200);
    return cols.map((c, i) => {
      // The header shows the name and, smaller, the type.
      let n = Math.max(c.name.length + Math.ceil(c.type.length * 0.75) + 1, 4);
      for (const r of sample) {
        const v = r[i];
        const len = v === null ? 4 : Math.min(v.length, 60);
        if (len > n) n = len;
      }
      return Math.max(70, Math.min(360, Math.round(n * 7.4 + 18)));
    });
  });
  const numW = $derived(Math.max(36, String(base.length).length * 8 + 14));
  const template = $derived(`${numW}px ${widths.map((w) => w + "px").join(" ")}`);
  const totalW = $derived(numW + widths.reduce((a, b) => a + b, 0));

  let scroller = $state<HTMLDivElement | null>(null);
  let scrollTop = $state(0);
  let viewH = $state(400);
  const first = $derived(Math.max(0, Math.floor(scrollTop / ROW_H) - 5));
  const last = $derived(Math.min(rows.length, Math.ceil((scrollTop + viewH) / ROW_H) + 10));
  const visible = $derived(rows.slice(first, last));

  // The picked cell (sel) and the corner the selection started from
  // (anchor): the selection is the rectangle between them. Click picks one
  // cell, Shift+click / Shift+arrows / a drag stretch it, a # click picks
  // whole rows, Ctrl+A everything.
  let sel = $state<{ r: number; c: number } | null>(null);
  let anchor = $state<{ r: number; c: number } | null>(null);
  let opened = $state<{ r: number; c: number } | null>(null);
  const rect = $derived(
    sel && anchor
      ? { r0: Math.min(sel.r, anchor.r), r1: Math.max(sel.r, anchor.r), c0: Math.min(sel.c, anchor.c), c1: Math.max(sel.c, anchor.c) }
      : null,
  );
  const multi = $derived(!!rect && (rect.r1 > rect.r0 || rect.c1 > rect.c0));
  function inRect(ri: number, ci: number): boolean {
    return !!rect && ri >= rect.r0 && ri <= rect.r1 && ci >= rect.c0 && ci <= rect.c1;
  }
  function pick(r: number, c: number, extend = false) {
    if (!extend || !anchor) anchor = { r, c };
    sel = { r, c };
  }
  function clearSel() {
    sel = null;
    anchor = null;
    openValue(null);
    inline = null;
  }
  // A new result: back to the top, nothing picked. The filter stays (it is
  // what you are looking for), and so does the sort while the column is
  // still there by name - Next on a table keeps its order.
  $effect(() => {
    const cs = result.columns ?? [];
    untrack(() => {
      if (sort && cs[sort.c]?.name !== sort.name) sort = null;
      clearSel();
    });
    if (scroller) scroller.scrollTop = 0;
  });
  // A new order or filter: what was picked points at another row.
  $effect(() => {
    void view;
    untrack(clearSel);
  });

  // A cell as shown: its edit when it has one.
  function shown(ri: number, ci: number): string | null {
    const k = `${view[ri]}:${ci}`;
    return edits?.has(k) ? edits.get(k)! : rows[ri]?.[ci] ?? null;
  }
  // The selection's server row indexes, top to bottom.
  const selRows = $derived(rect ? view.slice(rect.r0, rect.r1 + 1) : []);
  $effect(() => {
    const s = sel;
    const rs = selRows;
    untrack(() => onSelect?.(s ? rows[s.r] ?? null : null, rs.map((i) => base[i])));
  });
  // Rows with an edit in them: their # shows it, wherever the cell is.
  const dirtyRows = $derived(new Set([...(edits?.keys() ?? [])].map((k) => Number(k.split(":")[0]))));

  // The value pane below the grid; edit there for a long or multi-line
  // value. Opening another cell drops a half-made edit.
  let editing = $state<string | null>(null);
  function openValue(at: { r: number; c: number } | null, edit = false) {
    opened = at;
    editing = at && edit ? shown(at.r, at.c) ?? "" : null;
  }
  function saveEdit(v: string | null) {
    if (!opened || !onEdit) return;
    onEdit([{ row: view[opened.r], ci: opened.c, value: v }]);
    editing = null;
  }

  // In-place edit: double-click, F2 or Enter on a cell of an editable grid.
  // Enter keeps the value and steps down, Tab right (Shift+Tab left), Esc
  // drops it; leaving the field keeps it. A NULL left empty stays NULL.
  const canEdit = $derived(editable && !!onEdit);
  const LONG = 200;
  let inline = $state<{ r: number; c: number; orig: string | null; value: string } | null>(null);
  let inlineEl = $state<HTMLInputElement | null>(null);
  function startEdit(r: number, c: number) {
    if (!canEdit) return;
    const v = shown(r, c);
    pick(r, c);
    if (v !== null && (v.length > LONG || /[\r\n]/.test(v))) {
      openValue({ r, c }, true);
      return;
    }
    inline = { r, c, orig: v, value: v ?? "" };
    queueMicrotask(() => inlineEl?.select());
  }
  function commitInline() {
    const ed = inline;
    if (!ed || !onEdit) return;
    inline = null;
    if (ed.orig === null && ed.value === "") return;
    if (ed.value === ed.orig) return;
    onEdit([{ row: view[ed.r], ci: ed.c, value: ed.value }]);
  }
  function inlineKey(e: KeyboardEvent) {
    e.stopPropagation();
    // Enter that picks an IME candidate is the input method's, not ours.
    if (!inline || e.isComposing || e.keyCode === 229) return;
    const { r, c } = inline;
    let to: { r: number; c: number } | null = null;
    if (e.key === "Escape") {
      e.preventDefault();
      inline = null;
      scroller?.focus();
      return;
    }
    if (e.key === "Enter") to = { r: Math.min(rows.length - 1, r + 1), c };
    else if (e.key === "Tab") to = { r, c: Math.max(0, Math.min(cols.length - 1, c + (e.shiftKey ? -1 : 1))) };
    else return;
    e.preventDefault();
    commitInline();
    pick(to.r, to.c);
    scrollToSel();
    scroller?.focus();
  }

  // Mouse: press picks (Shift stretches), a drag stretches to where the
  // pointer is; a right press inside the selection keeps it for the menu.
  let dragging = false;
  function cellDown(e: MouseEvent, r: number, c: number) {
    if (inline && (inline.r !== r || inline.c !== c)) commitInline();
    if (e.button === 2) {
      if (!inRect(r, c)) pick(r, c);
      return;
    }
    if (e.button !== 0) return;
    pick(r, c, e.shiftKey);
    dragging = true;
    const up = () => {
      dragging = false;
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mouseup", up);
  }
  function cellEnter(r: number, c: number) {
    if (dragging && (sel?.r !== r || sel?.c !== c)) sel = { r, c };
  }
  function rowDown(e: MouseEvent, r: number) {
    if (inline) commitInline();
    if (e.button === 2 && rect && r >= rect.r0 && r <= rect.r1) return;
    const from = e.shiftKey && anchor ? anchor.r : r;
    anchor = { r: from, c: 0 };
    sel = { r, c: Math.max(0, cols.length - 1) };
  }

  function scrollToSel() {
    if (!scroller || !sel) return;
    const top = sel.r * ROW_H;
    if (top < scroller.scrollTop) scroller.scrollTop = top;
    else if (top + ROW_H * 2 > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = top + ROW_H * 2 - scroller.clientHeight;
  }

  // The selection's cells as shown, and its column names.
  function selCells(): (string | null)[][] {
    if (!rect) return [];
    const out: (string | null)[][] = [];
    for (let r = rect.r0; r <= rect.r1; r++) {
      const row: (string | null)[] = [];
      for (let c = rect.c0; c <= rect.c1; c++) row.push(shown(r, c));
      out.push(row);
    }
    return out;
  }
  function selCols() {
    return rect ? cols.slice(rect.c0, rect.c1 + 1) : [];
  }
  function copyText(text: string, what: string) {
    setPlain(text).then(
      () => toast.ok(`${what} copied`),
      (e) => toast.err(`Copy failed: ${String((e as any)?.message ?? e)}`),
    );
  }
  function sizeText(): string {
    if (!rect) return "";
    const nr = rect.r1 - rect.r0 + 1;
    const nc = rect.c1 - rect.c0 + 1;
    return `${nr} row${nr === 1 ? "" : "s"} x ${nc} column${nc === 1 ? "" : "s"}`;
  }
  // Ctrl+C: one cell is its value; more go as a table (Excel, Teams).
  async function copySel(withHead = false) {
    if (!sel) return;
    if (!multi && !withHead) {
      copyText(shown(sel.r, sel.c) ?? "", "Value");
      return;
    }
    try {
      await copyTable(withHead ? selCols().map((c) => c.name) : null, selCells());
      toast.ok(`${sizeText()} copied`);
    } catch (e) {
      toast.err(`Copy failed: ${String((e as any)?.message ?? e)}`);
    }
  }
  function copyWhere() {
    if (!sqlEngine) return;
    // A cut value (past the cell limit, ending in "...") would match no row.
    if (result.cut_cells > 0) {
      toast.err(`${result.cut_cells} value${result.cut_cells === 1 ? " is" : "s are"} cut in this result (too long to show whole): no WHERE from it`);
      return;
    }
    try {
      copyText(cellsWhere(sqlEngine, selCols(), selCells()), "WHERE");
    } catch (e) {
      toast.err(String((e as any)?.message ?? e));
    }
  }
  // Every cell of the selection set to v (null), or back to the server's
  // value (undefined).
  function setSel(v: string | null | undefined) {
    if (!rect || !onEdit) return;
    const changes: { row: number; ci: number; value: string | null }[] = [];
    for (let r = rect.r0; r <= rect.r1; r++) {
      for (let c = rect.c0; c <= rect.c1; c++) changes.push({ row: view[r], ci: c, value: v === undefined ? rows[r]?.[c] ?? null : v });
    }
    onEdit(changes);
  }
  function editedInSel(): number {
    if (!rect || !edits?.size) return 0;
    let n = 0;
    for (let r = rect.r0; r <= rect.r1; r++) {
      for (let c = rect.c0; c <= rect.c1; c++) if (edits.has(`${view[r]}:${c}`)) n++;
    }
    return n;
  }
  function openMenu(e: MouseEvent) {
    e.preventDefault();
    if (!sel) return;
    const at = sel;
    const nEdited = editedInSel();
    const items: MenuItem[] = [];
    if (canEdit) {
      if (!multi) items.push({ label: "Edit", iconComponent: IconPencil, onSelect: () => startEdit(at.r, at.c) });
      items.push({ label: multi ? "Set NULL (all selected)" : "Set NULL", iconComponent: IconX, onSelect: () => setSel(null) });
      if (nEdited) items.push({ label: nEdited === 1 ? "Revert the change" : `Revert ${nEdited} changes`, iconComponent: IconRotateCw, onSelect: () => setSel(undefined) });
    }
    if (!multi) items.push({ label: "Open value", iconComponent: IconEye, onSelect: () => openValue(at) });
    items.push(
      multi
        ? { label: `Copy ${sizeText()}`, iconComponent: IconCopy, sep: true, onSelect: () => void copySel() }
        : { label: "Copy value", iconComponent: IconCopy, sep: true, onSelect: () => void copySel() },
      { label: "Copy with headers", iconComponent: IconTable, onSelect: () => void copySel(true) },
      { label: "Copy as Markdown", iconComponent: IconClipboard, onSelect: () => copyText(tableMarkdown(selCols().map((c) => c.name), selCells()), "Markdown") },
    );
    if (sqlEngine) items.push({ label: "Copy as WHERE", iconComponent: IconSql, onSelect: copyWhere });
    const extra = rowMenu?.(selRows.map((i) => base[i])) ?? [];
    if (extra.length) items.push({ ...extra[0], sep: true }, ...extra.slice(1));
    contextMenu.show(e, items);
  }

  const openedValue = $derived(opened ? shown(opened.r, opened.c) : null);
  const openedPretty = $derived.by(() => {
    const v = openedValue;
    if (v === null) return null;
    const t = v.trim();
    if ((t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))) {
      try {
        return JSON.stringify(JSON.parse(t), null, 2);
      } catch {
        /* not JSON */
      }
    }
    return v;
  });

  function onKey(e: KeyboardEvent) {
    // A click focuses the cell itself (tabindex -1), so keys come from cells
    // too; a field or button inside the grid keeps its own.
    if ((e.target as HTMLElement | null)?.closest?.("input, textarea, button")) return;
    const mod = e.ctrlKey || e.metaKey;
    if (mod && e.key.toLowerCase() === "f") {
      e.preventDefault();
      e.stopPropagation();
      openFind();
      return;
    }
    if (e.key === "Escape" && findOpen) {
      closeFind();
      return;
    }
    if (mod && e.key.toLowerCase() === "a" && rows.length && cols.length) {
      e.preventDefault();
      anchor = { r: 0, c: 0 };
      sel = { r: rows.length - 1, c: cols.length - 1 };
      return;
    }
    if (!sel) return;
    if (mod && e.key.toLowerCase() === "c") {
      e.preventDefault();
      void copySel();
      return;
    }
    const moves: Record<string, [number, number]> = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] };
    const m = moves[e.key];
    if (m) {
      e.preventDefault();
      pick(Math.max(0, Math.min(rows.length - 1, sel.r + m[0])), Math.max(0, Math.min(cols.length - 1, sel.c + m[1])), e.shiftKey);
      scrollToSel();
    } else if (e.key === "Enter" && e.shiftKey) {
      openValue(sel);
    } else if ((e.key === "Enter" || e.key === "F2") && canEdit) {
      e.preventDefault();
      startEdit(sel.r, sel.c);
    } else if (e.key === "Enter") {
      openValue(sel);
    }
  }

  function exportAs(kind: "csv" | "json") {
    // As shown: an edited cell exports its edit.
    const out = rows.map((r, ri) => r.map((_, ci) => shown(ri, ci)));
    return saveExport(kind, kind === "csv" ? rowsCsv(cols, out) : rowsJson(cols, out));
  }
  async function saveExport(kind: "csv" | "json", text: string) {
    try {
      const path = await api.saveTextFile(`${exportName}.${kind}`, text);
      if (path) toast.ok(`Exported ${rows.length} row${rows.length === 1 ? "" : "s"}${rows.length !== base.length ? " (filtered)" : ""} to ${path}`);
    } catch (e) {
      toast.err(`Export: ${String((e as any)?.message ?? e)}`);
    }
  }
</script>

<div class="grid-wrap">
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="scroller"
    bind:this={scroller}
    bind:clientHeight={viewH}
    onscroll={(e) => (scrollTop = (e.currentTarget as HTMLDivElement).scrollTop)}
    onkeydown={onKey}
    tabindex="0"
    role="grid"
    aria-rowcount={rows.length}
  >
    <div class="inner" style:width="{totalW}px">
      <div class="row head" style:grid-template-columns={template}>
        <div class="cell num">#</div>
        {#each cols as c, i (i)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div
            class="cell sortable"
            class:sorted={sort?.c === i || serverSort?.column === c.name}
            role="columnheader"
            tabindex="-1"
            aria-sort={sort?.c === i ? (sort.desc ? "descending" : "ascending") : serverSort?.column === c.name ? (serverSort.desc ? "descending" : "ascending") : "none"}
            title="{c.name} ({c.type}) - click to sort {onServerSort ? 'the table' : 'the rows shown'}"
            onclick={() => toggleSort(i)}
          >
            <span class="cname">{c.name}</span><span class="ctype">{c.type}</span>
            {#if sort?.c === i}<span class="sort-ic">{#if sort.desc}<IconArrowDown size={11} />{:else}<IconArrowUp size={11} />{/if}</span>{/if}
            {#if serverSort?.column === c.name}<span class="sort-ic">{#if serverSort.desc}<IconArrowDown size={11} />{:else}<IconArrowUp size={11} />{/if}</span>{/if}
          </div>
        {/each}
      </div>
      <div class="body" style:height="{rows.length * ROW_H}px">
        {#each visible as r, k (first + k)}
          {@const ri = first + k}
          <div class="row" class:odd={ri % 2 === 1} style:grid-template-columns={template} style:top="{ri * ROW_H}px">
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="cell num"
              class:dirty={dirtyRows.has(view[ri])}
              title={dirtyRows.has(view[ri]) ? "This row has changes not in a script yet" : "Click to pick the row (Shift: rows)"}
              onmousedown={(e) => rowDown(e, ri)}
              oncontextmenu={openMenu}
            >{view[ri] + 1}</div>
            {#each r as v0, ci (ci)}
              {@const ek = `${view[ri]}:${ci}`}
              {@const edited = !!edits?.has(ek)}
              {@const v = edited ? edits!.get(ek)! : v0}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <div
                class="cell"
                class:lines
                class:edited
                class:null={v === null}
                class:hit={needle !== "" && v !== null && v.toLowerCase().includes(needle)}
                class:sel={sel?.r === ri && sel?.c === ci}
                class:inrect={multi && inRect(ri, ci)}
                class:inline={inline?.r === ri && inline?.c === ci}
                role="gridcell"
                tabindex="-1"
                title={edited ? `Changed, not in a script yet (was: ${v0 === null ? "NULL" : v0.length > 80 ? v0.slice(0, 80) + "..." : v0})` : undefined}
                onmousedown={(e) => cellDown(e, ri, ci)}
                onmouseenter={() => cellEnter(ri, ci)}
                ondblclick={() => (canEdit ? startEdit(ri, ci) : openValue({ r: ri, c: ci }))}
                oncontextmenu={openMenu}
              >{#if inline?.r === ri && inline?.c === ci}<input
                  class="inline-edit"
                  bind:this={inlineEl}
                  bind:value={inline.value}
                  placeholder={inline.orig === null ? "NULL" : ""}
                  spellcheck="false"
                  onkeydown={inlineKey}
                  onblur={commitInline}
                  onmousedown={(e) => e.stopPropagation()}
                  ondblclick={(e) => e.stopPropagation()}
                />{:else}{#if v !== null && onLink && canLink?.(r, ci)}<button
                  class="fk"
                  title={linkTitle?.(ci) ?? "Open the referenced row"}
                  onclick={(e) => { e.stopPropagation(); onLink(r, ci); }}
                  ondblclick={(e) => e.stopPropagation()}
                ><IconLink size={11} /></button>{/if}{v === null ? "NULL" : v.length > 300 ? v.slice(0, 300) + "..." : v}{/if}</div>
            {/each}
          </div>
        {/each}
      </div>
    </div>
  </div>
  {#if opened}
    <div class="value-pane">
      <div class="vp-head">
        <span class="vp-title">{cols[opened.c]?.name} · row {view[opened.r] + 1}{openedValue !== null ? ` · ${openedValue.length} chars` : ""}</span>
        {#if editable && onEdit && editing === null}
          <button title="Change this value (collected into an UPDATE script, nothing is sent yet)" onclick={() => opened && openValue(opened, true)}>Edit</button>
        {/if}
        <button title="Copy the value" onclick={() => api.clipboardSetText(openedValue ?? "").catch(() => {})}><IconCopy size={13} /></button>
        <button title="Close" onclick={() => openValue(null)}><IconX size={13} /></button>
      </div>
      {#if editing !== null}
        <textarea class="vp-edit" bind:value={editing} spellcheck="false"></textarea>
        <div class="vp-actions">
          <button onclick={() => saveEdit(editing)}>Set</button>
          <button title="Set the value to NULL" onclick={() => saveEdit(null)}>Set NULL</button>
          <button onclick={() => (editing = null)}>Cancel</button>
        </div>
      {:else}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
        <pre class:null={openedValue === null} tabindex="0" onkeydown={selectAllWithin}>{openedPretty ?? "NULL"}</pre>
      {/if}
    </div>
  {/if}
  {#if findOpen}
    <div class="find">
      <IconSearch size={12} />
      <input
        bind:this={findInput}
        bind:value={findText}
        placeholder="Find in these rows"
        spellcheck="false"
        onkeydown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            closeFind();
          }
        }}
      />
      <span class="find-count">{needle ? `${rows.length} of ${base.length} rows` : ""}</span>
      <button title="Close (Esc)" onclick={closeFind}><IconX size={12} /></button>
    </div>
  {/if}
  <div class="foot">
    <span>
      {#if needle}{rows.length} of {base.length} rows match{:else}{rows.length} row{rows.length === 1 ? "" : "s"}{/if}{result.truncated ? " (limit reached - more rows exist)" : ""}
      {#if sort || needle}
        · {sort ? `sorted by ${cols[sort.c]?.name}` : "filtered"} here{paged ? " (this page only)" : result.truncated ? " (fetched rows only)" : ""}
      {/if}
      {#if result.cut_cells > 0} · {result.cut_cells} long value{result.cut_cells === 1 ? "" : "s"} cut{/if}
      {#if result.note} · {result.note}{/if}
      · {result.duration_ms} ms
    </span>
    <span class="spacer"></span>
    {#if base.length > 0 && !findOpen}
      <button onclick={openFind} title="Find in these rows (Ctrl+F)"><IconSearch size={12} /></button>
    {/if}
    {#if rows.length > 0}
      <button onclick={() => exportAs("csv")} title="Save these rows as CSV"><IconDownload size={12} /> CSV</button>
      <button onclick={() => exportAs("json")} title="Save these rows as JSON"><IconDownload size={12} /> JSON</button>
    {/if}
  </div>
</div>

<style>
  .grid-wrap { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .scroller { flex: 1 1 auto; min-height: 0; overflow: auto; outline: none; font-size: var(--fs-sm); font-family: var(--mono-font, ui-monospace, monospace); }
  .inner { position: relative; min-width: 100%; }
  .row { display: grid; height: 24px; align-items: stretch; }
  .body { position: relative; }
  .body .row { position: absolute; left: 0; right: 0; }
  .row.odd { background: color-mix(in srgb, var(--surface0) 30%, transparent); }
  .head { position: sticky; top: 0; z-index: 1; background: var(--surface0); font-family: inherit; }
  .cell {
    padding: 0 0.45rem; line-height: 24px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    border-right: 1px solid color-mix(in srgb, var(--surface1) 60%, transparent); color: var(--text); cursor: default;
  }
  .cell.lines { white-space: pre; }
  .head .cell { display: flex; gap: 0.35rem; align-items: baseline; font-weight: 600; }
  .cname { overflow: hidden; text-overflow: ellipsis; }
  .ctype { font-weight: 400; font-size: var(--fs-xs); color: var(--overlay1); }
  .cell.num { color: var(--overlay1); text-align: right; background: color-mix(in srgb, var(--surface0) 55%, transparent); }
  .cell.null { color: var(--overlay0); font-style: italic; }
  .cell.edited { position: relative; background: color-mix(in srgb, var(--yellow) 18%, transparent); }
  /* The corner mark stays when the cell is picked (its blue would hide the yellow). */
  .cell.edited::after {
    content: ""; position: absolute; top: 0; right: 0; width: 0; height: 0;
    border-top: 6px solid var(--yellow); border-left: 6px solid transparent;
  }
  .cell.num.dirty { color: var(--yellow); font-weight: 600; box-shadow: inset 3px 0 0 var(--yellow); }
  .cell.num { cursor: pointer; }
  .vp-edit {
    flex: 1 1 auto; min-height: 4rem; margin: 0.3rem 0.5rem; font-family: var(--mono-font, ui-monospace, monospace); font-size: var(--fs-sm);
    background: var(--base); color: var(--text); border: 1px solid var(--surface1); border-radius: var(--r-sm); padding: 0.3rem;
  }
  .vp-actions { display: flex; gap: 0.35rem; padding: 0 0.5rem 0.4rem; }
  .cell .fk {
    display: inline-flex; vertical-align: -1px; margin-right: 0.3rem; padding: 0; border: 0; background: none;
    color: var(--blue); cursor: pointer;
  }
  .cell .fk:hover { color: var(--sapphire); }
  .head .sortable { cursor: pointer; }
  .head .sortable:hover { background: var(--surface1); }
  .head .sorted .cname { color: var(--blue); }
  .sort-ic { display: inline-flex; align-self: center; color: var(--blue); margin-left: auto; flex: 0 0 auto; }
  .cell.hit { background: color-mix(in srgb, var(--yellow) 22%, transparent); }
  .find { flex: 0 0 auto; display: flex; align-items: center; gap: 0.35rem; padding: 0.2rem 0.5rem; border-top: 1px solid var(--surface0); color: var(--overlay1); }
  .find input { flex: 0 1 22rem; min-width: 8rem; font-size: var(--fs-sm); padding: 0.15rem 0.4rem; background: var(--base); color: var(--text); border: 1px solid var(--surface1); border-radius: var(--r-sm); outline: none; }
  .find input:focus { border-color: var(--blue); }
  .find-count { font-size: var(--fs-xs); color: var(--subtext0); }
  .find button { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0.15rem; display: inline-flex; }
  .find button:hover { color: var(--text); }
  .cell.inrect { background: color-mix(in srgb, var(--blue) 12%, transparent); }
  .cell.sel { outline: 1px solid var(--blue); outline-offset: -1px; background: color-mix(in srgb, var(--blue) 14%, transparent); }
  .cell.edited.sel, .cell.edited.inrect { background: color-mix(in srgb, var(--yellow) 22%, color-mix(in srgb, var(--blue) 14%, transparent)); }
  .cell.inline { padding: 0; overflow: visible; }
  .inline-edit {
    width: 100%; height: 100%; box-sizing: border-box; margin: 0; padding: 0 0.4rem; font: inherit; color: var(--text);
    background: var(--base); border: 1px solid var(--blue); border-radius: 0; outline: none;
  }
  .inline-edit::placeholder { color: var(--overlay0); font-style: italic; }
  .scroller { user-select: none; }
  .value-pane { flex: 0 0 auto; max-height: 40%; display: flex; flex-direction: column; border-top: 1px solid var(--surface1); background: var(--mantle); }
  .vp-head { display: flex; align-items: center; gap: 0.3rem; padding: 0.2rem 0.5rem; font-size: var(--fs-sm); color: var(--subtext0); }
  .vp-title { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .vp-head button { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0.15rem; display: inline-flex; }
  .vp-head button:hover { color: var(--text); }
  pre { margin: 0; padding: 0.4rem 0.6rem; overflow: auto; font-size: var(--fs-sm); white-space: pre-wrap; word-break: break-all; color: var(--text); user-select: text; }
  pre.null { color: var(--overlay0); font-style: italic; }
  pre:focus { outline: none; }
  .foot { flex: 0 0 auto; display: flex; align-items: center; gap: 0.4rem; padding: 0.2rem 0.5rem; font-size: var(--fs-sm); color: var(--subtext0); border-top: 1px solid var(--surface0); }
  .spacer { flex: 1 1 auto; }
  .foot button {
    display: inline-flex; align-items: center; gap: 0.25rem; font-size: var(--fs-xs); padding: 0.1rem 0.45rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: var(--r-sm); cursor: pointer;
  }
  .foot button:hover { background: var(--surface1); }
</style>
