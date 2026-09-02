// Punts keyboard focus to the active tab's interactive surface after the next
// two animation frames. Two hops are required because tab cycling and the
// snippet palette both trigger a display: none -> flex flip on the tab-content
// host; calling focus before that flip lands silently on a still-hidden node.
//
// Selectors reach through the .tab-content.active gate (only one tab is
// .active at a time). For terminals that gate is what isolates the right pane:
// every Terminal component renders .term-wrap.active, because xterm focus
// inside a tab is per-pane and unrelated to which tab is currently shown.
//
// A tab can also be a VNC console, whose keyboard target is the noVNC canvas
// rather than an xterm textarea. Missing that case did not merely leave the
// console unfocused - focus STAYED on the previously active tab's terminal, so
// typing at a console quietly went into another host's shell.
//
// Centralised so every call site (keyboard shortcuts, tab-label clicks,
// snippet fire) shares the same timing and selectors.
export function focusActivePane(): void {
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      const active = document.querySelector(".tab-content.active");
      if (!active) return;

      const ta = active.querySelector(
        ".term-wrap.active .xterm-helper-textarea",
      ) as HTMLTextAreaElement | null;
      if (ta) {
        ta.focus();
        return;
      }

      // noVNC gives its canvas tabIndex -1: not reachable by Tab, but
      // focusable programmatically, which is exactly what we want here.
      const canvas = active.querySelector(
        ".vnc-screen canvas",
      ) as HTMLCanvasElement | null;
      if (canvas) {
        canvas.focus();
        return;
      }

      // Nothing focusable yet - a console still connecting, or one showing its
      // password form (which autofocuses its own input). Make sure focus is not
      // left sitting on some other tab's terminal, or the next keystroke lands
      // in an unrelated shell.
      const current = document.activeElement as HTMLElement | null;
      if (current?.classList.contains("xterm-helper-textarea")) {
        current.blur();
      }
    });
  });
}

/** focusSessionTerminal moves keyboard focus to the terminal showing the
 *  given session, wherever that pane sits in the layout. Used by the SFTP
 *  browser's "cd here": the command is typed into the shell, so the user
 *  has to be able to press Enter without clicking the terminal first.
 *
 *  Panes are matched by session AND view - an SFTP or VNC pane can share a
 *  session with a terminal, and only the terminal has an xterm textarea.
 *  Returns whether focus was actually moved.
 */
export function focusSessionTerminal(sessionId: string): boolean {
  const wraps = document.querySelectorAll<HTMLElement>(
    `.term-wrap[data-session="${CSS.escape(sessionId)}"]`,
  );
  for (const w of wraps) {
    const view = w.dataset.view ?? "term";
    if (view === "sftp" || view === "vnc") continue;
    const ta = w.querySelector(
      ".xterm-helper-textarea",
    ) as HTMLTextAreaElement | null;
    if (ta) {
      ta.focus();
      return true;
    }
  }
  return false;
}
