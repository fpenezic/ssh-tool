<script lang="ts">
  // A table (or view) tab: its rows a page at a time in key order, its
  // structure, and its DDL. Read-only by construction - changes go through
  // a query tab ("Query" opens one on this table).

  import { api, type DbHandleInfo, type DbResult, type DbTableDetail } from "./api";
  import type { DbTab, DbTableView } from "./dbState.svelte";
  import DbResultGrid from "./DbResultGrid.svelte";
  import { errMsg } from "./connectErrors";
  import { IconRefresh, IconSql, IconCopy } from "./iconMap";
  import { selectAllWithin } from "./selectWithin";

  interface Props {
    sessionId: string;
    tab: DbTab;
    info: DbHandleInfo;
    onChange: () => void;
    onQuery: (sql: string) => void;
  }
  let { sessionId, tab, info, onChange, onQuery }: Props = $props();

  const PAGE = 200;
  const viewName = $derived<DbTableView>(tab.view ?? "data");
  let offset = $state(0);
  let page = $state<DbResult | null>(null);
  let detail = $state<DbTableDetail | null>(null);
  let err = $state<string | null>(null);
  let loading = $state(false);

  const qname = $derived(
    info.engine === "mysql"
      ? "`" + (tab.name ?? "").replace(/`/g, "``") + "`"
      : (tab.schema && tab.schema !== "public" ? `"${tab.schema.replace(/"/g, '""')}".` : "") + `"${(tab.name ?? "").replace(/"/g, '""')}"`,
  );

  async function loadPage() {
    loading = true;
    err = null;
    try {
      page = await api.dbBrowse(sessionId, tab.database, tab.schema ?? "", tab.name ?? "", offset, PAGE);
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }
  async function loadDetail() {
    loading = true;
    err = null;
    try {
      detail = await api.dbDescribe(sessionId, tab.database, tab.schema ?? "", tab.name ?? "");
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    const v = viewName;
    if (v === "data" && !page) loadPage();
    if (v !== "data" && !detail) loadDetail();
  });

  function show(v: DbTableView) {
    tab.view = v;
    onChange();
  }
  function go(delta: number) {
    offset = Math.max(0, offset + delta);
    loadPage();
  }
  function refresh() {
    if (viewName === "data") loadPage();
    else loadDetail();
  }
</script>

<div class="ttab">
  <div class="bar">
    <div class="seg">
      <button class:on={viewName === "data"} onclick={() => show("data")}>Data</button>
      <button class:on={viewName === "structure"} onclick={() => show("structure")}>Structure</button>
      <button class:on={viewName === "ddl"} onclick={() => show("ddl")}>DDL</button>
    </div>
    {#if viewName === "data"}
      <button disabled={offset === 0 || loading} onclick={() => go(-PAGE)}>Prev</button>
      <span class="range">{page ? `${offset + 1} - ${offset + (page.rows ?? []).length}` : ""}</span>
      <button disabled={loading || !page || (page.rows ?? []).length < PAGE} onclick={() => go(PAGE)}>Next</button>
    {/if}
    <span class="spacer"></span>
    <button title="Open a query tab on this table" onclick={() => onQuery(`SELECT *\nFROM ${qname}\nLIMIT 100;`)}><IconSql size={13} /> Query</button>
    <button title="Reload" onclick={refresh}><IconRefresh size={13} /></button>
  </div>
  <div class="body">
    {#if err}
      <pre class="err">{err}</pre>
    {:else if viewName === "data"}
      {#if page}
        <div class="gridbox"><DbResultGrid result={page} exportName={`${tab.name}-${offset + 1}`} /></div>
      {:else}
        <p class="muted">Loading...</p>
      {/if}
    {:else if !detail}
      <p class="muted">Loading...</p>
    {:else if viewName === "structure"}
      <div class="scroll">
        <table>
          <thead><tr><th>Column</th><th>Type</th><th>Null</th><th>Default</th><th>Key</th><th>Extra</th><th>Comment</th></tr></thead>
          <tbody>
            {#each detail.columns ?? [] as c (c.name)}
              <tr>
                <td class="name">{c.name}</td><td class="mono">{c.type}</td><td>{c.nullable ? "yes" : "no"}</td>
                <td class="mono">{c.default ?? ""}</td><td>{c.key ?? ""}</td><td>{c.extra ?? ""}</td><td>{c.comment ?? ""}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if (detail.indexes ?? []).length}
          <h4>Indexes</h4>
          <table>
            <thead><tr><th>Name</th><th>Columns</th><th>Kind</th></tr></thead>
            <tbody>
              {#each detail.indexes ?? [] as ix (ix.name)}
                <tr><td class="name">{ix.name}</td><td class="mono">{ix.columns.join(", ")}</td><td>{ix.primary ? "primary" : ix.unique ? "unique" : ""}</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}
        {#if (detail.foreign_keys ?? []).length}
          <h4>Foreign keys</h4>
          <table>
            <tbody>
              {#each detail.foreign_keys ?? [] as fk (fk.name)}
                <tr><td class="name">{fk.name}</td><td class="mono">{fk.def}</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    {:else}
      <div class="scroll">
        <div class="ddl-head">
          {#if detail.ddl_note}<span class="muted small">{detail.ddl_note}</span>{/if}
          <span class="spacer"></span>
          <button title="Copy the DDL" onclick={() => api.clipboardSetText(detail?.ddl ?? "").catch(() => {})}><IconCopy size={13} /> Copy</button>
        </div>
        <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
        <pre class="ddl" tabindex="0" onkeydown={selectAllWithin}>{detail.ddl}</pre>
      </div>
    {/if}
  </div>
</div>

<style>
  .ttab { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 0.35rem; padding: 0.3rem 0.5rem; border-bottom: 1px solid var(--surface0); }
  .bar button, .ddl-head button {
    display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.78rem; padding: 0.2rem 0.55rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; cursor: pointer;
  }
  .bar button:disabled { opacity: 0.45; cursor: default; }
  .seg { display: inline-flex; }
  .seg button { border-radius: 0; }
  .seg button:first-child { border-radius: 4px 0 0 4px; }
  .seg button:last-child { border-radius: 0 4px 4px 0; }
  .seg button.on { background: var(--surface2); border-color: var(--blue); }
  .range { font-size: 0.75rem; color: var(--subtext0); min-width: 6em; text-align: center; }
  .spacer { flex: 1 1 auto; }
  .body { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
  .gridbox { flex: 1 1 auto; min-height: 0; }
  .scroll { overflow: auto; padding: 0.5rem 0.75rem; }
  table { border-collapse: collapse; font-size: 0.78rem; margin-bottom: 0.8rem; }
  th, td { text-align: left; padding: 0.2rem 0.6rem 0.2rem 0; border-bottom: 1px solid var(--surface0); vertical-align: top; }
  th { color: var(--subtext0); font-weight: 600; }
  td.name { font-weight: 600; }
  .mono, pre { font-family: var(--mono-font, ui-monospace, monospace); }
  h4 { margin: 0.4rem 0 0.3rem; font-size: 0.8rem; color: var(--subtext0); }
  .ddl-head { display: flex; align-items: center; gap: 0.4rem; margin-bottom: 0.3rem; }
  pre.ddl { margin: 0; font-size: 0.78rem; white-space: pre-wrap; color: var(--text); user-select: text; }
  pre.ddl:focus { outline: none; }
  pre.err { margin: 0.5rem 0.75rem; color: var(--red); white-space: pre-wrap; font-size: 0.78rem; }
  .muted { margin: 0.6rem 0.75rem; font-size: 0.8rem; color: var(--subtext0); }
  .muted.small { margin: 0; font-size: 0.72rem; }
</style>
