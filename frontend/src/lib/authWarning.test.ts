import { describe, it, expect } from "vitest";
import {
  effectiveAuthRef,
  isKeyAuthKind,
  shouldWarnPasswordWithKey,
  isSelectableSSHCred,
} from "./authWarning";

describe("effectiveAuthRef", () => {
  it("prefers the connection's own reference", () => {
    expect(effectiveAuthRef("own-id", "folder-id")).toBe("own-id");
  });

  // The editor stores an unset reference as "" rather than null. Treating
  // that as a value would skip inheritance - which is the shape of the
  // reported bug: an inherited key showed no warning.
  it("falls back to inheritance when the own reference is an empty string", () => {
    expect(effectiveAuthRef("", "folder-id")).toBe("folder-id");
  });

  it("falls back to inheritance when the own reference is null or undefined", () => {
    expect(effectiveAuthRef(null, "folder-id")).toBe("folder-id");
    expect(effectiveAuthRef(undefined, "folder-id")).toBe("folder-id");
  });

  it("returns null when neither is set", () => {
    expect(effectiveAuthRef("", "")).toBeNull();
    expect(effectiveAuthRef(null, null)).toBeNull();
  });
});

describe("isKeyAuthKind", () => {
  it("covers every credential that authenticates without a password", () => {
    expect(isKeyAuthKind("key")).toBe(true);
    expect(isKeyAuthKind("agent")).toBe(true);
    expect(isKeyAuthKind("opkssh")).toBe(true);
  });

  it("does not treat a password or token as key auth", () => {
    expect(isKeyAuthKind("password")).toBe(false);
    expect(isKeyAuthKind("api_token")).toBe(false);
    expect(isKeyAuthKind(undefined)).toBe(false);
  });
});

describe("shouldWarnPasswordWithKey", () => {
  it("warns when a key and a saved password are both present", () => {
    expect(shouldWarnPasswordWithKey("key", true)).toBe(true);
    expect(shouldWarnPasswordWithKey("agent", true)).toBe(true);
    expect(shouldWarnPasswordWithKey("opkssh", true)).toBe(true);
  });

  it("stays quiet with only one of the two", () => {
    expect(shouldWarnPasswordWithKey("key", false)).toBe(false);
    expect(shouldWarnPasswordWithKey("password", true)).toBe(false);
    expect(shouldWarnPasswordWithKey(undefined, true)).toBe(false);
  });
});

describe("isSelectableSSHCred", () => {
  // api_token belongs to the inventory providers and cannot authenticate SSH.
  it("hides api_token from the SSH picker", () => {
    expect(isSelectableSSHCred("api_token", "tok-1", null)).toBe(false);
  });

  it("keeps an api_token that is already the stored reference", () => {
    expect(isSelectableSSHCred("api_token", "tok-1", "tok-1")).toBe(true);
  });

  it("offers every credential that can actually authenticate SSH", () => {
    for (const k of ["password", "key", "agent", "opkssh", "vault"] as const) {
      expect(isSelectableSSHCred(k, "id", null)).toBe(true);
    }
  });
});
