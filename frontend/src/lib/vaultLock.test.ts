import { describe, it, expect } from "vitest";
import { isLockedState, normalizeVaultState } from "./vaultLock";

describe("isLockedState", () => {
  it("treats only an actually-locked vault as locked", () => {
    expect(isLockedState("locked")).toBe(true);
    expect(isLockedState("unlocked")).toBe(false);
  });

  // A fresh install has no vault at all. Warning that it is "locked" would
  // point the user at an unlock flow for something that does not exist.
  it("does not call an uninitialised vault locked", () => {
    expect(isLockedState("not_initialized")).toBe(false);
  });
});

describe("normalizeVaultState", () => {
  it("passes the three known states through unchanged", () => {
    expect(normalizeVaultState("locked")).toBe("locked");
    expect(normalizeVaultState("unlocked")).toBe("unlocked");
    expect(normalizeVaultState("not_initialized")).toBe("not_initialized");
  });

  // The whole point of the indicator is that the user finds out BEFORE a
  // reveal or a connect fails. Resolving an unknown value to "unlocked"
  // would restore exactly the silence this replaces, so every unrecognised
  // shape has to fail closed.
  it("fails closed on anything it does not recognise", () => {
    for (const bad of [undefined, null, "", "LOCKED", "open", 0, 1, {}, [], NaN]) {
      expect(normalizeVaultState(bad)).toBe("locked");
    }
  });

  it("does not treat a truthy unknown string as unlocked", () => {
    expect(normalizeVaultState("definitely-fine")).toBe("locked");
  });
});
