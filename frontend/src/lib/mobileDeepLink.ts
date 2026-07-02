// Android ssh-tool:// deep-link delivery.
//
// MainActivity captures a ssh-tool:// URL from the launch Intent (cold start)
// or onNewIntent (warm), stashes it on the bridge, and exposes it to JS via
// window.wails.takeDeepLink(). We drain it here and re-dispatch it as the
// `deep_link_import` Wails event - the same event the desktop deep-link path
// emits, so App.svelte's existing handler picks it up unchanged.
//
// Two triggers: once at startup (cold launch via a link), and on the
// 'android-deep-link' window event that the bridge fires from onNewIntent
// (warm launch while the app is already open).

import { isAndroid } from "./platform";

function drainOnce() {
  try {
    const take = (window as any).wails?.takeDeepLink;
    if (typeof take !== "function") return;
    const url: string = take();
    if (!url) return;
    const dispatch = (window as any)._wails?.dispatchWailsEvent;
    if (typeof dispatch === "function") {
      dispatch({ name: "deep_link_import", data: url });
    }
  } catch (e) {
    console.warn("deep-link drain failed", e);
  }
}

export function startMobileDeepLink() {
  if (!isAndroid) return;
  // Warm launch: the Activity fires this when onNewIntent stashes a link.
  window.addEventListener("android-deep-link", drainOnce);
  // Cold launch: drain once the runtime/event system is up. A small delay
  // lets dispatchWailsEvent + App.svelte's EventsOn handler be registered
  // first, so the event isn't dispatched into the void.
  setTimeout(drainOnce, 1200);
}
