import { describe, it, expect } from "vitest";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { wrapWithVS16 } from "./unicodeVS16";

interface Provider {
  readonly version: string;
  wcwidth(cp: number): number;
  charProperties(cp: number, preceding: number): number;
}

function v11(): Provider {
  let p: Provider | undefined;
  new Unicode11Addon().activate({
    unicode: { register: (x: unknown) => { p = x as Provider; } },
  } as never);
  if (!p) throw new Error("no provider");
  return p;
}

// Mirrors UnicodeService.getStringCellWidth: width of the cluster, with
// the join rule applied.
function clusterWidth(p: Provider, codepoints: number[]): number {
  let total = 0;
  let preceding = 0;
  for (const cp of codepoints) {
    const info = p.charProperties(cp, preceding);
    let w = (info >> 1) & 3;
    if ((info & 1) !== 0) w -= (preceding >> 1) & 3;
    total += w;
    preceding = info;
  }
  return total;
}

const VS16 = 0xfe0f;

describe("wrapWithVS16", () => {
  const base = v11();
  const wrapped = wrapWithVS16(base, "11-vs16");

  it("registers under the requested version", () => {
    expect(wrapped.version).toBe("11-vs16");
  });

  // The characters that were rendering a column short. All default to
  // text presentation and are narrow in the Unicode tables.
  const narrowWithVS16: [string, number][] = [
    ["warning U+26A0", 0x26a0],
    ["info U+2139", 0x2139],
    ["check U+2714", 0x2714],
    ["sun U+2600", 0x2600],
    ["cloud U+2601", 0x2601],
    ["snowflake U+2744", 0x2744],
    ["heart U+2764", 0x2764],
    ["arrow U+27A1", 0x27a1],
    ["recycle U+267B", 0x267b],
    ["gear U+2699", 0x2699],
  ];

  it("measures a bare narrow emoji as 1, unchanged", () => {
    for (const [name, cp] of narrowWithVS16) {
      expect(clusterWidth(wrapped, [cp]), name).toBe(1);
    }
  });

  it("measures narrow emoji + VS16 as 2", () => {
    for (const [name, cp] of narrowWithVS16) {
      expect(clusterWidth(base, [cp, VS16]), `${name} (v11 baseline)`).toBe(1);
      expect(clusterWidth(wrapped, [cp, VS16]), name).toBe(2);
    }
  });

  it("leaves already-wide emoji at 2 with VS16", () => {
    for (const [name, cp] of [["cross U+274C", 0x274c], ["star U+2B50", 0x2b50]] as [string, number][]) {
      expect(clusterWidth(wrapped, [cp, VS16]), name).toBe(2);
    }
  });

  it("leaves wide emoji that need no selector alone", () => {
    expect(clusterWidth(wrapped, [0x1f534])).toBe(2); // red circle
    expect(clusterWidth(wrapped, [0x2705])).toBe(2); // check button
    expect(clusterWidth(wrapped, [0x1f680])).toBe(2); // rocket
  });

  it("does not disturb ASCII, CJK, Cyrillic or diacritics", () => {
    expect(clusterWidth(wrapped, [0x41])).toBe(1); // A
    expect(clusterWidth(wrapped, [0x691c])).toBe(2); // CJK
    expect(clusterWidth(wrapped, [0x0422])).toBe(1); // Cyrillic T
    expect(clusterWidth(wrapped, [0x010d])).toBe(1); // c-caron
  });

  it("delegates wcwidth unchanged", () => {
    for (const cp of [0x41, 0x691c, 0x1f534, 0x26a0, 0x0422]) {
      expect(wrapped.wcwidth(cp)).toBe(base.wcwidth(cp));
    }
  });

  it("does not widen a VS16 that follows a wide character", () => {
    // Not a meaningful sequence, but it must not add a column.
    expect(clusterWidth(wrapped, [0x691c, VS16])).toBe(2);
  });
});
