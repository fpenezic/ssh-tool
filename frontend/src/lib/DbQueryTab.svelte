<script lang="ts">
  // A query tab: editor on top, results below. It runs on its own server
  // session (app_db.go), so SET, temp tables and an open transaction carry
  // from one run to the next. The SQL lives in the tab state (dbState), the
  // results in its run state, so a remount shows both again.

  import { untrack } from "svelte";
  import { api, type DbHandleInfo, type DbResult } from "./api";
  import { dbTabs, byteOffset, charIndex, type DbTab } from "./dbState.svelte";
  import DbEditor from "./DbEditor.svelte";
  import DbResultGrid from "./DbResultGrid.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconPlay, IconStop, IconSql } from "./iconMap";

  interface Props {
    sessionId: string;
    tab: DbTab;
    info: DbHandleInfo;
    databases: string[];
    tables: string[];
    onChange: () => void;
  }
  let { sessionId, tab, info, databases, tables, onChange }: Props = $props();

  const run = $derived(dbTabs.peek(sessionId, tab.key));
  const live = () => dbTabs.run(sessionId, tab.key);
  let editor = $state<DbEditor | null>(null);
  let maxRows = $state(500);
  let editorH = $state(220);

  // The editor's text, mirrored into the tab state (which is saved). Each
  // effect tracks one side only, or a keystroke would be undone by the
  // other side's stale value.
  let sqlText = $state(untrack(() => tab.sql ?? ""));
  $effect(() => {
    const s = tab.sql ?? "";
    untrack(() => {
      if (s !== sqlText) sqlText = s;
    });
  });
  $effect(() => {
    const v = sqlText;
    untrack(() => {
      if (v !== (tab.sql ?? "")) {
        tab.sql = v;
        onChange();
      }
    });
  });

  async function exec(mode: "statement" | "all") {
    if (run.running) return;
    let script = sqlText;
    if (mode === "statement") {
      const s = editor?.selection();
      if (s?.text) script = s.text;
      else {
        const st = await api.dbStatementAt(info.engine, sqlText, byteOffset(sqlText, s?.cursor ?? 0));
        if (!st) return;
        const from = charIndex(sqlText, st.start);
        const to = charIndex(sqlText, st.end);
        editor?.flash(from, to);
        script = sqlText.slice(from, to);
      }
    }
    if (!script.trim()) return;
    if (info.sensitive && !info.read_only) {
      const writes = (await api.dbIsWrite(info.engine, script)) ?? [];
      if (writes.length > 0) {
        const list = writes.slice(0, 5).map((w) => (w.length > 160 ? w.slice(0, 160) + "..." : w)).join("\n\n");
        const ok = await showConfirm({
          title: "Change data on a sensitive connection?",
          message: `${writes.length} statement${writes.length === 1 ? " changes" : "s change"} data on ${info.name}:\n\n${list}${writes.length > 5 ? `\n\n... and ${writes.length - 5} more` : ""}`,
          okLabel: "Run",
          danger: true,
        });
        if (!ok) return;
      }
    }
    await send(script);
  }

  async function send(script: string) {
    const run = live();
    run.running = true;
    run.error = undefined;
    try {
      const r = await api.dbRun(sessionId, tab.key, tab.database, script, maxRows);
      const results = r.results ?? [];
      run.results = results;
      // In front: the error, else the last result set, else the last result.
      let shown = results.findIndex((x) => !!x.error);
      if (shown < 0) {
        shown = results.length - 1;
        for (let i = results.length - 1; i >= 0; i--) if (results[i].has_rows) { shown = i; break; }
      }
      run.shown = Math.max(0, shown);
      run.inTx = r.in_tx;
      run.ranAt = Date.now();
    } catch (e) {
      run.error = errMsg(e);
    } finally {
      run.running = false;
    }
  }

  function setDatabase(db: string) {
    if (db === tab.database) return;
    if (run.inTx) {
      live().error = "Commit or roll back the open transaction before switching database.";
      return;
    }
    tab.database = db;
    onChange();
  }

  function label(r: DbResult, i: number): string {
    if (r.error) return `${i + 1}: error`;
    if (r.has_rows) return `${i + 1}: ${(r.rows ?? []).length}${r.truncated ? "+" : ""} rows`;
    return `${i + 1}: ${r.command || (r.rows_affected >= 0 ? `${r.rows_affected} affected` : "done")}`;
  }

  function startDrag(e: PointerEvent) {
    const y0 = e.clientY;
    const h0 = editorH;
    const move = (ev: PointerEvent) => (editorH = Math.max(80, Math.min(window.innerHeight - 200, h0 + ev.clientY - y0)));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }

  const shownResult = $derived(run.results[run.shown]);
</script>

<div class="qtab">
  <div class="bar">
    <select title="Database this tab runs on" value={tab.database} onchange={(e) => setDatabase((e.currentTarget as HTMLSelectElement).value)}>
      {#if !databases.includes(tab.database)}<option value={tab.database}>{tab.database || "(default)"}</option>{/if}
      {#each databases as d (d)}<option value={d}>{d}</option>{/each}
    </select>
    {#if run.running}
      <button class="stop" title="Stop the running statement" onclick={() => api.dbCancel(sessionId, tab.key)}><IconStop size={13} /> Stop</button>
    {:else}
      <button class="go" title="Run the statement under the cursor, or the selection (Ctrl+Enter)" onclick={() => exec("statement")}><IconPlay size={13} /> Run</button>
      <button title="Run the whole script, statement by statement (Ctrl+Shift+Enter)" onclick={() => exec("all")}><IconSql size={13} /> Run all</button>
    {/if}
    <label class="limit" title="Rows a statement brings back at most; the rest is not read">
      Limit
      <select bind:value={maxRows}>
        {#each [100, 500, 1000, 5000, 20000, 100000] as n (n)}<option value={n}>{n}</option>{/each}
      </select>
    </label>
    <span class="spacer"></span>
    {#if run.inTx}
      <span class="tx" title="This tab's session has an open transaction">in transaction</span>
      <button onclick={() => send("COMMIT")}>Commit</button>
      <button onclick={() => send("ROLLBACK")}>Roll back</button>
    {/if}
    {#if info.read_only}<span class="ro" title="Writes are refused on this connection">read-only</span>{/if}
  </div>
  <div class="editor" style:height="{editorH}px">
    <DbEditor bind:this={editor} bind:value={sqlText} engine={info.engine} {tables} onRun={exec} />
  </div>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="drag" onpointerdown={startDrag} title="Drag to resize"></div>
  <div class="results">
    {#if run.error}
      <pre class="err">{run.error}</pre>
    {:else if run.running && run.results.length === 0}
      <p class="muted">Running...</p>
    {:else if run.results.length === 0}
      <p class="muted">Results show here. Ctrl+Enter runs the statement under the cursor.</p>
    {:else}
      {#if run.results.length > 1}
        <div class="rtabs">
          {#each run.results as r, i (i)}
            <button class:on={i === run.shown} class:bad={!!r.error} title={r.statement} onclick={() => (live().shown = i)}>{label(r, i)}</button>
          {/each}
        </div>
      {/if}
      {#if shownResult}
        {#if shownResult.error}
          <pre class="err">{shownResult.error}</pre>
          <pre class="stmt">{shownResult.statement}</pre>
        {:else if shownResult.has_rows}
          <div class="gridbox"><DbResultGrid result={shownResult} exportName={`${info.name}-${tab.title}`.replace(/[^\w.-]+/g, "_")} /></div>
        {:else}
          <p class="done">
            {shownResult.command || "Done"}{shownResult.rows_affected >= 0 && !shownResult.command ? ` - ${shownResult.rows_affected} row${shownResult.rows_affected === 1 ? "" : "s"} affected` : ""} · {shownResult.duration_ms} ms
          </p>
        {/if}
      {/if}
    {/if}
  </div>
</div>

<style>
  .qtab { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 0.35rem; padding: 0.3rem 0.5rem; border-bottom: 1px solid var(--surface0); flex-wrap: wrap; }
  .bar select { background: var(--mantle); color: var(--text); border: 1px solid var(--surface0); border-radius: 3px; padding: 0.15rem 0.3rem; font-size: 0.78rem; }
  .bar button {
    display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.78rem; padding: 0.2rem 0.6rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; cursor: pointer;
  }
  .bar button:hover { background: var(--surface1); }
  .bar button.go { background: color-mix(in srgb, var(--green) 22%, var(--surface0)); border-color: color-mix(in srgb, var(--green) 50%, var(--surface1)); }
  .bar button.stop { background: color-mix(in srgb, var(--red) 22%, var(--surface0)); border-color: color-mix(in srgb, var(--red) 50%, var(--surface1)); }
  .limit { display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.75rem; color: var(--subtext0); }
  .spacer { flex: 1 1 auto; }
  .tx { font-size: 0.72rem; padding: 0.05rem 0.4rem; border-radius: 3px; background: color-mix(in srgb, var(--yellow) 25%, transparent); color: var(--yellow); }
  .ro { font-size: 0.72rem; color: var(--peach); }
  .editor { flex: 0 0 auto; min-height: 80px; border-bottom: 1px solid var(--surface0); }
  .drag { flex: 0 0 5px; cursor: row-resize; background: var(--surface0); }
  .drag:hover { background: var(--surface1); }
  .results { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
  .gridbox { flex: 1 1 auto; min-height: 0; }
  .rtabs { display: flex; gap: 0.25rem; padding: 0.25rem 0.5rem; flex-wrap: wrap; border-bottom: 1px solid var(--surface0); }
  .rtabs button { font-size: 0.72rem; padding: 0.1rem 0.45rem; background: var(--surface0); color: var(--subtext0); border: 1px solid transparent; border-radius: 3px; cursor: pointer; }
  .rtabs button.on { color: var(--text); border-color: var(--blue); }
  .rtabs button.bad { color: var(--red); }
  .muted, .done { margin: 0.6rem 0.75rem; font-size: 0.8rem; color: var(--subtext0); }
  .done { color: var(--text); }
  pre { margin: 0.5rem 0.75rem; white-space: pre-wrap; font-size: 0.78rem; }
  pre.err { color: var(--red); }
  pre.stmt { color: var(--overlay1); }
</style>
