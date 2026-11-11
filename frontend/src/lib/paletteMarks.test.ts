import { describe, expect, it } from "vitest";
import { enterSet } from "./paletteMarks";

const m = (id: string) => ({ id });

describe("enterSet", () => {
  it("adds the unmarked host under the cursor to the marked set", () => {
    expect(enterSet([m("a"), m("b")], m("c")).map((x) => x.id)).toEqual(["a", "b", "c"]);
  });
  it("opens the set alone when the host under the cursor is marked", () => {
    expect(enterSet([m("a"), m("b")], m("a")).map((x) => x.id)).toEqual(["a", "b"]);
  });
  it("opens the set alone when the row is not a host", () => {
    expect(enterSet([m("a")], null).map((x) => x.id)).toEqual(["a"]);
  });
});
