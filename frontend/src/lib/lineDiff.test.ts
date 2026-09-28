import { describe, expect, it } from "vitest";
import { lineDiff, splitLines } from "./lineDiff";

describe("lineDiff", () => {
  it("pairs a changed line opposite its new version", () => {
    const rows = lineDiff(["a", "workers 4096;", "c"], ["a", "workers 1024;", "c"])!;
    expect(rows.map((r) => r.kind)).toEqual(["same", "chg", "same"]);
    expect(rows[1]).toMatchObject({ left: "workers 4096;", right: "workers 1024;", ln: 2, rn: 2 });
  });

  it("keeps line numbers right across an insertion", () => {
    const rows = lineDiff(["a", "b", "d"], ["a", "b", "c", "d"])!;
    expect(rows.map((r) => r.kind)).toEqual(["same", "same", "add", "same"]);
    expect(rows[3]).toMatchObject({ ln: 3, rn: 4 });
  });

  it("ignores whitespace when asked", () => {
    expect(lineDiff(["a  b"], ["a b"], true)!.map((r) => r.kind)).toEqual(["same"]);
    expect(lineDiff(["a  b"], ["a b"], false)!.map((r) => r.kind)).toEqual(["chg"]);
  });

  it("drops the trailing empty line of a final newline", () => {
    expect(splitLines("x\ny\n")).toEqual(["x", "y"]);
  });

  it("gives up rather than allocating a huge table", () => {
    const big = Array.from({ length: 3000 }, (_, i) => `a${i}`);
    const other = Array.from({ length: 3000 }, (_, i) => `b${i}`);
    expect(lineDiff(big, other)).toBeNull();
  });
});
