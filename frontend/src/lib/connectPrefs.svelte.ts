// Connection behaviour settings (Settings -> Connections -> Connection),
// beyond the connect timeout. The backend reads the reconnect and bastion
// keys itself; the bulk-connect ones are used here, by connectionActions.

import { api } from "./api";

export interface IntPref {
  key: string;
  def: number;
  min: number;
  max: number;
}

// Bulk connect (Connect all, multi-select + Enter). Every handshake that
// has not finished authenticating counts against the bastion's sshd
// MaxStartups (default 10:30:100): past 10 at once it starts dropping
// new ones, seen as "handshake failed: EOF". That is why parallelism is
// capped at 10 and the stagger never goes below 50 ms.
export const BULK_CONCURRENT: IntPref = { key: "bulk_connect_max_concurrent", def: 4, min: 1, max: 10 };
export const BULK_STAGGER_MS: IntPref = { key: "bulk_connect_stagger_ms", def: 150, min: 50, max: 5000 };
export const BULK_CONFIRM_ABOVE: IntPref = { key: "bulk_connect_confirm_threshold", def: 5, min: 1, max: 500 };

// Read by the backend (app.go runReconnect, app_jumppool.go); the limits
// here must match the clamps there.
export const RECONNECT_ATTEMPTS: IntPref = { key: "reconnect_max_attempts", def: 5, min: 1, max: 50 };
export const RECONNECT_MAX_DELAY: IntPref = { key: "reconnect_max_delay_seconds", def: 16, min: 1, max: 300 };
export const BASTION_LINGER: IntPref = { key: "bastion_linger_seconds", def: 10, min: 0, max: 3600 };

export const CONNECT_PREFS = [
  BULK_CONCURRENT, BULK_STAGGER_MS, BULK_CONFIRM_ABOVE,
  RECONNECT_ATTEMPTS, RECONNECT_MAX_DELAY, BASTION_LINGER,
];

export function clampPref(p: IntPref, v: number): number {
  if (!Number.isFinite(v)) return p.def;
  return Math.min(p.max, Math.max(p.min, Math.round(v)));
}

class ConnectPrefs {
  values = $state<Record<string, number>>(Object.fromEntries(CONNECT_PREFS.map((p) => [p.key, p.def])));
  private loaded = false;

  async load() {
    if (this.loaded) return;
    const next = { ...this.values };
    for (const p of CONNECT_PREFS) {
      try {
        const raw = await api.settingsGet(p.key);
        if (raw !== "" && raw != null) next[p.key] = clampPref(p, parseInt(raw, 10));
      } catch { /* unset - default */ }
    }
    this.values = next;
    this.loaded = true;
  }

  get(p: IntPref): number {
    return this.values[p.key] ?? p.def;
  }

  async set(p: IntPref, v: number) {
    const n = clampPref(p, v);
    this.values = { ...this.values, [p.key]: n };
    await api.settingsSet(p.key, String(n));
  }
}

export const connectPrefs = new ConnectPrefs();
