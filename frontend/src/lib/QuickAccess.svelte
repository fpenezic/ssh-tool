<script lang="ts">
  // "Quick access" panel pinned above the main tree. Lists favourites
  // first, then the N most-recent connections beneath a thin
  // separator. Same click semantics as the tree: single-click selects
  // and shows the editor, double-click connects.
  //
  // Collapsed state persists in the settings table so it survives a
  // restart (a power user who keeps it folded shouldn't see it
  // expand again on every launch).

  import { tree, selection, sessions, paneTabs, view } from "./stores.svelte";
  import { api, type Connection } from "./api";
  import { connectionActions } from "./connectionActions.svelte";
  import { IconStar, IconHistory } from "./iconMap";

  const RECENT_COUNT = 10;
  const COLLAPSED_KEY = "quick_access_collapsed";

  let collapsed = $state(false);
  $effect(() => {
    // Lazy load the persisted state on first mount.
    api.settingsGet(COLLAPSED_KEY).then((v) => {
      collapsed = v === "1";
    }).catch(() => {});
  });

  function toggleCollapsed() {
    collapsed = !collapsed;
    api.settingsSet(COLLAPSED_KEY, collapsed ? "1" : "0").catch(console.warn);
  }

  const favs = $derived(tree.favorites());
  const recent = $derived(
    // Recents excluding anything already shown in favourites - saves
    // visual real estate and avoids duplicates.
    tree.recent(RECENT_COUNT + favs.length)
      .filter((c) => !c.favorite)
      .slice(0, RECENT_COUNT)
  );

  let connectingId = $state<string | null>(null);
  let lastClickId = "";
  let lastClickAt = 0;

  async function quickConnect(c: Connection) {
    if (connectingId === c.id) return;
    connectingId = c.id;
    await connectionActions.connectOne(c.id);
    connectingId = null;
  }

  function onRowClick(c: Connection) {
    selection.selectConnection(c.id);
    const now = Date.now();
    if (c.id === lastClickId && now - lastClickAt < 400) {
      lastClickId = "";
      quickConnect(c);
    } else {
      lastClickId = c.id;
      lastClickAt = now;
    }
  }

  function isSelected(c: Connection): boolean {
    return selection.current.kind === "connection" && selection.current.id === c.id;
  }
</script>

{#if favs.length > 0 || recent.length > 0}
  <div class="qa" class:collapsed>
    <button class="qa-header" onclick={toggleCollapsed} title="Toggle quick access">
      <span class="chev">{collapsed ? "▸" : "▾"}</span>
      <span class="title">Quick access</span>
      <span class="counts">
        {#if favs.length}<span class="badge fav"><IconStar size={10} fill="currentColor" />{favs.length}</span>{/if}
        {#if recent.length}<span class="badge rec">{recent.length}</span>{/if}
      </span>
    </button>

    {#if !collapsed}
      {#if favs.length > 0}
        {#each favs as c (c.id)}
          <button
            class="row"
            class:selected={isSelected(c)}
            class:connecting={connectingId === c.id}
            onclick={() => onRowClick(c)}
            title={c.hostname}
          >
            <span class="ico"><IconStar size={12} fill="var(--yellow)" /></span>
            <span class="nm">{c.name}</span>
            {#if c.hostname}<span class="host">{c.hostname}</span>{/if}
          </button>
        {/each}
      {/if}

      {#if favs.length > 0 && recent.length > 0}
        <div class="sep"></div>
      {/if}

      {#each recent as c (c.id)}
        <button
          class="row"
          class:selected={isSelected(c)}
          class:connecting={connectingId === c.id}
          onclick={() => onRowClick(c)}
          title={c.hostname}
        >
          <span class="ico"><IconHistory size={12} /></span>
          <span class="nm">{c.name}</span>
          {#if c.hostname}<span class="host">{c.hostname}</span>{/if}
        </button>
      {/each}
    {/if}
  </div>
{/if}

<style>
  .qa {
    border-bottom: 1px solid var(--surface0);
    background: var(--mantle);
    flex-shrink: 0;
  }
  .qa-header {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    width: 100%;
    padding: 0.4rem 0.6rem;
    background: transparent;
    border: 0;
    color: var(--subtext0);
    cursor: pointer;
    font: inherit;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .qa-header:hover { color: var(--text); }
  .qa-header .chev { width: 0.9rem; font-size: 0.7rem; color: var(--overlay0); }
  .qa-header .title { flex: 1; text-align: left; }
  .qa-header .counts { display: flex; gap: 0.25rem; }
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 0.15rem;
    background: var(--surface0);
    color: var(--subtext0);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.7rem;
  }
  .badge.fav { background: var(--yellow)33; color: var(--yellow); }
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    width: 100%;
    padding: 0.18rem 0.6rem;
    background: transparent;
    border: 0;
    color: inherit;
    cursor: pointer;
    font: inherit;
    font-size: 0.82rem;
    text-align: left;
  }
  .row:hover { background: var(--surface0); }
  .row.selected { background: var(--surface1); }
  .row.connecting { opacity: 0.7; cursor: wait; }
  .ico { width: 1.1rem; text-align: center; font-size: 0.78rem; color: var(--overlay0); }
  .row .nm { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .host { color: var(--overlay0); font-size: 0.72rem; }
  .sep {
    height: 1px;
    background: var(--surface0);
    margin: 0.25rem 0.6rem;
  }
</style>
