// Update-check state shared between the boot-time fetch in
// App.svelte and the status-bar pill that surfaces it.
//
// Reactivity model: a tiny class with $state fields. App.svelte
// kicks the initial check 5 s after boot and re-checks every 6 h
// while the app stays open; StatusBar reads `available` to decide
// whether to render the pill.

import { api } from "./api";

class UpdateCheckStore {
  current = $state<string>("");
  latest = $state<string>("");
  available = $state<boolean>(false);
  changelogURL = $state<string>("");
  downloadURL = $state<string>("");
  downloadSize = $state<number>(0);
  lastCheckedAt = $state<number>(0);
  lastError = $state<string>("");

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
  }
}

export const updateCheck = new UpdateCheckStore();
