import { describe, expect, it, vi } from "vitest";

vi.mock("./api", () => ({ api: {} }));
vi.mock("./fleetStore.svelte", () => ({
  containerIssues: () => ({ problems: [], restarted: [] }),
  containerProblem: () => null,
}));

import { hostIssues } from "./factsIssues";

const DAY = 86400;
const at = Date.UTC(2026, 9, 21); // run time, ms
const rs = {
  expectedUnits: ["irqbalance.service"], diskWarn: 80, diskBad: 90,
  securityGraceDays: 7, rebootGraceDays: 7, staleDays: 30,
  expectedContainers: [], containerRestartDays: 7,
};
const snap = (facts: string[]) => ({ at, facts, results: [] });
const host = (f: Record<string, unknown>) => ({
  connection_id: "c1", name: "web-01", state: "ok",
  facts: { failed: 0, security: 0, mem_kb: 0, ...f },
}) as any;
const noIgnore = (_r: any, u: string) => rs.expectedUnits.includes(u);

describe("hostIssues", () => {
  it("leaves security updates inside the grace days alone", () => {
    const r = host({ security: 3, last_patch: at / 1000 - 2 * DAY });
    expect(hostIssues(r, snap(["updates"]), rs, noIgnore)).toEqual([]);
  });

  it("flags security updates past the grace days", () => {
    const r = host({ security: 3, last_patch: at / 1000 - 10 * DAY });
    expect(hostIssues(r, snap(["updates"]), rs, noIgnore)).toEqual([
      { level: "yellow", text: "3 security updates, last update 10 days ago" },
    ]);
  });

  it("does not count an expected failed unit", () => {
    const r = host({ failed: 2, failed_units: ["irqbalance.service", "nginx.service"] });
    expect(hostIssues(r, snap(["failed"]), rs, noIgnore)).toEqual([{ level: "red", text: "1 failed unit" }]);
  });

  it("grades disks by the folder's thresholds", () => {
    const r = host({ disks: [{ mount: "/", used_pct: 85 }, { mount: "/var", used_pct: 95 }] });
    expect(hostIssues(r, snap(["disks"]), rs, noIgnore)).toEqual([
      { level: "yellow", text: "/ 85%" },
      { level: "red", text: "/var 95%" },
    ]);
  });
});
