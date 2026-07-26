// Svelte action - fires `onOutside` when the user clicks outside
// the bound element AND didn't start the click inside it. The
// mousedown-origin check matters: a text selection that starts
// inside and drags out (mouseup outside) should NOT count as an
// outside click, otherwise modals close while you're still
// selecting text.
//
// Usage:
//   <div use:clickOutside={{ onOutside: close }}>...</div>
//   <div use:clickOutside={{ onOutside: close, enabled: !embedded }}>...</div>
//
// enabled defaults to true; pass false to keep the action mounted but inert
// (e.g. when the same component renders both as a dismissable overlay and as
// an embedded pane that must not close on outside clicks).

type Opts = { onOutside: () => void; enabled?: boolean };

export function clickOutside(node: HTMLElement, opts: Opts) {
  let mouseDownInside = false;

  function onDown(e: MouseEvent) {
    mouseDownInside = node.contains(e.target as Node);
  }
  function onUp(e: MouseEvent) {
    const upInside = node.contains(e.target as Node);
    const wasInside = mouseDownInside;
    mouseDownInside = false;
    if (opts.enabled === false) return;
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
