// Ctrl/Cmd+A inside a focusable text block selects that block, not the
// whole app (which also picks up hidden views and xterm's injected styles).
// The same fix as the Kafka record view; use as an onkeydown handler on an
// element with tabindex="0".
export function selectAllWithin(e: KeyboardEvent) {
  if (!(e.ctrlKey || e.metaKey) || (e.key !== "a" && e.key !== "A")) return;
  e.preventDefault();
  const sel = window.getSelection();
  if (!sel) return;
  const range = document.createRange();
  range.selectNodeContents(e.currentTarget as HTMLElement);
  sel.removeAllRanges();
  sel.addRange(range);
}
