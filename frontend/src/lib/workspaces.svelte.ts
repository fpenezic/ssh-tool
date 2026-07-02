// Workspaces - named bundles of "these tabs in this layout" the user
// can switch between. MVP scope:
//   - Serialise: snapshot of every open tab's connectionId + title +
//     group metadata. Pane splits collapse to the active leaf for now;
//     restoring multi-pane tabs is a follow-up.
//   - Restore: disconnect everything open, fan out sshConnect to the
//     workspace's connectionIds, rebuild tabs with their group label.
//   - Persist via backend store (workspaces table, migration 10).
//
// The serialiser writes a JSON shape the backend treats as opaque
// (just a TEXT column). Versioned so we can evolve the schema later.

import { api, type Workspace } from "./api";
import { paneTabs, sessions } from "./stores.svelte";
import { connectionActions } from "./connectionActions.svelte";

export const WORKSPACE_VERSION = 1;

export interface WorkspaceTabSpec {
  connectionId: string;
  title?: string;
  groupName?: string;
  groupColor?: string;
}

export interface WorkspaceLayout {
  version: number;
  tabs: WorkspaceTabSpec[];
}

class WorkspaceStore {
  list = $state<Workspace[]>([]);
  loading = $state(false);
  error = $state<string | null>(null);

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

  // Build a snapshot of the current tab set. Splits collapse to the
  // tab's active leaf - multi-pane workspaces are a future iteration.
  serializeCurrent(): WorkspaceLayout {
    const tabs: WorkspaceTabSpec[] = [];
    for (const t of paneTabs.tabs) {
      const leaf = paneTabs.activePane(t.tabId);
      if (!leaf) continue;
      const sess = sessions.tabs.find((s) => s.sessionId === leaf.sessionId);
      if (!sess) continue;
      tabs.push({
        connectionId: sess.connectionId,
        title: t.title,
        groupName: t.groupName,
        groupColor: t.groupColor,
      });
    }
    return { version: WORKSPACE_VERSION, tabs };
  }

  async saveCurrentAs(name: string): Promise<Workspace | null> {
    const layout = this.serializeCurrent();
    const created = await api.workspaceCreate(name, JSON.stringify(layout));
    await this.load();
    return created;
  }

  async overwrite(id: string, name: string): Promise<Workspace | null> {
    const layout = this.serializeCurrent();
    const updated = await api.workspaceUpdate(id, name, JSON.stringify(layout));
    await this.load();
    return updated;
  }

  async delete(id: string) {
    await api.workspaceDelete(id);
    await this.load();
  }

  // Restore a workspace: disconnect every open session (sessions are
  // owned by the previous workspace - keeping them around would clutter
  // the bar), then fan out connectOne to the workspace ids, attaching
  // the group metadata to the newly-opened tabs.
  async open(id: string) {
    const ws = this.list.find((w) => w.id === id);
    if (!ws) throw new Error("workspace not found");
    let layout: WorkspaceLayout;
    try {
      layout = JSON.parse(ws.layout_json);
    } catch {
      throw new Error("workspace layout is corrupt");
    }
    if (!layout?.tabs?.length) {
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

    // Open each tab. connectionActions.connectOne handles the IPC,
    // sessions.add, paneTabs.addTab, view switch. After it returns we
    // attach the group metadata.
    for (const spec of layout.tabs) {
      const beforeIds = new Set(paneTabs.tabs.map((t) => t.tabId));
      const ok = await connectionActions.connectOne(spec.connectionId);
      if (!ok) continue;
      // Find the newly-added tab (its tabId wasn't in the snapshot
      // we took before the connect call).
      const newTab = paneTabs.tabs.find((t) => !beforeIds.has(t.tabId));
      if (!newTab) continue;
      if (spec.title) paneTabs.setTitle(newTab.tabId, spec.title);
      if (spec.groupName || spec.groupColor) {
        paneTabs.setGroup(newTab.tabId, spec.groupName, spec.groupColor);
      }
    }

    try { await api.workspaceTouchLastOpened(id); } catch { /* ignore */ }
    await this.load();
  }
}

export const workspaces = new WorkspaceStore();
