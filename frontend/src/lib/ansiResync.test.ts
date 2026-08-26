import { describe, it, expect } from "vitest";
import { safeResyncOffset, resyncAnsi } from "./ansiResync";

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

describe("safeResyncOffset", () => {
  it("leaves plain text alone", () => {
    expect(safeResyncOffset(enc("hello world\n"))).toBe(0);
  });

  it("leaves a buffer that already starts with ESC alone", () => {
    expect(safeResyncOffset(enc("\x1b[2Jrest"))).toBe(0);
  });

  it("handles an empty buffer", () => {
    expect(safeResyncOffset(new Uint8Array(0))).toBe(0);
  });

  // The reported failure: the ring trimmed mid-sequence, so the replay began
  // with the tail of one. Writing that feeds xterm half an escape.
  it("skips the tail of a chopped CSI sequence", () => {
    // "\x1b[32m" cut after the ESC[ - the replay starts at "32m".
    const out = resyncAnsi(enc("32mgreen text"));
    expect(dec(out)).toBe("green text");
  });

  // A LONE final byte is deliberately NOT trimmed. "\x1b[2J" chopped to "J"
  // is indistinguishable from a line that simply starts with a capital J,
  // and eating real output is the worse failure of the two. One stray escape
  // costs a glitched cell; a swallowed line costs data.
  it("leaves a lone final byte alone - it is ambiguous with plain text", () => {
    expect(dec(resyncAnsi(enc("Jrest of screen")))).toBe("Jrest of screen");
  });

  it("skips the tail of a chopped OSC string", () => {
    // "\x1b]0;title\x07" cut to "0;title\x07".
    expect(dec(resyncAnsi(enc("0;title\x07after")))).toBe("after");
  });
});

describe("resyncAnsi - must not eat legitimate content", () => {
  // The dangerous failure mode is the opposite one: trimming text that was
  // never part of a sequence. These pin that it does not.
  it("keeps text containing letters in the CSI final range", () => {
    expect(dec(resyncAnsi(enc("Just a normal line")))).toBe("Just a normal line");
  });

  // Digits and '-' are CSI parameter/intermediate bytes, so a date-led log
  // line looks exactly like a chopped sequence until the space. This is the
  // ambiguity the trimmer must resolve in favour of keeping the text: a
  // timestamped log line is one of the most common things in a terminal.
  it("keeps a date-led log line, which mimics a chopped sequence", () => {
    const s = "2026-08-26 log line";
    expect(dec(resyncAnsi(enc(s)))).toBe(s);
  });

  it("keeps text that merely contains an escape later on", () => {
    const s = "prefix \x1b[31m red";
    expect(dec(resyncAnsi(enc(s)))).toBe(s);
  });

  it("keeps a plain newline-led buffer", () => {
    expect(dec(resyncAnsi(enc("\nnext line")))).toBe("\nnext line");
  });

  it("returns the same object when nothing needs trimming", () => {
    const b = enc("unchanged");
    expect(resyncAnsi(b)).toBe(b);
  });
});

describe("resyncAnsi - realistic TUI fragments", () => {
  // What a chopped Claude Code / htop frame actually looks like: the ring cut
  // inside a cursor-position sequence, leaving parameters and a final byte.
  it("resyncs a cut cursor-position sequence", () => {
    expect(dec(resyncAnsi(enc("12;40Hcontent")))).toBe("content");
  });

  it("resyncs a cut scroll-region sequence", () => {
    expect(dec(resyncAnsi(enc("1;24rmore")))).toBe("more");
  });

  // Box-drawing is the visible casualty in the report; it must survive.
  it("keeps box-drawing output intact", () => {
    const s = "├───────────────┼───────────────┤";
    expect(dec(resyncAnsi(enc(s)))).toBe(s);
  });
});
