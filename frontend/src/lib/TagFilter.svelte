<script lang="ts">
  // Tag filter chips.
  //
  // Renders whenever the tree has any tags OR the active filter is
  // non-empty. The second condition matters because the active filter
  // can stay populated with tag names that no longer exist on any
  // connection (user removed the last tagged row) - without this the
  // pill row would vanish and there'd be no way to clear the filter
  // from the UI, leaving the tree apparently broken.
  //
  // Collapsible header - collapsed state persists in the settings DB
  // so a long pill row stays the way you left it across app restarts
  // (sessionStorage would have reset every launch).

  import { tree } from "./stores.svelte";
  import { tagFilter, FACET_KEYS, type FacetKey } from "./tagFilter.svelte.ts";
  import { onMount } from "svelte";
  import { api } from "./api";

  const tags = $derived(tree.allTags());
  const activeCount = $derived(tagFilter.active.size);
  const facets = $derived(tagFilter.allFacets());
  const facetActiveCount = $derived.by(() => {
    let n = 0;
    for (const vals of tagFilter.activeFacets.values()) n += vals.size;
    return n;
  });
  const totalActive = $derived(activeCount + facetActiveCount);

  // Pretty labels for facet keys.
  const FACET_LABEL: Record<FacetKey, string> = {
    auth: "Auth",
    user: "User",
    via:  "Via",
    port: "Port",
  };
  // Active filter tags that are no longer in any connection's tag
  // list - surface them so the user can untoggle the orphan and
  // get the tree back.
  const orphanActives = $derived.by(() => {
    const known = new Set(tags.map((t) => t.tag));
    const out: string[] = [];
    for (const t of tagFilter.active) {
      if (!known.has(t)) out.push(t);
    }
    return out;
  });
  const visible = $derived(
    tags.length > 0 || activeCount > 0 || facets.size > 0 || facetActiveCount > 0,
  );

  const COLLAPSED_KEY = "tag_filter_collapsed";
  let collapsed = $state(false);
  onMount(async () => {
    try {
      const v = await api.settingsGet(COLLAPSED_KEY);
      collapsed = v === "1";
    } catch { /* missing - default open */ }
  });
  function toggleCollapsed() {
    collapsed = !collapsed;
    api.settingsSet(COLLAPSED_KEY, collapsed ? "1" : "0").catch(console.warn);
  }
</script>

{#if visible}
  <div class="tag-filter" class:collapsed>
    <button type="button" class="tf-header" onclick={toggleCollapsed}>
      <span class="chev">{collapsed ? "▸" : "▾"}</span>
      <span class="tf-label">Filter</span>
      {#if totalActive > 0}
        <span class="active-badge" title="{totalActive} filter(s) active">{totalActive}</span>
      {/if}
    </button>
    {#if !collapsed}
      {#if tags.length > 0 || orphanActives.length > 0}
        <div class="tf-section">
          <span class="tf-section-label">Tags</span>
          <div class="tf-row">
            {#each tags as t (t.tag)}
              {@const on = tagFilter.active.has(t.tag)}
              <button
                type="button"
                class="pill"
                class:on
                onclick={() => tagFilter.toggle(t.tag)}
              >
                {t.tag}
                <span class="cnt">{t.count}</span>
              </button>
            {/each}
            {#each orphanActives as t (t)}
              <button
                type="button"
                class="pill on stale"
                title="No connections wear this tag anymore - click to remove from filter"
                onclick={() => tagFilter.toggle(t)}
              >
                {t}
                <span class="cnt">0</span>
              </button>
            {/each}
          </div>
        </div>
      {/if}

      {#each [...FACET_KEYS] as fk (fk)}
        {@const items = facets.get(fk) ?? []}
        {#if items.length > 0}
          <div class="tf-section">
            <span class="tf-section-label">{FACET_LABEL[fk]}</span>
            <div class="tf-row">
              {#each items as it (it.value)}
                {@const on = tagFilter.isFacetActive(fk, it.value)}
                <button
                  type="button"
                  class="pill facet facet-{fk}"
                  class:on
                  onclick={() => tagFilter.toggleFacet(fk, it.value)}
                >
                  {fk}:{it.value}
                  <span class="cnt">{it.count}</span>
                </button>
              {/each}
            </div>
          </div>
        {/if}
      {/each}

      {#if totalActive > 0}
        <div class="tf-row footer">
          <button type="button" class="clear" onclick={() => tagFilter.clear()}>
            ✕ clear all
          </button>
        </div>
      {/if}
    {/if}
  </div>
{/if}

<style>
  .tag-filter {
    border-bottom: 1px solid var(--surface0);
    background: var(--mantle);
    padding: 0.2rem 0.5rem 0.35rem;
  }
  .tag-filter.collapsed { padding-bottom: 0.2rem; }
  .tf-header {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    background: transparent;
    border: 0;
    color: var(--subtext0);
    cursor: pointer;
    font: inherit;
    padding: 0.2rem 0;
  }
  .tf-header:hover { color: var(--text); }
  .chev { color: var(--overlay0); font-size: 0.7rem; width: 0.8rem; text-align: center; }
  .tf-label {
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .active-badge {
    background: var(--blue);
    color: var(--on-accent);
    border-radius: 999px;
    padding: 0 0.4rem;
    font-size: 0.65rem;
    font-weight: 600;
    line-height: 1.3;
  }
  .tf-section {
    margin-top: 0.3rem;
  }
  .tf-section-label {
    font-size: 0.62rem;
    text-transform: uppercase;
    color: var(--overlay1);
    letter-spacing: 0.06em;
    margin-bottom: 0.15rem;
    display: block;
  }
  .tf-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    align-items: center;
  }
  .tf-row.footer { margin-top: 0.4rem; }
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    background: var(--surface0);
    color: var(--subtext0);
    border: 1px solid var(--surface0);
    border-radius: 999px;
    padding: 0.1rem 0.55rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.75rem;
  }
  .pill:hover { background: var(--surface1); color: var(--text); }
  .pill.on {
    background: var(--base);
    color: var(--blue);
    border-color: var(--blue);
  }
  .pill .cnt {
    color: var(--overlay0);
    font-size: 0.68rem;
  }
  .pill.on .cnt { color: var(--blue); }
  /* Facet pills get per-key accents so the eye separates auth from
     user from via at a glance. Off-state is dim; on-state lights up
     the facet's colour. */
  .pill.facet { color: var(--overlay1); }
  .pill.facet-auth.on { background: var(--base); color: var(--mauve); border-color: var(--mauve); }
  .pill.facet-auth.on .cnt { color: var(--mauve); }
  .pill.facet-user.on { background: var(--base); color: var(--teal); border-color: var(--teal); }
  .pill.facet-user.on .cnt { color: var(--teal); }
  .pill.facet-via.on  { background: var(--base); color: var(--yellow); border-color: var(--yellow); }
  .pill.facet-via.on .cnt { color: var(--yellow); }
  .pill.facet-port.on { background: var(--base); color: var(--pink); border-color: var(--pink); }
  .pill.facet-port.on .cnt { color: var(--pink); }
  .pill.stale {
    border-color: var(--yellow);
    color: var(--yellow);
    text-decoration: line-through;
  }
  .pill.stale .cnt { color: var(--yellow); }
  .clear {
    background: transparent;
    color: var(--red);
    border: 0;
    cursor: pointer;
    font: inherit;
    font-size: 0.72rem;
    padding: 0.1rem 0.4rem;
  }
  .clear:hover { color: var(--maroon); }
</style>
