import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { dropActionFor, shouldHoldScrollback, DropScheduler, BG_SCROLLBACK_OFF } from "./bgScrollback";

describe("dropActionFor", () => {
  it("treats a negative delay as 'never release'", () => {
    expect(dropActionFor(BG_SCROLLBACK_OFF)).toEqual({ kind: "never" });
    expect(dropActionFor(-5)).toEqual({ kind: "never" });
  });

  // The bug this guards: collapsing -1 and 0 into "falsy" would make
  // "disabled" behave as "release immediately" - the exact opposite.
  it("keeps 0 distinct from -1", () => {
    expect(dropActionFor(0)).toEqual({ kind: "now" });
    expect(dropActionFor(0)).not.toEqual(dropActionFor(-1));
  });

  it("converts a positive delay to milliseconds", () => {
    expect(dropActionFor(15)).toEqual({ kind: "after", ms: 15000 });
    expect(dropActionFor(600)).toEqual({ kind: "after", ms: 600000 });
    expect(dropActionFor(1)).toEqual({ kind: "after", ms: 1000 });
  });

  it("falls back to the default rather than releasing on a bad value", () => {
    expect(dropActionFor(NaN)).toEqual({ kind: "after", ms: 15000 });
  });
});

describe("shouldHoldScrollback", () => {
  // Until the observer reports, doing anything races the mount's snapshot
  // fetch - that is what made every newly opened tab print twice.
  it("returns null while visibility is unknown", () => {
    expect(shouldHoldScrollback(true, null)).toBeNull();
    expect(shouldHoldScrollback(false, null)).toBeNull();
  });

  it("holds only when the pane is both active and on screen", () => {
    expect(shouldHoldScrollback(true, true)).toBe(true);
  });

  // The hidden-split case: active stays true for every pane inside a split
  // that is not being shown, so active alone would never release anything.
  it("releases an active pane that is off screen", () => {
    expect(shouldHoldScrollback(true, false)).toBe(false);
  });

  it("releases an inactive pane", () => {
    expect(shouldHoldScrollback(false, true)).toBe(false);
    expect(shouldHoldScrollback(false, false)).toBe(false);
  });
});

// The guarantee that matters most in practice: a tab must keep its
// scrollback for the whole grace period. If this regresses, users lose
// history they can still see on screen, which is worse than the memory the
// feature saves.
describe("DropScheduler timing", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("does NOT drop before the delay elapses", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 15);
    s.schedule();

    vi.advanceTimersByTime(14_999);
    expect(drops).toBe(0);
    expect(s.isDropped).toBe(false);
    expect(s.isPending).toBe(true);
  });

  it("drops exactly when the delay elapses", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 15);
    s.schedule();

    vi.advanceTimersByTime(15_000);
    expect(drops).toBe(1);
    expect(s.isDropped).toBe(true);
    expect(s.isPending).toBe(false);
  });

  it("cancels the drop when the tab comes back in time", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 15);
    s.schedule();
    vi.advanceTimersByTime(10_000);

    // Returning within the grace period: nothing was released, so no replay.
    expect(s.restore()).toBe(false);
    vi.advanceTimersByTime(60_000);
    expect(drops).toBe(0);
  });

  it("reports that a replay is needed only after an actual drop", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 15);
    s.schedule();
    vi.advanceTimersByTime(15_000);
    expect(drops).toBe(1);

    expect(s.restore()).toBe(true);
    expect(s.isDropped).toBe(false);
  });

  // Flipping back and forth must never accumulate timers or drop early.
  it("survives repeated hide/show cycles without dropping", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 15);
    for (let i = 0; i < 20; i++) {
      s.schedule();
      vi.advanceTimersByTime(5_000);
      s.restore();
      vi.advanceTimersByTime(1_000);
    }
    expect(drops).toBe(0);
    expect(s.isPending).toBe(false);
  });

  it("never drops at all when disabled", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => BG_SCROLLBACK_OFF);
    s.schedule();
    vi.advanceTimersByTime(3_600_000);
    expect(drops).toBe(0);
    expect(s.isPending).toBe(false);
  });

  it("drops synchronously when the delay is zero", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 0);
    s.schedule();
    expect(drops).toBe(1);
  });

  it("does not stack timers when schedule is called repeatedly", () => {
    let drops = 0;
    const s = new DropScheduler(() => drops++, () => 15);
    s.schedule();
    s.schedule();
    s.schedule();
    vi.advanceTimersByTime(15_000);
    expect(drops).toBe(1);
  });

  it("honours a delay changed between cycles", () => {
    let drops = 0;
    let delay = 15;
    const s = new DropScheduler(() => drops++, () => delay);
    s.schedule();
    vi.advanceTimersByTime(15_000);
    expect(drops).toBe(1);
    s.restore();

    delay = 60;
    s.schedule();
    vi.advanceTimersByTime(59_000);
    expect(drops).toBe(1);
    vi.advanceTimersByTime(1_000);
    expect(drops).toBe(2);
  });
});
