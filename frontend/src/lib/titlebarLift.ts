// Compact title bar: a modal's backdrop covers the whole window, title bar
// included, so the window could no longer be moved, snapped or maximised
// while one is open - the native frame never has that problem. While
// something covers the bar's band, the bar is lifted above it (class
// "lifted": the component raises it over every overlay and lays a shield
// over its own controls, so the tabs stay out of reach of the modal but
// the bar still drags). Covered means an element at the top centre of the
// window that is neither the bar, inside it nor one of its ancestors.
// Modals come and go without a common hook, so any DOM change re-checks:
// a MutationObserver callback runs before the next paint, so the bar is
// never seen dimmed first. A pointer move in the band checks too, and a
// slow timer catches what changes without a mutation (a CSS transition).
export function titlebarLift(node: HTMLElement, active: boolean) {
  let timer: ReturnType<typeof setInterval> | null = null;
  let observer: MutationObserver | null = null;

  function covered(): boolean {
    const y = Math.min(4, node.offsetHeight - 1);
    return document.elementsFromPoint(window.innerWidth / 2, y)
      .some((el) => !node.contains(el) && !el.contains(node));
  }
  function check() {
    const on = covered();
    // Only a real change touches the class: the observer watches class
    // attributes, so toggling it every time would feed itself.
    if (on !== node.classList.contains("lifted")) node.classList.toggle("lifted", on);
  }
  function onMove(e: PointerEvent) {
    if (e.clientY < node.offsetHeight) check();
  }
  function start() {
    if (timer) return;
    timer = setInterval(check, 1000);
    observer = new MutationObserver(check);
    observer.observe(document.body, {
      childList: true, subtree: true,
      attributes: true, attributeFilter: ["class", "hidden", "open"],
    });
    window.addEventListener("pointermove", onMove, true);
    check();
  }
  function stop() {
    if (timer) clearInterval(timer);
    timer = null;
    observer?.disconnect();
    observer = null;
    window.removeEventListener("pointermove", onMove, true);
    node.classList.remove("lifted");
  }

  if (active) start();
  return {
    update(next: boolean) { if (next) start(); else stop(); },
    destroy: stop,
  };
}
