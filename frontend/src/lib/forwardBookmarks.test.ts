import { describe, it, expect } from "vitest";
import { forwardBookmarks } from "./forwardBookmarks";
import type { PortForward } from "./api";

describe("forwardBookmarks", () => {
  it("lists own bookmarks first and shared ones not already there", () => {
    const f = {
      bookmarks: [{ name: "mine", url: "https://a.example.com", in_tree: true }],
      catalog_bookmarks: [{ name: "same", url: "https://a.example.com" }, { name: "team", url: "https://b.example.com" }],
    } as unknown as PortForward;
    expect(forwardBookmarks(f).map((b) => b.name)).toEqual(["mine", "team"]);
    expect(forwardBookmarks({ bookmarks: [] } as unknown as PortForward)).toEqual([]);
  });
  it("offers a shared URL listed twice once", () => {
    const f = {
      bookmarks: [],
      catalog_bookmarks: [{ name: "one", url: "https://c.example.com" }, { name: "two", url: "https://c.example.com" }],
    } as unknown as PortForward;
    expect(forwardBookmarks(f).map((b) => b.name)).toEqual(["one"]);
  });
});
