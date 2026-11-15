import { describe, it, expect } from "vitest";
import { importSourceFromLink } from "./deepLink.svelte";

describe("importSourceFromLink", () => {
  it("keeps a plain source URL (desktop)", () => {
    expect(importSourceFromLink("https://x.example.com/a.toml")).toBe("https://x.example.com/a.toml");
  });
  it("unpacks the whole link (Android)", () => {
    expect(importSourceFromLink("ssh-tool://import?source=https%3A%2F%2Fx.example.com%2Fa.toml")).toBe("https://x.example.com/a.toml");
  });
  it("ignores other ssh-tool links", () => {
    expect(importSourceFromLink("ssh-tool://catalog/add?url=a&code=b")).toBeNull();
    expect(importSourceFromLink("ssh-tool://import")).toBeNull();
  });
});
