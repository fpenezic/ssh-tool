import { describe, it, expect } from "vitest";
import { mayDropScrollback, mayReplayOnRestore } from "./bgScrollbackPolicy";

describe("mayDropScrollback", () => {
  it("allows the drop for ordinary shell output", () => {
    expect(mayDropScrollback("normal")).toBe(true);
  });

  // The reported corruption: a TUI (Claude Code) in a tab left in the
  // background had its scrollback dropped, and the reset+replay on return
  // replayed cursor positioning into the wrong buffer.
  it("refuses while a full-screen TUI owns the terminal", () => {
    expect(mayDropScrollback("alternate")).toBe(false);
  });

  it("allows the drop when the buffer type is unknown", () => {
    // Undefined means an xterm version without the field; the previous
    // behaviour (always drop) is the safe default for memory, and the
    // corruption case is specific to a known alternate screen.
    expect(mayDropScrollback(undefined)).toBe(true);
  });
});

describe("mayReplayOnRestore", () => {
  it("replays for a normal buffer", () => {
    expect(mayReplayOnRestore("normal")).toBe(true);
  });

  // A TUI can start AFTER the drop, while the tab is still backgrounded: the
  // drop was legitimate, the replay would not be.
  it("refuses to replay into an alternate screen", () => {
    expect(mayReplayOnRestore("alternate")).toBe(false);
  });
});

describe("the two rules together", () => {
  // Whatever is true of dropping must be true of replaying, or a terminal
  // can end up dropped-but-never-restored.
  it("never allows a replay where a drop was refused", () => {
    for (const b of ["normal", "alternate", undefined] as const) {
      if (!mayDropScrollback(b)) {
        expect(mayReplayOnRestore(b)).toBe(false);
      }
    }
  });
});

import { terminalHasScrollback } from "./bgScrollbackPolicy";

describe("terminalHasScrollback", () => {
  it("is false for a terminal showing only its viewport", () => {
    // The measured case: a TUI redrawing in place sat at exactly the
    // viewport height while the ring grew from 303 to 711 lines. Nothing to
    // scroll back to, so nothing to lose by dropping.
    expect(terminalHasScrollback(69, 70)).toBe(false);
    expect(terminalHasScrollback(70, 70)).toBe(false);
  });

  it("is true once the terminal holds more than fits on screen", () => {
    // The session that lost history: 826 lines against a 70-row viewport.
    expect(terminalHasScrollback(826, 70)).toBe(true);
    expect(terminalHasScrollback(71, 70)).toBe(true);
  });

  it("treats an unknown viewport as any content being history", () => {
    expect(terminalHasScrollback(10, 0)).toBe(true);
    expect(terminalHasScrollback(0, 0)).toBe(false);
  });
});
