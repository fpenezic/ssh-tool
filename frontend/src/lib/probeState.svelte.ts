// Liveness-probe state for connection rows inside expanded folders.
//
// A rendered/expanded TreeNode registers its connection ids as "visible"; the
// store TCP-probes them (backend ProbeConnections, WG/bastion aware, never
// forces a tunnel) and paints an up/down/unknown dot. Only visible ids are
// ever probed, so a 300-host tree still probes just what's on screen. Visible
// ids are re-probed on a 30s interval and immediately (debounced) when the set
// grows on expand. Collapsing deregisters, so probing stops.
//
// Gated by the global liveness_probe_enabled setting (loaded once); when off,
// register() is a no-op and no dials happen.

import { api } from "./api";

export type ProbeState = "up" | "down" | "unknown";

const DEFAULT_REPROBE_SEC = 60;
const MIN_REPROBE_SEC = 10;
const DEBOUNCE_MS = 250;

class ProbeStore {
  // connectionId -> last known state. Version bumps on any change so $derived
  // consumers (the dot) re-read.
  private states = new Map<string, ProbeState>();
  version = $state(0);

  // Enabled mirrors the global setting; loaded once, refreshable from Settings.
  private enabled = false;
  private enabledLoaded = false;
  // Re-probe cadence in seconds (configurable in Settings, floored at 10s).
  private reprobeSec = DEFAULT_REPROBE_SEC;

  // Reference counts of visible registrations per connection id. A connection
  // can appear once per rendered row; count so overlapping registers/unregisters
  // (re-render churn) don't drop a still-visible id.
  private visible = new Map<string, number>();

  private reprobeTimer: ReturnType<typeof setInterval> | null = null;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  // Guards against overlapping rounds: a big "expand all" probe can take longer
  // than the 30s interval, and firing a second round on top of it would double
  // the socket load and thrash the worker pool. Skip if one is still running.
  private inFlight = false;

  async loadEnabled(): Promise<boolean> {
    if (this.enabledLoaded) return this.enabled;
    try {
      this.enabled = (await api.settingsGet("liveness_probe_enabled")) === "1";
    } catch {
      this.enabled = false;
    }
    try {
      const raw = await api.settingsGet("liveness_probe_interval_sec");
      const n = parseInt(raw, 10);
      if (Number.isFinite(n) && n >= MIN_REPROBE_SEC) this.reprobeSec = n;
    } catch { /* keep default */ }
    this.enabledLoaded = true;
    return this.enabled;
  }

  intervalSec(): number {
    return this.reprobeSec;
  }

  // setIntervalSec is called by the Settings input; restarts the timer with the
  // new cadence (floored at MIN_REPROBE_SEC).
  setIntervalSec(sec: number) {
    this.reprobeSec = Math.max(MIN_REPROBE_SEC, Math.floor(sec) || DEFAULT_REPROBE_SEC);
    if (this.reprobeTimer) {
      this.stopTimer();
      this.ensureTimer();
    }
  }

  // setEnabled is called by the Settings toggle so the change takes effect
  // without a reload. Turning off clears state + stops the timer; turning on
  // probes whatever is currently visible.
  setEnabled(on: boolean) {
    this.enabled = on;
    this.enabledLoaded = true;
    if (!on) {
      this.states.clear();
      this.stopTimer();
      this.version++;
      return;
    }
    this.ensureTimer();
    this.probeVisible();
  }

  stateOf(id: string): ProbeState | null {
    void this.version;
    return this.states.get(id) ?? null;
  }

  // register/unregister track which connection ids are on screen. Idempotent
  // per call site via refcount.
  register(id: string) {
    if (!this.enabled || !id) return;
    const n = (this.visible.get(id) ?? 0) + 1;
    this.visible.set(id, n);
    if (n === 1) {
      this.ensureTimer();
      this.scheduleProbe(); // debounced: batch a burst of expands into one call
    }
  }

  unregister(id: string) {
    if (!id) return;
    const n = (this.visible.get(id) ?? 0) - 1;
    if (n <= 0) {
      this.visible.delete(id);
    } else {
      this.visible.set(id, n);
    }
    if (this.visible.size === 0) this.stopTimer();
  }

  private scheduleProbe() {
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => {
      this.debounceTimer = null;
      this.probeVisible();
    }, DEBOUNCE_MS);
  }

  private ensureTimer() {
    if (this.reprobeTimer || !this.enabled) return;
    this.reprobeTimer = setInterval(() => this.probeVisible(), this.reprobeSec * 1000);
  }

  private stopTimer() {
    if (this.reprobeTimer) {
      clearInterval(this.reprobeTimer);
      this.reprobeTimer = null;
    }
  }

  private async probeVisible() {
    if (!this.enabled || this.inFlight) return;
    const ids = [...this.visible.keys()];
    if (ids.length === 0) return;
    this.inFlight = true;
    try {
      const results = await api.probeConnections(ids);
      let changed = false;
      for (const r of results ?? []) {
        if (this.states.get(r.connection_id) !== r.state) {
          this.states.set(r.connection_id, r.state as ProbeState);
          changed = true;
        }
      }
      // Drop states for ids no longer visible.
      for (const id of [...this.states.keys()]) {
        if (!this.visible.has(id)) {
          this.states.delete(id);
          changed = true;
        }
      }
      if (changed) this.version++;
    } catch {
      // transient - keep last states, try again next interval
    } finally {
      this.inFlight = false;
    }
  }
}

export const probeState = new ProbeStore();
