import { describe, it, expect } from "vitest";
import { errMsg, unwrapRaw } from "./connectErrors";

// Wails v3 rejects an IPC call with an Error whose .message is itself a
// JSON envelope: {"message":"...","cause":{},"kind":"RuntimeError"}. Any
// catch site that reads e.message directly puts that raw JSON on screen -
// which is exactly what the copy-password hint did on a key-auth host.
describe("errMsg", () => {
  const envelope = JSON.stringify({
    message: '"opkssh" authenticates with an opkssh certificate, so there is no password to copy',
    cause: {},
    kind: "RuntimeError",
  });

  it("peels the Go message out of an Error carrying the envelope", () => {
    expect(errMsg(new Error(envelope))).toBe(
      '"opkssh" authenticates with an opkssh certificate, so there is no password to copy',
    );
  });

  it("peels a bare envelope string", () => {
    expect(errMsg(envelope)).toBe(
      '"opkssh" authenticates with an opkssh certificate, so there is no password to copy',
    );
  });

  it("never leaks the envelope's own keys to the UI", () => {
    const out = errMsg(new Error(envelope));
    expect(out).not.toContain("RuntimeError");
    expect(out).not.toContain('"kind"');
    expect(out).not.toContain('"cause"');
  });

  it("passes a plain error through unchanged", () => {
    expect(errMsg(new Error("connection refused"))).toBe("connection refused");
    expect(errMsg("connection refused")).toBe("connection refused");
  });

  it("leaves a message that merely starts with a quote alone", () => {
    // Our own messages open with a quoted credential name. That must not
    // be mistaken for JSON and mangled.
    const msg = '"prod key" authenticates with an SSH key, so there is no password to copy';
    expect(errMsg(new Error(msg))).toBe(msg);
  });

  it("survives malformed JSON rather than throwing", () => {
    expect(errMsg('{"message":"truncated')).toBe('{"message":"truncated');
  });

  it("handles null / undefined", () => {
    expect(errMsg(null)).toBe("Unknown error");
    expect(errMsg(undefined)).toBe("Unknown error");
  });
});

describe("unwrapRaw", () => {
  it("peels the envelope for the substring rules downstream", () => {
    const raw = JSON.stringify({ message: "dial tcp: i/o timeout", cause: {}, kind: "RuntimeError" });
    expect(unwrapRaw(raw)).toBe("dial tcp: i/o timeout");
  });
});
