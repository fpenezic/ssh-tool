import { describe, it, expect, afterEach, vi } from "vitest";
import { resolveTheme, isUITheme, osPrefersDark, setPlatformPrefersDark } from "./uiTheme";

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

// osPrefersDark reads window.matchMedia, so stubbing the bare global
// would miss it - jsdom keeps them separate.
function stubMatchMedia(matches: boolean) {
  vi.stubGlobal("window", { matchMedia: () => ({ matches }) });
}
function stubMatchMediaThrowing() {
  vi.stubGlobal("window", { matchMedia: () => { throw new Error("nope"); } });
}

describe("osPrefersDark platform override", () => {
  afterEach(() => { setPlatformPrefersDark(null); vi.unstubAllGlobals(); });

  // KDE is the case this exists for: the webview insists it is dark
  // because WebKitGTK reads the GTK theme, while the desktop setting
  // (via the portal) says light. The platform value has to win.
  it("prefers the platform answer over matchMedia", () => {
    stubMatchMedia(true);
    setPlatformPrefersDark(false);
    expect(osPrefersDark()).toBe(false);

    setPlatformPrefersDark(true);
    expect(osPrefersDark()).toBe(true);
  });

  // Windows and GNOME report nothing different, and platforms that do
  // not report at all must not be forced to a value we invented.
  it("falls back to matchMedia when the platform is silent", () => {
    setPlatformPrefersDark(null);
    stubMatchMedia(true);
    expect(osPrefersDark()).toBe(true);
    stubMatchMedia(false);
    expect(osPrefersDark()).toBe(false);
  });

  // A broken matchMedia keeps the old behaviour: dark, because the
  // app's own default is dark and flipping to light is the worse
  // surprise.
  it("defaults to dark when matchMedia throws and nothing was reported", () => {
    setPlatformPrefersDark(null);
    stubMatchMediaThrowing();
    expect(osPrefersDark()).toBe(true);
  });

  // An explicit theme pick must never consult the OS, whichever source
  // the OS answer came from.
  it("does not affect explicit theme choices", () => {
    setPlatformPrefersDark(false);
    expect(resolveTheme("mocha", osPrefersDark())).toBe("mocha");
    expect(resolveTheme("latte", osPrefersDark())).toBe("latte");
    expect(resolveTheme("system", osPrefersDark())).toBe("latte");
  });
});
