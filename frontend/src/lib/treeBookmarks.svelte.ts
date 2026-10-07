// Bookmarks the user pinned to the connection tree (ProxyBookmark.in_tree),
// grouped by connection, plus the open flow shared with the quick palette.
//
// The list is fetched in one IPC (forwardsListAll) when the tree loads and
// after the forwards editor saves; nothing polls.

import { api, type PortForward, type ProxyBookmark } from "./api";
import { sessions, paneTabs, tree, selection } from "./stores.svelte";
import { collapsedBookmarks } from "./treeState.svelte";
import { toast } from "./toast.svelte.ts";
import { errMsg } from "./connectErrors";

export interface TreeBookmark {
  /** Stable row key: forward id + index in its bookmark list. */
  key: string;
  spec: PortForward;
  bm: ProxyBookmark;
}

class TreeBookmarks {
  byConn = $state<Map<string, TreeBookmark[]>>(new Map());
  /** Row last clicked; drawn as selected while its connection is. */
  selected = $state<{ key: string; connId: string } | null>(null);

  async refresh() {
    let all: PortForward[] = [];
    try {
      all = (await api.forwardsListAll()) ?? [];
    } catch (e) {
      console.warn("tree bookmarks load:", e);
      return;
    }
    const m = new Map<string, TreeBookmark[]>();
    for (const spec of all) {
      (spec.bookmarks ?? []).forEach((bm, i) => {
        if (!bm.in_tree) return;
        const list = m.get(spec.connection_id) ?? [];
        list.push({ key: `${spec.id}:${i}`, spec, bm });
        m.set(spec.connection_id, list);
      });
    }
    this.byConn = m;
    await collapsedBookmarks.load();
    collapsedBookmarks.prune(new Set(m.keys()));
  }

  of(connId: string): TreeBookmark[] {
    return this.byConn.get(connId) ?? [];
  }

  /** A bookmark row under connId holds the selection, so the connection
   *  row itself should not look selected. */
  selectedUnder(connId: string): boolean {
    return this.selected?.connId === connId
      && selection.current.kind === "connection" && selection.current.id === connId;
  }

  isSelected(b: TreeBookmark): boolean {
    return this.selected?.key === b.key && this.selectedUnder(b.spec.connection_id);
  }

  /** Hide one bookmark from the tree (it stays on the forward). */
  async hide(b: TreeBookmark) {
    const idx = Number(b.key.slice(b.spec.id.length + 1));
    const updated = (b.spec.bookmarks ?? []).map((bm, i) => {
      if (i !== idx) return bm;
      const { in_tree: _, ...rest } = bm;
      return rest;
    });
    try {
      await api.forwardsSetBookmarks(b.spec.id, updated);
    } catch (e) {
      toast.err(`Hide bookmark failed: ${errMsg(e)}`);
    }
    await this.refresh();
  }
}

export const treeBookmarks = new TreeBookmarks();

// sessionFor returns a live session id for the connection, opening one
// (with a visible tab) when nothing is connected yet. Null on failure,
// already surfaced as a toast.
export async function sessionFor(connectionId: string): Promise<string | null> {
  const live = sessions.tabs
    .filter((t) => t.connectionId === connectionId && t.status === "connected")
    .at(-1);
  if (live) return live.sessionId;
  const c = tree.connectionById(connectionId);
  if (!c) {
    toast.err(`Connection not found.`);
    return null;
  }
  try {
    const res = await api.sshConnect(connectionId);
    sessions.add({
      sessionId: res.session_id,
      connectionId,
      name: c.name,
      hostname: c.hostname,
      status: "connected",
    });
    paneTabs.addTab(res.session_id, c.name);
    return res.session_id;
  } catch (e) {
    toast.err(`Connect failed: ${errMsg(e)}`);
    return null;
  }
}

// openBookmark starts the forward when it is not listening (connecting
// first if needed) and opens the URL the way the forward's browser mode
// says. `running` skips the status round-trip when the caller knows.
export async function openBookmark(spec: PortForward, bm: ProxyBookmark, running?: boolean) {
  try {
    if (running === undefined) {
      const live = (await api.forwardsActive("")) ?? [];
      running = live.some((f) => f.id === spec.id && f.state === "listening");
    }
    if (!running) {
      const sid = await sessionFor(spec.connection_id);
      if (!sid) return;
      await api.forwardsStart(spec.id, sid);
    }
    await api.sshLaunchBrowser(spec.id, bm.url);
  } catch (e) {
    toast.err(`Open bookmark failed: ${errMsg(e)}`);
  }
}
