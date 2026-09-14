import { describe, it, expect } from "vitest";
import { parseOsc52 } from "./osc52";

const b64 = (s: string) => {
  const bytes = new TextEncoder().encode(s);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
};

describe("parseOsc52", () => {
  it("decodes a clipboard write", () => {
    expect(parseOsc52(`c;${b64("hello")}`)).toEqual({ kind: "write", text: "hello" });
  });

  it("accepts the other selection targets", () => {
    for (const t of ["c", "p", "s", "0", "7", "pc", ""]) {
      expect(parseOsc52(`${t};${b64("x")}`), `target=${t}`)
        .toEqual({ kind: "write", text: "x" });
    }
  });

  it("decodes UTF-8 rather than mangling it", () => {
    const s = "čćž 漢字 🚀 naïve";
    expect(parseOsc52(`c;${b64(s)}`)).toEqual({ kind: "write", text: s });
  });

  it("handles a long URL, the case this was written for", () => {
    const url =
      "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a" +
      "&scope=org%3Acreate_api_key+user%3Aprofile&code_challenge_method=S256";
    expect(parseOsc52(`c;${b64(url)}`)).toEqual({ kind: "write", text: url });
  });

  it("reports a read request separately so it can be refused", () => {
    expect(parseOsc52("c;?")).toEqual({ kind: "read" });
  });

  it("returns null with no separator", () => {
    expect(parseOsc52("nonsense")).toBeNull();
  });

  it("returns null on an empty payload", () => {
    expect(parseOsc52("c;")).toBeNull();
  });

  it("returns null on undecodable base64", () => {
    expect(parseOsc52("c;!!!not base64!!!")).toBeNull();
  });

  it("returns null on a payload that decodes to nothing", () => {
    expect(parseOsc52(`c;${b64("")}`)).toBeNull();
  });

  it("rejects an oversized payload instead of writing it", () => {
    expect(parseOsc52("c;" + "A".repeat(1024 * 1024 + 4))).toBeNull();
  });

  it("splits on the first semicolon only", () => {
    // Targets can be empty and the base64 alphabet excludes ";", but the
    // split must not be greedy either way.
    const payload = b64("a;b;c");
    expect(parseOsc52(`c;${payload}`)).toEqual({ kind: "write", text: "a;b;c" });
  });

  it("preserves newlines and tabs in copied text", () => {
    const s = "line1\nline2\ttabbed\n";
    expect(parseOsc52(`c;${b64(s)}`)).toEqual({ kind: "write", text: s });
  });
});
