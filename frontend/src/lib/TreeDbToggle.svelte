<script lang="ts">
  // Badge on a connection row that has database rows under it: how many,
  // and a click folds them away / back. The same as TreeBookmarkToggle.
  import { onMount } from "svelte";
  import { dbTreeRows } from "./dbState.svelte";
  import { tree } from "./stores.svelte";
  import { collapsedDbs } from "./treeState.svelte";
  import { IconDb } from "./iconMap";

  let { connId }: { connId: string } = $props();
  onMount(() => void collapsedDbs.load());
  const n = $derived(dbTreeRows(tree.connectionById(connId)).length);
  const folded = $derived(collapsedDbs.isExpanded(connId));
</script>

{#if n > 0}
  <button
    class="db-toggle"
    class:folded
    title={folded ? `Show ${n} database${n === 1 ? "" : "s"}` : `Hide database${n === 1 ? "" : "s"}`}
    onclick={(e) => { e.stopPropagation(); collapsedDbs.toggle(connId); }}
    ondblclick={(e) => e.stopPropagation()}
    onmousedown={(e) => e.stopPropagation()}
  ><IconDb size={10} />{n}</button>
{/if}

<style>
  .db-toggle {
    flex-shrink: 0; display: inline-flex; align-items: center; gap: 0.15rem;
    font: inherit; font-size: 0.66rem; line-height: 1;
    margin-right: 0.25rem; padding: 0.1rem 0.3rem; border: 0; border-radius: 7px;
    color: var(--teal); background: color-mix(in srgb, var(--teal) 14%, transparent);
    cursor: pointer;
  }
  .db-toggle.folded { background: transparent; color: var(--overlay1); }
  .db-toggle:hover { color: var(--teal); background: color-mix(in srgb, var(--teal) 24%, transparent); }
</style>
