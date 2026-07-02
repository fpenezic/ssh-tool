// Punts focus to the currently visible xterm textarea after the next
// two animation frames. Two hops are required because tab cycling and
// the snippet palette both trigger a display: none -> flex flip on the
// tab-content host; calling focus before that flip lands silently on a
// still-hidden node.
//
// Selector reaches through the .tab-content.active gate (only one tab
// is .active at a time) into the active pane's .term-wrap. Every
// Terminal component renders .term-wrap.active because xterm focus
// inside a tab is per-pane and unrelated to which tab is currently
// shown - so the .tab-content gate is what isolates the right one.
//
// Centralised so every call site (keyboard shortcuts, tab-label clicks,
// snippet fire) shares the same timing and selector.
export function focusActiveTerminal(): void {
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      const ta = document.querySelector(
        ".tab-content.active .term-wrap.active .xterm-helper-textarea",
      ) as HTMLTextAreaElement | null;
      ta?.focus();
    });
  });
}
