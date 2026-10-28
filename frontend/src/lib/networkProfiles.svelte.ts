// Shared store for network profiles (userspace WireGuard). Loaded
// lazily by the first consumer (Settings manager card, DetailPane
// network dropdown); refreshed on the backend's network_tunnel_changed
// event so tunnel status pills stay live.

import { api, type NetworkProfileInfo } from "./api";
import { EventsOn } from "./wailsRuntime";

class NetworkProfilesStore {
  list = $state<NetworkProfileInfo[]>([]);
  loading = $state(false);
  error = $state<string | null>(null);
  private loaded = false;
  private subscribed = false;

  // Loads overlap (a write's refresh, a tunnel event): only the newest
  // one's answer is applied, so an older list cannot land last.
  private gen = 0;

  async load(force = false) {
    if (this.loaded && !force) return;
    const gen = ++this.gen;
    this.loading = true;
    this.error = null;
    try {
      const list = (await api.networkProfilesList()) ?? [];
      if (gen === this.gen) {
        this.list = list;
        this.loaded = true;
      }
    } catch (e) {
      if (gen === this.gen) this.error = String(e);
    } finally {
      if (gen === this.gen) this.loading = false;
    }
    if (!this.subscribed) {
      this.subscribed = true;
      EventsOn("network_tunnel_changed", () => {
        this.load(true).catch(() => {});
      });
    }
  }

  byId(id: string): NetworkProfileInfo | undefined {
    return this.list.find((p) => p.id === id);
  }

  nameOf(id: string): string {
    return this.byId(id)?.name ?? id;
  }
}

export const networkProfiles = new NetworkProfilesStore();
