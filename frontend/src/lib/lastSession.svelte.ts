// Reopen-last-session: continuous snapshot of the open tabs, restored
// once at startup after the vault unlocks. Chrome-style three-way
// behaviour: "ask" (default) prompts when the last session had tabs,
// "always" restores silently, "never" stays off.
//
// Snapshot scope: SSH tabs (connectionId), dynamic-inventory tabs
// (folderId + entryId, captured from the tree cache while it's warm)
// and local shells (shell kind). Split tabs are saved whole - the pane
// tree, its split directions and ratios, and one entry per pane (see
// paneSpec.ts, shared with the workspaces serializer). A pane whose
// session cannot be restored is dropped and its split collapsed.
//
// Save discipline matches window_state.go: saving is gated until the
// startup restore has run, otherwise the empty boot state would
// overwrite the tab set we're about to restore. Writes coalesce over
// a short window and flush() is wired to pagehide so a quit right
// after opening a tab doesn't lose it.

import { api } from "./api";
import { paneTabs, sessions, tree, view } from "./stores.svelte";
import { connectionActions, isTransientConnectError } from "./connectionActions.svelte";
import { showConfirm } from "./confirmModal.svelte.ts";
import {
  serializePaneSpec,
  restorePaneSpec,
  specFromFlat,
  type SessionSpec,
  type TabSpec,
} from "./paneSpec";
import { specForSession, connectSpec, beginRestore } from "./sessionSpec";
import { toast } from "./toast.svelte.ts";

const MODE_KEY = "reopen_last_session_mode";
const TABS_KEY = "last_session_tabs_v2";
// Read-only fallback: a v1 blob (one session per tab, no pane tree) is still
// restored on the first launch after the upgrade, then rewritten as v2.
const LEGACY_TABS_KEY = "last_session_tabs_v1";

export type ReopenMode = "ask" | "always" | "never";

interface SavedTab {
  kind: "ssh" | "dyn" | "local";
  connectionId?: string; // ssh
  folderId?: string;     // dyn
  entryId?: string;      // dyn - last-known row id (regenerates per refresh!)
  externalId?: string;   // dyn - provider-stable id, the real restore key
  entryName?: string;    // dyn - tab label + name-match fallback
  hostname?: string;     // dyn - hostname-match fallback
  shellKind?: string;    // local
  title?: string;
  groupName?: string;
  groupColor?: string;
}

class LastSessionStore {
  mode = $state<ReopenMode>("ask");
  restoring = $state<boolean>(false);

  private loaded = false;
  private restoreDone = false;
  // Dynamic folders whose entries were already pulled during THIS restore, so
  // a 25-host restore hits each provider once instead of once per host.
  private inventoryPulled = new Set<string>();
  private saveTimer: ReturnType<typeof setTimeout> | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const v = await api.settingsGet(MODE_KEY);
      if (v === "ask" || v === "always" || v === "never") this.mode = v;
    } catch { /* missing key fine */ }
    this.loaded = true;
  }

  setMode(v: ReopenMode) {
    if (this.mode === v) return;
    this.mode = v;
    api.settingsSet(MODE_KEY, v).catch(console.warn);
  }

  private serialize(): TabSpec[] {
    const out: TabSpec[] = [];
    for (const t of paneTabs.tabs) {
      const spec = serializePaneSpec(t.root, (sid) => specForSession(sid));
      // Every pane was unrestorable (a lone VNC console, say) - skip the tab
      // rather than writing an empty one.
      if (!spec) continue;
      out.push({
        title: t.title,
        titleCustom: t.titleCustom,
        groupName: t.groupName,
        groupColor: t.groupColor,
        sessions: spec.sessions,
        root: spec.root,
      });
    }
    return out;
  }

  // Coalesced snapshot - called from an $effect in App.svelte on every
  // tab/session change. Persists even in "never" mode so switching
  // away from it later restores the genuinely-last session, not the
  // last one from before the mode flip.
  schedule() {
    // No saves before the startup restore decision, and none during
    // the restore itself - a kill mid-restore must not overwrite the
    // saved set with a partially-rebuilt one.
    if (!this.restoreDone || this.restoring) return;
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      this.saveTimer = null;
      this.flush();
    }, 150);
  }

  // Write the snapshot now. Also wired to window pagehide from
  // App.svelte so a quit with a coalesce window still pending gets
  // its final state out (best effort - IPC is async).
  flush() {
    if (!this.restoreDone || this.restoring) return;
    if (this.saveTimer) {
      clearTimeout(this.saveTimer);
      this.saveTimer = null;
    }
    api.settingsSet(TABS_KEY, JSON.stringify(this.serialize())).catch(console.warn);
  }

  // Restore once after the first vault unlock. `recovered` = number of
  // tabs the backend session-recovery already brought back (UI reload
  // case) - if anything survived, this was not a cold start and
  // restoring on top would duplicate tabs.
  async restoreOnStartup(recovered: number) {
    if (this.restoreDone) return;
    this.restoreDone = true;
    if (recovered > 0 || this.mode === "never") return;

    let saved: TabSpec[] = [];
    try {
      const raw = await api.settingsGet(TABS_KEY);
      if (raw) {
        saved = JSON.parse(raw);
      } else {
        // First launch after the upgrade: read the flat v1 snapshot and lift
        // each row to a one-pane spec. The next flush writes v2.
        const legacy = await api.settingsGet(LEGACY_TABS_KEY);
        if (legacy) saved = upgradeLegacyTabs(JSON.parse(legacy));
      }
    } catch { /* missing or corrupt - nothing to restore */ }
    if (!Array.isArray(saved) || saved.length === 0) return;

    if (this.mode === "ask") {
      const n = saved.length;
      const ok = await showConfirm({
        title: "Reopen last session?",
        message: `Reconnect ${n} tab${n === 1 ? "" : "s"} from the last session? You can change this behaviour in Settings - Window - Startup.`,
        okLabel: "Reopen",
      });
      // Declining keeps the snapshot intact: schedule() won't fire
      // until a tab/session mutation happens, and by then the user
      // has moved on deliberately.
      if (!ok) return;
    }

    this.restoring = true;
    beginRestore();
    // Connects run sequentially (preserves tab order) and each one
    // blocks until auth+PTY, so a slow host opens its tab visibly
    // late. The toasts attribute those stragglers - without them a
    // terminal popping up 30s after launch looks like a ghost.
    toast.ok(`Reopening ${saved.length} tab${saved.length === 1 ? "" : "s"} from the last session…`);
    let opened = 0;
    try {
      for (const spec of saved) {
        try {
          await this.restoreOne(spec);
          opened++;
        } catch (e: any) {
          toast.err(`Reopen failed: ${e?.message ?? String(e)}`);
        }
      }
    } finally {
      this.restoring = false;
      if (opened < saved.length) {
        toast.ok(`Reopened ${opened} of ${saved.length} tabs`);
      }
      // Persist the rebuilt state once - failed entries drop out so
      // they don't error again on every start.
      this.flush();
    }
  }

  private async restoreOne(spec: TabSpec) {
    if (!spec?.root) return;
    const built = await restorePaneSpec(spec, (one) => connectSpec(one));
    if (!built) throw new Error(`${spec.title || "tab"}: nothing could be reconnected`);
    const tab = paneTabs.addTabFromLayout({
      title: spec.title ?? "",
      root: built.root,
      groupName: spec.groupName,
      groupColor: spec.groupColor,
    });
    if (spec.titleCustom && spec.title) paneTabs.setTitle(tab.tabId, spec.title, true);
    // A tab title is normally derived from the connection; a saved one that
    // is empty must not blank the restored tab.
    if (!spec.title) {
      const first = sessions.tabs.find((x) => x.sessionId === built.sessionIds[0]);
      if (first) paneTabs.setTitle(tab.tabId, first.name);
    }
    view.setTab("terminal");
  }

}

// Lift a v1 snapshot (flat, one session per tab) into v2 tab specs.
function upgradeLegacyTabs(rows: unknown): TabSpec[] {
  if (!Array.isArray(rows)) return [];
  const out: TabSpec[] = [];
  for (const r of rows as SavedTab[]) {
    if (!r) continue;
    const kind = r.kind ?? (r.connectionId ? "ssh" : undefined);
    if (!kind) continue;
    const { title, groupName, groupColor, ...session } = r;
    out.push(specFromFlat({ ...session, kind } as SessionSpec, { title, groupName, groupColor }));
  }
  return out;
}

export const lastSession = new LastSessionStore();
