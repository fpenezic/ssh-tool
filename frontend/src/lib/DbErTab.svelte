<script lang="ts">
  // The ER diagram of one schema: its tables as boxes (primary key and
  // foreign key columns marked), the foreign keys between them as lines.
  // Drag a box by its header, drag the background to pan, Ctrl + wheel to
  // zoom; hovering a box lights up its keys, a double-click on the header
  // opens the table. Moved boxes stay where they were put (saved with the
  // tab); Arrange lays them out again.

  import { onDestroy, onMount, untrack } from "svelte";
  import { api, type DbForeignKey, type DbHandleInfo, type DbSchemaGraph } from "./api";
  import type { DbTab } from "./dbState.svelte";
  import { errMsg } from "./connectErrors";
  import { HEAD, ROW, bounds, boxes, edgePath, edgesOf, fkColumns, layout, type ErBox } from "./erLayout";
  import { IconRefresh } from "./iconMap";

  interface Props {
    sessionId: string;
    tab: DbTab;
    info: DbHandleInfo;
    onChange: () => void;
    onOpenTable: (database: string, schema: string, name: string) => void;
  }
  let { sessionId, tab, info, onChange, onOpenTable }: Props = $props();

  // Above this many tables the boxes start folded to their keys.
  const FOLD_AT = 120;

  let graph = $state<DbSchemaGraph | null>(null);
  let err = $state<string | null>(null);
  let loading = $state(false);
  let list = $state<ErBox[]>([]);
  let filter = $state("");
  let hover = $state<string | null>(null);
  let hoverEdge = $state<DbForeignKey | null>(null);
  let view = $state({ x: 20, y: 20, k: 1 });
  let svg: SVGSVGElement | undefined = $state();

  const keysOnly = $derived(tab.keysOnly ?? (graph ? graph.tables.length > FOLD_AT : false));

  // Text widths in the diagram's font.
  let ctx: CanvasRenderingContext2D | null = null;
  function measure(s: string): number {
    ctx ??= document.createElement("canvas").getContext("2d");
    if (!ctx) return s.length * 7;
    ctx.font = "12px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace";
    return ctx.measureText(s).width;
  }

  function build(arrange: boolean) {
    if (!graph) return;
    const b = boxes(graph.tables, graph.foreign_keys, keysOnly, measure);
    const saved = arrange ? undefined : tab.layout;
    const has = (n: string) => !!saved && Object.hasOwn(saved, n);
    if (saved && b.every((x) => has(x.name))) {
      for (const x of b) [x.x, x.y] = saved[x.name];
    } else {
      layout(b, edgesOf(graph.tables, graph.foreign_keys));
      if (saved) {
        // Tables new since the save: laid out among themselves under the
        // saved ones, which stay where they were put.
        const old = b.filter((x) => has(x.name));
        const fresh = b.filter((x) => !has(x.name));
        for (const x of old) [x.x, x.y] = saved[x.name];
        if (old.length && fresh.length) {
          const top = bounds(old);
          const sub = bounds(fresh);
          for (const x of fresh) {
            x.x += top.x - sub.x;
            x.y += top.y + top.h + 80 - sub.y;
          }
        }
      }
    }
    list = b;
    if (arrange) {
      tab.layout = undefined;
      onChange();
    }
  }

  async function load() {
    if (loading) return;
    loading = true;
    try {
      graph = await api.dbSchemaGraph(sessionId, tab.database, tab.schema ?? "");
      err = null;
      build(false);
      requestAnimationFrame(fit);
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  const byName = $derived(new Map(list.map((b) => [b.name, b])));
  const edges = $derived(graph ? edgesOf(graph.tables, graph.foreign_keys) : []);
  const fkc = $derived(graph ? fkColumns(graph.foreign_keys) : new Map<string, Set<string>>());
  // Where a column's key goes when the table is in another schema.
  const outside = $derived.by(() => {
    const m = new Map<string, string>();
    if (!graph) return m;
    const inside = new Set(edges.map((e) => e.fk));
    for (const fk of graph.foreign_keys) {
      if (inside.has(fk)) continue;
      const to = [fk.ref_database !== fk.database ? fk.ref_database : "", fk.ref_schema, fk.ref_table].filter(Boolean).join(".");
      for (const c of fk.columns ?? []) m.set(`${fk.table}\0${c}`, to);
    }
    return m;
  });

  // Filter: matching tables and the ones they key to or from stay lit.
  const lit = $derived.by(() => {
    const f = filter.trim().toLowerCase();
    const focus = hover ?? null;
    if (!f && !focus) return null;
    // The matches, then their direct neighbours (checked against the
    // matches, not the growing set, so the result does not depend on the
    // order of the keys).
    const hits = new Set<string>();
    for (const b of list) if ((f && b.name.toLowerCase().includes(f)) || b.name === focus) hits.add(b.name);
    const s = new Set(hits);
    for (const e of edges) {
      if (focus ? e.from === focus || e.to === focus : hits.has(e.from) || hits.has(e.to)) {
        s.add(e.from);
        s.add(e.to);
      }
    }
    return s;
  });

  function fit() {
    if (!svg || !list.length) return;
    const r = svg.getBoundingClientRect();
    const bb = bounds(list);
    const k = Math.min(1.2, Math.max(0.1, Math.min((r.width - 40) / Math.max(1, bb.w), (r.height - 40) / Math.max(1, bb.h))));
    view = { k, x: 20 - bb.x * k + Math.max(0, (r.width - 40 - bb.w * k) / 2), y: 20 - bb.y * k };
  }
  function zoomBy(f: number, cx?: number, cy?: number) {
    if (!svg) return;
    const r = svg.getBoundingClientRect();
    const px = cx ?? r.width / 2;
    const py = cy ?? r.height / 2;
    const k = Math.min(3, Math.max(0.08, view.k * f));
    view = { k, x: px - ((px - view.x) * k) / view.k, y: py - ((py - view.y) * k) / view.k };
  }
  function onWheel(e: WheelEvent) {
    e.preventDefault();
    if (e.ctrlKey || e.metaKey) {
      const r = svg!.getBoundingClientRect();
      zoomBy(Math.exp(-e.deltaY / 300), e.clientX - r.left, e.clientY - r.top);
    } else {
      view = { ...view, x: view.x - (e.shiftKey ? e.deltaY : e.deltaX), y: view.y - (e.shiftKey ? 0 : e.deltaY) };
    }
  }

  // One drag at a time: the background pans, a header moves its box. A
  // cancelled pointer or an unmounted tab ends it without saving.
  let endDrag: ((save: boolean) => void) | null = null;
  onDestroy(() => endDrag?.(false));
  function drag(e: PointerEvent, box?: ErBox) {
    if (e.button !== 0) return;
    e.stopPropagation();
    endDrag?.(false);
    const x0 = e.clientX, y0 = e.clientY;
    const start = box ? { x: box.x, y: box.y } : { x: view.x, y: view.y };
    let moved = false;
    const move = (ev: PointerEvent) => {
      const dx = ev.clientX - x0, dy = ev.clientY - y0;
      moved ||= Math.abs(dx) + Math.abs(dy) > 2;
      if (box) {
        box.x = Math.round(start.x + dx / view.k);
        box.y = Math.round(start.y + dy / view.k);
      } else {
        view = { ...view, x: start.x + dx, y: start.y + dy };
      }
    };
    const finish = (save: boolean) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", cancel);
      endDrag = null;
      if (save && box && moved) {
        tab.layout = Object.fromEntries(list.map((b) => [b.name, [b.x, b.y] as [number, number]]));
        onChange();
      } else if (!save && box && moved) {
        box.x = start.x;
        box.y = start.y;
      }
    };
    const up = () => finish(true);
    const cancel = () => finish(false);
    endDrag = finish;
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", cancel);
  }

  function setKeysOnly(v: boolean) {
    tab.keysOnly = v;
    onChange();
    // Box heights change; the positions stay (an arranged layout is redone).
    untrack(() => build(false));
  }

  function edgeTitle(fk: DbForeignKey): string {
    return `${fk.name}: ${fk.table}(${(fk.columns ?? []).join(", ")}) -> ${fk.ref_table}(${(fk.ref_columns ?? []).join(", ")})`;
  }
  const where = $derived(tab.schema ? `${tab.database}.${tab.schema}` : tab.database);
</script>

<div class="er">
  <div class="bar">
    <span class="title">{where}</span>
    {#if graph}<span class="muted">{graph.tables.length} table{graph.tables.length === 1 ? "" : "s"} · {graph.foreign_keys.length} foreign key{graph.foreign_keys.length === 1 ? "" : "s"}</span>{/if}
    <input placeholder="Find tables" bind:value={filter} spellcheck="false" />
    <label title="Show only the primary and foreign key columns"><input type="checkbox" checked={keysOnly} onchange={(e) => setKeysOnly((e.currentTarget as HTMLInputElement).checked)} /> keys only</label>
    <span class="spacer"></span>
    <button onclick={() => zoomBy(1 / 1.25)} title="Zoom out (Ctrl + wheel)">-</button>
    <span class="muted zoom">{Math.round(view.k * 100)}%</span>
    <button onclick={() => zoomBy(1.25)} title="Zoom in (Ctrl + wheel)">+</button>
    <button onclick={fit} title="Fit the diagram in the view">Fit</button>
    <button onclick={() => { build(true); requestAnimationFrame(fit); }} title="Lay the boxes out again (drops where they were moved)">Arrange</button>
    <button class="icon" onclick={load} disabled={loading} title="Read the schema again"><IconRefresh size={13} /></button>
  </div>
  {#if err}
    <pre class="err">{err}</pre>
  {:else if !graph}
    <p class="muted pad">{loading ? "Reading the schema..." : ""}</p>
  {:else if !graph.tables.length}
    <p class="muted pad">No tables in {where}.</p>
  {:else}
    {#if graph.tables.length > FOLD_AT && tab.keysOnly === undefined}
      <p class="note">{graph.tables.length} tables: the boxes show only their key columns. Untick "keys only" for every column.</p>
    {/if}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <svg bind:this={svg} class="canvas" onpointerdown={(e) => drag(e)} onwheel={onWheel}>
      <defs>
        <marker id="er-arrow-{sessionId}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" class="arrow" />
        </marker>
      </defs>
      <g transform="translate({view.x} {view.y}) scale({view.k})">
        {#each edges as e (e.fk.table + "\0" + e.fk.name)}
          {@const a = byName.get(e.from)}
          {@const b = byName.get(e.to)}
          {#if a && b}
            <path
              class="edge"
              class:on={lit?.has(e.from) && lit?.has(e.to) && (!hover || e.from === hover || e.to === hover)}
              class:dim={lit && !(lit.has(e.from) && lit.has(e.to) && (!hover || e.from === hover || e.to === hover))}
              d={edgePath(a, b, e.fk.columns?.[0], e.fk.ref_columns?.[0])}
              marker-end="url(#er-arrow-{sessionId})"
              onpointerenter={() => (hoverEdge = e.fk)}
              onpointerleave={() => (hoverEdge = null)}
            ><title>{edgeTitle(e.fk)}</title></path>
          {/if}
        {/each}
        {#each list as b (b.name)}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <g
            class="box"
            class:dim={lit && !lit.has(b.name)}
            class:hit={filter.trim() && b.name.toLowerCase().includes(filter.trim().toLowerCase())}
            transform="translate({b.x} {b.y})"
            onpointerenter={() => (hover = b.name)}
            onpointerleave={() => (hover = null)}
          >
            <rect class="frame" width={b.w} height={b.h} rx="4" />
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <g class="head" onpointerdown={(e) => drag(e, b)} ondblclick={() => onOpenTable(tab.database, tab.schema ?? "", b.name)}>
              <rect width={b.w} height={HEAD} rx="4" />
              <text x="8" y={HEAD / 2 + 4} class="tname">{b.name}</text>
              <title>{b.name} - double-click to open</title>
            </g>
            {#each b.cols as c, i (c.name)}
              {@const isFk = fkc.get(b.name)?.has(c.name)}
              {@const out = outside.get(`${b.name}\0${c.name}`)}
              <g transform="translate(0 {HEAD + i * ROW})">
                {#if c.key || isFk}<text x="8" y={ROW / 2 + 4} class="badge" class:key={c.key}>{c.key ? "PK" : "FK"}</text>{/if}
                <text x="30" y={ROW / 2 + 4} class="cname" class:key={c.key} class:fk={isFk}>{c.name}</text>
                <text x={b.w - 8} y={ROW / 2 + 4} class="ctype" text-anchor="end">{c.type}{c.nullable ? "?" : ""}</text>
                {#if out}<title>{c.name} references {out}</title>{/if}
              </g>
            {/each}
          </g>
        {/each}
      </g>
    </svg>
    {#if hoverEdge}<div class="tip">{edgeTitle(hoverEdge)}</div>{/if}
  {/if}
</div>

<style>
  .er { display: flex; flex-direction: column; height: 100%; min-height: 0; position: relative; }
  .bar { display: flex; align-items: center; gap: 0.5rem; padding: 0.35rem 0.6rem; border-bottom: 1px solid var(--surface0); font-size: 0.78rem; flex-wrap: wrap; }
  .bar input:not([type]) { width: 12rem; background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: 3px; padding: 0.2rem 0.4rem; font-size: 0.78rem; }
  .bar label { display: inline-flex; align-items: center; gap: 0.25rem; color: var(--subtext0); }
  .bar button { background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 3px; padding: 0.1rem 0.5rem; cursor: pointer; font-size: 0.78rem; }
  .bar button.icon { display: inline-flex; padding: 0.2rem 0.35rem; }
  .bar button:hover { background: var(--surface1); }
  .title { font-weight: 600; }
  .zoom { min-width: 2.6rem; text-align: center; }
  .spacer { flex: 1 1 auto; }
  .muted { color: var(--subtext0); }
  .pad { padding: 1rem; }
  .note { margin: 0; padding: 0.3rem 0.6rem; font-size: 0.75rem; color: var(--subtext0); background: var(--mantle); border-bottom: 1px solid var(--surface0); }
  .err { color: var(--red); padding: 1rem; white-space: pre-wrap; }
  .canvas { flex: 1 1 auto; min-height: 0; width: 100%; cursor: grab; background: var(--base); user-select: none; touch-action: none; }
  .canvas:active { cursor: grabbing; }
  .frame { fill: var(--mantle); stroke: var(--surface2); stroke-width: 1; }
  .head rect { fill: var(--surface0); stroke: var(--surface2); stroke-width: 1; }
  .head { cursor: move; }
  .tname { font: 600 12px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; fill: var(--text); }
  .cname { font: 12px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; fill: var(--subtext1); }
  .badge { font: 600 9px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; fill: var(--blue); }
  .badge.key { fill: var(--yellow); }
  .cname.key { fill: var(--yellow); font-weight: 600; }
  .cname.fk { fill: var(--blue); }
  .ctype { font: 11px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; fill: var(--overlay1); }
  .box.dim { opacity: 0.25; }
  .box.hit .frame { stroke: var(--blue); stroke-width: 2; }
  .edge { fill: none; stroke: var(--overlay0); stroke-width: 1.4; }
  .edge.on { stroke: var(--blue); stroke-width: 2.2; }
  .edge.dim { opacity: 0.15; }
  .arrow { fill: var(--overlay1); }
  .tip { position: absolute; left: 0.6rem; bottom: 0.5rem; font: 11px ui-monospace, monospace; background: var(--mantle); border: 1px solid var(--surface1); border-radius: 3px; padding: 0.2rem 0.4rem; color: var(--text); pointer-events: none; }
</style>
