// Mobile system-back handling.
//
// Android routes its hardware/gesture Back to WebView.goBack() (see
// MainActivity.onBackPressed). A single-page app has no browser history by
// default, so canGoBack() is always false and Back kills the app from any
// screen. To make Back step back through the UI (detail -> list, or a
// secondary tab -> connections) we keep one synthetic history entry present
// whenever there is somewhere to go back to. goBack() then consumes it and
// fires popstate without a reload (same-document pushState entry), and we run
// the registered back action. At the UI root no entry is present, so Back
// falls through to the default (exit) - the expected behaviour.
//
// The model is intentionally sync-with-state rather than an imperative stack:
// the consumer reports, via canGoBack(), whether a back step is currently
// meaningful, and provides goBack() to perform one step. We mirror that into
// exactly one pushed entry. This avoids the desync that an action stack hits
// when state changes through paths other than the Back button.

let installed = false;
let entryPushed = false;
let getCanGoBack: () => boolean = () => false;
let doGoBack: () => void = () => {};
// Guard so the popstate we trigger ourselves (re-arming the entry) is not
// mistaken for a user Back press.
let rearming = false;

const MARKER = { sshToolBack: true };

function syncEntry() {
  const want = getCanGoBack();
  if (want && !entryPushed) {
    history.pushState(MARKER, "");
    entryPushed = true;
  }
  // When `want` is false we deliberately leave any pushed entry in place: the
  // only way it should disappear is by being consumed via popstate (Back) or
  // when the user actually navigates back through it. Re-pushing/clobbering
  // here would desync the depth. The popstate handler clears entryPushed.
}

function onPopState() {
  if (rearming) {
    rearming = false;
    return;
  }
  // Our synthetic entry was consumed by a Back press.
  entryPushed = false;
  if (getCanGoBack()) {
    // There is a UI back step to take. Perform it, then re-arm an entry so
    // the next Back is caught too. Re-pushing fires no popstate, so no guard
    // needed for the push itself.
    doGoBack();
    if (getCanGoBack()) {
      history.pushState(MARKER, "");
      entryPushed = true;
    }
  }
  // If there's nothing to go back to, the entry is already gone; the next
  // Back press will fall through to the system default (exit).
}

// installMobileBackNav wires the popstate listener and returns a tick()
// function the app calls (from a reactive $effect) whenever the
// can-go-back condition may have changed, so a fresh entry gets armed.
export function installMobileBackNav(opts: {
  canGoBack: () => boolean;
  goBack: () => void;
}): { tick: () => void; dispose: () => void } {
  getCanGoBack = opts.canGoBack;
  doGoBack = opts.goBack;
  if (!installed) {
    window.addEventListener("popstate", onPopState);
    installed = true;
  }
  return {
    tick: syncEntry,
    dispose: () => {
      window.removeEventListener("popstate", onPopState);
      installed = false;
      entryPushed = false;
      rearming = false;
    },
  };
}
