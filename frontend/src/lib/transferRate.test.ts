import { describe, expect, it } from "vitest";
import { etaSeconds, fmtEta, pushSample, rateBps, summarizeBatch, type RateSample } from "./transferRate";

function feed(points: Array<[number, number]>): RateSample[] {
  let s: RateSample[] = [];
  for (const [t, bytes] of points) s = pushSample(s, { t, bytes });
  return s;
}

describe("rateBps", () => {
  it("is null until the span is long enough to mean anything", () => {
    const s = feed([[0, 0], [100, 1000]]);
    expect(rateBps(s, 100)).toBeNull();
  });

  it("measures a steady 1 MB/s", () => {
    const pts: Array<[number, number]> = [];
    for (let t = 0; t <= 5000; t += 100) pts.push([t, t * 1000]);
    expect(rateBps(feed(pts), 5000)).toBeCloseTo(1_000_000, -3);
  });

  it("follows a slowdown within the window instead of averaging the whole transfer", () => {
    const pts: Array<[number, number]> = [];
    let bytes = 0;
    for (let t = 0; t <= 10000; t += 100) {
      pts.push([t, bytes]);
      bytes += t < 5000 ? 100_000 : 10_000; // 1 MB/s, then 100 KB/s
    }
    expect(rateBps(feed(pts), 10000)).toBeCloseTo(100_000, -3);
  });

  it("drops to 0 when the transfer stalls and no events arrive", () => {
    const s = feed([[0, 0], [1000, 1_000_000]]);
    expect(rateBps(s, 5000)).toBe(0);
  });

  it("keeps the window bounded", () => {
    const pts: Array<[number, number]> = [];
    for (let t = 0; t <= 60000; t += 100) pts.push([t, t]);
    expect(feed(pts).length).toBeLessThanOrEqual(32);
  });
});

describe("etaSeconds", () => {
  it("is the remaining bytes over the rate, rounded up", () => {
    expect(etaSeconds(0, 1000, 300)).toBe(4);
  });
  it("is null for an unknown or stalled rate and for a finished transfer", () => {
    expect(etaSeconds(0, 1000, null)).toBeNull();
    expect(etaSeconds(0, 1000, 0)).toBeNull();
    expect(etaSeconds(1000, 1000, 500)).toBeNull();
  });
});

describe("fmtEta", () => {
  it("formats seconds, minutes and hours", () => {
    expect(fmtEta(42)).toBe("42s");
    expect(fmtEta(185)).toBe("3m 05s");
    expect(fmtEta(3725)).toBe("1h 02m");
  });
});

describe("summarizeBatch", () => {
  const steady = (bps: number): RateSample[] => [{ t: 0, bytes: 0 }, { t: 1000, bytes: bps }];

  it("sums bytes, totals and the running transfers' rates", () => {
    const s = summarizeBatch([
      { bytes: 1000, total: 4000, samples: steady(1000), running: true, failed: false },
      { bytes: 500, total: 1000, samples: steady(500), running: true, failed: false },
    ], 1000);
    expect(s).toEqual({ running: 2, bytes: 1500, total: 5000, bps: 1500, eta: 3 });
  });

  it("keeps finished transfers in the totals so the percentage never goes backwards", () => {
    const s = summarizeBatch([
      { bytes: 1000, total: 1000, samples: steady(1000), running: false, failed: false },
      { bytes: 0, total: 1000, samples: [], running: true, failed: false },
    ], 1000);
    expect(s.bytes).toBe(1000);
    expect(s.total).toBe(2000);
    expect(s.running).toBe(1);
  });

  it("drops failed and cancelled transfers from the totals", () => {
    const s = summarizeBatch([
      { bytes: 10, total: 1000, samples: [], running: false, failed: true },
      { bytes: 50, total: 100, samples: [], running: true, failed: false },
    ], 0);
    expect(s.total).toBe(100);
    expect(s.bps).toBeNull();
    expect(s.eta).toBeNull();
  });
});
