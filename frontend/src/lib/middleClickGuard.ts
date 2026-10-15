// Stops WebKitGTK pasting the primary selection on a middle click that
// lands on app chrome.
//
// WebKitGTK runs PasteGlobalSelection on EVERY middle-button release, into
// whatever element holds focus at that moment - not the one under the
// pointer. Only preventDefault on mouseup suppresses it; the tab's auxclick
// preventDefault does not. Middle-clicking tabs closed is where this bit:
// closeTab awaits the SSH disconnect and then refocuses the surviving
// terminal, and when that focus lands between the next click's mousedown
// and mouseup the selection is pasted into the shell (and runs, if it ends
// in a newline). Remote sessions disconnect slowly enough to hit it.
//
// The rule: a middle release on an editable field keeps the native paste
// (the Linux convention); on a terminal the terminal's own handler owns
// middle-click; anywhere else it is a click on chrome, never a paste.

function wantsNativePaste(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  if (target.closest(".xterm")) return true;
  return !!target.closest("input, textarea, [contenteditable]:not([contenteditable='false'])");
}

export function installMiddleClickGuard(): () => void {
  const onMouseUp = (ev: MouseEvent) => {
    if (ev.button !== 1) return;
    if (wantsNativePaste(ev.target)) return;
    ev.preventDefault();
  };
  window.addEventListener("mouseup", onMouseUp, { capture: true });
  return () => window.removeEventListener("mouseup", onMouseUp, { capture: true });
}
