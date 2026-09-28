// Saturation thresholds for the server-status readouts, shared by the
// System status popup (bar colours) and the status bar (icon colours) so
// both agree on when a metric is "near the limit". Per metric: a disk at
// 75% is fine; a load at 0.75/core or any real swap use is not.
import type { DiskPart, ServerStats } from "./api";

export type Level = "ok" | "warn" | "crit";

const CUTS = {
  cpu: [0.7, 1.0],
  cpuPct: [0.8, 0.95], // busy share, where there is no load average
  mem: [0.75, 0.9],
  swap: [0.25, 0.6],
  disk: [0.8, 0.92],
} as const;

export type Metric = keyof typeof CUTS;

/** Level for a 0..1 fraction of the metric's capacity. */
export function level(metric: Metric, frac: number): Level {
  const [warn, crit] = CUTS[metric];
  if (frac >= crit) return "crit";
  if (frac >= warn) return "warn";
  return "ok";
}

export const rank: Record<Level, number> = { ok: 0, warn: 1, crit: 2 };

export const levelVar: Record<Level, string> = {
  ok: "var(--green)",
  warn: "var(--yellow)",
  crit: "var(--red)",
};

/** The fullest filesystem, or null when the probe listed none. A small
 *  root with a full data mount is the case the status bar must not hide. */
export function fullestPartition(s: ServerStats): DiskPart | null {
  let worst: DiskPart | null = null;
  for (const p of s.partitions ?? []) {
    if (!worst || p.used_pct > worst.used_pct) worst = p;
  }
  return worst;
}

/** The filesystem with the highest inode use, or null when none reports
 *  inodes. */
export function fullestInodes(s: ServerStats): DiskPart | null {
  let worst: DiskPart | null = null;
  for (const p of s.partitions ?? []) {
    if (p.inode_pct >= 0 && (!worst || p.inode_pct > worst.inode_pct)) worst = p;
  }
  return worst;
}
