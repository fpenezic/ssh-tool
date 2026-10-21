// Per-host findings of one facts run, by the folder's report settings.
// Shared by the health report (FactsReport) and the home pane, so both
// count a host as needing someone by the same rules: thresholds and grace
// days from ReportSettings, expected units and containers left out.
import type { FactsHostResult } from "./api";
import { containerIssues, containerProblem, type FactsSnapshot, type ReportSettings } from "./fleetStore.svelte";

const DAY = 86400;

export interface HostIssue {
  level: "red" | "yellow";
  text: string;
}

// isExpected: a failed unit on the report's own list, or ignored for this
// host or a folder above it.
export type UnitExpected = (r: FactsHostResult, unit: string) => boolean;

export const factsHas = (s: FactsSnapshot | null, k: string) => !!s?.facts.includes(k);

export function unexpectedUnits(r: FactsHostResult, isExpected: UnitExpected): string[] {
  return (r.facts.failed_units ?? []).filter((u) => !isExpected(r, u));
}

// Older runs carry only a count, which counts as is.
export function failedCount(r: FactsHostResult, isExpected: UnitExpected): number {
  return r.facts.failed_units ? unexpectedUnits(r, isExpected).length : Math.max(r.facts.failed, 0);
}

export function fsUse(r: FactsHostResult): { mount: string; pct: number; kind: string }[] {
  return [
    ...(r.facts.disks ?? []).filter((d) => d.used_pct !== undefined).map((d) => ({ mount: d.mount, pct: d.used_pct!, kind: d.network ? "space (network share)" : "space" })),
    ...(r.facts.inodes ?? []).map((d) => ({ mount: d.mount, pct: d.pct, kind: "inodes" })),
  ];
}

// Days since the last package upgrade, at the run's time.
export function patchDays(r: FactsHostResult, atMs: number): number | null {
  const t = r.facts.last_patch ?? 0;
  return t > 0 ? Math.floor((atMs / 1000 - t) / DAY) : null;
}

export const needsReboot = (r: FactsHostResult) => r.facts.reboot === "yes" || r.facts.kernel_pending === "yes";

// Since when a reboot has been waiting: the newest kernel's install time,
// else (a library update, no kernel) the last upgrade.
export function rebootSince(r: FactsHostResult): number | null {
  if (!needsReboot(r)) return null;
  if (r.facts.kernel_pending === "yes" && (r.facts.kernel_latest_at ?? 0) > 0) return r.facts.kernel_latest_at!;
  return (r.facts.last_patch ?? 0) > 0 ? r.facts.last_patch! : null;
}

export function rebootDays(r: FactsHostResult, atMs: number): number | null {
  const t = rebootSince(r);
  return t === null ? null : Math.max(0, Math.floor((atMs / 1000 - t) / DAY));
}

export function rebootFlagged(r: FactsHostResult, atMs: number, rs: ReportSettings): boolean {
  if (!needsReboot(r)) return false;
  const d = rebootDays(r, atMs);
  return d === null || d >= rs.rebootGraceDays;
}

export function hostIssues(r: FactsHostResult, snap: FactsSnapshot, rs: ReportSettings, isExpected: UnitExpected): HostIssue[] {
  const out: HostIssue[] = [];
  const f = r.facts;
  const ct = containerIssues(f, rs, snap.at);
  if (ct.problems.length) out.push({ level: "red", text: ct.problems.map((c) => `${c.name} ${containerProblem(c)}`).join(", ") });
  if (ct.restarted.length) out.push({ level: "yellow", text: `${ct.restarted.map((c) => c.name).join(", ")} restarted by the engine` });
  const nf = failedCount(r, isExpected);
  if (factsHas(snap, "failed") && nf > 0) out.push({ level: "red", text: `${nf} failed unit${nf === 1 ? "" : "s"}` });
  for (const u of fsUse(r)) {
    if (u.pct >= rs.diskBad) out.push({ level: "red", text: `${u.mount} ${u.pct}%` });
    else if (u.pct >= rs.diskWarn) out.push({ level: "yellow", text: `${u.mount} ${u.pct}%` });
  }
  const pd = patchDays(r, snap.at);
  if (factsHas(snap, "updates") && f.security > 0 && (pd === null || pd >= rs.securityGraceDays)) {
    out.push({ level: "yellow", text: `${f.security} security updates${pd !== null ? `, last update ${pd} days ago` : ""}` });
  }
  if (rebootFlagged(r, snap.at, rs)) {
    const d = rebootDays(r, snap.at);
    out.push({ level: "yellow", text: d === null ? "reboot pending" : `reboot pending for ${d} days` });
  }
  if (pd !== null && pd >= rs.staleDays) out.push({ level: "yellow", text: `not updated for ${pd} days` });
  return out;
}
