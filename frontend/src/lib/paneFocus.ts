// ---------------------------------------------------------------------
// Keyboard ownership
//
// The DOM check below answers "is something holding the keyboard RIGHT
// NOW", which is not the same question as "is the user busy with a
// palette". Three things make the difference matter:
//
//   - A palette focuses its input in an effect, so between mounting and
//     that effect running it owns the keyboard without any DOM evidence.
//   - It closes BEFORE the connection it started is dialled, so a
//     terminal finishing 300ms later sees a clean document and takes the
//     keyboard - correct on its own, wrong when the user has already
//     pressed Ctrl+K for the next host.
//   - Every terminal polls for up to 600ms to grab focus. Open several
//     sessions and those pollers overlap, so the last one to finish wins
//     regardless of what the user is doing.
//
// So palettes and modals declare ownership explicitly, for as long as
// they are open, and focus moves defer to that declaration without
// having to find evidence in the DOM.
let keyboardOwners = 0;
const releaseWaiters = new Set<() => void>();

/** claimKeyboard marks the keyboard as owned by a palette or modal until
 *  the returned function is called. Call it when the component mounts,
 *  and the result from its cleanup - a $effect return, or onDestroy.
 *
 *  Nested owners (a modal opened from a palette) are counted rather than
 *  flagged, so the inner one closing does not hand the keyboard to a
 *  terminal while the outer one is still up.
 */
export function claimKeyboard(): () => void {
  keyboardOwners++;
  let released = false;
  return () => {
    if (released) return;   // idempotent: cleanup can run more than once
    released = true;
    keyboardOwners--;
    if (keyboardOwners < 0) keyboardOwners = 0;
    if (keyboardOwners === 0) {
      // Whoever deferred to the claim gets a chance to try again. Without
      // this a terminal that found the keyboard claimed would simply give
      // up: the palette closes a moment later and nothing asks for focus
      // again, leaving the session the user just opened unfocused.
      const waiting = [...releaseWaiters];
      releaseWaiters.clear();
      for (const w of waiting) w();
    }
  };
}

/** onKeyboardReleased registers fn to run when the last claim is dropped.
 *  Fires at most once; returns a function that cancels the registration.
 *
 *  Callers use this instead of simply abandoning a focus move: "someone
 *  else holds the keyboard" is a reason to wait, not a reason to stop.
 */
export function onKeyboardReleased(fn: () => void): () => void {
  if (keyboardOwners === 0) return () => {};
  releaseWaiters.add(fn);
  return () => releaseWaiters.delete(fn);
}

/** keyboardIsClaimed reports whether any palette or modal currently holds
 *  the keyboard. */
export function keyboardIsClaimed(): boolean {
  return keyboardOwners > 0;
}

/** __resetKeyboardClaims is for tests only. */
export function __resetKeyboardClaims(): void {
  keyboardOwners = 0;
  releaseWaiters.clear();
}

/** userIsTypingElsewhere reports whether the keyboard currently belongs to
 *  something that must keep it: an open palette or modal (they all render
 *  inside .overlay, most with role=dialog), or any text field.
 *
 *  Focus moves here are deferred by two animation frames, and connecting is
 *  asynchronous on top of that, so the world can change before they land -
 *  typically the user hits Ctrl+K again while a session is still coming up.
 *  Taking the keyboard then types into a terminal they are not looking at.
 */
export function userIsTypingElsewhere(
  active: HTMLElement | null = typeof document === "undefined"
    ? null
    : (document.activeElement as HTMLElement | null),
): boolean {
  // An open palette outranks whatever the DOM says: it may not have
  // focused its input yet, and a terminal must not race it to the
  // keyboard.
  if (keyboardIsClaimed()) return true;
  if (!active) return false;
  // document is guarded so this stays callable without a DOM (unit tests,
  // and any future non-browser consumer); body is only reachable when there
  // is one.
  if (typeof document !== "undefined" && active === document.body) return false;
  if (active.closest?.('[role="dialog"], .overlay')) return true;
  const tag = active.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  return active.isContentEditable;
}

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
      // A dialog holding the keyboard means wait, not stop - the palette
      // that triggered this closes a moment later, and nothing else would
      // ask again. Same reasoning as Terminal.focusWhenVisible.
      if (keyboardIsClaimed()) {
        onKeyboardReleased(() => focusActivePane());
        return;
      }
      // Re-checked inside the frames, not before them: the user may have
      // clicked into a text field while we were waiting.
      if (userIsTypingElsewhere()) return;
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
