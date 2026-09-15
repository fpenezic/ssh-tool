// Fast tooltips for icon toolbars.
//
// The native `title` attribute is the right default almost everywhere, but
// its show delay belongs to the OS/WebView (roughly a second on Windows
// WebView2) and no attribute or stylesheet shortens it. A toolbar of
// unlabelled icons - copy host, copy password, tcpdump, LLM share - is
// exactly the case where that second is too long: the icon is the only
// affordance, so the user is waiting on the tooltip to know what the button
// even does.
//
// This replaces it for opted-in containers only. `use:tooltipGroup` on an
// element delegates over its descendants: any child carrying `title` gets a
// custom tooltip after SHOW_DELAY instead. Delegation is deliberate - the
// session toolbar alone has 28 buttons, and a per-button action would have to
// be threaded through every one of them (plus every button added later).
//
// The `title` attribute is MOVED to data-tooltip while the pointer is inside
// the group and restored on the way out. Leaving it in place would show both
// tooltips, ours immediately and the native one a second later. Restoring it
// keeps assistive tech and any non-pointer path working: the DOM is back to
// what it was the moment the mouse leaves.

const SHOW_DELAY = 150;
// Once one tooltip in a group is up, moving along the row shows the next
// instantly. Re-waiting per icon makes scanning a toolbar feel broken. The
// group stays "warm" briefly after leaving so a pointer crossing a gap
// between buttons does not reset it.
const WARM_MS = 400;
const HIDE_DELAY = 60;

export interface TooltipState {
  text: string;
  x: number;
  y: number;
  /** Placement relative to the anchor; the host flips it when near an edge. */
  below: boolean;
}

class TooltipStore {
  current = $state<TooltipState | null>(null);

  private showTimer: ReturnType<typeof setTimeout> | null = null;
  private hideTimer: ReturnType<typeof setTimeout> | null = null;
  private warmUntil = 0;
  private anchor: HTMLElement | null = null;

  /** Whether a tooltip should appear with no delay right now. */
  private get warm(): boolean {
    return this.current !== null || Date.now() < this.warmUntil;
  }

  show(el: HTMLElement, text: string) {
    if (!text) return;
    if (this.anchor === el && this.current) return;
    this.anchor = el;
    this.clearTimers();
    const run = () => {
      this.showTimer = null;
      // The element can leave the DOM while the timer runs (a button that
      // hides once its capture stops, a pane that closes under the pointer).
      if (!el.isConnected) {
        this.hideNow();
        return;
      }
      const r = el.getBoundingClientRect();
      // Anchored to the icon's horizontal centre; the host clamps to the
      // viewport, which it can only do once it knows the tooltip's width.
      this.current = {
        text,
        x: r.left + r.width / 2,
        y: r.top,
        below: r.top < 48,
      };
    };
    if (this.warm) run();
    else this.showTimer = setTimeout(run, SHOW_DELAY);
  }

  hide() {
    this.clearTimers();
    // A short grace period: moving between two buttons fires leave on the
    // first before enter on the second, and hiding in between flickers.
    this.hideTimer = setTimeout(() => {
      this.hideTimer = null;
      this.hideNow();
    }, HIDE_DELAY);
  }

  /** Drop the tooltip immediately - a click, a scroll, a key. */
  hideNow() {
    this.clearTimers();
    if (this.current) this.warmUntil = Date.now() + WARM_MS;
    this.current = null;
    this.anchor = null;
  }

  private clearTimers() {
    if (this.showTimer) { clearTimeout(this.showTimer); this.showTimer = null; }
    if (this.hideTimer) { clearTimeout(this.hideTimer); this.hideTimer = null; }
  }
}

export const tooltips = new TooltipStore();

/** Reads the tooltip text of an element, wherever the text currently lives. */
function tipTextOf(el: HTMLElement): string {
  return el.getAttribute("title") || el.getAttribute("data-tooltip") || "";
}

/** Moves `title` out of the way so the native tooltip cannot also fire. */
function parkTitle(el: HTMLElement) {
  const t = el.getAttribute("title");
  if (t !== null) {
    el.setAttribute("data-tooltip", t);
    el.removeAttribute("title");
  }
}

/** Puts `title` back, leaving the DOM as we found it. */
function restoreTitle(el: HTMLElement) {
  const t = el.getAttribute("data-tooltip");
  if (t !== null) {
    el.setAttribute("title", t);
    el.removeAttribute("data-tooltip");
  }
}

/** The nearest ancestor (within the group) that carries tooltip text. */
function tipTargetOf(from: EventTarget | null, root: HTMLElement): HTMLElement | null {
  // Duck-typed rather than `instanceof HTMLElement`: the event can carry a
  // text node or the document, and in a detached window the constructor comes
  // from a different realm, where instanceof is false for a real element.
  let el = isEl(from) ? from : null;
  while (el && el !== root.parentElement) {
    if (el.hasAttribute("title") || el.hasAttribute("data-tooltip")) return el;
    el = el.parentElement;
  }
  return null;
}

function isEl(v: unknown): v is HTMLElement {
  return !!v && typeof (v as HTMLElement).hasAttribute === "function";
}

/**
 * Svelte action: fast tooltips for every `title`-carrying descendant.
 *
 * Pointer events only. A keyboard user tabbing through the toolbar keeps the
 * native behaviour, because focus never parks the `title` attribute.
 */
export function tooltipGroup(root: HTMLElement) {
  // Every element whose title we parked, so teardown can restore all of them
  // even if the pointer never left (the pane closed under it).
  const parked = new Set<HTMLElement>();

  const onOver = (e: MouseEvent) => {
    const el = tipTargetOf(e.target, root);
    if (!el) return;
    const text = tipTextOf(el);
    if (!text) return;
    parkTitle(el);
    parked.add(el);
    tooltips.show(el, text);
  };

  const onOut = (e: MouseEvent) => {
    const el = tipTargetOf(e.target, root);
    if (!el) return;
    // Ignore moves that stay inside the same element (child to parent).
    const to = e.relatedTarget;
    if (to && typeof el.contains === "function" && el.contains(to as Node)) return;
    restoreTitle(el);
    parked.delete(el);
    tooltips.hide();
  };

  // A click means the user knows what the button does; keeping the tooltip up
  // over the result of the action is just in the way.
  const onDown = () => tooltips.hideNow();

  // Coordinates are captured once and rendered position: fixed, so anything
  // that moves the anchor without a pointer event (a pane resize, the toolbar
  // scrolling, the window changing size) would leave the bubble behind. Drop
  // it instead of tracking - it reappears on the next hover anyway.
  const onDisplace = () => tooltips.hideNow();

  root.addEventListener("mouseover", onOver);
  root.addEventListener("mouseout", onOut);
  root.addEventListener("mousedown", onDown, true);
  window.addEventListener("scroll", onDisplace, true);
  window.addEventListener("resize", onDisplace);

  return {
    destroy() {
      root.removeEventListener("mouseover", onOver);
      root.removeEventListener("mouseout", onOut);
      root.removeEventListener("mousedown", onDown, true);
      window.removeEventListener("scroll", onDisplace, true);
      window.removeEventListener("resize", onDisplace);
      for (const el of parked) restoreTitle(el);
      parked.clear();
      tooltips.hideNow();
    },
  };
}

export const __test = { SHOW_DELAY, WARM_MS, HIDE_DELAY, tipTextOf, parkTitle, restoreTitle, tipTargetOf };
