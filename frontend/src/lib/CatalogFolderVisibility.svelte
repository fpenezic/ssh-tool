<script lang="ts">
  // Settings - Catalogs: which of a catalog's folders show in this user's
  // tree. Ticked = shown. Unticking a folder hides everything under it;
  // nothing is removed or unsynced, and ticking it brings it straight back.
  import { api, type Folder } from "./api";
  import { tree } from "./stores.svelte";
  import { catalogVisibility } from "./catalogVisibility.svelte";
  import { IconChevronRight, IconChevronDown } from "./iconMap";

  let { catalogId, rootFolderId }: { catalogId: string; rootFolderId: string } = $props();

  // The full list, hidden folders included (the tree has them filtered out).
  let all = $state<Folder[]>([]);
  let expanded = $state<Record<string, boolean>>({});
  let filter = $state("");

  async function reload() {
    try { all = ((await api.foldersList()) ?? []).filter((f) => f.catalog_id === catalogId && !!f.remote_uuid); }
    catch { all = []; }
  }
  $effect(() => { void catalogId; void reload(); });

  const children = $derived.by(() => {
    const m = new Map<string, Folder[]>();
    for (const f of all) {
      const p = f.parent_id ?? "";
      m.set(p, [...(m.get(p) ?? []), f]);
    }
    for (const l of m.values()) l.sort((a, b) => a.name.localeCompare(b.name));
    return m;
  });
  const top = $derived(children.get(rootFolderId) ?? []);
  const q = $derived(filter.trim().toLowerCase());
  // With a filter: the matching folders and their ancestors, all open.
  const matchIds = $derived.by(() => {
    if (!q) return null;
    const byId = new Map(all.map((f) => [f.id, f]));
    const keep = new Set<string>();
    for (const f of all) {
      if (!f.name.toLowerCase().includes(q)) continue;
      for (let c: Folder | undefined = f, d = 0; c && d < 64; d++) {
        keep.add(c.id);
        c = c.parent_id ? byId.get(c.parent_id) : undefined;
      }
    }
    return keep;
  });

  const hidden = (f: Folder) => catalogVisibility.isHidden(catalogId, f.remote_uuid ?? "");
  async function toggle(f: Folder, show: boolean) {
    await catalogVisibility.set(catalogId, f.remote_uuid ?? "", !show);
    await tree.load();
  }
  async function showAll() {
    await catalogVisibility.showAll(catalogId);
    await tree.load();
  }
</script>

{#snippet node(f: Folder, depth: number, underHidden: boolean)}
  {#if !matchIds || matchIds.has(f.id)}
    {@const kids = children.get(f.id) ?? []}
    {@const open = !!matchIds || !!expanded[f.id]}
    <div class="row" style={`padding-left: ${depth * 16}px`}>
      {#if kids.length > 0}
        <button type="button" class="tw" onclick={() => (expanded = { ...expanded, [f.id]: !expanded[f.id] })} title={open ? "Collapse" : "Expand"}>
          {#if open}<IconChevronDown size={11} />{:else}<IconChevronRight size={11} />{/if}
        </button>
      {:else}<span class="tw"></span>{/if}
      <label class:dim={underHidden}>
        <input type="checkbox" checked={!hidden(f)} disabled={underHidden} onchange={(e) => toggle(f, (e.currentTarget as HTMLInputElement).checked)} />
        {f.name}
      </label>
    </div>
    {#if open}
      {#each kids as k (k.id)}{@render node(k, depth + 1, underHidden || hidden(f))}{/each}
    {/if}
  {/if}
{/snippet}

<div class="vis">
  <h3>Folders in your tree <span class="hint-inline">{catalogVisibility.count(catalogId) === 0 ? "all shown" : `${catalogVisibility.count(catalogId)} hidden`}</span></h3>
  <p class="hint sub">Untick what you do not work with: it leaves your tree, search and Quick Connect, here and on your other machines. It still syncs; tick it again to bring it back. Access is the catalog's, not this.</p>
  {#if all.length > 12}
    <input class="filter" placeholder="Filter folders..." bind:value={filter} />
  {/if}
  <div class="list">
    {#each top as f (f.id)}{@render node(f, 0, false)}{/each}
    {#if top.length === 0}<p class="hint sub">No folders yet.</p>{/if}
  </div>
  {#if catalogVisibility.count(catalogId) > 0}
    <button class="show-all" onclick={showAll}>Show all</button>
  {/if}
</div>

<style>
  .vis { margin-top: 10px; display: flex; flex-direction: column; gap: 6px; }
  .vis h3 { margin: 0; }
  .hint-inline { font-weight: 400; opacity: 0.6; font-size: var(--fs-sm); }
  .hint.sub { margin: 0; font-size: var(--fs-sm); }
  .filter { max-width: 420px; }
  .list { max-height: 320px; overflow-y: auto; border: 1px solid var(--border, #333); border-radius: var(--r-md); padding: 4px 6px; }
  .row { display: flex; align-items: center; gap: 2px; }
  .row label { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); cursor: pointer; }
  .row label.dim { opacity: 0.5; }
  .tw { width: 16px; flex: 0 0 16px; display: inline-flex; align-items: center; justify-content: center; background: none; border: 0; color: var(--subtext0); cursor: pointer; padding: 0; }
  .show-all { align-self: flex-start; }
</style>
