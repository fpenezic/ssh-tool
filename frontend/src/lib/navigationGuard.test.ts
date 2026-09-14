import { describe, it, expect, beforeAll } from "vitest";
import { __test } from "./navigationGuard";

const { isExternal } = __test;

// The module reads `location`, which does not exist without a DOM. The
// project has no jsdom, and this is a pure string/URL decision, so stub
// the one property it needs.
beforeAll(() => {
  (globalThis as any).location = new URL("http://wails.localhost/");
});

describe("isExternal", () => {
  it("treats same-origin paths as internal", () => {
    expect(isExternal("/")).toBe(false);
    expect(isExternal("/index.html")).toBe(false);
    expect(isExternal("?detached=1")).toBe(false);
    expect(isExternal("#/settings")).toBe(false);
    expect(isExternal("http://wails.localhost/foo")).toBe(false);
  });

  it("treats a different host as external", () => {
    expect(isExternal("https://claude.com/cai/oauth/authorize?code=true")).toBe(true);
    expect(isExternal("http://example.com/")).toBe(true);
  });

  it("treats a different scheme on the same host as external", () => {
    expect(isExternal("https://wails.localhost/")).toBe(true);
  });

  it("treats a different port as external", () => {
    expect(isExternal("http://wails.localhost:8080/")).toBe(true);
  });

  it("routes other protocols outward", () => {
    expect(isExternal("mailto:someone@example.com")).toBe(true);
    expect(isExternal("ssh://host.example.com")).toBe(true);
  });

  it("leaves about: and javascript: to the page", () => {
    expect(isExternal("about:blank")).toBe(false);
    expect(isExternal("javascript:void 0")).toBe(false);
  });

  it("refuses blob: and data:, which have no comparable origin", () => {
    expect(isExternal("data:text/html,<h1>hi</h1>")).toBe(true);
    expect(isExternal("blob:http://wails.localhost/abc")).toBe(true);
  });

  it("treats an unparseable href as external rather than allowing it", () => {
    expect(isExternal("http://[")).toBe(true);
  });

  // The case that prompted the guard.
  it("catches the OAuth login URL that navigated the app window", () => {
    const url =
      "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a" +
      "&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback";
    expect(isExternal(url)).toBe(true);
  });
});
