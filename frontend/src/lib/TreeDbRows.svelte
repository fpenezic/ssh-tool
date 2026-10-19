<script lang="ts">
  // The databases under a connection in the tree (dbTreeRows): its own
  // database and the listed ones, each unless "Show in tree" is off. A row
  // opens its Database tab on double-click or Enter, next to whatever the
  // connection itself opens (a terminal, usually). Folded away with the
  // badge on the connection row (TreeDbToggle).
  import { onMount } from "svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import { dbTreeRows, type DbTreeRow } from "./dbState.svelte";
  import { selection, tree } from "./stores.svelte";
  import { collapsedDbs } from "./treeState.svelte";
  import { contextMenu } from "./contextMenu.svelte.ts";
  import { IconDb } from "./iconMap";

  let { connId, depth }: { connId: string; depth: number } = $props();
  onMount(() => void collapsedDbs.load());
  const rows = $derived(collapsedDbs.isExpanded(connId) ? [] : dbTreeRows(tree.connectionById(connId)));

  // Double-click by timing, like connection rows: native dblclick does not
  // fire reliably on touch WebViews.
  let lastId: string | null = null;
  let lastAt = 0;
  function onClick(r: DbTreeRow) {
    selection.selectConnection(connId);
    const now = Date.now();
    if (r.id === lastId && now - lastAt < 400) {
      lastId = null;
      void connectionActions.openDb(connId, r.id);
    } else {
      lastId = r.id;
      lastAt = now;
    }
  }
  function openMenu(e: MouseEvent, r: DbTreeRow) {
    selection.selectConnection(connId);
    contextMenu.show(e, [
      { label: "Open", iconComponent: IconDb, onSelect: () => void connectionActions.openDb(connId, r.id) },
    ]);
  }
</script>

{#each rows as r (r.id)}
  <div
    class="row"
    style="--depth: {depth};"
    role="treeitem"
    tabindex="0"
    aria-selected="false"
    data-kind="database"
    title={`${r.label}${r.detail ? ` (${r.detail})` : ""}\nDouble-click to open the database`}
    onclick={() => onClick(r)}
    oncontextmenu={(e) => openMenu(e, r)}
    onkeydown={(e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        void connectionActions.openDb(connId, r.id);
      }
    }}
  >
    <span class="chev"></span>
    <span class="icon"><IconDb size={12} /></span>
    <span class="name">{r.label}</span>
    <span class="host">{r.detail}</span>
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
