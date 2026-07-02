<script lang="ts">
  // Vertical resize handle. Reports the new pixel width to the parent
  // via onResize. The parent owns the actual storage / CSS variable
  // wiring; this component is purely the drag affordance.

  interface Props {
    // Current width (used as the starting point of each drag).
    width: number;
    // Called with the new candidate width on every pointermove during
    // a drag. The parent decides whether to clamp / persist.
    onResize: (px: number) => void;
  }
  let { width, onResize }: Props = $props();

  let dragging = $state(false);
  let dragStartX = 0;
  let dragStartW = 0;

  function start(e: PointerEvent) {
    dragging = true;
    dragStartX = e.clientX;
    dragStartW = width;
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent) {
    if (!dragging) return;
    onResize(dragStartW + (e.clientX - dragStartX));
  }
  function end(e: PointerEvent) {
    dragging = false;
    try { (e.target as HTMLElement).releasePointerCapture(e.pointerId); } catch {}
  }
</script>

<div
  class="handle"
  class:dragging
  role="separator"
  aria-orientation="vertical"
  aria-label="Resize sidebar"
  onpointerdown={start}
  onpointermove={move}
  onpointerup={end}
  onpointercancel={end}
></div>

<style>
  .handle {
    width: 5px;
    cursor: col-resize;
    background: transparent;
    border-left: 1px solid var(--surface0);
    transition: background 80ms ease;
    user-select: none;
    touch-action: none;
  }
  .handle:hover { background: var(--surface1); }
  .handle.dragging { background: var(--blue); }
</style>
