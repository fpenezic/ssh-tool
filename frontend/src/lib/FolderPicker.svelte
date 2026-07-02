<script lang="ts">
  // Modal that lets the user pick a destination folder from the
  // connections tree. Returns null = "(root)", or a folder id.
  //
  // Filter prop lets the caller exclude folders that would be invalid
  // targets (e.g. a folder you're moving plus its descendants).

  import { tree } from "./stores.svelte";
  import { IconFolder } from "./iconMap";
  import type { Folder } from "./api";
  import { clickOutside } from "./clickOutside";

  interface Props {
    title?: string;
    /**
     * Optional set of folder ids that must not appear in the picker.
     * Used to hide a folder being moved and everything beneath it.
     */
    excludeIds?: Set<string>;
    onPick: (folderId: string | null) => void;
    onCancel: () => void;
  }
  let { title = "Move to folder…", excludeIds = new Set<string>(), onPick, onCancel }: Props = $props();

  let expanded = $state<Set<string>>(new Set());
  let query = $state("");

  function isExcluded(id: string): boolean {
    return excludeIds.has(id);
  }

  function toggle(id: string) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id); else next.add(id);
    expanded = next;
  }

  // Auto-expand any ancestor of a folder whose name matches the query
  // so the user can see live matches without clicking through.
  const matchedIds = $derived.by(() => {
    if (!query.trim()) return new Set<string>();
    const q = query.toLowerCase();
    const out = new Set<string>();
    for (const f of tree.folders) {
      if (isExcluded(f.id)) continue;
      if (f.name.toLowerCase().includes(q)) {
        out.add(f.id);
        let cur = f.parent_id;
        while (cur) {
          out.add(cur);
          cur = tree.folderById(cur)?.parent_id ?? null;
        }
      }
    }
    return out;
  });

  function shouldShow(f: Folder): boolean {
    if (isExcluded(f.id)) return false;
    if (!query.trim()) return true;
    return matchedIds.has(f.id);
  }

  function isExpanded(id: string): boolean {
    if (query.trim()) return matchedIds.has(id);
    return expanded.has(id);
  }
</script>

<div class="overlay" role="presentation">
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    use:clickOutside={{ onOutside: onCancel }}
    onkeydown={(e) => { if (e.key === "Escape") onCancel(); }}
  >
    <h2>{title}</h2>
    <input
      class="search"
      placeholder="Filter folders…"
      bind:value={query}
    />
    <div class="picker-tree">
      <button
        type="button"
        class="row root"
        onclick={() => onPick(null)}
      >
        <span class="ico">📂</span>
        <span class="nm">(root - top level)</span>
      </button>
      {#each tree.childrenOf(null) as f (f.id)}
        {@render folderRow(f, 0)}
      {/each}
    </div>
    <div class="actions">
      <button onclick={onCancel}>Cancel</button>
    </div>
  </div>
</div>

{#snippet folderRow(f: Folder, depth: number)}
  {#if shouldShow(f)}
    {@const children = tree.childrenOf(f.id).filter((c) => !isExcluded(c.id))}
    {@const open = isExpanded(f.id)}
    <div class="folder-line" style="--depth: {depth}">
      <button
        type="button"
        class="chev"
        onclick={() => toggle(f.id)}
        aria-label={open ? "collapse" : "expand"}
      >{children.length ? (open ? "▾" : "▸") : " "}</button>
      <button
        type="button"
        class="row"
        onclick={() => onPick(f.id)}
        title="Move into {f.name}"
      >
        <span class="ico"><IconFolder size={13} /></span>
        <span class="nm">{f.name}</span>
      </button>
    </div>
    {#if open}
      {#each children as sub (sub.id)}
        {@render folderRow(sub, depth + 1)}
      {/each}
    {/if}
  {/if}
{/snippet}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.55);
    display: flex; align-items: center; justify-content: center;
    z-index: 60;
  }
  .modal {
    background: var(--crust);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    padding: 1rem 1.2rem;
    width: 90vw; max-width: 480px;
    max-height: 80vh;
    display: flex; flex-direction: column; gap: 0.5rem;
  }
  h2 { margin: 0; font-size: 1rem; }
  .search {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  .search:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .picker-tree {
    overflow: auto;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.3rem;
    min-height: 200px;
    flex: 1;
  }
  .folder-line { display: flex; align-items: center; gap: 0.15rem; padding-left: calc(var(--depth, 0) * 1rem); }
  .row, .chev {
    background: transparent; color: inherit; border: 0;
    font: inherit; cursor: pointer;
    padding: 0.2rem 0.4rem;
    border-radius: 3px;
    display: flex; align-items: center; gap: 0.3rem;
  }
  .chev { width: 1.1rem; color: var(--overlay0); font-size: 0.85rem; justify-content: center; padding: 0.2rem 0; }
  .row { flex: 1; min-width: 0; text-align: left; }
  .row:hover { background: var(--surface0); }
  .row.root { background: var(--base); margin-bottom: 0.3rem; }
  .row.root:hover { background: var(--surface0); }
  .ico { width: 1.2rem; text-align: center; font-size: 0.85rem; }
  .nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .actions { display: flex; justify-content: flex-end; gap: 0.4rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.9rem; border-radius: 3px; cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
</style>
