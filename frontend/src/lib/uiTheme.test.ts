import { describe, it, expect } from "vitest";
import { resolveTheme, isUITheme } from "./uiTheme";

describe("resolveTheme", () => {
  it("follows the OS only for 'system'", () => {
    expect(resolveTheme("system", true)).toBe("mocha");
    expect(resolveTheme("system", false)).toBe("latte");
  });

  // The point of an explicit pick is that it survives a desktop that
  // disagrees with it - Latte on a dark desktop must stay Latte.
  it("leaves an explicit choice alone whatever the OS says", () => {
    for (const dark of [true, false]) {
      expect(resolveTheme("mocha", dark)).toBe("mocha");
      expect(resolveTheme("latte", dark)).toBe("latte");
      expect(resolveTheme("hc", dark)).toBe("hc");
    }
  });

  // "system" must never reach applyThemeClasses: it is not a palette, and
  // a class named theme-system does not exist in style.css.
  it("never returns 'system'", () => {
    for (const dark of [true, false]) {
      expect(resolveTheme("system", dark)).not.toBe("system");
    }
  });
});

describe("isUITheme", () => {
  it("accepts the four choices", () => {
    for (const v of ["system", "mocha", "latte", "hc"]) {
      expect(isUITheme(v)).toBe(true);
    }
  });

  // Guards the persisted settings value and the localStorage boot cache,
  // both of which can hold anything a previous version wrote.
  it("rejects anything else", () => {
    for (const v of ["", "dark", "light", null, undefined, 1, {}]) {
      expect(isUITheme(v)).toBe(false);
    }
  });
});
