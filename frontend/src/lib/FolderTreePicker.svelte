<script lang="ts">
  // A folder picker shown as a tree: top-level folders first, children
  // indented and collapsed until opened, a search box that lists matching
  // full paths. For long per-customer trees where a flat list sorted by
  // path buries the folder you want.
  import { clickOutside } from "./clickOutside";
  import { IconChevronRight, IconChevronDown, IconFolder } from "./iconMap";

  interface Item { value: string; path: string }
  interface Props {
    items: Item[];
    value: string | null;
    onChange: (v: string) => void;
    // An extra entry above the tree (e.g. "New space (top level)").
    extra?: Item | null;
    disabled?: boolean;
  }
  let { items, value, onChange, extra = null, disabled = false }: Props = $props();

  const SEP = " / ";
  let open = $state(false);
  let query = $state("");
  let expanded = $state<Record<string, boolean>>({});

  const sorted = $derived(
    [...items].sort((a, b) => {
      const x = a.path.split(SEP), y = b.path.split(SEP);
      for (let i = 0; i < Math.min(x.length, y.length); i++) {
        const c = x[i].localeCompare(y[i]);
        if (c !== 0) return c;
      }
      return x.length - y.length;
    }),
  );
  const paths = $derived(new Set(sorted.map((i) => i.path)));
  const hasChildren = $derived(new Set(sorted.map((i) => i.path.split(SEP).slice(0, -1).join(SEP)).filter((p) => p !== "")));
  // Visible rows: every ancestor that is itself listed must be expanded.
  const rows = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (q) return sorted.filter((i) => i.path.toLowerCase().includes(q)).map((i) => ({ ...i, depth: 0, label: i.path }));
    return sorted
      .filter((i) => {
        const parts = i.path.split(SEP);
        for (let n = 1; n < parts.length; n++) {
          const anc = parts.slice(0, n).join(SEP);
          if (paths.has(anc) && !expanded[anc]) return false;
        }
        return true;
      })
      .map((i) => {
        const parts = i.path.split(SEP);
        let depth = 0;
        for (let n = 1; n < parts.length; n++) if (paths.has(parts.slice(0, n).join(SEP))) depth++;
        return { ...i, depth, label: depth ? parts[parts.length - 1] : i.path };
      });
  });
  const current = $derived(value === "" && extra ? extra.path : (items.find((i) => i.value === value)?.path ?? "(choose)"));

  function show() {
    if (disabled) return;
    open = true;
    query = "";
    // Open the chain down to the current choice.
    const cur = items.find((i) => i.value === value)?.path;
    if (cur) {
      const parts = cur.split(SEP);
      const next = { ...expanded };
      for (let n = 1; n < parts.length; n++) next[parts.slice(0, n).join(SEP)] = true;
      expanded = next;
    }
  }
  function pick(v: string) {
    onChange(v);
    open = false;
  }
</script>

<div class="ftp" use:clickOutside={{ onOutside: () => (open = false) }}>
  <button type="button" class="current" onclick={() => (open ? (open = false) : show())} {disabled} title={current}>
    <IconFolder size={12} /> <span>{current}</span> <IconChevronDown size={12} />
  </button>
  {#if open}
    <div class="panel">
      <!-- svelte-ignore a11y_autofocus -->
      <input class="search" placeholder="Search folders..." bind:value={query} autofocus onkeydown={(e) => { if (e.key === "Escape") { e.stopPropagation(); open = false; } }} />
      <div class="list">
        {#if extra && !query}
          <button type="button" class="row" class:sel={value === extra.value} onclick={() => pick(extra.value)}>{extra.path}</button>
        {/if}
        {#each rows as r (r.value)}
          <div class="row-wrap" style={`padding-left: ${r.depth * 14}px`}>
            {#if !query && hasChildren.has(r.path)}
              <button type="button" class="tw" onclick={() => (expanded = { ...expanded, [r.path]: !expanded[r.path] })} title={expanded[r.path] ? "Collapse" : "Expand"}>
                {#if expanded[r.path]}<IconChevronDown size={11} />{:else}<IconChevronRight size={11} />{/if}
              </button>
            {:else}<span class="tw-space"></span>{/if}
            <button type="button" class="row" class:sel={r.value === value} onclick={() => pick(r.value)} title={r.path}>{r.label}</button>
          </div>
        {/each}
        {#if rows.length === 0}<div class="empty">No folder matches.</div>{/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .ftp { position: relative; min-width: 0; flex: 1; }
  .current { display: flex; align-items: center; gap: 5px; width: 100%; text-align: left; background: var(--mantle); border: 1px solid var(--surface1); border-radius: var(--r-sm); padding: 3px 8px; color: var(--text); cursor: pointer; font-size: var(--fs-sm); }
  .current span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .panel { position: absolute; z-index: 20; top: calc(100% + 2px); left: 0; right: 0; background: var(--base); border: 1px solid var(--surface1); border-radius: var(--r-sm); box-shadow: 0 6px 20px rgba(0,0,0,0.45); display: flex; flex-direction: column; max-height: 320px; }
  .search { margin: 6px; padding: 3px 6px; font-size: var(--fs-sm); }
  .list { overflow-y: auto; padding: 0 4px 6px; }
  .row-wrap { display: flex; align-items: center; }
  .tw, .tw-space { width: 16px; flex: 0 0 16px; display: inline-flex; align-items: center; justify-content: center; }
  .tw { background: none; border: 0; color: var(--subtext0); cursor: pointer; padding: 0; }
  .row { flex: 1; text-align: left; background: none; border: 0; color: var(--text); padding: 2px 6px; border-radius: var(--r-sm); cursor: pointer; font-size: var(--fs-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row:hover { background: var(--surface0); }
  .row.sel { background: var(--surface1); }
  .empty { color: var(--subtext0); font-size: var(--fs-sm); padding: 4px 8px; }
</style>
