// Svelte action - fires `onOutside` when the user clicks outside
// the bound element AND didn't start the click inside it. The
// mousedown-origin check matters: a text selection that starts
// inside and drags out (mouseup outside) should NOT count as an
// outside click, otherwise modals close while you're still
// selecting text.
//
// Usage:
//   <div use:clickOutside={{ onOutside: close }}>...</div>

type Opts = { onOutside: () => void };

export function clickOutside(node: HTMLElement, opts: Opts) {
  let mouseDownInside = false;

  function onDown(e: MouseEvent) {
    mouseDownInside = node.contains(e.target as Node);
  }
  function onUp(e: MouseEvent) {
    const upInside = node.contains(e.target as Node);
    const wasInside = mouseDownInside;
    mouseDownInside = false;
    if (!upInside && !wasInside) opts.onOutside();
  }

  document.addEventListener("mousedown", onDown, true);
  document.addEventListener("click", onUp, true);

  return {
    update(next: Opts) { opts = next; },
    destroy() {
      document.removeEventListener("mousedown", onDown, true);
      document.removeEventListener("click", onUp, true);
    },
  };
}
