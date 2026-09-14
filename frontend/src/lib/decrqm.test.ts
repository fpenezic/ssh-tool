import { describe, it, expect } from "vitest";
import { DecrqmStripper } from "./decrqm";

const enc = new TextEncoder();
const dec = new TextDecoder();

/** Feeds a byte string through a single stripper, split at `cuts`. */
function feed(input: string, cuts: number[] = []): string {
  const bytes = enc.encode(input);
  const s = new DecrqmStripper();
  const parts: number[] = [];
  const points = [0, ...cuts, bytes.length];
  for (let i = 0; i < points.length - 1; i++) {
    const out = s.push(bytes.subarray(points[i], points[i + 1]));
    parts.push(...out);
  }
  parts.push(...s.flush());
  return dec.decode(new Uint8Array(parts));
}

describe("DecrqmStripper", () => {
  it("passes plain text through untouched", () => {
    expect(feed("hello world")).toBe("hello world");
  });

  it("strips a DECRQM query in one chunk", () => {
    expect(feed("HELLO\x1b[?2026$pWORLD")).toBe("HELLOWORLD");
  });

  it("strips DECRPM ($y) as well", () => {
    expect(feed("A\x1b[?1049$yB")).toBe("AB");
  });

  // 0x9b is a raw byte on the wire, not a codepoint: TextEncoder would
  // turn it into the two-byte UTF-8 encoding of U+009B, which is not what
  // a PTY sends. Build this one by hand.
  it("strips the 8-bit CSI form", () => {
    const s = new DecrqmStripper();
    const bytes = new Uint8Array([0x41, 0x9b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x24, 0x70, 0x42]);
    expect(dec.decode(s.push(bytes))).toBe("AB");
  });

  // The regression this class exists for: the old stateless strip leaked
  // the sequence through whenever a chunk boundary fell inside it.
  it("strips the sequence no matter where the chunk boundary falls", () => {
    const input = "HELLO\x1b[?2026$pWORLD";
    for (let cut = 1; cut < enc.encode(input).length; cut++) {
      expect(feed(input, [cut]), `cut=${cut}`).toBe("HELLOWORLD");
    }
  });

  it("survives a sequence split into single-byte chunks", () => {
    const input = "A\x1b[?2026$pB";
    const cuts = Array.from({ length: enc.encode(input).length - 1 }, (_, i) => i + 1);
    expect(feed(input, cuts)).toBe("AB");
  });

  it("keeps non-DECRQM CSI sequences intact", () => {
    expect(feed("\x1b[31mred\x1b[0m")).toBe("\x1b[31mred\x1b[0m");
    expect(feed("\x1b[2J\x1b[H")).toBe("\x1b[2J\x1b[H");
  });

  it("keeps a private-mode set/reset, which shares the '?' prefix", () => {
    expect(feed("\x1b[?25l")).toBe("\x1b[?25l");
    expect(feed("\x1b[?1049h")).toBe("\x1b[?1049h");
  });

  it("keeps private-mode sequences split across chunks", () => {
    const input = "\x1b[?25lTEXT";
    for (let cut = 1; cut < enc.encode(input).length; cut++) {
      expect(feed(input, [cut]), `cut=${cut}`).toBe(input);
    }
  });

  it("replays a lone trailing ESC via flush", () => {
    expect(feed("abc\x1b")).toBe("abc\x1b");
  });

  it("does not swallow a doubled ESC", () => {
    expect(feed("\x1b\x1b[?25l")).toBe("\x1b\x1b[?25l");
  });

  it("gives back an over-long parameter run instead of buffering it", () => {
    const long = "\x1b[?" + "1".repeat(200) + "$p";
    // Not a plausible DECRQM: the scanner bails and emits rather than
    // holding the stream hostage. The important part is that nothing is
    // lost.
    expect(feed(long)).toBe(long);
  });

  it("handles several sequences and text in one chunk", () => {
    expect(feed("a\x1b[?2026$pb\x1b[?1$yc")).toBe("abc");
  });

  it("handles back-to-back sequences split at the join", () => {
    const input = "\x1b[?2026$p\x1b[?1$y";
    for (let cut = 1; cut < enc.encode(input).length; cut++) {
      expect(feed(input, [cut]), `cut=${cut}`).toBe("");
    }
  });

  it("is reusable after flush", () => {
    const s = new DecrqmStripper();
    s.push(enc.encode("x\x1b"));
    expect(dec.decode(s.flush())).toBe("\x1b");
    expect(dec.decode(s.push(enc.encode("\x1b[?2026$pY")))).toBe("Y");
  });

  it("does not corrupt UTF-8 multi-byte text", () => {
    const input = "čćž漢字🚀\x1b[?2026$pđš";
    expect(feed(input)).toBe("čćž漢字🚀đš");
  });
});
