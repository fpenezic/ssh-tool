<script lang="ts">
  // A table (or view) tab: its rows a page at a time in key order, its
  // structure, and its DDL. Read-only by construction - changes go through
  // a query tab ("Query" opens one on this table; edited cells and "Copy as
  // SQL" become a script there to read and run). A foreign key cell links
  // to the row it references: the tab walks there (the referenced table,
  // filtered to that key) and Back returns, a breadcrumb showing the path;
  // "Referenced by" walks the other way. Filter and sort run on the server.

  import { onDestroy, untrack } from "svelte";
  import { api, type DbCond, type DbForeignKey, type DbHandleInfo, type DbOrder, type DbResult, type DbTableDetail } from "./api";
  import DbFilterBar from "./DbFilterBar.svelte";
  import DbRefPanel from "./DbRefPanel.svelte";
  import { deleteSql, insertSql, isBinaryType, primaryKey, serverFilled, updateSql, type SqlEngine } from "./dbSql";
  import { toast } from "./toast.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { clickOutside } from "./clickOutside";
  import { dbTableEdits, type DbTab, type DbTableView, type DbTrailStep } from "./dbState.svelte";
  import DbResultGrid from "./DbResultGrid.svelte";
  import { errMsg } from "./connectErrors";
  import { IconRefresh, IconSql, IconCopy, IconArrowLeft, IconSearch } from "./iconMap";
  import { selectAllWithin } from "./selectWithin";

  interface Props {
    sessionId: string;
    tab: DbTab;
    info: DbHandleInfo;
    onChange: () => void;
    // database: where the query runs (a followed key can cross to another
    // database on MySQL).
    onQuery: (sql: string, database?: string) => void;
  }
  let { sessionId, tab, info, onChange, onQuery }: Props = $props();

  const PAGE = 200;
  const viewName = $derived<DbTableView>(tab.view ?? "data");

  // Where the tab stands: the table it was opened on, or a row reached
  // through foreign keys from it (the last of trail). The trail is part
  // of the saved tab, so a reload or a detached window keeps the path;
  // each step keeps the page the level before it was on, for Back.
  interface Target { database: string; schema: string; name: string; where: DbCond[] }
  // The pane remounts this per tab ({#key}), so the saved trail is read once.
  let trail = $state<DbTrailStep[]>(untrack(() => tab.trail ?? []));
  const cur = $derived<Target>(
    trail.length ? trail[trail.length - 1] : { database: tab.database, schema: tab.schema ?? "", name: tab.name ?? "", where: [] },
  );
  function saveTrail() {
    tab.trail = trail.length ? $state.snapshot(trail) : undefined;
    onChange();
  }

  // The Data view's own filter and server sort, for the table shown (a
  // move to another one starts without them).
  let filter = $state<DbCond[]>([]);
  let filterOpen = $state(false);
  let sort = $state<{ column: string; desc: boolean } | null>(null);
  // The row picked in the grid (for Referenced by and Copy as SQL) and the
  // edited cells, keyed "row:col" by the page's row index.
  let picked = $state<(string | null)[] | null>(null);
  let edits = $state(new Map<string, string | null>());
  // The pane asks before a tab switch or close drops them.
  onDestroy(() => delete dbTableEdits[sessionId + "\0" + tab.key]);
  $effect(() => {
    const k = sessionId + "\0" + tab.key;
    if (edits.size) dbTableEdits[k] = edits.size;
    else delete dbTableEdits[k];
  });
  // A cell whose column is in several foreign keys: which one to follow.
  let choose = $state<{ row: (string | null)[]; keys: DbForeignKey[] } | null>(null);
  let sqlMenu = $state(false);

  let offset = $state(0);
  let page = $state<DbResult | null>(null);
  let detail = $state<DbTableDetail | null>(null);
  let err = $state<string | null>(null);
  let loading = $state(false);

  const qname = $derived(
    info.engine === "mysql" || info.engine === "clickhouse"
      ? "`" + cur.name.replace(/`/g, "``") + "`"
      : info.engine === "mssql"
      ? `[${(cur.schema || "dbo").replace(/]/g, "]]")}].[${cur.name.replace(/]/g, "]]")}]`
      : (cur.schema && cur.schema !== "public" ? `"${cur.schema.replace(/"/g, '""')}".` : "") + `"${cur.name.replace(/"/g, '""')}"`,
  );

  // The query a "Query" click starts with, in the engine's language.
  function queryText(): string {
    const name = cur.name;
    if (info.engine === "opensearch") return `GET /${name}/_search\n{\n  "query": { "match_all": {} },\n  "size": 100\n}`;
    if (info.engine === "redis") {
      const glob = name.endsWith(":*") ? name.slice(0, -1).replace(/[*?[\]\\]/g, "\\$&") + "*" : "*";
      return `SCAN 0 MATCH "${glob.replace(/["\\]/g, "\\$&")}" COUNT 1000`;
    }
    if (info.engine === "oracle" || info.engine === "db2") return `SELECT *\nFROM ${qname}\nFETCH FIRST 100 ROWS ONLY;`;
    if (info.engine === "mssql") return `SELECT TOP 100 *\nFROM ${qname};`;
    return `SELECT *\nFROM ${qname}\nLIMIT 100;`;
  }

  // Each read numbered: a sort or filter rereads the same table, so only
  // the newest answer may land.
  let readNo = 0;
  async function loadPage() {
    loading = true;
    err = null;
    const t = cur;
    const no = ++readNo;
    try {
      const order: DbOrder[] = sort ? [{ column: sort.column, desc: sort.desc }] : [];
      const p = await api.dbBrowse(sessionId, t.database, t.schema, t.name, [...t.where, ...filter], order, offset, PAGE);
      if (t !== cur || no !== readNo) return; // moved on, or a newer read is out
      page = p;
    } catch (e) {
      if (t === cur && no === readNo) err = errMsg(e);
    } finally {
      if (t === cur && no === readNo) loading = false;
    }
    // The links in the grid come from the table's foreign keys.
    if (!detail && hasFks) void loadDetail(true);
  }
  async function loadDetail(quiet = false) {
    if (!quiet) {
      loading = true;
      err = null;
    }
    const t = cur;
    try {
      const d = await api.dbDescribe(sessionId, t.database, t.schema, t.name);
      if (t === cur) detail = d;
    } catch (e) {
      if (!quiet) err = errMsg(e);
    } finally {
      if (!quiet) loading = false;
    }
  }
  // Engines with foreign keys; the others have nothing to follow.
  const hasFks = $derived(["postgres", "mysql", "oracle", "mssql", "db2"].includes(info.engine));
  $effect(() => {
    const v = viewName;
    if (v === "data" && !page) loadPage();
    if (v !== "data" && !detail) loadDetail();
  });

  // Each grid column that is part of a foreign key, with its keys (a
  // column can be in several: the link then asks which).
  const fkOf = $derived.by(() => {
    const m = new Map<number, DbForeignKey[]>();
    const cols = page?.columns ?? [];
    for (const fk of detail?.foreign_keys ?? []) {
      if (!fk.ref_table || !fk.columns?.length || fk.columns.length !== fk.ref_columns?.length) continue;
      for (const c of fk.columns) {
        const i = cols.findIndex((x) => x.name === c);
        if (i >= 0) m.set(i, [...(m.get(i) ?? []), fk]);
      }
    }
    return m;
  });
  // A key a row can follow: every member has a value (with a NULL member
  // it references nothing, MATCH SIMPLE).
  function usable(fk: DbForeignKey, row: (string | null)[]): boolean {
    const cols = page?.columns ?? [];
    return fk.columns!.every((c) => {
      const i = cols.findIndex((x) => x.name === c);
      return i >= 0 && row[i] !== null;
    });
  }
  function canLink(row: (string | null)[], ci: number): boolean {
    return (fkOf.get(ci) ?? []).some((fk) => usable(fk, row));
  }

  function linkTitle(ci: number): string {
    const ks = fkOf.get(ci) ?? [];
    if (ks.length > 1) return `Open a row this references (${ks.length} keys: ${ks.map((k) => k.ref_table).join(", ")})`;
    return ks[0] ? `Open the ${ks[0].ref_table} row this references (${ks[0].name})` : "";
  }
  function follow(row: (string | null)[], ci: number) {
    const ks = (fkOf.get(ci) ?? []).filter((fk) => usable(fk, row));
    if (ks.length > 1) {
      choose = { row, keys: ks };
      return;
    }
    if (ks[0]) followKey(row, ks[0]);
  }
  function followKey(row: (string | null)[], fk: DbForeignKey) {
    choose = null;
    if (!fk.columns || !fk.ref_columns || !fk.ref_table) return;
    const cols = page?.columns ?? [];
    const where: DbCond[] = [];
    for (let k = 0; k < fk.columns.length; k++) {
      const i = cols.findIndex((x) => x.name === fk.columns![k]);
      if (i < 0 || row[i] === null) return;
      where.push({ column: fk.ref_columns[k], value: row[i], hex: isBinaryType(info.engine, cols[i].type) || undefined });
    }
    walk({ database: fk.ref_database || cur.database, schema: fk.ref_schema ?? "", name: fk.ref_table, where });
  }
  // Moves to another table's rows (a followed key either way).
  // Unsent cell edits are lost by a move: ask first.
  async function dropEdits(): Promise<boolean> {
    if (!edits.size) return true;
    return showConfirm({
      title: "Unsent changes",
      message: `${edits.size} edited cell${edits.size === 1 ? "" : "s"} of ${cur.name} ${edits.size === 1 ? "is" : "are"} not in a script yet. Drop ${edits.size === 1 ? "it" : "them"}?`,
      okLabel: "Drop",
      danger: true,
    });
  }
  async function walk(t: { database: string; schema: string; name: string; where: DbCond[] }) {
    if (!(await dropEdits())) return;
    trail = [...trail, { ...t, offset }];
    saveTrail();
    moveTo(0);
  }
  async function back(to = trail.length - 1) {
    if (to < 0 || to >= trail.length) return;
    if (!(await dropEdits())) return;
    const back = trail[to].offset;
    trail = trail.slice(0, to);
    saveTrail();
    moveTo(back);
  }
  // A new target: the view's effect loads it once page / detail clear.
  function moveTo(at: number) {
    offset = at;
    page = null;
    detail = null;
    err = null;
    filter = [];
    sort = null;
    picked = null;
    choose = null;
    edits = new Map();
  }
  // The same table, read again from the first page (a new filter or sort).
  function reread() {
    offset = 0;
    picked = null;
    edits = new Map();
    loadPage();
  }
  function condText(where: DbCond[]): string {
    return where
      .map((c) => `${c.column} ${c.value === null ? (c.op === "<>" ? "IS NOT NULL" : "IS NULL") : `${c.op || "="} ${c.value}`}`)
      .join(", ");
  }

  // SQL text for rows: the engines with a SQL grammar here.
  const sqlEngine = $derived<SqlEngine | null>(hasFks ? (info.engine as SqlEngine) : null);
  const sqlTable = $derived(sqlEngine ? { engine: sqlEngine, database: cur.database, schema: cur.schema, name: cur.name } : null);
  const pk = $derived(primaryKey(detail?.columns));
  // Identity and generated columns: the server fills them.
  const filled = $derived(new Set((detail?.columns ?? []).filter((c) => serverFilled(c.extra)).map((c) => c.name)));
  // A value cut at the cell limit would go into the SQL cut, and without
  // the table's description the key is unknown (the WHERE would name every
  // column): refuse both.
  function sqlBlocked(needKey: boolean): boolean {
    if (needKey && !detail) {
      toast.err("The table's structure is not read yet (its primary key is needed): try again in a moment, or Reload");
      if (hasFks) void loadDetail(true);
      return true;
    }
    if (page && page.cut_cells > 0) {
      toast.err(`${page.cut_cells} value${page.cut_cells === 1 ? " is" : "s are"} cut on this page (too long to show whole): no SQL from it`);
      return true;
    }
    return false;
  }
  function copySql(kind: "insert" | "delete", all: boolean) {
    if (!sqlTable || !page || sqlBlocked(true)) return;
    const rows = all ? page.rows ?? [] : picked ? [picked] : [];
    if (!rows.length) {
      toast.info("Pick a row first");
      return;
    }
    const cols = page.columns ?? [];
    let text: string;
    try {
      text = kind === "insert" ? insertSql(sqlTable, cols, rows, filled) : deleteSql(sqlTable, cols, rows, pk);
    } catch (e) {
      toast.err(errMsg(e));
      return;
    }
    api.clipboardSetText(text).then(
      () => toast.ok(`${kind.toUpperCase()} for ${rows.length} row${rows.length === 1 ? "" : "s"} copied`),
      (e) => toast.err(`Copy failed: ${errMsg(e)}`),
    );
  }
  function onEdit(row: number, ci: number, value: string | null) {
    const k = `${row}:${ci}`;
    const next = new Map(edits);
    if (value === (page?.rows ?? [])[row]?.[ci]) next.delete(k);
    else next.set(k, value);
    edits = next;
  }
  // The edits as an UPDATE script in a query tab, to read and run there.
  function editScript() {
    if (!sqlTable || !page || !edits.size || sqlBlocked(true)) return;
    const byRow = new Map<number, Map<number, string | null>>();
    for (const [k, v] of edits) {
      const [r, c] = k.split(":").map(Number);
      byRow.set(r, (byRow.get(r) ?? new Map()).set(c, v));
    }
    const rows = page.rows ?? [];
    const changes = [...byRow.entries()].map(([r, set]) => ({ row: rows[r], set }));
    let text: string;
    try {
      text = updateSql(sqlTable, page.columns ?? [], changes, pk, filled);
    } catch (e) {
      toast.err(errMsg(e));
      return;
    }
    onQuery(text + "\n", cur.database);
  }

  function show(v: DbTableView) {
    tab.view = v;
    onChange();
  }
  async function go(delta: number) {
    if (!(await dropEdits())) return;
    offset = Math.max(0, offset + delta);
    edits = new Map();
    picked = null;
    loadPage();
  }
  async function refresh() {
    if (viewName === "data") {
      if (!(await dropEdits())) return;
      edits = new Map();
      picked = null;
      loadPage();
    } else loadDetail();
  }
</script>

<div class="ttab">
  {#if trail.length}
    <div class="trail">
      <button title="Back to where this row was reached from" onclick={() => back()}><IconArrowLeft size={13} /> Back</button>
      <button class="crumb" onclick={() => back(0)}>{tab.name}</button>
      {#each trail as t, i (i)}
        <span class="sep">/</span>
        {#if i < trail.length - 1}
          <button class="crumb" onclick={() => back(i + 1)}>{t.name} <span class="cond">({condText(t.where)})</span></button>
        {:else}
          <span class="here">{t.name} <span class="cond">({condText(t.where)})</span></span>
        {/if}
      {/each}
    </div>
  {/if}
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
      {#if sqlEngine}
        <button class:on={filterOpen || filter.length > 0} title="Filter the table on the server" onclick={() => (filterOpen = !filterOpen)}>
          <IconSearch size={13} /> Filter{filter.length ? ` (${filter.length})` : ""}
        </button>
        <span class="menu-wrap" use:clickOutside={{ onOutside: () => (sqlMenu = false), enabled: sqlMenu }}>
          <button title="The picked row (or the page) as SQL, copied to the clipboard" onclick={() => (sqlMenu = !sqlMenu)}><IconCopy size={13} /> Copy as SQL</button>
          {#if sqlMenu}
            <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
            <div class="menu" onclick={() => (sqlMenu = false)}>
              <button disabled={!picked} onclick={() => copySql("insert", false)}>INSERT - picked row</button>
              <button onclick={() => copySql("insert", true)}>INSERT - this page</button>
              <button disabled={!picked} onclick={() => copySql("delete", false)}>DELETE - picked row</button>
              <button onclick={() => copySql("delete", true)}>DELETE - this page</button>
            </div>
          {/if}
        </span>
        {#if edits.size}
          <span class="edits">{edits.size} edited</span>
          <button class="primary" title="Open the changes as an UPDATE script in a query tab, to read and run there" onclick={editScript}>Script</button>
          <button title="Throw the edits away" onclick={() => (edits = new Map())}>Discard</button>
        {/if}
      {/if}
    {/if}
    <span class="spacer"></span>
    <button title="Open a query tab on this table" onclick={() => onQuery(queryText(), cur.database)}><IconSql size={13} /> Query</button>
    <button title="Reload" onclick={refresh}><IconRefresh size={13} /></button>
  </div>
  {#if viewName === "data" && filterOpen && page && sqlEngine}
    <DbFilterBar engine={info.engine} columns={page.columns ?? []} applied={filter} onApply={async (c) => { if (!(await dropEdits())) return; filter = c; reread(); }} onClose={() => (filterOpen = false)} />
  {/if}
  {#if choose}
    <div class="choose">
      <span>Follow which key?</span>
      {#each choose.keys as k (k.name)}
        <button onclick={() => choose && followKey(choose.row, k)}>{k.ref_table} <span class="cond">({(k.columns ?? []).join(", ")})</span></button>
      {/each}
      <button onclick={() => (choose = null)}>Cancel</button>
    </div>
  {/if}
  <div class="body">
    {#if err}
      <pre class="err">{err}</pre>
    {:else if viewName === "data"}
      {#if page}
        <div class="gridbox">
          <DbResultGrid
            result={page}
            exportName={`${cur.name}-${offset + 1}`}
            paged
            {canLink}
            {linkTitle}
            onLink={follow}
            serverSort={sqlEngine ? sort : null}
            onServerSort={sqlEngine ? async (v) => { if (!(await dropEdits())) return; sort = v; reread(); } : undefined}
            onSelect={(r) => (picked = r)}
            editable={!!sqlEngine && !info.read_only}
            {edits}
            {onEdit}
          />
        </div>
        {#if (cur.where.length || filter.length) && (page.rows ?? []).length === 0}
          <p class="muted">No {cur.name} row matches {condText([...cur.where, ...filter])}.</p>
        {/if}
        {#if hasFks}
          <DbRefPanel {sessionId} engine={info.engine} database={cur.database} schema={cur.schema} name={cur.name} columns={page.columns ?? []} row={picked} onOpen={walk} />
        {/if}
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
  .bar button.on { border-color: var(--blue); }
  .bar button.primary { border-color: var(--blue); }
  .edits { font-size: 0.74rem; color: var(--yellow); }
  .menu-wrap { position: relative; }
  .menu {
    position: absolute; top: calc(100% + 2px); left: 0; z-index: 20; display: flex; flex-direction: column; min-width: 12rem;
    background: var(--mantle); border: 1px solid var(--surface1); border-radius: 4px; box-shadow: 0 4px 12px rgba(0,0,0,0.35); padding: 0.2rem;
  }
  .menu button { justify-content: flex-start; background: none; border-color: transparent; }
  .menu button:hover:not(:disabled) { background: var(--surface0); }
  .choose { display: flex; align-items: center; gap: 0.35rem; padding: 0.3rem 0.5rem; border-bottom: 1px solid var(--surface0); font-size: 0.76rem; }
  .choose button {
    display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.76rem; padding: 0.15rem 0.5rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; cursor: pointer;
  }
  .trail { display: flex; align-items: center; gap: 0.3rem; padding: 0.25rem 0.5rem; border-bottom: 1px solid var(--surface0); font-size: 0.76rem; overflow-x: auto; white-space: nowrap; }
  .trail button {
    display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.76rem; padding: 0.15rem 0.45rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; cursor: pointer;
  }
  .trail button.crumb { background: none; border-color: transparent; color: var(--blue); }
  .trail button.crumb:hover { border-color: var(--surface1); }
  .trail .sep { color: var(--overlay0); }
  .trail .here { font-weight: 600; }
  .trail .cond { font-weight: 400; color: var(--subtext0); font-family: var(--mono-font, ui-monospace, monospace); }
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
