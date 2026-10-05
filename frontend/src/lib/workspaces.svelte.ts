// Workspaces - named bundles of "these tabs in this layout" the user
// can switch between.
//   - Serialise: every open tab's pane tree, one session spec per pane
//     (split directions and ratios included), plus title + group metadata.
//   - Restore: reconnect each pane and rebuild the tabs with their layout
//     and group label, next to whatever is already open. The restored tabs
//     carry the workspace id and sit in their own frame in the tab bar;
//     "Save changes" writes that frame, not every open tab.
//   - Persist via backend store (workspaces table, migration 10).
//
// The serialiser writes a JSON shape the backend treats as opaque
// (just a TEXT column). Versioned so we can evolve the schema later.
//
// Version 2 added the pane tree; version 1 stored one connectionId per tab
// and collapsed splits to the active pane. Both are readable - a v1 workspace
// keeps working and is upgraded in memory on open, and rewritten as v2 the
// next time the user overwrites it.
//
// The pane tree lives in paneSpec.ts and the per-session translation in
// sessionSpec.ts, both shared with reopen-last-session.

import { api, type Workspace } from "./api";
import { paneTabs, sessions, view, type PaneTab } from "./stores.svelte";
import {
  serializePaneSpec,
  restorePaneSpec,
  upgradeWorkspaceTabs,
  PANE_SPEC_VERSION,
  type TabSpec,
  type SessionSpec,
} from "./paneSpec";
import { specForSession, connectSpec, beginRestore } from "./sessionSpec";
import { toast } from "./toast.svelte.ts";

export const WORKSPACE_VERSION = PANE_SPEC_VERSION;

/** Version 1 tab shape, still found in workspaces saved before pane trees. */
export interface WorkspaceTabSpecV1 {
  connectionId: string;
  title?: string;
  groupName?: string;
  groupColor?: string;
}

export interface WorkspaceLayout {
  version: number;
  tabs: TabSpec[];
}

const COMPACT_KEY = "ws_label_compact";
function readCompact(): boolean {
  try { return localStorage.getItem(COMPACT_KEY) === "1"; } catch { return false; }
}

class WorkspaceStore {
  list = $state<Workspace[]>([]);
  loading = $state(false);
  error = $state<string | null>(null);
  // The workspace last opened or saved into. Set by open() and the save
  // paths; cleared when that workspace is deleted or closed.
  activeId = $state<string | null>(null);

  // The workspace "Save changes" targets: the one whose frame holds the
  // focused tab, else the last one opened while its frame is still up.
  get active(): Workspace | null {
    const focused = paneTabs.tabs.find((t) => t.tabId === paneTabs.activeTabId)?.workspaceId;
    const id = focused && this.isOpen(focused) ? focused
      : this.activeId && this.isOpen(this.activeId) ? this.activeId
      : null;
    return id ? this.byId(id) : null;
  }

  // Workspaces whose frame changed since the last save or open (a tab was
  // dragged in or out). Splits and renames inside the frame do not count:
  // this is only the "membership changed, remember to save" hint.
  dirty = $state<Record<string, true>>({});

  markDirty(...ids: (string | null | undefined)[]) {
    const next = { ...this.dirty };
    for (const id of ids) if (id) next[id] = true;
    this.dirty = next;
  }

  private clearDirty(id: string) {
    if (!this.dirty[id]) return;
    const { [id]: _, ...rest } = this.dirty;
    this.dirty = rest;
  }

  // Frame labels shown as just the icon. A per-machine look preference, so
  // browser storage, guarded: it may be unavailable.
  compactLabels = $state(readCompact());

  setCompactLabels(on: boolean) {
    this.compactLabels = on;
    try { localStorage.setItem(COMPACT_KEY, on ? "1" : "0"); } catch { /* ignore */ }
  }

  byId(id: string | undefined | null): Workspace | null {
    if (!id) return null;
    return this.list.find((w) => w.id === id) ?? null;
  }

  // Tabs in this workspace's frame, in bar order.
  tabsOf(id: string): PaneTab[] {
    return paneTabs.tabs.filter((t) => t.workspaceId === id);
  }

  isOpen(id: string): boolean {
    return paneTabs.tabs.some((t) => t.workspaceId === id);
  }

  async load() {
    this.loading = true;
    this.error = null;
    try {
      const rows = await api.workspacesList();
      this.list = rows ?? [];
    } catch (e: any) {
      this.error = e?.message ?? String(e);
    } finally {
      this.loading = false;
    }
  }

  // Build a snapshot of the current tab set, pane trees and all. A tab whose
  // every pane is unrestorable (a lone VNC console) is skipped rather than
  // saved as an empty entry.
  serializeCurrent(): WorkspaceLayout {
    return this.serializeTabs(paneTabs.tabs);
  }

  serializeTabs(from: PaneTab[]): WorkspaceLayout {
    const tabs: TabSpec[] = [];
    for (const t of from) {
      const spec = serializePaneSpec(t.root, (sid) => specForSession(sid));
      if (!spec) continue;
      tabs.push({
        title: t.title,
        titleCustom: t.titleCustom,
        groupName: t.groupName,
        groupColor: t.groupColor,
        sessions: spec.sessions,
        root: spec.root,
      });
    }
    return { version: WORKSPACE_VERSION, tabs };
  }

  async saveCurrentAs(name: string): Promise<Workspace | null> {
    const layout = this.serializeCurrent();
    const created = await api.workspaceCreate(name, JSON.stringify(layout));
    await this.load();
    if (created) {
      // Everything that was saved is now this workspace's frame.
      paneTabs.setWorkspace(paneTabs.tabs.map((t) => t.tabId), created.id);
      this.activeId = created.id;
      this.dirty = {};
    }
    return created;
  }

  // Write the current tabs over the workspace that is already open. The
  // everyday save: no name prompt, no overwrite confirm, no picking from a
  // list. Returns false when nothing is open to save into.
  async saveActive(): Promise<boolean> {
    const w = this.active;
    if (!w) return false;
    await this.overwrite(w.id, w.name);
    return true;
  }

  /** The existing workspace with this name, if any. Name matching is
   *  case-insensitive: the table's UNIQUE constraint is what the user runs
   *  into otherwise, as a raw SQL error with no way forward. */
  findByName(name: string): Workspace | null {
    const n = name.trim().toLowerCase();
    return this.list.find((w) => w.name.toLowerCase() === n) ?? null;
  }

  // An open workspace gets its own frame written back. One that is not open
  // takes every open tab, which then becomes its frame - the same as
  // "Save current as" under an existing name.
  async overwrite(id: string, name: string): Promise<Workspace | null> {
    const open = this.isOpen(id);
    const from = open ? this.tabsOf(id) : paneTabs.tabs;
    const layout = this.serializeTabs(from);
    const updated = await api.workspaceUpdate(id, name, JSON.stringify(layout));
    await this.load();
    if (!open) paneTabs.setWorkspace(from.map((t) => t.tabId), id);
    this.activeId = id;
    this.clearDirty(id);
    return updated;
  }

  // Save just these tabs as a workspace - a new one, or (existingId) over an
  // existing one - and gather them into its frame. The other tabs are left
  // alone, so picking five of six tabs needs no closing of the sixth.
  async saveTabsAs(name: string, tabIds: string[], existingId?: string): Promise<Workspace | null> {
    const ids = new Set(tabIds);
    const from = paneTabs.tabs.filter((t) => ids.has(t.tabId));
    const layout = JSON.stringify(this.serializeTabs(from));
    let ws: Workspace | null;
    if (existingId) {
      // Tabs still framed for the old content of that workspace drop out of
      // the frame: the workspace is now exactly the chosen tabs.
      paneTabs.setWorkspace(this.tabsOf(existingId).filter((t) => !ids.has(t.tabId)).map((t) => t.tabId), undefined);
      ws = await api.workspaceUpdate(existingId, name, layout);
    } else {
      ws = await api.workspaceCreate(name, layout);
    }
    await this.load();
    const id = existingId ?? ws?.id;
    if (id) {
      const before = from.map((t) => t.workspaceId);
      paneTabs.moveToWorkspace(from.map((t) => t.tabId), id);
      this.markDirty(...before.filter((b) => b !== id));
      this.clearDirty(id);
      this.activeId = id;
    }
    return ws;
  }

  // Disconnect and remove the workspace's frame. The tab bar passes its own
  // closeTab so a closed workspace tab goes through the same teardown (and
  // Ctrl+Shift+T stack) as any other tab.
  async close(id: string, closeTab: (tabId: string) => Promise<void> | void) {
    for (const t of this.tabsOf(id)) await closeTab(t.tabId);
    if (this.activeId === id) this.activeId = null;
    this.clearDirty(id);
  }

  async delete(id: string) {
    await api.workspaceDelete(id);
    if (this.activeId === id) this.activeId = null;
    // Its tabs stay open, just no longer framed.
    paneTabs.setWorkspace(this.tabsOf(id).map((t) => t.tabId), undefined);
    await this.load();
  }

  // Restore a workspace next to the open tabs, pane trees included. Already
  // open: focus its frame instead of connecting everything a second time.
  async open(id: string) {
    const ws = this.list.find((w) => w.id === id);
    if (!ws) throw new Error("workspace not found");
    if (this.opening.has(id)) return; // a second click while it connects
    const already = this.tabsOf(id);
    if (already.length) {
      const first = already.find((t) => !t.hidden) ?? already[0];
      if (first.hidden) paneTabs.setHidden(first.tabId, false);
      paneTabs.activateTab(first.tabId);
      view.setTab("terminal");
      this.activeId = id;
      return;
    }
    let layout: WorkspaceLayout;
    try {
      layout = JSON.parse(ws.layout_json);
    } catch {
      throw new Error("workspace layout is corrupt");
    }
    const tabs = upgradeWorkspaceTabs(layout);
    if (!tabs.length) {
      // Empty workspace - just touch + return.
      await api.workspaceTouchLastOpened(id);
      await this.load();
      return;
    }

    // Rebuild each tab: one connect per pane, then the tree around them.
    // A pane that cannot be reconnected is dropped and its split collapsed,
    // so one dead host costs a pane rather than the whole tab.
    beginRestore();
    this.opening.add(id);
    try {
      await this.restoreTabs(id, tabs);
    } finally {
      this.opening.delete(id);
    }
    this.activeId = id;
    try { await api.workspaceTouchLastOpened(id); } catch { /* ignore */ }
    await this.load();
  }

  private opening = new Set<string>();

  private async restoreTabs(id: string, tabs: TabSpec[]) {
    let opened = 0;
    for (const spec of tabs) {
      const built = await restorePaneSpec(spec, (one) => connectSpec(one));
      if (!built) {
        toast.err(`${spec.title || "tab"}: nothing could be reconnected`);
        continue;
      }
      const tab = paneTabs.addTabFromLayout({
        title: spec.title ?? "",
        root: built.root,
        groupName: spec.groupName,
        groupColor: spec.groupColor,
        workspaceId: id,
      });
      if (spec.titleCustom && spec.title) paneTabs.setTitle(tab.tabId, spec.title, true);
      if (!spec.title) {
        const first = sessions.tabs.find((x) => x.sessionId === built.sessionIds[0]);
        if (first) paneTabs.setTitle(tab.tabId, first.name);
      }
      opened++;
    }
    if (opened > 0) view.setTab("terminal");
  }
}

export const workspaces = new WorkspaceStore();

// Frame colour for a workspace, stable per id. Workspaces have no colour of
// their own; hashing the id keeps two open frames apart without a setting.
const FRAME_COLORS = ["--mauve", "--teal", "--peach", "--sapphire", "--pink", "--green", "--yellow", "--flamingo"];
export function workspaceColor(id: string): string {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return `var(${FRAME_COLORS[h % FRAME_COLORS.length]})`;
}
