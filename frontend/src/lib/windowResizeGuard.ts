// Frameless windows (compact title bar, Windows and Linux) get their edge
// resize from the Wails runtime's drag.ts: it sets the resize cursor at the
// window edges and starts a native resize on drag. It only knows the
// window's resizable flag, not its state, so a maximised or fullscreen
// window still showed the resize cursor (Windows) or could be resized off
// its maximised size by dragging an edge (KDE / Wayland, issue #45).
//
// The runtime's switch is window._wails.setResizable. Wails itself calls it
// when the page loads (Linux WindowLoadFinished, Windows on creation), which
// can land after our first state check, so the switch is wrapped: the value
// Wails asks for is kept as the base and the runtime gets base && !locked.

type WailsGlobal = { setResizable?: (v: boolean) => void };

let installed = false;
let base = true;
let locked = false;
let apply: ((v: boolean) => void) | null = null;

function install(): void {
  if (installed) return;
  const w = (window as unknown as { _wails?: WailsGlobal })._wails;
  const orig = w?.setResizable;
  if (!w || typeof orig !== "function") return;
  installed = true;
  apply = orig;
  w.setResizable = (v: boolean) => {
    base = v;
    orig(v && !locked);
  };
}

/** Disable edge resize while the window is maximised or fullscreen. */
export function setEdgeResizeLocked(on: boolean): void {
  install();
  if (!apply) return;
  locked = on;
  apply(base && !locked);
}
