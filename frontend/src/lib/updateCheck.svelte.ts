// Update-check state shared between the boot-time fetch in
// App.svelte and the status-bar pill that surfaces it.
//
// Reactivity model: a tiny class with $state fields. App.svelte
// kicks the initial check 5 s after boot and re-checks every 6 h
// while the app stays open; StatusBar reads `available` to decide
// whether to render the pill.

import { api } from "./api";
import { toast } from "./toast.svelte.ts";
import type { PluginInfo } from "./api";

class UpdateCheckStore {
  current = $state<string>("");
  latest = $state<string>("");
  available = $state<boolean>(false);
  changelogURL = $state<string>("");
  downloadURL = $state<string>("");
  downloadSize = $state<number>(0);
  lastCheckedAt = $state<number>(0);
  lastError = $state<string>("");
  // Installed helpers with a newer helper release; the status bar shows a
  // pill while this is non-empty.
  pluginUpdates = $state<{ name: string; label: string; version: string; latest: string }[]>([]);

  async run() {
    try {
      const res = await api.checkForUpdate();
      this.current = res.current;
      this.latest = res.latest;
      this.available = res.is_newer;
      this.changelogURL = res.changelog_url;
      this.downloadURL = res.download_url;
      this.downloadSize = res.download_size ?? 0;
      this.lastCheckedAt = Date.now();
      this.lastError = res.error ?? "";
    } catch (e: any) {
      this.lastError = e?.message ?? String(e);
    }
    if (!this.lastError) await this.checkPlugins();
  }

  // Installed NetBird / Tailscale helpers with a newer helper release get
  // one toast per (plugin, release) per run of the app; a click opens
  // Settings -> Network profiles, where the Update button is. Helpers ship
  // on their own release line, so the app's own update pill does not
  // cover them.
  private notifiedPlugins = new Set<string>();
  private async checkPlugins() {
    let list: PluginInfo[] = [];
    try { list = (await api.pluginsStatus()) ?? []; } catch { return; }
    this.applyPluginStatus(list);
    for (const u of this.pluginUpdates) {
      const key = `${u.name}@${u.latest}`;
      if (this.notifiedPlugins.has(key)) continue;
      this.notifiedPlugins.add(key);
      toast.info(`${u.label} plugin update: ${u.version || "installed"} -> ${u.latest}. Click to open Plugins.`, 12000, openPlugins);
    }
  }

  // Settings calls this with its own fresh status (after an install or
  // remove), so the status bar pill clears without waiting for the next
  // 6-hour check.
  applyPluginStatus(list: PluginInfo[]) {
    this.pluginUpdates = list
      .filter((p) => p.installed && p.update_available && p.latest)
      .map((p) => ({
        name: p.name,
        label: p.name === "netbird" ? "NetBird" : p.name === "tailscale" ? "Tailscale" : p.name,
        version: p.version,
        latest: p.latest,
      }));
  }
}

// Settings -> Network profiles holds the Plugins cards and their Update
// button. Imported lazily: stores imports half the app.
export function openPlugins() {
  void import("./stores.svelte").then(({ view }) => view.setTabSettingsSection("network"));
}

export const updateCheck = new UpdateCheckStore();
