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
