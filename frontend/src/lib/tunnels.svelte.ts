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

class TunnelsStore {
  list = $state<TunnelInfo[]>([]);
  private subscribed = false;
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
        } while (this.again);
      } catch (e) {
        console.warn("tunnels list failed", e);
      } finally {
        this.inflight = null;
      }
    })();
    return this.inflight;
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
    return this.list.filter((t) => t.state === "error" || t.state === "reconnecting").length;
  }

  async start(connectionId: string) {
    try {
      await api.tunnelStart(connectionId);
    } catch (e) {
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
