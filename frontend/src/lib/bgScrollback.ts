// Decision logic for releasing a background tab's scrollback, extracted so
// it can be tested without a live xterm. Terminal.svelte owns the effects
// and the timer; this owns what the setting MEANS.

export const BG_SCROLLBACK_OFF = -1;

export type DropAction =
  | { kind: "never" }              // feature disabled
  | { kind: "now" }                // release as soon as the tab is hidden
  | { kind: "after"; ms: number }; // release after a grace period

// The three ranges are distinct and all meaningful, which is exactly why
// this is not a boolean and why nothing may treat -1 and 0 as "falsy":
// -1 keeps every buffer (pre-v0.85 behaviour), 0 releases immediately,
// N waits N seconds so flipping between two tabs costs nothing.
export function dropActionFor(delaySeconds: number): DropAction {
  if (!Number.isFinite(delaySeconds)) return { kind: "after", ms: 15000 };
  if (delaySeconds < 0) return { kind: "never" };
  if (delaySeconds === 0) return { kind: "now" };
  return { kind: "after", ms: delaySeconds * 1000 };
}

// Whether a pane should be holding its scrollback right now. `active` alone
// is not enough: it means "belongs to the active tab", so every pane inside
// a hidden split still reports true. onScreen === null means the visibility
// observer has not reported yet, and until it does we must not touch the
// buffer - doing so during mount raced the snapshot fetch and printed
// everything twice.
export function shouldHoldScrollback(
  active: boolean,
  onScreen: boolean | null,
): boolean | null {
  if (onScreen === null) return null;
  return active && onScreen;
}

// A scheduler for the drop, extracted from Terminal.svelte so the timing
// guarantee is testable: the buffer must NOT be released before the grace
// period elapses, and returning to the tab within it must cancel the drop
// entirely.
export class DropScheduler {
  private timer: ReturnType<typeof setTimeout> | null = null;
  private dropped = false;

  constructor(
    private readonly onDrop: () => void,
    private readonly delaySeconds: () => number,
  ) {}

  get isDropped(): boolean {
    return this.dropped;
  }
  get isPending(): boolean {
    return this.timer !== null;
  }

  // Called when the pane stops being visible.
  schedule(): void {
    if (this.dropped || this.timer !== null) return;
    const action = dropActionFor(this.delaySeconds());
    if (action.kind === "never") return;
    if (action.kind === "now") {
      this.fire();
      return;
    }
    this.timer = setTimeout(() => {
      this.timer = null;
      this.fire();
    }, action.ms);
  }

  // Called when the pane becomes visible again. Returns true when the tab
  // needs its buffer replayed - i.e. the drop had already happened. A return
  // within the grace period returns false: nothing was released, so the
  // switch costs nothing and must not trigger a replay.
  restore(): boolean {
    this.cancel();
    if (!this.dropped) return false;
    this.dropped = false;
    return true;
  }

  cancel(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private fire(): void {
    this.dropped = true;
    this.onDrop();
  }
}

// Caps how many background terminals keep their renderer during the grace
// period. Each one holds a WebGL context (when WebGL is on), and WebKit
// allows only about 16 per page: Ctrl+Tab through a row of tabs inside the
// grace period would otherwise lose the oldest contexts, and a terminal
// whose context is lost falls back to the canvas renderer for good. Past
// the cap the least recently hidden terminal is suspended at once.
export class RendererKeep {
  private kept: Array<() => void> = [];

  constructor(private readonly max: number) {}

  // A terminal went to the background still holding its renderer;
  // `suspend` drops it.
  add(suspend: () => void): void {
    this.remove(suspend);
    this.kept.push(suspend);
    while (this.kept.length > this.max) this.kept.shift()!();
  }

  // The terminal came back, was suspended by its own timer, or is gone.
  remove(suspend: () => void): void {
    const i = this.kept.indexOf(suspend);
    if (i >= 0) this.kept.splice(i, 1);
  }
}

export const backgroundRenderers = new RendererKeep(6);
