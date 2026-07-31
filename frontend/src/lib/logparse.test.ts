import { describe, it, expect } from "vitest";
import {
  parseLine,
  parseQuery,
  templateKey,
  groupLines,
  levelRank,
  suggestQuery,
} from "./logparse";

describe("parseLine", () => {
  it("parses JSON logs", () => {
    const p = parseLine('{"level":"error","msg":"disk full","ts":"2026-01-01T00:00:00Z","dev":"sda"}');
    expect(p.level).toBe("error");
    expect(p.msg).toBe("disk full");
    expect(p.ts).toBe("2026-01-01T00:00:00Z");
    expect(p.fields?.dev).toBe("sda");
  });

  it("maps JSON severity spellings", () => {
    expect(parseLine('{"severity":"WARNING","message":"x"}').level).toBe("warn");
    expect(parseLine('{"lvl":"fatal","msg":"x"}').level).toBe("error");
  });

  it("parses syslog / journald lines", () => {
    const p = parseLine("Jan  5 12:34:56 host1 nginx[123]: error: upstream timed out");
    expect(p.source).toBe("nginx");
    expect(p.ts).toBe("Jan  5 12:34:56");
    expect(p.level).toBe("error");
    expect(p.fields?.host).toBe("host1");
  });

  it("parses nginx access logs and derives level from status", () => {
    const line = '10.0.0.1 - - [05/Jan/2026:12:00:00 +0000] "GET /x HTTP/1.1" 500 12';
    const p = parseLine(line);
    expect(p.fields?.status).toBe("500");
    expect(p.fields?.method).toBe("GET");
    expect(p.level).toBe("error");
    const ok = parseLine('10.0.0.1 - - [05/Jan/2026:12:00:00 +0000] "GET /x HTTP/1.1" 200 12');
    expect(ok.level).toBe("info");
    const notfound = parseLine('10.0.0.1 - - [05/Jan/2026:12:00:00 +0000] "GET /x HTTP/1.1" 404 12');
    expect(notfound.level).toBe("warn");
  });

  it("scans keywords in unstructured lines", () => {
    expect(parseLine("2026 something WARN retrying").level).toBe("warn");
    expect(parseLine("plain informational line").level).toBe("none");
  });

  it("returns unknown lines verbatim", () => {
    const p = parseLine("just some text");
    expect(p.level).toBe("none");
    expect(p.msg).toBe("just some text");
    expect(p.raw).toBe("just some text");
  });

  it("does not mis-detect broken JSON", () => {
    const p = parseLine("{not really json");
    expect(p.level).toBe("none");
    expect(p.msg).toBe("{not really json");
  });
});

describe("parseQuery", () => {
  const err = parseLine('{"level":"error","msg":"boom"}');
  const info = parseLine('{"level":"info","msg":"hello world"}');
  const access500 = parseLine('1.2.3.4 - - [x] "POST /a HTTP/1.1" 500 1');

  it("empty query matches everything", () => {
    const p = parseQuery("   ");
    expect(p(err)).toBe(true);
    expect(p(info)).toBe(true);
  });

  it("level>= compares severity", () => {
    const p = parseQuery("level>=warn");
    expect(p(err)).toBe(true);
    expect(p(info)).toBe(false);
  });

  it("level= is exact", () => {
    const p = parseQuery("level=info");
    expect(p(info)).toBe(true);
    expect(p(err)).toBe(false);
  });

  it("field equality matches parsed fields", () => {
    expect(parseQuery("status=500")(access500)).toBe(true);
    expect(parseQuery("status=200")(access500)).toBe(false);
    expect(parseQuery("method=POST")(access500)).toBe(true);
  });

  it("negation excludes substrings", () => {
    expect(parseQuery("-hello")(info)).toBe(false);
    expect(parseQuery("-hello")(err)).toBe(true);
  });

  it("quoted phrase matches with spaces", () => {
    expect(parseQuery('"hello world"')(info)).toBe(true);
    expect(parseQuery('"hello there"')(info)).toBe(false);
  });

  it("implicit AND of terms", () => {
    const p = parseQuery("level>=warn boom");
    expect(p(err)).toBe(true);
    expect(p(parseLine('{"level":"error","msg":"quiet"}'))).toBe(false);
  });

  it("bad input falls back to substring, never throws", () => {
    const p = parseQuery('"unclosed');
    // treated as a literal substring including the quote char
    expect(p(parseLine('a "unclosed thing'))).toBe(true);
    expect(p(info)).toBe(false);
  });

  it("bare word greps case-insensitively", () => {
    expect(parseQuery("WORLD")(info)).toBe(true);
  });
});

describe("templateKey / groupLines", () => {
  it("collapses lines differing only by numbers", () => {
    const a = parseLine("user 5 failed login");
    const b = parseLine("user 42 failed login");
    expect(templateKey(a)).toBe(templateKey(b));
  });

  it("keeps genuinely different messages apart", () => {
    const a = parseLine("user 5 failed login");
    const c = parseLine("disk 5 full");
    expect(templateKey(a)).not.toBe(templateKey(c));
  });

  it("normalizes ips and hex", () => {
    const a = parseLine("conn from 10.0.0.1 id deadbeefcafe");
    const b = parseLine("conn from 192.168.1.9 id 0011223344ff");
    expect(templateKey(a)).toBe(templateKey(b));
  });

  it("groups with counts and preserves first text, ordered by last seen", () => {
    const lines = [
      parseLine("user 1 failed"),
      parseLine("unique startup message"),
      parseLine("user 2 failed"),
      parseLine("user 3 failed"),
    ];
    const groups = groupLines(lines);
    expect(groups.length).toBe(2);
    const failed = groups.find((g) => g.line.raw.includes("failed"));
    expect(failed?.count).toBe(3);
    expect(failed?.line.raw).toBe("user 1 failed"); // first occurrence text
    // "failed" group was last-active (idx 3), so it sorts after the singleton.
    expect(groups[groups.length - 1]).toBe(failed);
  });
});

describe("levelRank", () => {
  it("orders severities", () => {
    expect(levelRank("error")).toBeGreaterThan(levelRank("warn"));
    expect(levelRank("warn")).toBeGreaterThan(levelRank("info"));
    expect(levelRank("none")).toBe(0);
  });
});

describe("suggestQuery (autocomplete)", () => {
  const sample = [
    parseLine('1.2.3.4 - - [x] "POST /a HTTP/1.1" 500 1'),
    parseLine('1.2.3.4 - - [x] "GET /b HTTP/1.1" 200 1'),
  ];

  it("suggests keys when typing a bare word", () => {
    const s = suggestQuery("lev", 3, sample);
    expect(s.some((x) => x.insert.startsWith("level"))).toBe(true);
  });

  it("suggests level values after level=", () => {
    const s = suggestQuery("level=", 6, sample);
    const vals = s.map((x) => x.insert);
    expect(vals).toContain("level=error");
    expect(vals).toContain("level=warn");
  });

  it("suggests observed field values after status=", () => {
    const s = suggestQuery("status=", 7, sample);
    const vals = s.map((x) => x.insert);
    expect(vals).toContain("status=500");
    expect(vals).toContain("status=200");
  });

  it("completes only the current term, keeping earlier terms", () => {
    const s = suggestQuery("boom level=err", 14, sample);
    expect(s.every((x) => x.insert.startsWith("boom "))).toBe(true);
  });
});
