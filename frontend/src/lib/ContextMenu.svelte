<script lang="ts">
  // Singleton popover menu rendered by App.svelte. Reads from
  // contextMenu store; click outside / Esc closes.

  import { contextMenu } from "./contextMenu.svelte.ts";

  let menuEl: HTMLDivElement | undefined = $state();
  // Adjusted offsets after we measure the menu vs viewport. Falls
  // back to the raw click coordinates while the menu is mounting.
  let posX = $state(0);
  let posY = $state(0);

  // Reposition the menu whenever it (re)opens or its anchor moves.
  // The store sets x/y to clientX/clientY, which can be past the
  // window's right/bottom edge for a click near the corner. Flip
  // around the anchor in those cases so the entire menu stays
  // visible without scrollbars.
  $effect(() => {
    if (!contextMenu.open) return;
    posX = contextMenu.x;
    posY = contextMenu.y;
    queueMicrotask(() => {
      if (!menuEl) return;
      const rect = menuEl.getBoundingClientRect();
      const margin = 4;
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      if (rect.right > vw - margin) {
        posX = Math.max(margin, contextMenu.x - rect.width);
      }
      if (rect.bottom > vh - margin) {
        posY = Math.max(margin, contextMenu.y - rect.height);
      }
    });
  });
</script>

{#if contextMenu.open}
  <div
    class="backdrop"
    role="presentation"
    onclick={() => contextMenu.close()}
    oncontextmenu={(e) => { e.preventDefault(); contextMenu.close(); }}
  ></div>
  <div
    class="menu"
    bind:this={menuEl}
    style="left: {posX}px; top: {posY}px"
    role="menu"
    tabindex="-1"
    onkeydown={(e) => { if (e.key === "Escape") contextMenu.close(); }}
  >
    {#each contextMenu.items as item}
      <button
        type="button"
        class="item"
        class:danger={item.danger}
        disabled={item.disabled}
        onclick={() => contextMenu.pick(item)}
      >
        {#if item.icon}<span class="ico">{item.icon}</span>{/if}
        <span class="lbl">{item.label}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .backdrop {
    position: fixed; inset: 0;
    z-index: 70;
  }
  .menu {
    position: fixed;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.25rem;
    min-width: 200px;
    z-index: 71;
    box-shadow: 0 4px 12px rgba(0,0,0,0.5);
  }
  .item {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: 100%;
    padding: 0.35rem 0.6rem;
    background: transparent;
    color: var(--text);
    border: 0;
    cursor: pointer;
    font: inherit;
    font-size: 0.85rem;
    border-radius: 3px;
    text-align: left;
  }
  .item:hover:not(:disabled) { background: var(--surface0); }
  .item:disabled { color: var(--overlay0); cursor: not-allowed; }
  .item.danger { color: var(--red); }
  .item.danger:hover { background: var(--red); color: var(--on-accent); }
  .ico { width: 1.2rem; text-align: center; }
  .lbl { flex: 1; }
</style>
