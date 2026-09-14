import { describe, it, expect } from "vitest";
import { userIsTypingElsewhere } from "./paneFocus";

// Connecting is asynchronous, and focus moves are deferred on top of that,
// so by the time a terminal is ready to take the keyboard the user may have
// moved on. The reported symptom: open a connection from the Ctrl+K palette,
// press Ctrl+K again, start typing - and the keystrokes land in the terminal
// that just finished connecting in the background.
//
// No jsdom in this project (every other suite is dependency-free), so the
// few DOM bits the function touches are stubbed by hand: closest(), tagName
// and isContentEditable. Keep the stub honest - closest() must answer for
// the element's ancestors, which is exactly what the real one does.
type Stub = {
  tagName: string;
  isContentEditable?: boolean;
  /** selectors this element or an ancestor matches */
  matches?: string[];
};

function el(s: Stub): HTMLElement {
  return {
    tagName: s.tagName,
    isContentEditable: s.isContentEditable ?? false,
    closest: (sel: string) => ((s.matches ?? []).includes(sel) ? ({} as Element) : null),
  } as unknown as HTMLElement;
}

const DIALOG_SEL = '[role="dialog"], .overlay';

describe("userIsTypingElsewhere", () => {
  it("says no when nothing holds focus", () => {
    expect(userIsTypingElsewhere(null)).toBe(false);
  });

  it("protects an input inside the quick palette", () => {
    expect(userIsTypingElsewhere(el({ tagName: "INPUT", matches: [DIALOG_SEL] }))).toBe(true);
  });

  // Modals are matched by their container rather than by a list of
  // component names, so a new dialog is covered without touching this code.
  // A plain button inside one counts: Enter on it must not be swallowed.
  it("protects anything inside a dialog, including a plain button", () => {
    expect(userIsTypingElsewhere(el({ tagName: "BUTTON", matches: [DIALOG_SEL] }))).toBe(true);
  });

  it("protects text fields outside any dialog", () => {
    for (const tagName of ["INPUT", "TEXTAREA", "SELECT"]) {
      expect(userIsTypingElsewhere(el({ tagName })), tagName).toBe(true);
    }
    expect(userIsTypingElsewhere(el({ tagName: "DIV", isContentEditable: true }))).toBe(true);
  });

  // The case the guard must NOT block - the normal path, where focus sits
  // on the Connect button and the new terminal is meant to take it.
  it("allows the steal from an ordinary button", () => {
    expect(userIsTypingElsewhere(el({ tagName: "BUTTON" }))).toBe(false);
  });

  // xterm's focus target is a TEXTAREA, so it trips the text-field branch.
  // That is correct for the reported bug (do not yank focus out of a shell
  // someone is typing in) and does not break switching between terminals,
  // which goes through focusActivePane's own path rather than this guard.
  it("treats another terminal's xterm textarea as typing", () => {
    expect(userIsTypingElsewhere(el({ tagName: "TEXTAREA" }))).toBe(true);
  });
});
