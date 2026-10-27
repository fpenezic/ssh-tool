import { describe, expect, it } from "vitest";
import { flipTri } from "./triSwitch";

describe("flipTri", () => {
  it("on and off again returns to inherit", () => {
    const a = flipTri("", "", false);
    expect(a).toBe("on");
    expect(flipTri(a, "", false)).toBe("");
  });
  it("an inherited on turns off on the first click", () => {
    const a = flipTri("", "", true);
    expect(a).toBe("off");
    expect(flipTri(a, "", true)).toBe("");
  });
  it("an explicit value round-trips to itself", () => {
    expect(flipTri(flipTri("on", "on", false), "on", false)).toBe("on");
    expect(flipTri(flipTri("off", "off", true), "off", true)).toBe("off");
  });
});
