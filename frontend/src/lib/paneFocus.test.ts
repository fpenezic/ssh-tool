import { describe, it, expect, afterEach } from "vitest";
import {
  userIsTypingElsewhere,
  claimKeyboard,
  keyboardIsClaimed,
  __resetKeyboardClaims,
} from "./paneFocus";

afterEach(() => __resetKeyboardClaims());

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

// The DOM check answers "who holds the keyboard right now", which is not
// the same as "is the user busy with a palette". A palette focuses its
// input in an effect, and closes before the connection it started is
// dialled, so a terminal finishing later saw a clean document and took
// the keyboard - correct in isolation, wrong when the user had already
// pressed Ctrl+K for the next host. Ownership is therefore declared.
describe("keyboard ownership", () => {
  it("starts unclaimed", () => {
    expect(keyboardIsClaimed()).toBe(false);
  });

  it("blocks a focus steal while a palette is open, whatever the DOM says", () => {
    const release = claimKeyboard();
    // Focus sitting on an ordinary button would normally allow the steal.
    expect(userIsTypingElsewhere(el({ tagName: "BUTTON" }))).toBe(true);
    // Even with nothing focused at all - the gap between a palette
    // mounting and focusing its input.
    expect(userIsTypingElsewhere(null)).toBe(true);
    release();
    expect(userIsTypingElsewhere(el({ tagName: "BUTTON" }))).toBe(false);
  });

  // A modal opened from a palette: the inner one closing must not hand
  // the keyboard to a terminal while the outer one is still up.
  it("counts nested owners", () => {
    const outer = claimKeyboard();
    const inner = claimKeyboard();
    inner();
    expect(keyboardIsClaimed()).toBe(true);
    outer();
    expect(keyboardIsClaimed()).toBe(false);
  });

  // Svelte can run an effect's cleanup more than once; a second release
  // must not decrement another component's claim.
  it("ignores a repeated release", () => {
    const a = claimKeyboard();
    const b = claimKeyboard();
    a();
    a();
    a();
    expect(keyboardIsClaimed()).toBe(true);
    b();
    expect(keyboardIsClaimed()).toBe(false);
  });

  it("never drops below zero", () => {
    const release = claimKeyboard();
    release();
    release();
    expect(keyboardIsClaimed()).toBe(false);
    // A later claim still works rather than starting from a negative count.
    const again = claimKeyboard();
    expect(keyboardIsClaimed()).toBe(true);
    again();
    expect(keyboardIsClaimed()).toBe(false);
  });
});
