// Workspaces - named bundles of "these tabs in this layout" the user
// can switch between.
//   - Serialise: every open tab's pane tree, one session spec per pane
//     (split directions and ratios included), plus title + group metadata.
//   - Restore: disconnect everything open, reconnect each pane, rebuild the
//     tabs with their layout and group label.
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
import { paneTabs, sessions, view } from "./stores.svelte";
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

class WorkspaceStore {
  list = $state<Workspace[]>([]);
  loading = $state(false);
  error = $state<string | null>(null);
  // The workspace currently open, if any. Set by open() and by saving the
  // current tabs under a new name; cleared when that workspace is deleted.
  // Lets the UI offer "Save changes" against the right one instead of making
  // the user find it in a list and confirm an overwrite.
  activeId = $state<string | null>(null);

  get active(): Workspace | null {
    return this.list.find((w) => w.id === this.activeId) ?? null;
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
    const tabs: TabSpec[] = [];
    for (const t of paneTabs.tabs) {
      const spec = serializePaneSpec(t.root, (sid) => specForSession(sid));
      if (!spec) continue;
      tabs.push({
        title: t.title,
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
    if (created) this.activeId = created.id;
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

  async overwrite(id: string, name: string): Promise<Workspace | null> {
    const layout = this.serializeCurrent();
    const updated = await api.workspaceUpdate(id, name, JSON.stringify(layout));
    await this.load();
    // Saving into a workspace makes it the one you are working in, whether or
    // not it was open before.
    this.activeId = id;
    return updated;
  }

  async delete(id: string) {
    await api.workspaceDelete(id);
    if (this.activeId === id) this.activeId = null;
    await this.load();
  }

  // Restore a workspace: disconnect every open session (sessions are
  // owned by the previous workspace - keeping them around would clutter
  // the bar), then rebuild each saved tab, pane tree included.
  async open(id: string) {
    const ws = this.list.find((w) => w.id === id);
    if (!ws) throw new Error("workspace not found");
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

    // Disconnect every existing tab so the restored set has the floor.
    // Backend keeps the SSH chain alive but our pane bookkeeping
    // clears.
    for (const t of [...paneTabs.tabs]) {
      const leaves: string[] = [];
      const walk = (n: any): void => {
        if (n.kind === "pane") leaves.push(n.sessionId);
        else { walk(n.a); walk(n.b); }
      };
      walk(t.root);
      for (const sid of leaves) {
        try { await api.sshDisconnect(sid); } catch { /* ignore */ }
        sessions.remove(sid);
      }
      paneTabs.removeTab(t.tabId);
    }

    // Rebuild each tab: one connect per pane, then the tree around them.
    // A pane that cannot be reconnected is dropped and its split collapsed,
    // so one dead host costs a pane rather than the whole tab.
    beginRestore();
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
      });
      if (!spec.title) {
        const first = sessions.tabs.find((x) => x.sessionId === built.sessionIds[0]);
        if (first) paneTabs.setTitle(tab.tabId, first.name);
      }
      opened++;
    }
    if (opened > 0) view.setTab("terminal");

    this.activeId = id;
    try { await api.workspaceTouchLastOpened(id); } catch { /* ignore */ }
    await this.load();
  }
}

export const workspaces = new WorkspaceStore();
