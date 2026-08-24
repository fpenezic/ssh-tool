// Opt-in restore of the previous run's broadcast groups.
//
// Groups persist as NAMES only and come back empty - their members are
// session IDs from a dead run. Even so, re-creating them silently is a
// surprise: the user quits with three groups and finds three empty groups
// waiting, which reads as leftover state rather than a feature.
//
// So it follows the same three-way shape as reopen-last-session:
// "ask" (default) prompts when there is something to restore, "always"
// restores silently, "never" stays off.

import { api } from "./api";
import { showConfirm } from "./confirmModal.svelte.ts";

const MODE_KEY = "restore_broadcast_groups_mode";

export type BroadcastRestoreMode = "ask" | "always" | "never";

class BroadcastRestoreStore {
  mode = $state<BroadcastRestoreMode>("ask");

  private loaded = false;
  private ran = false;

  async load() {
    if (this.loaded) return;
    try {
      const v = await api.settingsGet(MODE_KEY);
      if (v === "ask" || v === "always" || v === "never") this.mode = v;
    } catch { /* missing key = default "ask" */ }
    this.loaded = true;
  }

  async setMode(m: BroadcastRestoreMode) {
    this.mode = m;
    try { await api.settingsSet(MODE_KEY, m); } catch { /* best effort */ }
  }

  /**
   * Run once per launch, after the vault is ready.
   *
   * Declining does NOT forget the saved groups - the user may just not want
   * them this session. Only switching the mode to "never" clears them, so a
   * single "no" is never destructive.
   */
  async maybeRestore(): Promise<void> {
    if (this.ran) return;
    this.ran = true;
    await this.load();
    if (this.mode === "never") return;

    let saved: string[] = [];
    try {
      saved = (await api.broadcastSavedGroups()) ?? [];
    } catch {
      return;
    }
    if (saved.length === 0) return;

    if (this.mode === "ask") {
      const n = saved.length;
      const ok = await showConfirm({
        title: "Restore broadcast groups?",
        message:
          `Re-create ${n} broadcast group${n === 1 ? "" : "s"} from the last session ` +
          `(${saved.join(", ")})? They come back empty - you pick the sessions. ` +
          `You can change this in Settings - Window - Startup.`,
        okLabel: "Restore",
      });
      if (!ok) return;
    }

    try {
      await api.broadcastRestoreSaved();
    } catch { /* nothing to show the user - the groups simply stay absent */ }
  }
}

export const broadcastRestore = new BroadcastRestoreStore();
