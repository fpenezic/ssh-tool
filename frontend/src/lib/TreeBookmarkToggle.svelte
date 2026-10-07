<script lang="ts">
  // Badge on a connection row that has bookmarks pinned to the tree: shows
  // how many, and folds the bookmark rows away / back on click. Rendered
  // only when there is at least one, so it doubles as the "this
  // connection has bookmarks" hint while they are folded.
  import { treeBookmarks } from "./treeBookmarks.svelte";
  import { collapsedBookmarks } from "./treeState.svelte";
  import { IconBookmark } from "./iconMap";

  let { connId }: { connId: string } = $props();
  const n = $derived(treeBookmarks.of(connId).length);
  const folded = $derived(collapsedBookmarks.isExpanded(connId));
</script>

{#if n > 0}
  <button
    class="bm-toggle"
    class:folded
    title={folded ? `Show ${n} bookmark${n === 1 ? "" : "s"}` : `Hide bookmark${n === 1 ? "" : "s"}`}
    onclick={(e) => { e.stopPropagation(); collapsedBookmarks.toggle(connId); }}
    ondblclick={(e) => e.stopPropagation()}
    onmousedown={(e) => e.stopPropagation()}
  ><IconBookmark size={10} fill={folded ? "none" : "currentColor"} />{n}</button>
{/if}

<style>
  .bm-toggle {
    flex-shrink: 0; display: inline-flex; align-items: center; gap: 0.15rem;
    font: inherit; font-size: 0.66rem; line-height: 1;
    margin-right: 0.25rem; padding: 0.1rem 0.3rem; border: 0; border-radius: 7px;
    color: var(--mauve); background: color-mix(in srgb, var(--mauve) 14%, transparent);
    cursor: pointer;
  }
  .bm-toggle.folded { background: transparent; color: var(--overlay1); }
  .bm-toggle:hover { color: var(--mauve); background: color-mix(in srgb, var(--mauve) 24%, transparent); }
</style>
