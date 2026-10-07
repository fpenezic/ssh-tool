<script lang="ts">
  // Bookmarks pinned to the tree (ProxyBookmark.in_tree), one row each under
  // their connection. Behaves like a connection row: click selects,
  // double-click or Enter opens (starting the tunnel, and connecting
  // first, when it is not up), right-click for the menu.
  import { treeBookmarks, openBookmark, type TreeBookmark } from "./treeBookmarks.svelte";
  import { collapsedBookmarks } from "./treeState.svelte";
  import { selection } from "./stores.svelte";
  import { contextMenu } from "./contextMenu.svelte.ts";
  import { IconBookmark, IconExternalLink, IconX } from "./iconMap";

  let { connId, depth }: { connId: string; depth: number } = $props();
  const rows = $derived(collapsedBookmarks.isExpanded(connId) ? [] : treeBookmarks.of(connId));

  // Host (and port) of the URL for the dim right-hand column; a {port}
  // placeholder stays as typed.
  function where(url: string): string {
    const m = url.match(/^[a-z][a-z0-9+.-]*:\/\/([^/?#]+)/i);
    return m ? m[1] : url;
  }

  // Double-click by timing, like connection rows: native dblclick does
  // not fire reliably on touch WebViews.
  let lastKey = "";
  let lastAt = 0;
  function onClick(b: TreeBookmark) {
    select(b);
    const now = Date.now();
    if (b.key === lastKey && now - lastAt < 400) {
      lastKey = "";
      void openBookmark(b.spec, b.bm);
    } else {
      lastKey = b.key;
      lastAt = now;
    }
  }

  function select(b: TreeBookmark) {
    selection.selectConnection(b.spec.connection_id);
    treeBookmarks.selected = { key: b.key, connId: b.spec.connection_id };
  }

  function openMenu(e: MouseEvent, b: TreeBookmark) {
    select(b);
    contextMenu.show(e, [
      { label: "Open", iconComponent: IconExternalLink, onSelect: () => void openBookmark(b.spec, b.bm) },
      { label: "Hide from tree", iconComponent: IconX, onSelect: () => void treeBookmarks.hide(b) },
    ]);
  }
</script>

{#each rows as b (b.key)}
  <div
    class="row bm"
    class:selected={treeBookmarks.isSelected(b)}
    style="--depth: {depth};"
    role="treeitem"
    tabindex="0"
    aria-selected={treeBookmarks.isSelected(b)}
    data-kind="bookmark"
    title={`${b.bm.url}\nDouble-click to open${b.spec.description ? ` (via ${b.spec.description})` : ""}`}
    onclick={() => onClick(b)}
    oncontextmenu={(e) => openMenu(e, b)}
    onkeydown={(e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        void openBookmark(b.spec, b.bm);
      }
    }}
  >
    <span class="chev"></span>
    <span class="icon"><IconBookmark size={12} /></span>
    <span class="name">{b.bm.name}</span>
    <span class="host">{where(b.bm.url)}</span>
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
  .row:focus-visible { outline: 1px solid var(--blue); outline-offset: -1px; }
  .chev { width: 1rem; flex-shrink: 0; }
  .icon { width: 1.2rem; display: inline-flex; justify-content: center; flex-shrink: 0; color: var(--mauve); }
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
