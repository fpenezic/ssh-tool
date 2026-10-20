import { describe, expect, it } from "vitest";
import { compareNumbers, isNumericType, sortedOrder } from "./dbSort";

describe("dbSort", () => {
  it("knows numeric types", () => {
    for (const t of ["int4", "bigint", "numeric(10,2)", "UInt64", "Nullable(Decimal(10, 2))", "double precision", "Float64", "int unsigned"]) {
      expect(isNumericType(t), t).toBe(true);
    }
    for (const t of ["text", "varchar(20)", "String", "DateTime", "interval", "Array(UInt8)", "json"]) {
      expect(isNumericType(t), t).toBe(false);
    }
  });

  it("compares numbers without losing big integers", () => {
    expect(compareNumbers("9007199254740993", "9007199254740992")).toBeGreaterThan(0);
    expect(compareNumbers("-5", "3")).toBeLessThan(0);
    expect(compareNumbers("-10", "-9")).toBeLessThan(0);
    expect(compareNumbers("2.5", "10")).toBeLessThan(0);
    expect(compareNumbers("007", "7")).toBe(0);
    expect(compareNumbers("nan", "1")).toBeGreaterThan(0);
  });

  it("sorts with NULLs last and ties stable", () => {
    const rows = [["10", "b"], [null, "a"], ["9", "a"], ["10", "a"]];
    expect(sortedOrder(rows, 0, "int", false)).toEqual([2, 0, 3, 1]);
    expect(sortedOrder(rows, 0, "int", true)).toEqual([0, 3, 2, 1]);
    // Text: natural order, case-insensitive.
    const names = [["item10"], ["Item2"], ["item1"]];
    expect(sortedOrder(names, 0, "text", false)).toEqual([2, 1, 0]);
    expect(sortedOrder(rows, 1, "text", false)).toEqual([1, 2, 3, 0]);
  });
});
