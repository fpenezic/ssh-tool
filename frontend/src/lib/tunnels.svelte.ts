// Shared store for the tunnel manager (app_tunnels.go): every tunnel-only
// connection with its live state. Refreshed on the backend's
// tunnels_changed event and whenever the tree reloads (a connection or
// folder edit can turn "Tunnel only" on or off). The status bar segment,
// the Tunnels panel, the tree rows and the default connect action all read
// it, so the default action knows without a round trip whether a
// double-click means "open a terminal" or "start the tunnel".

import { api, type TunnelInfo } from "./api";
import { EventsOn } from "./wailsRuntime";
import { errMsg } from "./connectErrors";
import { toast } from "./toast.svelte.ts";

// Detached windows mount their own status bar, and so their own copy of
// this store: an outcome nobody asked for by hand is told by the main window
// only, or every window would announce it.
const isMainWindow = typeof window === "undefined" ||
  !/(^|[?&#/])(detached|logtail)=/.test(window.location.search + window.location.hash);

class TunnelsStore {
  list = $state<TunnelInfo[]>([]);
  private subscribed = false;
  // Tunnels started by hand from this window, waiting for the outcome of
  // that start: connection id -> the start number (gen) the list showed
  // when the user clicked. Only a list with a newer gen can carry the
  // outcome; one already in flight before the click cannot.
  private pending = new Map<string, number>();
  private lastState = new Map<string, string>();
  private inflight: Promise<void> | null = null;
  private again = false;

  // Coalesces bursts (a start emits several events in a row): one request
  // in flight, at most one more queued behind it.
  async refresh(): Promise<void> {
    if (!this.subscribed) {
      this.subscribed = true;
      EventsOn("tunnels_changed", () => void this.refresh());
    }
    if (this.inflight) {
      this.again = true;
      return this.inflight;
    }
    this.inflight = (async () => {
      try {
        do {
          this.again = false;
          this.list = (await api.tunnelsList()) ?? [];
          this.announce();
        } while (this.again);
      } catch (e) {
        console.warn("tunnels list failed", e);
      } finally {
        this.inflight = null;
      }
    })();
    return this.inflight;
  }

  // Close the loop on a start by hand ("up, 3/3 forwards listening" or
  // "failed: ..."), told in the window that started it. A tunnel nobody
  // started by hand - the launch start, or a reconnect - is told in the main
  // window only, and only when it fails or comes up with a forward down,
  // several at once in one toast.
  private announce() {
    const seen = new Set<string>();
    const unwatched: string[] = [];
    for (const t of this.list) {
      seen.add(t.connection_id);
      const before = this.lastState.get(t.connection_id);
      this.lastState.set(t.connection_id, t.state);
      const mineGen = this.pending.get(t.connection_id);
      if (mineGen !== undefined && t.gen > mineGen && (t.state === "connected" || t.state === "error")) {
        this.pending.delete(t.connection_id);
        if (t.state === "error") {
          toast.err(`Tunnel ${t.name} failed: ${t.error ?? "unknown error"}`, 6000);
        } else {
          const up = t.forwards.filter((f) => f.state === "listening").length;
          const all = t.forwards.length;
          const fwd = all === 0 ? "no forwards configured" : `${up}/${all} forward${all === 1 ? "" : "s"} listening`;
          if (t.degraded) toast.err(`Tunnel ${t.name} is up, but only ${fwd}`, 6000);
          else toast.ok(`Tunnel ${t.name} up, ${fwd}`);
        }
        continue;
      }
      if (mineGen !== undefined && t.state === "stopped" && t.gen === 0 && before !== undefined && before !== "stopped") {
        this.pending.delete(t.connection_id); // stopped before it got anywhere
      }
      // A start by hand is reported by the window that asked; skip it here.
      if (!isMainWindow || t.by_hand || before === undefined || before === t.state) continue;
      if (t.state === "error") unwatched.push(`${t.name}: ${t.error ?? "unknown error"}`);
      else if (t.state === "connected" && t.degraded) unwatched.push(`${t.name}: up, but a forward is not listening`);
    }
    for (const id of [...this.lastState.keys()]) if (!seen.has(id)) this.lastState.delete(id);
    for (const id of [...this.pending.keys()]) if (!seen.has(id)) this.pending.delete(id);
    if (unwatched.length === 1) {
      toast.err(`Tunnel ${unwatched[0]}`, 6000);
    } else if (unwatched.length > 1) {
      toast.err(`${unwatched.length} tunnels need attention - ${unwatched.join("; ")}`, 8000);
    }
  }

  byConnection(connectionId: string): TunnelInfo | undefined {
    return this.list.find((t) => t.connection_id === connectionId);
  }

  // A connection whose default action is the tunnel, not a terminal.
  isTunnel(connectionId: string): boolean {
    return !!this.byConnection(connectionId);
  }

  running(t: TunnelInfo): boolean {
    return t.state === "connected" || t.state === "connecting" || t.state === "reconnecting";
  }

  get upCount(): number {
    return this.list.filter((t) => t.state === "connected").length;
  }

  get troubleCount(): number {
    return this.list.filter((t) => t.state === "error" || t.state === "reconnecting" || t.degraded).length;
  }

  // A start by hand: the outcome is announced (see announce).
  async start(connectionId: string) {
    // Starting one that is already up is a no-op with nothing to report.
    const t = this.byConnection(connectionId);
    if (!(t && this.running(t))) this.pending.set(connectionId, t?.gen ?? 0);
    try {
      await api.tunnelStart(connectionId);
    } catch (e) {
      this.pending.delete(connectionId);
      toast.err(errMsg(e));
    }
    void this.refresh();
  }

  async stop(connectionId: string) {
    try {
      await api.tunnelStop(connectionId);
    } catch (e) {
      toast.err(errMsg(e));
    }
    void this.refresh();
  }

  async toggle(connectionId: string) {
    const t = this.byConnection(connectionId);
    if (t && this.running(t)) await this.stop(connectionId);
    else await this.start(connectionId);
  }
}

export const tunnels = new TunnelsStore();
