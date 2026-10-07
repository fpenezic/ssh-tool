import { describe, it, expect } from "vitest";
import { credMatches } from "./credFilter";

const pw = { name: "Prod DB", kind: "password", default_username: "postgres", hint: "rotated monthly", tags: ["db"] };

describe("credMatches", () => {
  it("matches name, user, hint, tags and kind, case-insensitively", () => {
    for (const q of ["prod", "POSTGRES", "monthly", "db", "password"]) expect(credMatches(pw, q)).toBe(true);
    expect(credMatches(pw, "mysql")).toBe(false);
  });
  it("needs every word, and counts the folder names it sits in", () => {
    expect(credMatches(pw, "postgres prod")).toBe(true);
    expect(credMatches(pw, "postgres staging")).toBe(false);
    expect(credMatches(pw, "customer postgres", ["Customer A"])).toBe(true);
  });
  it("finds external backends and passes everything on an empty query", () => {
    expect(credMatches({ name: "x", kind: "password", config: { bitwarden_ref: "abc" } }, "bitwarden")).toBe(true);
    expect(credMatches(pw, "   ")).toBe(true);
  });
});
