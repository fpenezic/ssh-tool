import { describe, expect, it } from "vitest";
import type { KafkaMessage } from "./api";
import {
  formatBytes, formatCount, hexDump, matches, messageTextBytes, parseConfigLines, pretty, preview, ratePerSec,
} from "./kafkaFormat";

const msg = (over: Partial<KafkaMessage> = {}): KafkaMessage => ({
  seq: 1,
  partition: 0,
  offset: 7,
  timestamp: 1700000000000,
  key: { text: "order-1", encoding: "utf8", size: 7 },
  value: { text: '{"a":1,"b":[2]}', encoding: "utf8", size: 15 },
  headers: [{ key: "trace", value: { text: "abc", encoding: "utf8", size: 3 } }],
  size: 30,
  ...over,
});

describe("kafkaFormat", () => {
  it("formats counts and bytes", () => {
    expect(formatCount(-1)).toBe("?");
    expect(formatCount(999)).toBe("999");
    expect(formatCount(1500)).toBe("1.5k");
    expect(formatCount(25_000)).toBe("25k");
    expect(formatCount(3_400_000)).toBe("3.4M");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(-1)).toBe("?");
  });

  it("pretty prints JSON and leaves text alone", () => {
    expect(pretty(msg().value)).toEqual({ text: '{\n  "a": 1,\n  "b": [\n    2\n  ]\n}', kind: "json" });
    expect(pretty({ text: "{not json}", encoding: "utf8", size: 10 }).kind).toBe("text");
    expect(pretty(null).kind).toBe("null");
    const bin = pretty({ text: "/wA=", encoding: "base64", size: 2 });
    expect(bin.kind).toBe("binary");
    expect(bin.text).toContain("ff 00");
  });

  it("previews on one line", () => {
    expect(preview({ text: "a\n  b", encoding: "utf8", size: 5 })).toBe("a b");
    expect(preview({ text: "AAAA", encoding: "base64", size: 3 })).toBe("<3 bytes binary>");
    expect(preview(null)).toBe("null");
    expect(preview({ text: "x".repeat(10), encoding: "utf8", size: 10 }, 4)).toBe("xxxx…");
  });

  it("hex dumps", () => {
    const d = hexDump(new Uint8Array([0x41, 0x00, 0x7f]));
    expect(d).toBe("00000000  41 00 7f" + " ".repeat(47 - 8) + "  A..");
    expect(hexDump(new Uint8Array(20), 16)).toContain("4 more bytes");
  });

  it("filters over key, value and headers", () => {
    expect(matches(msg(), "")).toBe(true);
    expect(matches(msg(), "ORDER")).toBe(true);
    expect(matches(msg(), '"b"')).toBe(true);
    expect(matches(msg(), "trace")).toBe(true);
    expect(matches(msg(), "abc")).toBe(true);
    expect(matches(msg(), "nope")).toBe(false);
    expect(matches(msg({ key: null, value: null, headers: [] }), "x")).toBe(false);
  });

  it("counts a record's text", () => {
    expect(messageTextBytes(msg())).toBe(7 + 15 + 5 + 3);
    expect(messageTextBytes(msg({ key: null, value: null, headers: [] }))).toBe(0);
  });

  it("parses config lines", () => {
    expect(parseConfigLines("# c\nretention.ms = 1000\n\ncleanup.policy=compact")).toEqual({
      set: { "retention.ms": "1000", "cleanup.policy": "compact" },
    });
    expect(parseConfigLines("a=1\noops").error).toBe("line 2: expected key=value");
  });

  it("computes rates", () => {
    expect(ratePerSec(null, { at: 1000, v: 5 })).toBeNull();
    expect(ratePerSec({ at: 0, v: 0 }, { at: 2000, v: 10 })).toBe(5);
    expect(ratePerSec({ at: 0, v: -1 }, { at: 2000, v: 10 })).toBeNull();
  });
});
