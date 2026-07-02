// Persistent mirror of "the remote sync snapshot is ahead of this
// machine". The toast is easy to miss; this store backs the status
// bar pill that stays lit until the situation resolves (pull +
// restart, a successful push, or a manual status check that comes
// back clean).

import { EventsOn } from "./wailsRuntime";
import { api } from "./api";
import { toast } from "./toast.svelte";
import { showConfirm } from "./confirmModal.svelte.ts";
import { view } from "./stores.svelte";
import { errMsg } from "./connectErrors";

export interface RemoteAheadInfo {
  generation: number;
  device: string;
  updated_at: string;
  autoApply: boolean;
}

// uiIsIdle: safe to swap the profile under the user only when nothing
// is being edited. A pull replaces the whole tree, so applying it
// while a text field has focus (mid-edit) or a modal is open (cred
// editor, folder picker) would yank state out from under them. Cheap
// DOM probe, no global instrumentation needed.
function uiIsIdle(): boolean {
  const el = document.activeElement as HTMLElement | null;
  if (el) {
    const tag = el.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable) {
      return false;
    }
  }
  // Any open modal overlay means a focused task; defer.
  if (document.querySelector(".overlay, .ctx-backdrop")) {
    return false;
  }
  return true;
}

class SyncStateStore {
  remoteAhead = $state<RemoteAheadInfo | null>(null);
  // When an auto-apply was deferred because the UI was busy, retry on
  // the next idle moment instead of waiting for the next remote check.
  private autoApplyPending = false;

  private wired = false;

  init(onRemoteAhead?: (info: RemoteAheadInfo) => void) {
    if (this.wired) return;
    this.wired = true;
    EventsOn("sync_remote_ahead", (info: any) => {
      const ra: RemoteAheadInfo = {
        generation: Number(info?.generation ?? 0),
        device: String(info?.device ?? ""),
        updated_at: String(info?.updated_at ?? ""),
        autoApply: !!info?.auto_apply,
      };
      this.remoteAhead = ra;
      if (ra.autoApply) {
        // Auto-apply handles it silently - no "click to pull" toast,
        // it would be noise for a change that applies itself.
        this.tryAutoApply();
      } else {
        onRemoteAhead?.(ra);
      }
    });
    // A successful auto-push means the guard passed - we were not
    // behind, so any stale pill is wrong.
    EventsOn("sync_auto_pushed", () => {
      this.remoteAhead = null;
    });
  }

  clear() {
    this.remoteAhead = null;
  }

  // quickPull: the toast/pill click handler. When the local profile
  // is clean (no changes since its last push) the pull is lossless,
  // so one confirm dialog does the whole thing right there. Local
  // changes mean a real conflict - that decision belongs in
  // Settings > Sync with the full picture, not in a toast.
  async quickPull() {
    let st;
    try {
      st = await api.syncStatus();
    } catch (e) {
      toast.err(errMsg(e));
      view.setTabSettingsSection("sync");
      return;
    }
    if (st.state !== "remote_ahead") {
      this.clear();
      return;
    }
    // Local changes = a real conflict; that decision needs the full
    // Settings view, not a one-tap pull.
    if (st.local_dirty) {
      view.setTabSettingsSection("sync");
      return;
    }
    // Clean local -> lossless. Apply live, no confirm, no restart:
    // the goal is zero friction for "another machine has newer data".
    await this.applyLive();
  }

  // applyLive runs the no-restart pull: store mirrored into the running
  // app, vault merged in place. Frontend stores reload off the
  // profile_reloaded event. Only the rare different-vault-passphrase
  // case falls back to a restart.
  async applyLive() {
    try {
      const res = await api.syncPullLive();
      this.clear();
      if (res.vault_restart_needed) {
        await this.offerRestart(
          "Profile updated. The passwords/keys differ and need a restart to apply - the rest is already live.",
        );
      } else {
        toast.ok(`Synced from ${res.device}`, 3000);
      }
    } catch (e) {
      toast.err(errMsg(e));
      view.setTabSettingsSection("sync");
    }
  }

  // tryAutoApply applies an incoming change in the background when the
  // UI is idle. If the user is mid-edit, it defers and retries on the
  // next idle moment (focus change / mouse activity) - never yanks the
  // profile out from under an open editor.
  private tryAutoApply() {
    if (uiIsIdle()) {
      this.autoApplyPending = false;
      void this.applyLiveSilent();
      return;
    }
    if (this.autoApplyPending) return; // already waiting for idle
    this.autoApplyPending = true;
    const onActivity = () => {
      if (!this.autoApplyPending) {
        cleanup();
        return;
      }
      if (uiIsIdle()) {
        this.autoApplyPending = false;
        cleanup();
        void this.applyLiveSilent();
      }
    };
    const cleanup = () => {
      window.removeEventListener("focusout", onActivity, true);
      window.removeEventListener("mousemove", onActivity);
      window.removeEventListener("keyup", onActivity);
    };
    window.addEventListener("focusout", onActivity, true);
    window.addEventListener("mousemove", onActivity, { passive: true });
    window.addEventListener("keyup", onActivity);
  }

  // applyLiveSilent is the auto-apply variant: same live pull, but a
  // single quiet toast on success and no Settings redirect on a
  // transient error (it'll retry next check). Re-checks idleness right
  // before firing in case the user started editing in the gap.
  private async applyLiveSilent() {
    if (!uiIsIdle()) {
      this.autoApplyPending = true;
      this.tryAutoApply();
      return;
    }
    try {
      const res = await api.syncPullLive();
      this.clear();
      if (res.vault_restart_needed) {
        // Secrets need a restart - surface it, don't silently swallow.
        await this.offerRestart(
          "Synced. New passwords/keys need a restart to apply - the rest is already live.",
        );
      } else {
        toast.info(`Auto-synced from ${res.device}`, 2500);
      }
    } catch (e) {
      // Leave the pill lit so a manual pull is still offered.
      console.warn("auto-apply pull:", errMsg(e));
    }
  }

  // offerRestart: the staged part of a pull applies on next start;
  // relaunch does quit + fresh start in one click.
  async offerRestart(message?: string) {
    const ok = await showConfirm({
      title: "Restart to finish",
      message: message ?? "The synced profile applies on the next start. Restart ssh-tool now? Live SSH sessions will be disconnected.",
      okLabel: "Restart now",
      cancelLabel: "Later",
    });
    if (!ok) {
      toast.info("Pulled - quit and reopen ssh-tool to apply", 10000);
      return;
    }
    try {
      await api.appRelaunch();
    } catch (e) {
      toast.err(errMsg(e));
    }
  }
}

export const syncState = new SyncStateStore();
