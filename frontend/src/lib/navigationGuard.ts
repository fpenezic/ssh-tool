// Keeps the app window on the app.
//
// The WebView hosting this app is a browser, so anything that triggers a
// top-level navigation replaces the entire UI with a remote page - and
// there is no back button, no address bar, and no way out short of
// restarting. It has to be treated as a crash, not a navigation.
//
// This is reachable in ordinary use. A terminal pane renders whatever the
// remote process prints, and an OAuth login link that failed to open in
// the real browser left the WebView offering to navigate the app window
// to the login page instead. A link element, a form post, or a stray
// window.location assignment would do the same.
//
// The rule: same-origin navigation is the app (the router, a reload);
// anything else belongs in the user's real browser. We intercept in the
// capture phase, before any other handler, and hand the URL to the Go
// side.
//
// Note this cannot catch a navigation the embedder itself initiates (the
// WebView2 "do you want to navigate" prompt is one - it is the host
// asking, outside the page). It covers everything originating in the
// page, which is where our own bugs and any injected content live.

type OpenExternal = (url: string) => void;

function isExternal(href: string): boolean {
  let target: URL;
  try {
    target = new URL(href, location.href);
  } catch {
    // Unparseable - not something we can route anywhere, and not
    // something we want the WebView to try either.
    return true;
  }
  // about:blank and javascript: never navigate the top window in a way
  // that loses the app; leave them to the page.
  if (target.protocol === "about:" || target.protocol === "javascript:") {
    return false;
  }
  if (target.protocol === "blob:" || target.protocol === "data:") {
    // These would replace the document too, but they carry no origin to
    // compare. Nothing in this app navigates to one; treat as external
    // so it is refused rather than silently allowed.
    return true;
  }
  return target.origin !== location.origin;
}

/** installNavigationGuard intercepts page-initiated navigation away from
 *  the app and routes it to the system browser instead.
 *
 *  `openExternal` receives URLs that should open outside the app.
 *  Returns a teardown function.
 */
export function installNavigationGuard(openExternal: OpenExternal): () => void {
  // Anchor clicks. Capture phase so this runs before component handlers,
  // and before the default action.
  const onClick = (ev: MouseEvent) => {
    // Let modified clicks through untouched except for the navigation
    // itself: the browser's own "open in new window" has nowhere to go
    // here anyway.
    const el = (ev.target as HTMLElement | null)?.closest?.("a");
    if (!el) return;
    const href = el.getAttribute("href");
    if (!href) return;
    if (!isExternal(href)) return;
    ev.preventDefault();
    ev.stopPropagation();
    openExternal(new URL(href, location.href).toString());
  };

  // Form submissions to a remote action replace the document just as a
  // link does.
  const onSubmit = (ev: SubmitEvent) => {
    const form = ev.target as HTMLFormElement | null;
    if (!form) return;
    const action = form.getAttribute("action");
    if (!action) return;
    if (!isExternal(action)) return;
    ev.preventDefault();
    ev.stopPropagation();
  };

  // Last line of defence: if something still manages to start a
  // navigation, this at least records it. Cannot cancel without a
  // confirmation dialog (browsers require a user gesture), and a dialog
  // the user cannot act on usefully is worse than a log line.
  const onBeforeUnload = () => {
    console.warn("[nav] the app window is navigating away");
  };

  document.addEventListener("click", onClick, true);
  document.addEventListener("submit", onSubmit, true);
  window.addEventListener("beforeunload", onBeforeUnload);

  return () => {
    document.removeEventListener("click", onClick, true);
    document.removeEventListener("submit", onSubmit, true);
    window.removeEventListener("beforeunload", onBeforeUnload);
  };
}

// Exported for tests: the origin comparison is the whole security
// decision here, so it is worth pinning independently of the DOM wiring.
export const __test = { isExternal };
