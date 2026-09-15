<script lang="ts">
  // Renders the single active fast tooltip (see tooltip.svelte.ts). One host
  // per window; the store only ever holds one tooltip at a time.
  //
  // Clamping needs the rendered width, which is only known after the element
  // exists, so the position is corrected in an $effect rather than computed
  // up front. The first frame is hidden to keep that correction invisible.
  import { tooltips } from "./tooltip.svelte";

  const GAP = 6;      // space between the anchor and the bubble
  const MARGIN = 6;   // keep this far off the viewport edges

  let el = $state<HTMLDivElement | null>(null);
  let left = $state(0);
  let top = $state(0);
  let placed = $state(false);

  const tip = $derived(tooltips.current);

  $effect(() => {
    const t = tip;
    const node = el;
    if (!t || !node) {
      placed = false;
      return;
    }
    const w = node.offsetWidth;
    const h = node.offsetHeight;
    const maxLeft = window.innerWidth - w - MARGIN;
    left = Math.max(MARGIN, Math.min(t.x - w / 2, Math.max(MARGIN, maxLeft)));
    // `below` is the store's hint for anchors too close to the top to fit a
    // bubble above them; re-checked here now that the height is known.
    top = t.below || t.y - h - GAP < MARGIN ? t.y + GAP + 18 : t.y - h - GAP;
    placed = true;
  });
</script>

{#if tip}
  <div
    class="tip"
    class:placed
    role="tooltip"
    bind:this={el}
    style="left: {left}px; top: {top}px;"
  >{tip.text}</div>
{/if}

<style>
  .tip {
    position: fixed;
    z-index: 500; /* above toasts (400): a toast must not cover the tooltip */
    pointer-events: none;
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    padding: 0.28rem 0.5rem;
    font-size: 0.72rem;
    line-height: 1.35;
    max-width: 320px;
    box-shadow: 0 4px 14px rgba(0, 0, 0, 0.4);
    white-space: normal;
    /* Hidden until the $effect has clamped it into the viewport, so a wide
       tooltip never flashes at the unclamped position first. */
    opacity: 0;
  }
  .tip.placed {
    opacity: 1;
  }
</style>
