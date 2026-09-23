import { describe, expect, it } from "vitest";
import { expiringCredentials, fmtDuration, opksshExpiry } from "./credExpiry";

const DAY = 86_400;
const now = Date.UTC(2026, 8, 23);
const at = (days: number) => Math.floor(now / 1000) + days * DAY + 60;

describe("expiringCredentials", () => {
  it("keeps expired and soon, drops far-off and undated, most urgent first", () => {
    const got = expiringCredentials([
      { id: "a", name: "far", expires_at: at(90) },
      { id: "b", name: "soon", expires_at: at(5) },
      { id: "c", name: "none", expires_at: null },
      { id: "d", name: "gone", expires_at: at(-3) },
      { id: "e", name: "edge", expires_at: at(14) },
    ], now);
    expect(got.map((c) => c.id)).toEqual(["d", "b", "e"]);
    expect(got[0].info.level).toBe("expired");
    expect(got[1].info.level).toBe("soon");
  });

  it("is empty when nothing is close", () => {
    expect(expiringCredentials([{ id: "a", name: "x", expires_at: at(30) }], now)).toEqual([]);
  });
});

describe("opksshExpiry", () => {
  const start = 1_000_000;
  const end = start + 100 * 3600; // 100 h lifetime
  const nowAt = (secs: number) => secs * 1000;

  it("scales with the cert's own lifetime", () => {
    expect(opksshExpiry(start, end, nowAt(start + 50 * 3600)).level).toBe("ok");
    expect(opksshExpiry(start, end, nowAt(start + 80 * 3600)).level).toBe("soon");
    expect(opksshExpiry(start, end, nowAt(start + 95 * 3600)).level).toBe("critical");
    expect(opksshExpiry(start, end, nowAt(end + 1)).level).toBe("expired");
    // An 8 h cert reaches "soon" after 6 h, not after days.
    expect(opksshExpiry(start, start + 8 * 3600, nowAt(start + 6.5 * 3600)).level).toBe("soon");
  });

  it("labels the time left", () => {
    expect(opksshExpiry(start, end, nowAt(end - 2 * 3600 - 600)).label).toBe("sign-in in 2h 10m");
    expect(opksshExpiry(start, end, nowAt(end + 5)).label).toBe("sign-in due");
  });
});

describe("fmtDuration", () => {
  it("picks the two largest units", () => {
    expect(fmtDuration(59)).toBe("0m");
    expect(fmtDuration(3 * 86_400 + 4 * 3600 + 120)).toBe("3d 4h");
    expect(fmtDuration(45 * 60)).toBe("45m");
  });
});
