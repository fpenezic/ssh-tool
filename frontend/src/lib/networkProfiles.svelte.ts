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

  async load(force = false) {
    if (this.loaded && !force) return;
    this.loading = true;
    this.error = null;
    try {
      this.list = (await api.networkProfilesList()) ?? [];
      this.loaded = true;
    } catch (e) {
      this.error = String(e);
    } finally {
      this.loading = false;
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
