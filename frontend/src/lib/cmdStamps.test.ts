import { describe, expect, it } from "vitest";
import { isCommandInput, splitAtMarks } from "./cmdStamps";

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

describe("splitAtMarks", () => {
  // Snapshot "$ ls\r\na b\r\n$ pwd\r\n/root\r\n$ " ending at output byte 1000.
  const text = "$ ls\r\na b\r\n$ pwd\r\n/root\r\n$ ";
  const data = enc(text);
  const base = 1000 - data.length;

  it("cuts right after each command line", () => {
    const marks = [
      { cum: base + "$ ls".length, at: 1 },
      { cum: base + "$ ls\r\na b\r\n$ pwd".length, at: 2 },
    ];
    const segs = splitAtMarks(data, 1000, marks);
    expect(segs.map((s) => dec(s.bytes))).toEqual(["$ ls", "\r\na b\r\n$ pwd", "\r\n/root\r\n$ "]);
    expect(segs.map((s) => s.mark?.at)).toEqual([1, 2, undefined]);
    // Nothing lost or duplicated.
    expect(segs.map((s) => dec(s.bytes)).join("")).toBe(text);
  });

  it("ignores marks outside the snapshot", () => {
    const segs = splitAtMarks(data, 1000, [{ cum: base - 5, at: 1 }, { cum: 2000, at: 2 }]);
    expect(segs).toHaveLength(1);
    expect(dec(segs[0].bytes)).toBe(text);
  });

  it("keeps the later of two marks at one position", () => {
    const at = base + 4;
    const segs = splitAtMarks(data, 1000, [{ cum: at, at: 1 }, { cum: at, at: 2 }]);
    expect(segs.filter((s) => s.mark).map((s) => s.mark!.at)).toEqual([2]);
    expect(segs.map((s) => dec(s.bytes)).join("")).toBe(text);
  });

  it("returns the data whole when there are no marks", () => {
    expect(splitAtMarks(data, 1000, null).map((s) => dec(s.bytes))).toEqual([text]);
  });
});

describe("isCommandInput", () => {
  const shell = { altScreen: false, sendFocusMode: false, mouseTrackingMode: "none" };
  it("is Enter at a shell prompt", () => {
    expect(isCommandInput("\r", shell)).toBe(true);
    expect(isCommandInput("ls\r", shell)).toBe(true);
    expect(isCommandInput("l", shell)).toBe(false);
  });
  it("is not Enter inside a full-screen program", () => {
    expect(isCommandInput("\r", { ...shell, altScreen: true })).toBe(false); // vim, nmtui
    expect(isCommandInput("\r", { ...shell, sendFocusMode: true })).toBe(false); // Claude Code
    expect(isCommandInput("\r", { ...shell, mouseTrackingMode: "vt200" })).toBe(false);
  });
});

import { blockRange } from "./cmdStamps";

describe("blockRange", () => {
  const lines = [10, 14, 20];
  it("runs to the line before the next command", () => {
    expect(blockRange(lines, 0, 25)).toEqual([10, 13]);
    expect(blockRange(lines, 1, 25)).toEqual([14, 19]);
  });
  it("runs the newest command to the line before the prompt", () => {
    expect(blockRange(lines, 2, 25)).toEqual([20, 24]);
  });
  it("keeps at least the command line", () => {
    expect(blockRange([10], 0, 10)).toEqual([10, 10]);
    expect(blockRange([10, 11], 0, 30)).toEqual([10, 10]);
  });
});
