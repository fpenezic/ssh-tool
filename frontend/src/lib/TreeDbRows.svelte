<script lang="ts">
  // The databases under a connection in the tree (dbTreeRows): its own
  // database and the listed ones, each unless "Show in tree" is off. A row
  // opens its Database tab on double-click or Enter, next to whatever the
  // connection itself opens (a terminal, usually). Folded away with the
  // badge on the connection row (TreeDbToggle).
  import { onMount } from "svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import { dbTabs, dbTreeRows, dbTreeSelectedUnder, type DbTreeRow } from "./dbState.svelte";
  import { selection, tree, view } from "./stores.svelte";
  import { api } from "./api";
  import { toast } from "./toast.svelte.ts";
  import { IconStar } from "./iconMap";
  import { treeBookmarks } from "./treeBookmarks.svelte";
  import { collapsedDbs } from "./treeState.svelte";
  import { contextMenu } from "./contextMenu.svelte.ts";
  import { IconDb, IconTrash } from "./iconMap";

  let { connId, depth }: { connId: string; depth: number } = $props();
  onMount(() => void collapsedDbs.load());
  const rows = $derived(collapsedDbs.isExpanded(connId) ? [] : dbTreeRows(tree.connectionById(connId)));
  // Databases with a tab open, drawn green like a connected connection.
  const openIds = $derived(new Set(
    Object.values(dbTabs.infos).filter((i) => i.connection_id === connId).map((i) => i.target_id ?? ""),
  ));

  // Double-click by timing, like connection rows: native dblclick does not
  // fire reliably on touch WebViews.
  let lastId: string | null = null;
  let lastAt = 0;
  // A click selects the row and shows the connection's Database settings.
  // False on Stay in the unsaved prompt: then nothing else happens either.
  async function select(r: DbTreeRow): Promise<boolean> {
    // Services replaces the Forwards tab: a half-edited forward asks first.
    if (!(await view.forceEditorTab("db"))) return false;
    selection.selectConnection(connId);
    treeBookmarks.selected = null;
    dbTabs.treeSelected = { connId, id: r.id };
    return true;
  }
  function isSelected(r: DbTreeRow): boolean {
    return dbTreeSelectedUnder(connId) && dbTabs.treeSelected?.id === r.id;
  }
  async function onClick(r: DbTreeRow) {
    const now = Date.now();
    if (!(await select(r))) {
      lastId = null;
      return;
    }
    if (r.id === lastId && now - lastAt < 400) {
      lastId = null;
      void connectionActions.openDb(connId, r.id);
    } else {
      lastId = r.id;
      lastAt = now;
    }
  }
  async function openMenu(e: MouseEvent, r: DbTreeRow) {
    // Before the await: on Stay the app's menu is not shown, and the
    // browser's own must not come up in its place.
    e.preventDefault();
    if (!(await select(r))) return;
    contextMenu.show(e, [
      { label: "Open", iconComponent: IconDb, onSelect: () => void connectionActions.openDb(connId, r.id) },
      ...(r.isDefault || rows.length < 2 ? [] : [{ label: "Set as default", iconComponent: IconStar, onSelect: () => void setDefault(r) }]),
      { label: "Remove...", iconComponent: IconTrash, onSelect: () => void connectionActions.removeDbRow(connId, r.id) },
    ]);
  }
  // The default opens on "Double-click opens: Database" and lists first.
  async function setDefault(r: DbTreeRow) {
    try {
      await api.dbSetDefault(connId, r.id);
      await tree.load();
    } catch (e) {
      toast.err(`Set as default: ${String((e as any)?.message ?? e)}`);
    }
  }
</script>

{#each rows as r (r.id)}
  <div
    class="row"
    class:selected={isSelected(r)}
    class:live={openIds.has(r.id)}
    style="--depth: {depth};"
    role="treeitem"
    tabindex="0"
    aria-selected={isSelected(r)}
    data-kind="database"
    title={`${r.label}${r.detail ? ` (${r.detail})` : ""}${openIds.has(r.id) ? " - open" : ""}\nDouble-click to open the database`}
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
    {#if r.isDefault && rows.length > 1}<span class="def" title="The default: Double-click opens -> Database opens it">default</span>{/if}
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
  .row.selected { background: var(--surface1); }
  .row.live .name { font-weight: 600; color: var(--green); }
  .row.live .icon { color: var(--green); }
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
  .def { flex-shrink: 0; font-size: 0.62rem; color: var(--teal); border: 1px solid color-mix(in srgb, var(--teal) 45%, transparent); border-radius: 6px; padding: 0 0.3rem; }
  .host {
    color: var(--overlay1); font-size: 0.72rem; margin-left: 0.4rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 45%;
  }
</style>
