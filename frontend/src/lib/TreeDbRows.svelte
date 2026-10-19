<script lang="ts">
  // The databases listed under a connection (overrides.db_targets), one row
  // each: double-click or Enter opens its Database tab, right-click for the
  // menu. Shown only while the database is on for the connection.
  import { connectionActions } from "./connectionActions.svelte";
  import { dbEnabledFor } from "./dbState.svelte";
  import { selection, tree } from "./stores.svelte";
  import { contextMenu } from "./contextMenu.svelte.ts";
  import { IconDb } from "./iconMap";
  import type { DbTarget } from "./api";

  let { connId, depth }: { connId: string; depth: number } = $props();
  const conn = $derived(tree.connectionById(connId));
  const rows = $derived(dbEnabledFor(conn) ? (conn?.overrides?.db_targets ?? []) : []);

  function label(t: DbTarget): string {
    return t.name || t.database || "database";
  }
  function detail(t: DbTarget): string {
    const parts = [];
    if (t.database && t.name) parts.push(t.database);
    if (t.engine) parts.push(t.engine === "mysql" ? "MySQL" : "PostgreSQL");
    if (t.read_only) parts.push("read-only");
    return parts.join(" · ");
  }

  // Double-click by timing, like connection rows: native dblclick does not
  // fire reliably on touch WebViews.
  let lastId = "";
  let lastAt = 0;
  function onClick(t: DbTarget) {
    selection.selectConnection(connId);
    const now = Date.now();
    if (t.id === lastId && now - lastAt < 400) {
      lastId = "";
      void connectionActions.openDb(connId, t.id);
    } else {
      lastId = t.id;
      lastAt = now;
    }
  }
  function openMenu(e: MouseEvent, t: DbTarget) {
    selection.selectConnection(connId);
    contextMenu.show(e, [
      { label: "Open", iconComponent: IconDb, onSelect: () => void connectionActions.openDb(connId, t.id) },
    ]);
  }
</script>

{#each rows as t (t.id)}
  <div
    class="row"
    style="--depth: {depth};"
    role="treeitem"
    tabindex="0"
    aria-selected="false"
    data-kind="database"
    title={`${label(t)}${detail(t) ? ` (${detail(t)})` : ""}\nDouble-click to open`}
    onclick={() => onClick(t)}
    oncontextmenu={(e) => openMenu(e, t)}
    onkeydown={(e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        void connectionActions.openDb(connId, t.id);
      }
    }}
  >
    <span class="chev"></span>
    <span class="icon"><IconDb size={12} /></span>
    <span class="name">{label(t)}</span>
    <span class="host">{detail(t)}</span>
  </div>
{/each}

<style>
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    width: 100%;
    padding: var(--row-pad-y) 0.4rem var(--row-pad-y) calc(0.4rem + var(--depth, 0) * 1rem);
    cursor: pointer;
    border-radius: 3px;
  }
  .row:hover { background: var(--surface0); }
  .row:focus-visible { outline: 1px solid var(--blue); outline-offset: -1px; }
  .chev { width: 1rem; flex-shrink: 0; }
  .icon { width: 1.2rem; display: inline-flex; justify-content: center; flex-shrink: 0; color: var(--teal); }
  .name {
    flex: 1; min-width: 3rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    color: var(--subtext1); font-size: 0.92em;
  }
  @media (pointer: coarse) {
    .row { padding-top: 0.5rem; padding-bottom: 0.5rem; }
    .chev { width: 1.8rem; }
  }
  .host {
    color: var(--overlay1); font-size: 0.72rem; margin-left: 0.4rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 45%;
  }
</style>
