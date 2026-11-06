<script lang="ts">
  // A Database tab. sessionId is the backend handle (app_db.go); it outlives
  // this component on purpose - a split, a move, a detach or a reload
  // remounts the pane, and the tabs inside (dbState) and each query tab's
  // server session must be found where they were. The handle closes when
  // the tab's session is ended (endSession).
  //
  // Left: databases, and the tables / views of the ones opened (grouped by
  // schema on Postgres). Right: query and table tabs.

  import { onDestroy, onMount, untrack } from "svelte";
  import { EventsOn } from "./wailsRuntime";
  import { api, type DbDatabase, type DbHandleInfo, type DbObject, type DbServerInfo } from "./api";
  import { dbTableEdits, dbTabs, nextQueryKey, tableKey, type DbTab } from "./dbState.svelte";
  import DbQueryTab from "./DbQueryTab.svelte";
  import DbTableTab from "./DbTableTab.svelte";
  import DbSessionsTab from "./DbSessionsTab.svelte";
  import DbErTab from "./DbErTab.svelte";
  import { sessions } from "./stores.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { formatBytes } from "./kafkaFormat";
  import { IconActivity, IconRefresh, IconPlus, IconDb, IconX, IconTable, IconView, IconSql, IconChevronRight, IconChevronDown, IconGraph, IconCopy, IconDownload, IconFile, IconHash } from "./iconMap";
  import { contextMenu, type MenuItem } from "./contextMenu.svelte.ts";
  import { countText as countSql, ident, selectText, tableName, type SqlEngine } from "./dbSql";
  import { readAll, rowsCsv, rowsJson } from "./dbExport";
  import { toast } from "./toast.svelte.ts";
  import type { DbTableView } from "./dbState.svelte";

  interface Props {
    sessionId: string;
  }
  let { sessionId }: Props = $props();

  let info = $state<DbHandleInfo | null>(null);
  $effect(() => {
    if (!info) info = dbTabs.get(sessionId) ?? null;
  });
  let server = $state<DbServerInfo | null>(null);
  let databases = $state<DbDatabase[] | null>(null);
  let loadErr = $state<string | null>(null);
  let loading = $state(false);
  let showSystem = $state(false);
  let filter = $state("");
  // database -> its objects (loaded on expand), and which are expanded.
  let objects = $state<Record<string, DbObject[] | "loading" | { error: string }>>({});
  let expanded = $state<Record<string, boolean>>({});
  let treeW = $state(260);

  // The tab list: this window's copy, or the backend's for a window that
  // did not open the tab (detach, redock, reload), or a fresh query tab.
  $effect(() => {
    const sid = sessionId;
    untrack(async () => {
      if (dbTabs.tabs[sid] !== undefined) return;
      let json = "";
      try {
        json = await api.dbGetTabState(sid);
      } catch {
        /* closed meanwhile */
      }
      if (dbTabs.tabs[sid] === undefined) dbTabs.adopt(sid, json, dbTabs.get(sid)?.database ?? "");
    });
  });
  const saved = $derived(dbTabs.tabs[sessionId]);
  const tabs = $derived(saved?.tabs ?? []);
  const active = $derived(tabs.find((t) => t.key === saved?.active) ?? tabs[0]);

  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  function changed() {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => dbTabs.save(sessionId), 400);
  }
  onDestroy(() => {
    if (saveTimer) {
      clearTimeout(saveTimer);
      dbTabs.save(sessionId);
    }
  });

  // Leaving a table tab with unsent cell edits drops them: ask first.
  async function leaveEdits(key: string | undefined): Promise<boolean> {
    const n = key ? dbTableEdits[sessionId + "\0" + key] ?? 0 : 0;
    if (!n) return true;
    const ok = await showConfirm({
      title: "Unsent changes",
      message: `${n} edited cell${n === 1 ? "" : "s"} in this table tab ${n === 1 ? "is" : "are"} not in a script yet. Drop ${n === 1 ? "it" : "them"}?`,
      okLabel: "Drop",
      danger: true,
    });
    if (ok && key) delete dbTableEdits[sessionId + "\0" + key];
    return ok;
  }
  async function show(key: string) {
    if (!saved) return;
    if (key !== saved.active && !(await leaveEdits(saved.active))) return;
    saved.active = key;
    changed();
  }
  function addTab(t: Omit<DbTab, "key">, key?: string): string {
    if (!saved) return "";
    const k = key ?? nextQueryKey(saved.tabs);
    if (!saved.tabs.some((x) => x.key === k)) saved.tabs.push({ ...t, key: k });
    show(k);
    return k;
  }
  function newQuery(sql = "", database?: string) {
    if (!saved) return;
    const k = nextQueryKey(saved.tabs);
    addTab({ kind: "query", title: `Query ${k.slice(1)}`, database: database ?? active?.database ?? info?.database ?? "", sql }, k);
  }
  // The server's sessions, one tab per database tab.
  function openSessions() {
    addTab({ kind: "sessions", title: "Sessions", database: active?.database ?? info?.database ?? "" }, "sessions");
  }
  // A schema's ER diagram, one tab per schema. Engines whose schema is
  // the database (MySQL, Oracle, Db2) open it from the database row.
  function openEr(db: string, schema: string) {
    addTab({ kind: "er", title: `ER ${schema || db}`, database: db, schema }, "er:" + JSON.stringify([db, schema]));
  }
  const hasEr = $derived(!!info && ["postgres", "mysql", "oracle", "mssql", "db2"].includes(info.engine));
  const erOnDb = $derived(!!info && ["mysql", "oracle", "db2"].includes(info.engine));
  const hasSessions = $derived(!!info && ["postgres", "mysql", "oracle", "mssql", "db2"].includes(info.engine));
  function openTable(db: string, o: DbObject, view?: DbTableView) {
    const k = addTab(
      { kind: "table", title: o.name, database: db, schema: o.schema, name: o.name, view: view ?? "data" },
      tableKey(db, o.schema, o.name),
    );
    // An open tab keeps its view unless one is asked for (the tree's menu).
    const t = view && saved?.tabs.find((x) => x.key === k);
    if (t && t.view !== view) {
      t.view = view;
      changed();
    }
  }

  // Right-click in the tree. SQL engines get their own quoting; ClickHouse
  // quotes like MySQL; OpenSearch and Redis names go as they are.
  const SQL_ENGINES = ["postgres", "mysql", "oracle", "mssql", "db2"];
  const sqlEngine = $derived(info && SQL_ENGINES.includes(info.engine) ? (info.engine as SqlEngine) : null);
  // A whole table reads page by page through Browse: the engines that page
  // by offset without a ceiling (OpenSearch stops at 10000, Redis at its
  // key sample).
  const canExport = $derived(!!sqlEngine || info?.engine === "clickhouse");
  function fullName(db: string, schema: string, name: string): string {
    if (sqlEngine) return tableName({ engine: sqlEngine, database: db, schema, name });
    if (info?.engine === "clickhouse") return `\`${db.replace(/`/g, "``")}\`.\`${name.replace(/`/g, "``")}\``;
    return name;
  }
  function quoteName(n: string): string {
    if (sqlEngine) return ident(sqlEngine, n);
    if (info?.engine === "clickhouse") return "`" + n.replace(/`/g, "``") + "`";
    return n;
  }
  function copy(text: string, what: string) {
    api.clipboardSetText(text).then(
      () => toast.ok(`${what} copied`),
      (e) => toast.err(`Copy failed: ${errMsg(e)}`),
    );
  }
  async function copyDdl(db: string, o: DbObject) {
    try {
      const d = await api.dbDescribe(sessionId, db, o.schema, o.name);
      if (!d.ddl) {
        toast.info(`No DDL for ${o.name}${d.ddl_note ? `: ${d.ddl_note}` : ""}`);
        return;
      }
      copy(d.ddl, `DDL of ${o.name}`);
    } catch (e) {
      toast.err(`DDL of ${o.name}: ${errMsg(e)}`);
    }
  }
  // The exact count, run on a session of its own and closed after; on
  // OpenSearch the _count request opens in a query tab instead.
  async function countRows(db: string, o: DbObject) {
    const sql = info ? countSql(info.engine, o.schema, o.name) : null;
    if (!sql) return;
    if (info?.engine === "opensearch") {
      newQuery(sql, db);
      return;
    }
    const key = `count:${Date.now()}`;
    try {
      const r = await api.dbRun(sessionId, key, db, sql, 1);
      const res = r.results?.[0];
      if (res?.error) throw new Error(res.error);
      const n = res?.rows?.[0]?.[0];
      toast.ok(`${o.name}: ${n === undefined || n === null ? "?" : Number(n).toLocaleString()} rows`, 6000);
    } catch (e) {
      toast.err(`Count ${o.name}: ${errMsg(e)}`);
    } finally {
      api.dbCloseTab(sessionId, key).catch(() => {});
    }
  }

  // A whole table to a file: read a page at a time (shown below the tree,
  // with Stop), then saved where asked.
  const EXPORT_PAGE = 10000;
  let exporting = $state<{ name: string; rows: number; stop: boolean } | null>(null);
  async function exportTable(db: string, o: DbObject, kind: "csv" | "json") {
    if (exporting) {
      toast.info(`Exporting ${exporting.name} already: wait for it or stop it`);
      return;
    }
    exporting = { name: o.name, rows: 0, stop: false };
    try {
      const all = await readAll(
        (offset, limit) => api.dbBrowse(sessionId, db, o.schema, o.name, [], [], offset, limit),
        EXPORT_PAGE,
        () => !!exporting?.stop,
        (n) => {
          if (exporting) exporting.rows = n;
        },
      );
      if (!all) {
        toast.info(`Export of ${o.name} stopped`);
        return;
      }
      const text = kind === "csv" ? rowsCsv(all.columns, all.rows) : rowsJson(all.columns, all.rows);
      const path = await api.saveTextFile(`${o.name}.${kind}`, text);
      if (path) {
        const n = all.rows.length;
        const msg = `Exported ${n.toLocaleString()} row${n === 1 ? "" : "s"} of ${o.name} to ${path}`;
        if (all.cut) toast.err(`${msg} - ${all.cut} value${all.cut === 1 ? " was" : "s were"} too long and cut (256 KiB per value)`, 10000);
        else toast.ok(msg, 5000);
      }
    } catch (e) {
      toast.err(`Export ${o.name}: ${errMsg(e)}`);
    } finally {
      exporting = null;
    }
  }

  function objMenu(e: MouseEvent, db: string, o: DbObject) {
    e.preventDefault();
    const items: MenuItem[] = [
      { label: "Open data", iconComponent: IconTable, onSelect: () => openTable(db, o, "data") },
      { label: "Structure", iconComponent: IconView, onSelect: () => openTable(db, o, "structure") },
      { label: "DDL", iconComponent: IconFile, onSelect: () => openTable(db, o, "ddl") },
      { label: "New query", iconComponent: IconSql, sep: true, onSelect: () => info && newQuery(selectText(info.engine, o.schema, o.name), db) },
    ];
    if (info && countSql(info.engine, o.schema, o.name)) items.push({ label: "Count rows", iconComponent: IconHash, onSelect: () => void countRows(db, o) });
    items.push(
      { label: "Copy name", iconComponent: IconCopy, sep: true, onSelect: () => copy(fullName(db, o.schema, o.name), "Name") },
      { label: "Copy DDL", iconComponent: IconCopy, onSelect: () => void copyDdl(db, o) },
    );
    if (canExport) {
      items.push(
        { label: "Export to CSV...", iconComponent: IconDownload, sep: true, disabled: !!exporting, onSelect: () => void exportTable(db, o, "csv") },
        { label: "Export to JSON...", iconComponent: IconDownload, disabled: !!exporting, onSelect: () => void exportTable(db, o, "json") },
      );
    }
    if (hasEr) items.push({ label: "ER diagram", iconComponent: IconGraph, sep: true, onSelect: () => openEr(db, erOnDb ? "" : o.schema) });
    contextMenu.show(e, items);
  }
  function dbMenu(e: MouseEvent, db: string) {
    e.preventDefault();
    const items: MenuItem[] = [{ label: "New query", iconComponent: IconSql, onSelect: () => newQuery("", db) }];
    if (hasEr && erOnDb) items.push({ label: "ER diagram", iconComponent: IconGraph, onSelect: () => openEr(db, "") });
    items.push(
      { label: "Copy name", iconComponent: IconCopy, onSelect: () => copy(quoteName(db), "Name") },
      { label: "Refresh tables", iconComponent: IconRefresh, sep: true, onSelect: () => { expanded[db] = true; void loadObjects(db); } },
    );
    contextMenu.show(e, items);
  }
  function schemaMenu(e: MouseEvent, db: string, schema: string) {
    e.preventDefault();
    const items: MenuItem[] = [];
    if (hasEr && !erOnDb) items.push({ label: "ER diagram", iconComponent: IconGraph, onSelect: () => openEr(db, schema) });
    items.push({ label: "Copy name", iconComponent: IconCopy, onSelect: () => copy(quoteName(schema), "Name") });
    contextMenu.show(e, items);
  }
  async function closeTab(t: DbTab) {
    if (!saved) return;
    if (!(await leaveEdits(t.key))) return;
    if (t.kind === "query") {
      const inTx = await api.dbTabInTx(sessionId, t.key).catch(() => false);
      if (inTx) {
        const ok = await showConfirm({
          title: "Close with an open transaction?",
          message: `"${t.title}" has a transaction that was not committed. Closing the tab rolls it back.`,
          okLabel: "Roll back and close",
          danger: true,
        });
        if (!ok) return;
      }
      api.dbCloseTab(sessionId, t.key).catch(() => {});
      dbTabs.dropRun(sessionId, t.key);
    }
    const i = saved.tabs.findIndex((x) => x.key === t.key);
    saved.tabs = saved.tabs.filter((x) => x.key !== t.key);
    if (saved.active === t.key) saved.active = saved.tabs[Math.min(i, saved.tabs.length - 1)]?.key ?? "";
    changed();
  }

  // A tab requested from outside (dbTabs.pending).
  $effect(() => {
    const p = dbTabs.pending;
    if (!p || p.sessionId !== sessionId || !saved) return;
    untrack(() => {
      dbTabs.pending = null;
      if (p.tab.kind === "query") newQuery(p.tab.sql ?? "", p.tab.database);
      else addTab(p.tab);
    });
  });

  // The backend asks the server every 20 s (app_db_alive.go): the tab is
  // drawn connected only while it answers, and a tree that failed to load
  // loads again once it does.
  // The last check's answer while a load runs: a failure that arrives
  // then wins over the load's own success.
  let aliveDuringLoad: { ok: boolean; error?: string } | null = null;
  onMount(() => {
    dbTabs.watchAlive();
    return EventsOn<{ id: string; ok: boolean; error?: string }>("db_alive", (p) => {
      if (p.id !== sessionId) return;
      if (loading) aliveDuringLoad = p;
      if (!p.ok) {
        sessions.setStatus(sessionId, "error", `The server stopped answering: ${p.error ?? ""}`);
        return;
      }
      if (loadErr) void load();
      else if (!loading) {
        sessions.setStatus(sessionId, "connected");
        retryFailedObjects();
      }
    });
  });
  // Expanded databases whose tables failed to load (during an outage):
  // read again once the server answers.
  function retryFailedObjects() {
    for (const [db, v] of Object.entries(objects)) {
      if (expanded[db] && v && typeof v === "object" && !Array.isArray(v)) void loadObjects(db);
    }
  }

  async function load() {
    loading = true;
    loadErr = null;
    aliveDuringLoad = null;
    sessions.setStatus(sessionId, "connecting");
    try {
      const [st, srv, dbs] = await Promise.all([api.dbStatus(sessionId), api.dbInfo(sessionId), api.dbDatabases(sessionId)]);
      info = st;
      dbTabs.set(st);
      server = srv;
      databases = dbs ?? [];
      const late = aliveDuringLoad as { ok: boolean; error?: string } | null;
      const down = late && !late.ok ? late.error || "not answering" : st.down;
      if (down) sessions.setStatus(sessionId, "error", `The server stopped answering: ${down}`);
      else {
        sessions.setStatus(sessionId, "connected");
        // Expanded databases whose tables failed while the server was away.
        retryFailedObjects();
      }
      // The database the login landed on opens by itself.
      const cur = srv.database || st.database;
      if (cur && expanded[cur] === undefined) toggle(cur, true);
    } catch (e) {
      loadErr = errMsg(e);
      sessions.setStatus(sessionId, "error", loadErr);
      try {
        info = await api.dbStatus(sessionId);
      } catch {
        /* closed */
      }
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void sessionId;
    untrack(() => load());
  });

  async function loadObjects(db: string) {
    objects[db] = "loading";
    try {
      objects[db] = (await api.dbObjects(sessionId, db, showSystem)) ?? [];
    } catch (e) {
      objects[db] = { error: errMsg(e) };
    }
  }
  function toggle(db: string, open?: boolean) {
    const next = open ?? !expanded[db];
    expanded[db] = next;
    if (next && (objects[db] === undefined || typeof objects[db] === "object" && !Array.isArray(objects[db]))) loadObjects(db);
  }
  function refreshAll() {
    objects = {};
    load().then(() => {
      for (const [db, open] of Object.entries(expanded)) if (open) loadObjects(db);
    });
  }
  $effect(() => {
    void showSystem;
    untrack(() => {
      for (const [db, open] of Object.entries(expanded)) if (open && objects[db] !== undefined) loadObjects(db);
    });
  });

  const shownDbs = $derived((databases ?? []).filter((d) => showSystem || !d.system));
  function groups(list: DbObject[]): [string, DbObject[]][] {
    const f = filter.toLowerCase();
    const m = new Map<string, DbObject[]>();
    for (const o of list) {
      if (f && !o.name.toLowerCase().includes(f)) continue;
      const k = o.schema;
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(o);
    }
    return [...m.entries()];
  }
  // Completion: the tables of every database listed so far.
  const tableNames = $derived.by(() => {
    const out = new Set<string>();
    for (const v of Object.values(objects)) {
      if (!Array.isArray(v)) continue;
      for (const o of v) {
        out.add(o.name);
        if (o.schema) out.add(`${o.schema}.${o.name}`);
      }
    }
    return [...out];
  });
  const dbNames = $derived((databases ?? []).map((d) => d.name));
  // Where each listed table lives, for the query tab's column completion.
  const tableRefs = $derived.by(() => {
    const out: { database: string; schema: string; name: string }[] = [];
    for (const [db, v] of Object.entries(objects)) {
      if (!Array.isArray(v)) continue;
      for (const o of v) out.push({ database: db, schema: o.schema ?? "", name: o.name });
    }
    return out;
  });

  function countText(n: number): string {
    if (n < 0) return "";
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(n);
  }
  function startDrag(e: PointerEvent) {
    const x0 = e.clientX;
    const w0 = treeW;
    const move = (ev: PointerEvent) => (treeW = Math.max(160, Math.min(600, w0 + ev.clientX - x0)));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }
  const serverText = $derived.by(() => {
    if (!server) return "";
    // "PostgreSQL 17.6 (Debian ...) on x86_64..." / "8.4.6" -> the name and number.
    const v = server.version.split(/\s+on\s+|,|\s\(/)[0];
    return `${v} · ${server.user}`;
  });
</script>

<div class="db-pane">
  <aside class="tree" style:width="{treeW}px">
    <div class="tree-head">
      <IconDb size={14} />
      <span class="srv" title={server?.version ?? info?.addr ?? ""}>{serverText || info?.addr || "..."}</span>
      {#if hasSessions}<button title="The server's sessions: who runs what, and stop one" onclick={openSessions}><IconActivity size={13} /></button>{/if}
      <button title="Reload the databases and tables" onclick={refreshAll} disabled={loading}><IconRefresh size={13} /></button>
    </div>
    <div class="tree-tools">
      <input placeholder="Filter tables" bind:value={filter} spellcheck="false" />
      <label title="Show system databases and schemas"><input type="checkbox" bind:checked={showSystem} /> system</label>
    </div>
    <div class="tree-body">
      {#if loadErr}
        <pre class="err">{loadErr}</pre>
        <button class="retry" onclick={refreshAll}>Try again</button>
      {:else if !databases}
        <p class="muted">{info?.state === "connecting" ? "Connecting over SSH..." : "Loading..."}</p>
      {:else}
        {#each shownDbs as d (d.name)}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="db-row" class:sys={d.system} oncontextmenu={(e) => dbMenu(e, d.name)}>
            <button class="twisty" onclick={() => toggle(d.name)}>
              {#if expanded[d.name]}<IconChevronDown size={12} />{:else}<IconChevronRight size={12} />{/if}
              <span class="dname">{d.name}</span>
            </button>
            {#if d.size_bytes > 0}<span class="meta">{formatBytes(d.size_bytes)}</span>{/if}
            {#if hasEr && erOnDb}<button class="mini" title="ER diagram of {d.name}" onclick={() => openEr(d.name, "")}><IconGraph size={12} /></button>{/if}
            <button class="mini" title="New query tab on {d.name}" onclick={() => newQuery("", d.name)}><IconSql size={12} /></button>
          </div>
          {#if expanded[d.name]}
            {@const obj = objects[d.name]}
            {#if obj === "loading" || obj === undefined}
              <p class="muted ind">Loading...</p>
            {:else if !Array.isArray(obj)}
              <pre class="err ind">{obj.error}</pre>
            {:else if obj.length === 0}
              <p class="muted ind">No tables</p>
            {:else}
              {#each groups(obj) as [schema, list] (schema)}
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                {#if schema}<div class="schema ind" oncontextmenu={(e) => schemaMenu(e, d.name, schema)}><span class="sname">{schema}</span>{#if hasEr && !erOnDb}<button class="mini" title="ER diagram of {schema}" onclick={() => openEr(d.name, schema)}><IconGraph size={12} /></button>{/if}</div>{/if}
                {#each list as o (o.schema + "." + o.name)}
                  <button
                    class="obj"
                    class:ind2={!!schema}
                    class:sys={o.system}
                    class:on={active?.kind === "table" && active.key === tableKey(d.name, o.schema, o.name)}
                    title={o.comment || `${o.kind}${o.rows >= 0 ? ` · about ${o.rows} rows` : ""}${o.size_bytes >= 0 ? ` · ${formatBytes(o.size_bytes)}` : ""}`}
                    onclick={() => openTable(d.name, o)}
                    oncontextmenu={(e) => objMenu(e, d.name, o)}
                  >
                    {#if o.kind === "view" || o.kind === "matview"}<IconView size={12} />{:else}<IconTable size={12} />{/if}
                    <span class="oname">{o.name}</span>
                    <span class="meta">{countText(o.rows)}</span>
                  </button>
                {/each}
              {/each}
            {/if}
          {/if}
        {/each}
      {/if}
    </div>
    {#if exporting}
      <div class="export-bar" title="The rows are read a page at a time, then saved where you choose">
        <span class="ex-text">Exporting {exporting.name}: {exporting.rows.toLocaleString()} rows</span>
        <button onclick={() => exporting && (exporting.stop = true)}>Stop</button>
      </div>
    {/if}
  </aside>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="vdrag" onpointerdown={startDrag}></div>
  <section class="main">
    <div class="tabs">
      {#each tabs as t (t.key)}
        <div class="tab" class:on={active?.key === t.key} title={t.kind === "table" ? `${t.database}${t.schema ? "." + t.schema : ""}.${t.name}` : t.database}>
          <button class="tab-btn" onclick={() => show(t.key)} onauxclick={(e) => { if (e.button === 1) closeTab(t); }}>
            {#if t.kind === "query"}<IconSql size={12} />{:else if t.kind === "sessions"}<IconActivity size={12} />{:else if t.kind === "er"}<IconGraph size={12} />{:else}<IconTable size={12} />{/if}
            {t.title}
            {#if t.kind === "query" && dbTabs.runs[sessionId + "\0" + t.key]?.running}<span class="dot" title="Running"></span>{/if}
            {#if t.kind === "query" && dbTabs.runs[sessionId + "\0" + t.key]?.inTx}<span class="dot tx" title="Open transaction"></span>{/if}
          </button>
          <button class="tab-x" title="Close" onclick={() => closeTab(t)}><IconX size={11} /></button>
        </div>
      {/each}
      <button class="tab-add" title="New query tab" onclick={() => newQuery()}><IconPlus size={13} /></button>
    </div>
    <div class="content">
      {#if !info}
        <p class="muted">This database tab is not open any more.</p>
      {:else if active}
        {#key active.key}
          {#if active.kind === "query"}
            <DbQueryTab {sessionId} tab={active} {info} databases={dbNames} tables={tableNames} {tableRefs} onChange={changed} />
          {:else if active.kind === "sessions"}
            <DbSessionsTab {sessionId} {info} />
          {:else if active.kind === "er"}
            <DbErTab {sessionId} tab={active} {info} onChange={changed} onOpenTable={(db, schema, name) => openTable(db, { schema, name, kind: "table" } as DbObject)} />
          {:else}
            <DbTableTab {sessionId} tab={active} {info} onChange={changed} onQuery={(sql, db) => newQuery(sql, db ?? active?.database)} />
          {/if}
        {/key}
      {:else}
        <p class="muted">Open a table from the list, or a new query tab with +.</p>
      {/if}
    </div>
  </section>
</div>

<style>
  .db-pane { display: flex; height: 100%; min-height: 0; background: var(--base); color: var(--text); }
  .tree { flex: 0 0 auto; display: flex; flex-direction: column; min-height: 0; border-right: 1px solid var(--surface0); background: var(--mantle); }
  .vdrag { flex: 0 0 4px; cursor: col-resize; }
  .vdrag:hover { background: var(--surface1); }
  .tree-head { display: flex; align-items: center; gap: 0.4rem; padding: 0.4rem 0.5rem; font-size: 0.78rem; color: var(--subtext0); border-bottom: 1px solid var(--surface0); }
  .srv { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tree-head button, .mini { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0.1rem; display: inline-flex; }
  .tree-head button:hover, .mini:hover { color: var(--text); }
  .tree-tools { display: flex; align-items: center; gap: 0.4rem; padding: 0.35rem 0.5rem; }
  .tree-tools input:not([type]) { flex: 1 1 auto; min-width: 0; background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: 3px; padding: 0.2rem 0.4rem; font-size: 0.78rem; }
  .tree-tools label { font-size: 0.72rem; color: var(--subtext0); display: inline-flex; align-items: center; gap: 0.2rem; white-space: nowrap; }
  .tree-body { flex: 1 1 auto; overflow: auto; padding-bottom: 0.5rem; font-size: 0.8rem; }
  .db-row { display: flex; align-items: center; gap: 0.25rem; padding: 0 0.4rem; }
  .db-row:hover { background: var(--surface0); }
  .twisty { flex: 1 1 auto; min-width: 0; display: flex; align-items: center; gap: 0.25rem; background: none; border: none; color: var(--text); cursor: pointer; padding: 0.2rem 0; text-align: left; font: inherit; }
  .dname { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .sys { opacity: 0.6; }
  .meta { flex: 0 0 auto; font-size: 0.68rem; color: var(--overlay1); }
  .schema { display: flex; align-items: center; gap: 0.25rem; padding-right: 0.4rem; }
  .schema .sname { flex: 1 1 auto; }
  .schema { font-size: 0.7rem; color: var(--overlay1); text-transform: uppercase; letter-spacing: 0.03em; padding: 0.3rem 0 0.1rem; }
  .ind { padding-left: 1.4rem; }
  .obj { display: flex; align-items: center; gap: 0.35rem; width: 100%; background: none; border: none; color: var(--text); cursor: pointer; padding: 0.15rem 0.5rem 0.15rem 1.4rem; text-align: left; font: inherit; }
  .obj.ind2 { padding-left: 1.9rem; }
  .obj:hover { background: var(--surface0); }
  .obj.on { background: color-mix(in srgb, var(--blue) 18%, transparent); }
  .oname { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .main { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; min-height: 0; }
  /* Fixed height: with no tab left only the + button would set it. */
  .tabs { display: flex; align-items: stretch; gap: 1px; height: 30px; border-bottom: 1px solid var(--surface0); background: var(--mantle); overflow-x: auto; overflow-y: hidden; flex: 0 0 auto; }
  .tab { display: flex; align-items: center; background: var(--surface0); border-bottom: 2px solid transparent; }
  .tab.on { background: var(--base); border-bottom-color: var(--blue); }
  .tab-btn { display: inline-flex; align-items: center; gap: 0.3rem; background: none; border: none; color: var(--text); cursor: pointer; padding: 0.3rem 0.2rem 0.3rem 0.55rem; font-size: 0.78rem; white-space: nowrap; }
  .tab-x { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0.2rem 0.4rem; display: inline-flex; }
  .tab-x:hover { color: var(--red); }
  .tab-add { background: none; border: none; color: var(--overlay1); cursor: pointer; padding: 0 0.5rem; display: inline-flex; align-items: center; }
  .tab-add:hover { color: var(--text); }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--green); }
  .dot.tx { background: var(--yellow); }
  .content { flex: 1 1 auto; min-height: 0; }
  .export-bar { flex: 0 0 auto; display: flex; align-items: center; gap: 0.4rem; padding: 0.3rem 0.5rem; font-size: 0.74rem; color: var(--subtext0); border-top: 1px solid var(--surface0); }
  .ex-text { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .export-bar button { font-size: 0.72rem; padding: 0.1rem 0.45rem; background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 3px; cursor: pointer; }
  .muted { margin: 0.6rem 0.75rem; font-size: 0.8rem; color: var(--subtext0); }
  pre.err { margin: 0.5rem; color: var(--red); white-space: pre-wrap; font-size: 0.75rem; }
  .retry { margin: 0 0.5rem; font-size: 0.75rem; background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 3px; cursor: pointer; padding: 0.15rem 0.5rem; }
  @media (max-width: 640px) {
    .db-pane { flex-direction: column; }
    .tree { width: 100% !important; max-height: 35%; border-right: none; border-bottom: 1px solid var(--surface0); }
    .vdrag { display: none; }
  }
</style>
