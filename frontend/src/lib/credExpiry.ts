// Shared expiry helpers for credentials with a time-limited secret
// (API tokens, setup / auth keys). expires_at is a unix timestamp or
// null. Used by the credential list badge and the detail view.

export type ExpiryLevel = "none" | "ok" | "soon" | "expired";

// How many days out counts as "expiring soon" (amber warning).
const SOON_DAYS = 14;

export interface ExpiryInfo {
  level: ExpiryLevel;
  days: number; // whole days until expiry; negative = past
  label: string; // short human text, "" when level === "none"
}

export function expiryInfo(expiresAt: number | null | undefined, now = Date.now()): ExpiryInfo {
  if (!expiresAt) return { level: "none", days: 0, label: "" };
  const ms = expiresAt * 1000 - now;
  const days = Math.floor(ms / 86_400_000);
  if (days < 0) {
    const d = Math.abs(days);
    return { level: "expired", days, label: d === 0 ? "expired today" : `expired ${d}d ago` };
  }
  if (days === 0) return { level: "soon", days, label: "expires today" };
  if (days <= SOON_DAYS) return { level: "soon", days, label: `expires in ${days}d` };
  return { level: "ok", days, label: `expires in ${days}d` };
}

export interface ExpiringCredential {
  id: string;
  name: string;
  info: ExpiryInfo;
}

// expiringCredentials picks the credentials the status bar should warn
// about - expired or inside the SOON_DAYS window - most urgent first, so
// the badge can open the one that needs attention.
export function expiringCredentials(
  list: { id: string; name: string; expires_at: number | null }[],
  now = Date.now(),
): ExpiringCredential[] {
  const out: ExpiringCredential[] = [];
  for (const c of list) {
    const info = expiryInfo(c.expires_at, now);
    if (info.level === "soon" || info.level === "expired") out.push({ id: c.id, name: c.name, info });
  }
  return out.sort((a, b) => a.info.days - b.info.days);
}

// ----- opkssh cert lifetime -----
//
// opkssh certs are short-lived and their lifetime is set per provider
// (hours for some, a week for others), so a fixed "N days" window would
// warn all the time for one and never for another. The warning is a
// fraction of the cert's own lifetime instead: amber with a quarter
// left, red with a tenth left.

export type OpksshLevel = "ok" | "soon" | "critical" | "expired";

const OPK_SOON = 0.25;
const OPK_CRITICAL = 0.1;

export interface OpksshExpiry {
  level: OpksshLevel;
  remaining: number; // seconds, negative once past
  fraction: number;  // of the lifetime still left, 0..1
  label: string;
}

export function fmtDuration(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const d = Math.floor(s / 86_400);
  const h = Math.floor((s % 86_400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function opksshExpiry(start: number, end: number, now = Date.now()): OpksshExpiry {
  const t = now / 1000;
  const remaining = end - t;
  const life = end - start;
  const fraction = life > 0 ? Math.max(0, Math.min(1, remaining / life)) : 0;
  if (remaining <= 0) return { level: "expired", remaining, fraction: 0, label: "sign-in due" };
  const label = `sign-in in ${fmtDuration(remaining)}`;
  if (fraction < OPK_CRITICAL) return { level: "critical", remaining, fraction, label };
  if (fraction < OPK_SOON) return { level: "soon", remaining, fraction, label };
  return { level: "ok", remaining, fraction, label };
}
