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
import { paneTabs, sessions, tree, view, type PaneTab } from "./stores.svelte";
import { detachTabsToWindow } from "./detachTabs";
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

// The open tabs of THIS window as restorable specs. The main window saves
// them under TABS_KEY; a detached window hands them to the backend
// (DetachedSessionSave), which keeps them only when the app quits with
// the window open.
export function serializeOpenTabs(): TabSpec[] {
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
      workspaceId: t.workspaceId,
      sessions: spec.sessions,
      root: spec.root,
    });
  }
  return out;
}

function parseSpecs(raw: string): TabSpec[] {
  try {
    const v = JSON.parse(raw);
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

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
  // The JSON last written, so a rerun that changes nothing saved (focus,
  // output, a status tick) does not write again.
  private lastSaved = "";

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

  // The serialized tab set as JSON. Called inside an $effect so every
  // reactive value it reads becomes a dependency.
  snapshot(): string {
    return JSON.stringify(serializeOpenTabs());
  }

  // Coalesced snapshot - called from an $effect in App.svelte on every
  // tab/session change. Persists even in "never" mode so switching
  // away from it later restores the genuinely-last session, not the
  // last one from before the mode flip.
  schedule(snap?: string) {
    // No saves before the startup restore decision, and none during
    // the restore itself - a kill mid-restore must not overwrite the
    // saved set with a partially-rebuilt one.
    if (!this.restoreDone || this.restoring) return;
    if (snap !== undefined && snap === this.lastSaved) return;
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      this.saveTimer = null;
      this.flush();
    }, 150);
  }

  // Write the snapshot now. Also wired to window pagehide from
  // App.svelte so a quit with a coalesce window still pending gets
  // its final state out (best effort - IPC is async).
  flush(onlyIfChanged = false) {
    if (!this.restoreDone || this.restoring) return;
    if (this.saveTimer) {
      clearTimeout(this.saveTimer);
      this.saveTimer = null;
    }
    const json = this.snapshot();
    if (onlyIfChanged && json === this.lastSaved) return;
    this.lastSaved = json;
    api.settingsSet(TABS_KEY, json).catch(console.warn);
  }

  // Restore once after the first vault unlock. `recovered` = number of
  // tabs the backend session-recovery already brought back (UI reload
  // case) - if anything survived, this was not a cold start and
  // restoring on top would duplicate tabs.
  async restoreOnStartup(recovered: number) {
    if (this.restoreDone) return;
    this.restoreDone = true;
    // A UI reload: the detached windows are still open and saving.
    if (recovered > 0) return;
    // The windows detached when the app last quit, one tab list each.
    // Taken (and so forgotten) even in "never" mode, or they would pile up.
    const windows = ((await api.detachedSessionTake().catch(() => [])) ?? [])
      .map(parseSpecs)
      .filter((w) => w.length > 0);
    if (this.mode === "never") return;

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
    if (!Array.isArray(saved)) saved = [];
    const inWindows = windows.reduce((n, w) => n + w.length, 0);
    if (saved.length === 0 && inWindows === 0) return;

    if (this.mode === "ask") {
      const n = saved.length + inWindows;
      const where = inWindows > 0 ? ` (${inWindows} in ${windows.length === 1 ? "a separate window" : `${windows.length} separate windows`})` : "";
      const ok = await showConfirm({
        title: "Reopen last session?",
        message: `Reconnect ${n} tab${n === 1 ? "" : "s"}${where} from the last session? You can change this behaviour in Settings - Window - Startup.`,
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
    const total = saved.length + inWindows;
    toast.ok(`Reopening ${total} tab${total === 1 ? "" : "s"} from the last session…`);
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
      // Each detached window: its tabs are rebuilt here, then moved out
      // into a window of their own the way a tab is detached by hand.
      for (const specs of windows) {
        const built: PaneTab[] = [];
        for (const spec of specs) {
          try {
            built.push(await this.restoreOne(spec));
            opened++;
          } catch (e: any) {
            toast.err(`Reopen failed: ${e?.message ?? String(e)}`);
          }
        }
        await detachTabsToWindow(built).catch((e) => {
          toast.err(`Reopening a detached window failed: ${e?.message ?? String(e)}`);
        });
      }
    } finally {
      this.restoring = false;
      if (opened < total) {
        toast.ok(`Reopened ${opened} of ${total} tabs`);
      }
      // Persist the rebuilt state once - failed entries drop out so
      // they don't error again on every start.
      this.flush();
    }
  }

  private async restoreOne(spec: TabSpec): Promise<PaneTab> {
    if (!spec?.root) throw new Error(`${spec?.title || "tab"}: nothing saved`);
    const built = await restorePaneSpec(spec, (one) => connectSpec(one));
    if (!built) throw new Error(`${spec.title || "tab"}: nothing could be reconnected`);
    const tab = paneTabs.addTabFromLayout({
      title: spec.title ?? "",
      root: built.root,
      groupName: spec.groupName,
      groupColor: spec.groupColor,
      workspaceId: spec.workspaceId,
    });
    if (spec.titleCustom && spec.title) paneTabs.setTitle(tab.tabId, spec.title, true);
    // A tab title is normally derived from the connection; a saved one that
    // is empty must not blank the restored tab.
    if (!spec.title) {
      const first = sessions.tabs.find((x) => x.sessionId === built.sessionIds[0]);
      if (first) paneTabs.setTitle(tab.tabId, first.name);
    }
    view.setTab("terminal");
    return tab;
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
