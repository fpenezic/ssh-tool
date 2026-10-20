<script lang="ts">
  // A result set as a grid: only the rows in view are in the DOM, so the
  // 100k-row ceiling stays smooth. Click picks a cell (Ctrl+C copies it),
  // double-click opens the value in full below the grid (JSON
  // pretty-printed). A header click sorts and Ctrl+F filters the rows that
  // were fetched, here in the window; # keeps the server's row number.
  // Export writes the rows as shown.

  import { untrack } from "svelte";
  import { api, type DbResult } from "./api";
  import { toast } from "./toast.svelte.ts";
  import { IconDownload, IconX, IconCopy, IconArrowUp, IconArrowDown, IconSearch } from "./iconMap";
  import { selectAllWithin } from "./selectWithin";
  import { sortedOrder } from "./dbSort";

  interface Props {
    result: DbResult;
    // File name stem for exports.
    exportName: string;
    // The rows are one page of more (a table's Data view): sorting and
    // filtering say they cover this page only.
    paged?: boolean;
  }
  let { result, exportName, paged = false }: Props = $props();

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

  let sel = $state<{ r: number; c: number } | null>(null);
  let opened = $state<{ r: number; c: number } | null>(null);
  // A new result: back to the top, nothing picked. The filter stays (it is
  // what you are looking for), and so does the sort while the column is
  // still there by name - Next on a table keeps its order.
  $effect(() => {
    const cs = result.columns ?? [];
    untrack(() => {
      if (sort && cs[sort.c]?.name !== sort.name) sort = null;
    });
    sel = null;
    opened = null;
    if (scroller) scroller.scrollTop = 0;
  });
  // A new order or filter: what was picked points at another row.
  $effect(() => {
    void view;
    sel = null;
    opened = null;
  });

  const openedValue = $derived(opened ? rows[opened.r]?.[opened.c] ?? null : null);
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
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f") {
      e.preventDefault();
      e.stopPropagation();
      openFind();
      return;
    }
    if (e.key === "Escape" && findOpen) {
      closeFind();
      return;
    }
    if (!sel) return;
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "c") {
      const v = rows[sel.r]?.[sel.c];
      api.clipboardSetText(v ?? "").catch(() => {});
      e.preventDefault();
      return;
    }
    const moves: Record<string, [number, number]> = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] };
    const m = moves[e.key];
    if (m) {
      e.preventDefault();
      sel = { r: Math.max(0, Math.min(rows.length - 1, sel.r + m[0])), c: Math.max(0, Math.min(cols.length - 1, sel.c + m[1])) };
      if (scroller) {
        const top = sel.r * ROW_H;
        if (top < scroller.scrollTop) scroller.scrollTop = top;
        else if (top + ROW_H * 2 > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = top + ROW_H * 2 - scroller.clientHeight;
      }
    } else if (e.key === "Enter") {
      opened = sel;
    }
  }

  function csvCell(v: string | null): string {
    if (v === null) return "";
    return /[",\n\r]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
  }
  async function exportAs(kind: "csv" | "json") {
    let text: string;
    if (kind === "csv") {
      text = [cols.map((c) => csvCell(c.name)).join(","), ...rows.map((r) => r.map(csvCell).join(","))].join("\r\n") + "\r\n";
    } else {
      text = JSON.stringify(rows.map((r) => Object.fromEntries(cols.map((c, i) => [c.name, r[i]]))), null, 2) + "\n";
    }
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
            class:sorted={sort?.c === i}
            role="columnheader"
            tabindex="-1"
            aria-sort={sort?.c === i ? (sort.desc ? "descending" : "ascending") : "none"}
            title="{c.name} ({c.type}) - click to sort the rows shown"
            onclick={() => toggleSort(i)}
          >
            <span class="cname">{c.name}</span><span class="ctype">{c.type}</span>
            {#if sort?.c === i}<span class="sort-ic">{#if sort.desc}<IconArrowDown size={11} />{:else}<IconArrowUp size={11} />{/if}</span>{/if}
          </div>
        {/each}
      </div>
      <div class="body" style:height="{rows.length * ROW_H}px">
        {#each visible as r, k (first + k)}
          {@const ri = first + k}
          <div class="row" class:odd={ri % 2 === 1} style:grid-template-columns={template} style:top="{ri * ROW_H}px">
            <div class="cell num">{view[ri] + 1}</div>
            {#each r as v, ci (ci)}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <div
                class="cell"
                class:lines
                class:null={v === null}
                class:hit={needle !== "" && v !== null && v.toLowerCase().includes(needle)}
                class:sel={sel?.r === ri && sel?.c === ci}
                role="gridcell"
                tabindex="-1"
                onclick={() => (sel = { r: ri, c: ci })}
                ondblclick={() => (opened = { r: ri, c: ci })}
              >{v === null ? "NULL" : v.length > 300 ? v.slice(0, 300) + "..." : v}</div>
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
        <button title="Copy the value" onclick={() => api.clipboardSetText(openedValue ?? "").catch(() => {})}><IconCopy size={13} /></button>
        <button title="Close" onclick={() => (opened = null)}><IconX size={13} /></button>
      </div>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <pre class:null={openedValue === null} tabindex="0" onkeydown={selectAllWithin}>{openedPretty ?? "NULL"}</pre>
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
  .scroller { flex: 1 1 auto; min-height: 0; overflow: auto; outline: none; font-size: 0.78rem; font-family: var(--mono-font, ui-monospace, monospace); }
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
  .ctype { font-weight: 400; font-size: 0.68rem; color: var(--overlay1); }
  .cell.num { color: var(--overlay1); text-align: right; background: color-mix(in srgb, var(--surface0) 55%, transparent); }
  .cell.null { color: var(--overlay0); font-style: italic; }
  .head .sortable { cursor: pointer; }
  .head .sortable:hover { background: var(--surface1); }
  .head .sorted .cname { color: var(--blue); }
  .sort-ic { display: inline-flex; align-self: center; color: var(--blue); margin-left: auto; flex: 0 0 auto; }
  .cell.hit { background: color-mix(in srgb, var(--yellow) 22%, transparent); }
  .find { flex: 0 0 auto; display: flex; align-items: center; gap: 0.35rem; padding: 0.2rem 0.5rem; border-top: 1px solid var(--surface0); color: var(--overlay1); }
  .find input { flex: 0 1 22rem; min-width: 8rem; font-size: 0.76rem; padding: 0.15rem 0.4rem; background: var(--base); color: var(--text); border: 1px solid var(--surface1); border-radius: 3px; outline: none; }
  .find input:focus { border-color: var(--blue); }
  .find-count { font-size: 0.72rem; color: var(--subtext0); }
  .find button { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0.15rem; display: inline-flex; }
  .find button:hover { color: var(--text); }
  .cell.sel { outline: 1px solid var(--blue); outline-offset: -1px; background: color-mix(in srgb, var(--blue) 14%, transparent); }
  .value-pane { flex: 0 0 auto; max-height: 40%; display: flex; flex-direction: column; border-top: 1px solid var(--surface1); background: var(--mantle); }
  .vp-head { display: flex; align-items: center; gap: 0.3rem; padding: 0.2rem 0.5rem; font-size: 0.75rem; color: var(--subtext0); }
  .vp-title { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .vp-head button { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0.15rem; display: inline-flex; }
  .vp-head button:hover { color: var(--text); }
  pre { margin: 0; padding: 0.4rem 0.6rem; overflow: auto; font-size: 0.78rem; white-space: pre-wrap; word-break: break-all; color: var(--text); user-select: text; }
  pre.null { color: var(--overlay0); font-style: italic; }
  pre:focus { outline: none; }
  .foot { flex: 0 0 auto; display: flex; align-items: center; gap: 0.4rem; padding: 0.2rem 0.5rem; font-size: 0.74rem; color: var(--subtext0); border-top: 1px solid var(--surface0); }
  .spacer { flex: 1 1 auto; }
  .foot button {
    display: inline-flex; align-items: center; gap: 0.25rem; font-size: 0.72rem; padding: 0.1rem 0.45rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 3px; cursor: pointer;
  }
  .foot button:hover { background: var(--surface1); }
</style>
