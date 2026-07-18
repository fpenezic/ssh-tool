<script lang="ts">
  import { sessions, paneTabs, view, drag, tree, closedTabs, mcpShared, shareBridge, shareShared, type PaneNode as PaneNodeType, encodePaneLayout, decodePaneLayout } from "./stores.svelte";
  import { api } from "./api";
  import PaneNode from "./PaneNode.svelte";
  import ShareDialog from "./ShareDialog.svelte";
  import { projectTabs, realSessionIds } from "./shareProject";
  import TcpdumpModal from "./TcpdumpModal.svelte";
  import { tcpdump } from "./tcpdumpStore.svelte";
  import { broadcast } from "./broadcast.svelte";
  import { recording } from "./recording.svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import { IconBroadcast, IconFolder, IconBot, IconHost } from "./iconMap";
  import Icon from "./Icon.svelte";
  import BroadcastManager from "./BroadcastManager.svelte";
  import { setTabDetachDragImage } from "./dragImage";
  import { onMount, onDestroy } from "svelte";
  import { EventsOn } from "./wailsRuntime";
  import { appPrefs } from "./appPrefs.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { focusActiveTerminal } from "./terminalFocus";
  let broadcastManagerOpen = $state(false);
  let shareDialogOpen = $state(false);
  // The ShareDialog defaults its tab selection to the ACTIVE tab, so switch to
  // the right-clicked tab first, then open.
  function openShareDialog(tabId: string) {
    paneTabs.activateTab(tabId);
    shareDialogOpen = true;
  }
  // Loud indicator: a control guest on the currently-visible tab means someone
  // can type here right now. The pulsing red tab badge covers other tabs; this
  // banner makes the active one impossible to forget.
  const activeTabHasControlGuest = $derived(
    paneTabs.activeTabId ? tabHasControlGuest(paneTabs.activeTabId) : false,
  );

  // Tell guests when the host switches tabs, so a following guest tracks it.
  // Fires for every share that includes the newly-active tab, with the tab's
  // index within that share (guests may share a subset in a different order).
  $effect(() => {
    const tabId = paneTabs.activeTabId;
    if (!tabId || !shareBridge.enabled) return;
    for (const { shareId, index } of shareShared.shareIndexFor(tabId)) {
      api.shareSetActiveTab(shareId, index).catch(() => {});
    }
  });
  async function stopAllShares() {
    try {
      for (const s of (await api.shareActive()) ?? []) await api.shareStop(s.share_id);
    } catch { /* ignore */ }
  }

  // Re-project one share's current tabs and push the update to guests. Called
  // when a shared tab's layout changes (on-the-fly split) or a tab is added.
  async function resyncShare(shareId: string) {
    const tabIds = shareShared.tabsOf(shareId);
    const chosen = paneTabs.tabs.filter((t) => tabIds.includes(t.tabId));
    if (chosen.length === 0) return;
    // Carry the existing slot assignment so sessions keep their slots (their
    // guest streams aren't disrupted, and the backend routing stays in sync).
    const proj = projectTabs(chosen, (sid) => {
      const s = sessions.tabs.find((x) => x.sessionId === sid);
      return s?.name ?? s?.hostname ?? sid;
    }, shareShared.slotMapFor(shareId));
    const activeIdx = Math.max(0, chosen.findIndex((t) => t.tabId === paneTabs.activeTabId));
    shareShared.recordShare(shareId, realSessionIds(chosen), chosen.map((t) => t.tabId), proj.slotByReal);
    try {
      await api.shareUpdate(shareId, {
        bind_ip: "", port: 0, level: "read", scrollback: false,
        active_tab: activeIdx, tabs_blob: proj.tabsBlob, sessions: proj.sessions,
      });
    } catch { /* ignore */ }
  }

  // Auto-sync: re-push every active share when the pane layout changes (a
  // split, a pane close, ungroup, pop). We watch paneTabs.layoutVersion - a
  // counter bumped on every structural mutation - rather than diffing tree
  // signatures, which didn't fire reliably. Debounced so a drag-resize (which
  // does NOT bump layoutVersion) or a rapid sequence collapses to one push. The
  // first observation is skipped: ShareStart's manifest already has the current
  // layout, so only real subsequent changes resync.
  let lastLayoutVersion = -1;
  let resyncTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const v = paneTabs.layoutVersion;
    if (!shareBridge.enabled) { lastLayoutVersion = v; return; }
    if (shareShared.activeShareTabs().length === 0) { lastLayoutVersion = v; return; }
    if (lastLayoutVersion === -1) { lastLayoutVersion = v; return; } // first pass
    if (v === lastLayoutVersion) return;
    lastLayoutVersion = v;
    clearTimeout(resyncTimer);
    resyncTimer = setTimeout(() => {
      for (const { shareId } of shareShared.activeShareTabs()) resyncShare(shareId);
    }, 250);
  });

  function addTabToShare(tabId: string, shareId: string) {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return;
    shareShared.addTabToShare(shareId, tabId, realSessionIds([tab]));
    resyncShare(shareId);
  }
  // Shares that DON'T already include a given tab (for the "add to share" menu).
  function sharesWithoutTab(tabId: string): { shareId: string; label: string }[] {
    return shareShared.activeShareTabs()
      .filter((sh) => !sh.tabIds.includes(tabId))
      .map((sh) => ({ shareId: sh.shareId, label: sh.tabIds.length + " tab(s)" }));
  }

  function tabSessionIdArr(tabId: string): string[] {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return [];
    const ids: string[] = [];
    function walk(node: PaneNodeType) {
      if (node.kind === "pane") ids.push(node.sessionId);
      else { walk(node.a); walk(node.b); }
    }
    walk(tab.root);
    return ids;
  }

  function tabBroadcastState(tabId: string): "none" | "partial" | "all" {
    const ids = tabSessionIdArr(tabId);
    if (ids.length === 0) return "none";
    const inSet = ids.filter((s) => broadcast.hasInAnyGroup(s)).length;
    if (inSet === 0) return "none";
    if (inSet === ids.length) return "all";
    return "partial";
  }

  // Distinct group names every session inside the tab belongs to.
  // Lets the tab chip show 'BC: ops, dr' when the user wires the
  // same pane into more than the default group. Default group
  // ("") renders as "default".
  function tabBroadcastGroups(tabId: string): string[] {
    const ids = tabSessionIdArr(tabId);
    const seen = new Set<string>();
    const out: string[] = [];
    for (const sid of ids) {
      for (const g of broadcast.groupsOf(sid)) {
        if (seen.has(g)) continue;
        seen.add(g);
        out.push(g === "" ? "default" : g);
      }
    }
    return out;
  }

  // Recording acts on the tab's active pane - per-session, not
  // per-tab, because split panes are independent PTYs with their
  // own output streams (and their own .cast files).
  function tabActiveSessionId(tabId: string): string | null {
    return paneTabs.activePane(tabId)?.sessionId ?? null;
  }

  function tabRecordingCount(tabId: string): number {
    return tabSessionIdArr(tabId).filter((sid) => recording.isRecording(sid)).length;
  }

  // Any pane in this tab shared with the LLM (MCP bridge)?
  function tabHasSharedSession(tabId: string): boolean {
    return tabSessionIdArr(tabId).some((sid) => mcpShared.has(sid));
  }
  // Any pane in this tab has a browser guest attached?
  function tabHasGuest(tabId: string): boolean {
    return tabSessionIdArr(tabId).some((sid) => shareShared.has(sid));
  }
  // ...and can any of them type (control)? Drives the louder badge.
  function tabHasControlGuest(tabId: string): boolean {
    return tabSessionIdArr(tabId).some((sid) => shareShared.isControlled(sid));
  }

  function tabAddAllToBroadcast(tabId: string) {
    for (const sid of tabSessionIdArr(tabId)) broadcast.add(sid);
  }

  function tabRemoveAllFromBroadcast(tabId: string) {
    for (const sid of tabSessionIdArr(tabId)) broadcast.remove(sid);
  }

  // Is this capture's session on the currently-active tab? Used to hide
  // a capture overlay belonging to a background tab. Plain function (not
  // reactive $derived) so reading it inside the host {#each} doesn't
  // weave extra reactive edges that can feed a render loop.
  function tcpdumpOnActiveTab(sessionId: string): boolean {
    return paneTabs.findTabForSession(sessionId)?.tabId === paneTabs.activeTabId;
  }

  async function closeTab(tabId: string) {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return;
    const root = tab.root;
    const sessionsToKill = new Set<string>();
    function collect(node: typeof root) {
      if (node.kind === "pane") sessionsToKill.add(node.sessionId);
      else { collect(node.a); collect(node.b); }
    }
    collect(root);
    // Snapshot reopenable connections before we kill anything. Only
    // SSH sessions are reopenable - local shells lose their identity
    // (PID) on disconnect. Order follows tree-walk so reopen recreates
    // panes left-to-right roughly matching the original.
    const reopenIds: string[] = [];
    for (const sid of sessionsToKill) {
      const sess = sessions.tabs.find((s) => s.sessionId === sid);
      if (sess && sess.kind !== "local" && sess.connectionId) {
        reopenIds.push(sess.connectionId);
      }
    }
    if (reopenIds.length > 0) {
      closedTabs.push({
        title: tab.title,
        connectionIds: reopenIds,
        groupName: tab.groupName,
        groupColor: tab.groupColor,
        closedAt: Date.now(),
      });
    }
    for (const sid of sessionsToKill) {
      const sess = sessions.tabs.find((s) => s.sessionId === sid);
      // Closing the tab kills these sessions, so tear down any background
      // capture for them (the overlay is window-level now, keyed by
      // sessionId - it won't unmount just because the pane tree did).
      tcpdump.close(sid);
      try {
        if (sess?.kind === "local") {
          await api.localShellDisconnect(sid);
        } else {
          await api.sshDisconnect(sid);
        }
      } catch {}
      sessions.remove(sid);
    }
    paneTabs.removeTab(tabId);
    if (paneTabs.tabs.length === 0) view.setTab("connections");
    // Focus follows the tab promoted to active (mirrors autoCloseSession).
    else focusActiveTerminal();
  }

  function tabTitle(tabId: string): string {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return "?";
    const segs = tabSegments(tabId);
    if (segs.length === 0) return tab.title;
    const fmt = (s: TabSeg) => s.name + (s.kind === "sftp" ? " (sftp)" : "");
    if (segs.length <= 3) return segs.map(fmt).join(" | ");
    return segs.slice(0, 2).map(fmt).join(" | ") + ` +${segs.length - 2}`;
  }

  // Richer per-pane segments - same data tabTitle uses, but kept as
  // structured objects so the tab label can render an SFTP icon
  // instead of a textual '(sftp)' suffix in the visible UI. tabTitle
  // still falls back to the string form for the window title bar
  // and for ARIA labels.
  type TabSeg = { name: string; kind: "terminal" | "sftp" };
  function tabSegments(tabId: string): TabSeg[] {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return [];
    const segs: TabSeg[] = [];
    function collect(node: PaneNodeType) {
      if (node.kind === "pane") {
        const s = sessions.tabs.find((s) => s.sessionId === node.sessionId);
        if (s) {
          const kind: "terminal" | "sftp" = (node as any).view === "sftp" ? "sftp" : "terminal";
          segs.push({ name: s.name, kind });
        }
      } else {
        collect(node.a);
        collect(node.b);
      }
    }
    collect(tab.root);
    return segs;
  }

  // Drag-out-of-window: open a detached window when the user releases
  // the tab outside the WebView2 window. Disabled in detached windows
  // (we don't want detached→detached chains).
  const isDetachedWindow = new URLSearchParams(window.location.search).has("detached");
  // The original tabId from the main window - used as the window name key
  // ("detached-<detachedTabKey>"). A fresh tabId is generated when the
  // detached window reconstructs its pane tree, so we must NOT use t.tabId
  // when registering the pending drag; we need the original key instead.
  const detachedTabKey = new URLSearchParams(window.location.search).get("detached") ?? "";
  // This window's own backend name, for "Send to window" routing.
  const selfWindowName = isDetachedWindow ? `detached-${detachedTabKey}` : "main";
  let sendTargets = $state<{ name: string; label: string }[]>([]);
  let draggingTabId: string | null = null;
  // Per-tab reorder hint shown while the user drags a tab over
  // another tab's label. side === "left" draws a thin bar on the
  // left edge of the hovered tab, "right" on its right edge.
  let tabReorderIndicator = $state<{ tabId: string; side: "left" | "right" } | null>(null);

  let ctxMenu = $state<{ tabId: string; x: number; y: number } | null>(null);

  function openCtxMenu(e: MouseEvent, tabId: string) {
    refreshSendTargets();
    e.preventDefault();
    ctxMenu = { tabId, x: e.clientX, y: e.clientY };
  }

  function closeCtxMenu() { ctxMenu = null; }

  // First connectionId we can find in a tab's pane tree. Tabs almost
  // always have a single connection across all panes (SFTP shares the
  // session with its terminal sibling), so picking the first leaf is
  // the right default.
  function tabConnectionId(tabId: string): string | null {
    const t = paneTabs.tabs.find((x) => x.tabId === tabId);
    if (!t) return null;
    let id: string | null = null;
    function walk(n: PaneNodeType) {
      if (id) return;
      if (n.kind === "pane") {
        const s = sessions.tabs.find((x) => x.sessionId === n.sessionId);
        if (s) id = s.connectionId;
      } else { walk(n.a); walk(n.b); }
    }
    walk(t.root);
    return id;
  }

  // The tab's connection icon (custom image / built-in named icon), so
  // the tab bar shows the same glyph as the tree row. Returns null for
  // local shells and dynamic-inventory tabs (no stored connection) - the
  // caller falls back to the default terminal glyph.
  function tabIconFor(tabId: string): { imageId: string | null; name: string | null; color: string | null } | null {
    const cid = tabConnectionId(tabId);
    if (!cid || cid.startsWith("dyn:")) return null;
    const c = tree.connectionById(cid);
    if (!c) return null;
    return {
      imageId: c.icon_image_id ?? null,
      name: c.icon_name ?? null,
      color: c.icon_color ?? null,
    };
  }

  // Returns the tab's connection id IF that connection has VNC enabled
  // and the tab isn't already a VNC console - so a right-click on an SSH
  // tab of a VNC-capable host can open the desktop. null otherwise.
  function tabVncConnId(tabId: string): string | null {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab || tab.locked) return null; // locked = already a VNC tab
    const cid = tabConnectionId(tabId);
    if (!cid || cid.startsWith("dyn:")) return null;
    return tree.connectionById(cid)?.overrides?.vnc_enabled ? cid : null;
  }

  // For a Proxmox dynamic-inventory tab (connectionId "dyn:<entryId>"),
  // resolve the folder + entry so the tab right-click can open the
  // guest's noVNC console. Proxmox guests get a console by default - no
  // per-connection enable, since the API gives us one for free. Returns
  // {folderId, entryId} or null.
  function tabProxmoxConsole(tabId: string): { folderId: string; entryId: string } | null {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab || tab.locked) return null;
    const cid = tabConnectionId(tabId);
    if (!cid || !cid.startsWith("dyn:")) return null;
    const entryId = cid.slice(4);
    for (const [folderId, entries] of Object.entries(tree.dynamicEntries)) {
      const e = entries.find((x) => x.id === entryId);
      if (!e) continue;
      const provider = tree.dynamicFolders[folderId]?.provider;
      if (provider === "proxmox" && (e.kind === "guest_vm" || e.kind === "guest_lxc")) {
        return { folderId, entryId };
      }
      return null;
    }
    return null;
  }

  function tabSessionIds(tabId: string): string {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return "";
    const ids: string[] = [];
    function walk(node: PaneNodeType) {
      if (node.kind === "pane") ids.push(node.sessionId);
      else { walk(node.a); walk(node.b); }
    }
    walk(tab.root);
    return ids.join(",");
  }

  function tabLayoutBlob(tabId: string): string {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    return tab ? encodePaneLayout(tab) : "";
  }

  async function detachTab(tabId: string) {
    const sessions = tabSessionIds(tabId);
    const layout = tabLayoutBlob(tabId);
    paneTabs.removeTab(tabId);
    try {
      await api.windowDetachTab(tabId, sessions, layout);
    } catch (e) {
      console.error("detach failed", e);
    }
  }

  // Reconstruct a tab in THIS window from a pending-drag payload (used by the
  // detached-drop redock AND the "Send to window" receive). Does not remove
  // anything from the source - the caller decides that.
  async function reconstructTabFromPayload(p: { tab_id: string; sessions: string; layout: string }) {
    const sessionIds = p.sessions ? p.sessions.split(",") : [];
    const live = (await api.sshActiveSessions()) ?? [];
    // Ensure every session the tab references exists in this window's store.
    for (const s of live) {
      if (!sessionIds.includes(s.session_id)) continue;
      if (!sessions.tabs.find((t) => t.sessionId === s.session_id)) {
        sessions.add({
          sessionId: s.session_id,
          connectionId: s.connection_id,
          name: s.name,
          hostname: s.hostname,
          status: "connected",
        });
      }
    }
    const layout = decodePaneLayout(p.layout ?? "");
    if (layout) {
      paneTabs.addTabFromLayout(layout);
    } else {
      for (const sid of sessionIds) {
        const s = live.find((x) => x.session_id === sid);
        if (!s) continue;
        paneTabs.addTab(s.session_id, s.name);
      }
    }
    view.setTab("terminal");
  }

  async function onTabBarDrop(e: DragEvent) {
    if (isDetachedWindow) return;
    e.preventDefault();
    try {
      const p = await api.windowAcceptTabDrag();
      if (!p) return;
      await reconstructTabFromPayload(p);
      // Redock: the source is a detached window; close it.
      await api.windowCloseSelf("detached-" + p.tab_id);
    } catch {
      // No pending drag - silently ignore
    }
  }

  // ----- "Send to window" (cross-monitor tab move, no new window) -----

  // Load the list of other open windows this tab can be sent to. Called when
  // the context menu opens so it's fresh.
  async function refreshSendTargets() {
    try { sendTargets = (await api.windowListTargets(selfWindowName)) ?? []; }
    catch { sendTargets = []; }
  }

  // Send a tab to another open window: register the payload, tell the target
  // to claim it, then remove the tab locally. The session stays alive.
  async function sendTabToWindow(tabId: string, targetName: string) {
    const sids = tabSessionIds(tabId);
    const layout = tabLayoutBlob(tabId);
    // Use the detached window's original key when we're a detached window, so
    // the target reconstructs the right pane tree; else the live tabId.
    const key = isDetachedWindow ? detachedTabKey : tabId;
    try {
      await api.windowSendTab(selfWindowName, targetName, key, sids, layout);
      // Remove the tab from THIS window without disconnecting its sessions -
      // the target now owns them. removeTab keeps the pool intact.
      paneTabs.removeTab(tabId);
      if (paneTabs.tabs.length === 0 && isDetachedWindow) {
        // A detached window that just gave away its only tab should close.
        await api.windowCloseSelf(selfWindowName);
      }
    } catch (e) {
      console.warn("send tab to window failed", e);
    }
  }

  // Receive a tab sent to this window from another one. The backend emits
  // window_receive_tab globally with the intended target's name; only that
  // window claims the pending payload.
  let unsubReceive: (() => void) | null = null;
  onMount(() => {
    unsubReceive = EventsOn("window_receive_tab", async (data: any) => {
      if ((data?.target ?? "") !== selfWindowName) return;
      try {
        const p = await api.windowAcceptTabDrag();
        if (p) await reconstructTabFromPayload(p);
      } catch { /* nothing pending */ }
    });
  });
  onDestroy(() => { unsubReceive?.(); });

  async function duplicateTab(tabId: string) {
    const connId = tabConnectionId(tabId);
    if (!connId) return;
    const conn = tree.connectionById(connId);
    if (!conn) return;
    try {
      const r = await api.sshConnect(connId);
      sessions.add({
        sessionId: r.session_id,
        connectionId: connId,
        name: conn.name,
        hostname: conn.hostname,
        status: "connected",
      });
      paneTabs.addTab(r.session_id, conn.name);
    } catch (e) {
      console.error("duplicate tab failed", e);
    }
  }

  function tabIsGrouped(tabId: string): boolean {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    return tab?.root.kind === "split";
  }

  function currentGroup(tabId: string): string | undefined {
    return paneTabs.tabs.find((t) => t.tabId === tabId)?.groupName;
  }

  async function setTabGroupName(tabId: string) {
    const cur = currentGroup(tabId);
    const name = await showPrompt("Group name? (empty to clear)", cur ?? "");
    if (name === null) return;
    const trimmed = name.trim();
    if (!trimmed) {
      paneTabs.setGroup(tabId, undefined, undefined);
      return;
    }
    // Pick a color from a tiny palette - same hash → same colour, so
    // tabs with the same group name match without the user picking.
    const PALETTE = ["var(--blue)", "var(--green)", "var(--yellow)", "var(--peach)", "var(--mauve)", "var(--teal)", "var(--pink)"];
    let h = 0;
    for (let i = 0; i < trimmed.length; i++) h = (h * 31 + trimmed.charCodeAt(i)) >>> 0;
    paneTabs.setGroup(tabId, trimmed, PALETTE[h % PALETTE.length]);
  }

  function tabStatus(tabId: string): { color: string; hint: string; isClosed: boolean } {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return { color: "var(--overlay0)", hint: "?", isClosed: true };
    const root = tab.root;
    const ids: string[] = [];
    function collect(node: typeof root) {
      if (node.kind === "pane") ids.push(node.sessionId);
      else { collect(node.a); collect(node.b); }
    }
    collect(root);
    const ss = ids.map((id) => sessions.tabs.find((s) => s.sessionId === id)).filter(Boolean) as any[];
    if (ss.length === 0) return { color: "var(--overlay0)", hint: "no session", isClosed: true };
    if (ss.some((s) => s.status === "error")) return { color: "var(--red)", hint: "error", isClosed: false };
    if (ss.some((s) => s.status === "disconnected")) return { color: "var(--overlay0)", hint: "disconnected", isClosed: true };
    if (ss.some((s) => s.status === "connecting")) return { color: "var(--yellow)", hint: "connecting", isClosed: false };
    return { color: "var(--green)", hint: "connected", isClosed: false };
  }

  // Resolved color tag for the tab. Picks the first pane's connection
  // color; mixed tabs (e.g. terminal + SFTP for the same conn) trivially
  // agree, and the rare "split panes from two different connections"
  // case is fine to just take the first.
  function tabColor(tabId: string): string {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return "";
    let connId: string | null = null;
    function walk(node: PaneNodeType) {
      if (connId) return;
      if (node.kind === "pane") {
        const s = sessions.tabs.find((x) => x.sessionId === node.sessionId);
        if (s) connId = s.connectionId;
      } else { walk(node.a); walk(node.b); }
    }
    walk(tab.root);
    if (!connId) return "";
    return tree.resolveColorForConnection(connId);
  }

  // Tick once every 30s so tabUptimeLabel re-evaluates. Reactive
  // through nowTick; cheap because formatUptime is O(1) per tab.
  let nowTick = $state(Date.now());
  $effect(() => {
    if (!appPrefs.tabTimer) return;
    const h = setInterval(() => { nowTick = Date.now(); }, 30_000);
    return () => clearInterval(h);
  });

  // Returns the earliest connectedAt across all sessions in a tab -
  // i.e. the oldest still-live connection. Multi-pane tabs show the
  // longest uptime so a freshly-split window doesn't reset the
  // displayed value.
  function tabUptimeLabel(tabId: string): string {
    const ids = tabSessionIdArr(tabId);
    let earliest = Number.POSITIVE_INFINITY;
    for (const sid of ids) {
      const s = sessions.tabs.find((t) => t.sessionId === sid);
      if (!s || s.status !== "connected" || !s.connectedAt) continue;
      if (s.connectedAt < earliest) earliest = s.connectedAt;
    }
    if (!isFinite(earliest)) return "";
    const dt = Math.max(0, nowTick - earliest);
    const sec = Math.floor(dt / 1000);
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h${min % 60}m`;
    const day = Math.floor(hr / 24);
    return `${day}d${hr % 24}h`;
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="area"
  ondragover={(e) => {
    // While a tab drag is in flight from within the app, preventDefault
    // everywhere inside the window so the OS stops painting the
    // "no-drop" forbidden-circle cursor on top of every non-drop area.
    // Specific drop targets (tabbar, panes) already preventDefault on
    // their own - this is the fallback for the rest of the surface.
    if (draggingTabId) e.preventDefault();
  }}
>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="tabbar"
    ondragover={(e) => { if (!isDetachedWindow) e.preventDefault(); }}
    ondrop={(e: DragEvent) => onTabBarDrop(e)}
  >
    {#each paneTabs.tabs as t (t.tabId)}
      {@const active = paneTabs.activeTabId === t.tabId}
      {@const st = tabStatus(t.tabId)}
      {@const tagCol = tabColor(t.tabId)}
      {@const segs = tabSegments(t.tabId)}
      <div
        class="tab"
        class:active
        class:closed={st.isClosed}
        class:tagged={!!tagCol}
        style:--tag-color={tagCol || "transparent"}
        role="listitem"
        ondragenter={() => {
          // While dragging another tab, hovering this tab's label
          // activates it - so the user can do "grab A, hover B,
          // drop onto B's pane to merge" without first clicking B
          // (which would lose the drag).
          if (!drag.tabId || drag.tabId === t.tabId) return;
          if (paneTabs.activeTabId === t.tabId) return;
          paneTabs.activateTab(t.tabId);
        }}
        ondragover={(e: DragEvent) => {
          // Accept tab-on-tab drops so the tab bar reorders. We
          // figure out left/right insertion side from the cursor's
          // X relative to this tab's midpoint. The pane underneath
          // also accepts drops (split gesture), so we stop
          // propagation here to keep the bar drop self-contained.
          if (!drag.tabId || drag.tabId === t.tabId) return;
          e.preventDefault();
          e.stopPropagation();
          const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
          const onLeft = e.clientX < rect.left + rect.width / 2;
          tabReorderIndicator = { tabId: t.tabId, side: onLeft ? "left" : "right" };
        }}
        ondragleave={(e: DragEvent) => {
          // Clear the indicator unless the pointer is moving into
          // a descendant element of this tab.
          if (e.relatedTarget && (e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) {
            return;
          }
          if (tabReorderIndicator?.tabId === t.tabId) tabReorderIndicator = null;
        }}
        ondrop={(e: DragEvent) => {
          if (!drag.tabId || drag.tabId === t.tabId) return;
          e.preventDefault();
          e.stopPropagation();
          const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
          const onLeft = e.clientX < rect.left + rect.width / 2;
          // Reorder: insert source tab before this one if dropped
          // on the left half, otherwise before the next tab (= after
          // this one).
          if (onLeft) {
            paneTabs.moveTabBefore(drag.tabId, t.tabId);
          } else {
            const idx = paneTabs.tabs.findIndex((tt) => tt.tabId === t.tabId);
            const after = paneTabs.tabs[idx + 1]?.tabId ?? null;
            paneTabs.moveTabBefore(drag.tabId, after);
          }
          tabReorderIndicator = null;
          drag.end();
          draggingTabId = null;
        }}
        onauxclick={(e) => {
          // Middle click anywhere on the tab closes it. button === 1
          // is the middle mouse button (left=0, right=2).
          if (e.button === 1) {
            e.preventDefault();
            closeTab(t.tabId);
          }
        }}
        draggable="true"
        ondragstart={(e) => {
          drag.startTab(t.tabId);
          draggingTabId = t.tabId;
          // Replace the OS-default "document with no-drop slash"
          // ghost image with a pill that reads "Detach: <name>".
          // Pairs with effectAllowed='move' so the cursor reads as
          // a copy/move hint instead of a forbidden one.
          setTabDetachDragImage(e.dataTransfer, t.title || t.tabId.slice(0, 6));
          // Custom MIME so other apps don't interpret the drag as
          // something they can accept (Chrome would treat text/plain
          // as "open as new tab" and show a green plus over its
          // window; Explorer would offer URL drops, etc.). Our own
          // drop targets don't read this - they pull from the drag
          // store. The benign value is just to keep the drag alive
          // for browsers that hide empty drags.
          try {
            e.dataTransfer?.setData("application/x-ssh-tool-tab", t.tabId);
          } catch { /* ignore */ }
          if (isDetachedWindow) {
            api.windowStartTabDrag(detachedTabKey, tabSessionIds(t.tabId), tabLayoutBlob(t.tabId)).catch(console.error);
          }
          e.stopPropagation();
        }}
        ondragend={(e) => {
          const tabId = draggingTabId;
          drag.end();
          draggingTabId = null;
          if (isDetachedWindow) {
            // Cancel the pending drag - no-op if main window already accepted it.
            api.windowCancelTabDrag().catch(console.error);
          } else {
            const outside = e.clientX < 0 || e.clientX > window.innerWidth ||
                            e.clientY < 0 || e.clientY > window.innerHeight;
            if (tabId && outside) {
              const sids = tabSessionIds(tabId);
              const layout = tabLayoutBlob(tabId);
              paneTabs.removeTab(tabId);
              api.windowDetachTabAt(tabId, e.screenX, e.screenY, sids, layout).catch(console.error);
            }
          }
        }}
        oncontextmenu={(e) => openCtxMenu(e, t.tabId)}
      >
        {#if tabReorderIndicator?.tabId === t.tabId}
          <div class="tab-reorder-bar {tabReorderIndicator.side}"></div>
        {/if}
        <button class="label" onclick={() => { paneTabs.activateTab(t.tabId); focusActiveTerminal(); }} title={st.hint}>
          <span class="dot" style="background: {st.color}"></span>
          {#if t.groupName}
            <span
              class="group-chip"
              style:background={t.groupColor ?? "var(--surface1)"}
              title="Group: {t.groupName}"
            >{t.groupName}</span>
          {/if}
          {#if tabRecordingCount(t.tabId) > 0}
            <span
              class="rec-dot"
              title={tabRecordingCount(t.tabId) === 1
                ? "Recording session"
                : `Recording ${tabRecordingCount(t.tabId)} panes`}
            ></span>
          {/if}
          {#if tabHasSharedSession(t.tabId)}
            <span class="mcp-badge" title="Shared with LLM (MCP)">
              <IconBot size={10} />
            </span>
          {/if}
          {#if tabHasGuest(t.tabId)}
            <span
              class="share-badge"
              class:control={tabHasControlGuest(t.tabId)}
              title={tabHasControlGuest(t.tabId)
                ? "A browser guest can type in this session"
                : "Shared to a browser guest (read-only)"}
            >●</span>
          {/if}
          {#if shareShared.isGuestViewing(t.tabId)}
            <span class="guest-watching" title="Your guest is looking at this tab"></span>
          {/if}
          {#if tabBroadcastState(t.tabId) !== "none"}
            {@const groups = tabBroadcastGroups(t.tabId)}
            <span
              class="bcast"
              class:partial={tabBroadcastState(t.tabId) === "partial"}
              title={(tabBroadcastState(t.tabId) === "all" ? "All panes broadcast" : "Some panes broadcast") + (groups.length > 0 ? " - groups: " + groups.join(", ") : "")}
            >
              <IconBroadcast size={10} />
              {#if groups.length > 1}<span class="bcast-groups">{groups.join(",")}</span>{/if}
            </span>
          {/if}
          {#each [tabIconFor(t.tabId)] as tabIcon (t.tabId)}
            {#if tabIcon && (tabIcon.imageId || tabIcon.name)}
              <span class="tab-icon">
                <Icon imageId={tabIcon.imageId} iconName={tabIcon.name} iconColor={tabIcon.color} size={13}>
                  <IconHost size={13} />
                </Icon>
              </span>
            {/if}
          {/each}
          <span class="tab-label-segs">
            {#if segs.length === 0}
              {tabTitle(t.tabId)}
            {:else if segs.length <= 3}
              {#each segs as seg, i (i)}
                {#if i > 0}<span class="seg-sep">|</span>{/if}
                {seg.name}
                {#if seg.kind === "sftp"}<IconFolder size={11} class="sftp-marker" />{/if}
              {/each}
            {:else}
              {#each segs.slice(0, 2) as seg, i (i)}
                {#if i > 0}<span class="seg-sep">|</span>{/if}
                {seg.name}
                {#if seg.kind === "sftp"}<IconFolder size={11} class="sftp-marker" />{/if}
              {/each}
              <span class="seg-more">+{segs.length - 2}</span>
            {/if}
          </span>
          {#if appPrefs.tabTimer}
            {@const up = tabUptimeLabel(t.tabId)}
            {#if up}<span class="uptime" title="Connected for {up}">{up}</span>{/if}
          {/if}
        </button>
        <button class="close" onclick={() => closeTab(t.tabId)} title="Close tab">✕</button>
      </div>
    {/each}
    <div class="tabbar-end">
      <button
        class="bcast-btn"
        class:active={broadcast.totalMembers() > 1}
        title={broadcast.totalMembers() > 1
          ? `Broadcast active: ${broadcast.totalMembers()} sessions across all groups`
          : "Broadcast manager"}
        onclick={() => (broadcastManagerOpen = true)}
      >
        <IconBroadcast size={13} />
        {#if broadcast.totalMembers() > 1}<span class="bcount">{broadcast.totalMembers()}</span>{/if}
      </button>
    </div>
  </div>
  {#if ctxMenu}
    <div class="ctx-backdrop" role="presentation" onclick={closeCtxMenu} oncontextmenu={(e) => { e.preventDefault(); closeCtxMenu(); }}></div>
    <div class="ctx-menu" style="left: {ctxMenu.x}px; top: {ctxMenu.y}px;">
      <button onclick={() => { duplicateTab(ctxMenu!.tabId); closeCtxMenu(); }}>
        Duplicate tab
      </button>
      {#if tabVncConnId(ctxMenu.tabId)}
        <button onclick={() => { connectionActions.openVncConnection(tabVncConnId(ctxMenu!.tabId)!); closeCtxMenu(); }}>
          Open VNC console
        </button>
      {:else if tabProxmoxConsole(ctxMenu.tabId)}
        {@const pc = tabProxmoxConsole(ctxMenu.tabId)!}
        <button onclick={() => { connectionActions.openVncProxmox(pc.folderId, pc.entryId); closeCtxMenu(); }}>
          Open VNC console
        </button>
      {/if}
      <button onclick={() => { setTabGroupName(ctxMenu!.tabId); closeCtxMenu(); }}>
        Set group name…
      </button>
      {#if currentGroup(ctxMenu.tabId)}
        <button onclick={() => { paneTabs.setGroup(ctxMenu!.tabId, undefined, undefined); closeCtxMenu(); }}>
          Clear group
        </button>
      {/if}
      <button onclick={() => { detachTab(ctxMenu!.tabId); closeCtxMenu(); }}>
        ↗ Detach to new window
      </button>
      {#if shareBridge.enabled}
        <button onclick={() => { openShareDialog(ctxMenu!.tabId); closeCtxMenu(); }}>
          Share to browser…
        </button>
        {#each sharesWithoutTab(ctxMenu.tabId) as sh (sh.shareId)}
          <button onclick={() => { addTabToShare(ctxMenu!.tabId, sh.shareId); closeCtxMenu(); }}>
            Add to share ({sh.label})
          </button>
        {/each}
      {/if}
      {#each sendTargets as w (w.name)}
        <button onclick={() => { sendTabToWindow(ctxMenu!.tabId, w.name); closeCtxMenu(); }}>
          → Send to {w.label}
        </button>
      {/each}
      {#if tabActiveSessionId(ctxMenu.tabId)}
        {@const recSid = tabActiveSessionId(ctxMenu.tabId)!}
        {#if recording.isRecording(recSid)}
          <button onclick={() => { recording.stop(recSid); closeCtxMenu(); }}>
            Stop recording
          </button>
        {:else}
          <button onclick={() => { recording.start(recSid); closeCtxMenu(); }}>
            Record session
          </button>
        {/if}
      {/if}
      {#if tabBroadcastState(ctxMenu.tabId) === "all"}
        <button onclick={() => { tabRemoveAllFromBroadcast(ctxMenu!.tabId); closeCtxMenu(); }}>
          Remove from broadcast
        </button>
      {:else if tabBroadcastState(ctxMenu.tabId) === "partial"}
        <button onclick={() => { tabAddAllToBroadcast(ctxMenu!.tabId); closeCtxMenu(); }}>
          Add remaining panes to broadcast
        </button>
      {:else}
        <button onclick={() => { tabAddAllToBroadcast(ctxMenu!.tabId); closeCtxMenu(); }}>
          Add to broadcast
        </button>
      {/if}
      {#if tabIsGrouped(ctxMenu.tabId)}
        <button onclick={() => { paneTabs.ungroupTab(ctxMenu!.tabId); closeCtxMenu(); }}>
          Ungroup tabs
        </button>
      {/if}
      <button onclick={() => { closeTab(ctxMenu!.tabId); closeCtxMenu(); }}>
        Close tab
      </button>
    </div>
  {/if}

  <!-- Always in the DOM (grid needs a stable child count); collapses to 0
       height when there's no control guest. -->
  <div class="control-guest-banner" class:shown={activeTabHasControlGuest}>
    <span>A browser guest can type in this terminal.</span>
    <button onclick={stopAllShares}>End sharing</button>
  </div>
  <div class="term-area">
    {#each paneTabs.tabs as t (t.tabId)}
      <div class="tab-content" class:active={paneTabs.activeTabId === t.tabId}>
        <PaneNode tabId={t.tabId} node={t.root} />
      </div>
    {/each}
  </div>
</div>

<!-- tcpdump capture overlays live HERE, above the pane tree, mounted once
     per session and keyed by sessionId. This is what makes a capture
     survive every layout mutation (split, SFTP-split, closing one side
     of a split, drag, redock) - none of which touch this list, because
     the key is the stable sessionId, not a pane/leaf id that layout ops
     rewrite. Only sessions that belong to THIS window appear (a detached
     session resolves to no tab here, so its capture is owned by its own
     window). The overlay is hidden when minimised or when its session
     isn't on the active tab, so you never see another tab's capture. -->
{#each tcpdump.list() as cap (cap.sessionId)}
  {#if paneTabs.findTabForSession(cap.sessionId)}
    <TcpdumpModal
      sessionId={cap.sessionId}
      hidden={tcpdump.modeOf(cap.sessionId) === "minimized" || !tcpdumpOnActiveTab(cap.sessionId)}
      onClose={() => tcpdump.close(cap.sessionId)}
      onMinimize={() => tcpdump.minimize(cap.sessionId)}
      onStats={(s) => tcpdump.setStats(cap.sessionId, s)}
    />
  {/if}
{/each}


<BroadcastManager open={broadcastManagerOpen} onClose={() => (broadcastManagerOpen = false)} />
{#if shareDialogOpen}
  <ShareDialog onClose={() => (shareDialogOpen = false)} />
{/if}


<style>
  .area {
    display: grid;
    /* Tab row grows to fit however many wrapped rows it needs; the optional
       control-guest banner takes its own auto row; the terminal pane takes the
       rest. The banner row collapses to 0 when absent (no banner element), so
       this is safe with or without it. Previously grid-template-rows was
       `auto 1fr` with only two rows - adding the banner as a third child pushed
       the terminal into an implicit 0-height row, so the red banner filled the
       screen and the terminal vanished. */
    grid-template-rows: auto auto 1fr;
    height: 100%;
    background: var(--mantle);
    overflow: hidden;
  }
  .tabbar {
    /* Wrap rows instead of scrolling horizontally - at smaller widths
       or with many long-named connections, a single-row scroll bar
       hides tabs the user needs to click. flex-wrap keeps everything
       visible without scroll. Each tab gets a max-width + ellipsis so
       one screaming-long hostname can't push the others off-row.
       No max-height: the bar grows to fit every tab so nothing is
       hidden behind a vertical scrollbar. The terminal pane below
       shrinks to compensate. */
    display: flex;
    flex-wrap: wrap;
    align-items: stretch;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
  }
  .ctx-backdrop {
    position: fixed;
    inset: 0;
    z-index: 100;
  }
  .ctx-menu {
    position: fixed;
    z-index: 101;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    padding: 0.25rem;
    min-width: 140px;
    box-shadow: 0 4px 16px rgba(0,0,0,0.5);
  }
  .ctx-menu button {
    display: block;
    width: 100%;
    background: transparent;
    border: none;
    color: var(--text);
    padding: 0.35rem 0.75rem;
    text-align: left;
    font: inherit;
    font-size: 0.82rem;
    cursor: pointer;
    border-radius: 4px;
  }
  .ctx-menu button:hover { background: var(--surface0); }

  .tab-reorder-bar {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    background: var(--blue);
    pointer-events: none;
    z-index: 5;
  }
  .tab-reorder-bar.left  { left: -1px; }
  .tab-reorder-bar.right { right: -1px; }

  .tab {
    display: flex;
    align-items: center;
    background: var(--crust);
    border-right: 1px solid var(--surface0);
    border-bottom: 1px solid var(--surface0);
    color: var(--subtext0);
    font-size: 0.78rem;
    min-width: 0;
    /* Cap a single tab's width - long hostnames truncate with
       ellipsis below in .label. Without this the first big tab
       eats the row and forces wrapping after just one entry. */
    max-width: 220px;
    cursor: grab;
    position: relative;
  }
  .tab.tagged::before {
    content: "";
    position: absolute;
    left: 0; right: 0;
    top: 0;
    height: 2px;
    background: var(--tag-color);
  }
  .tab.active {
    background: var(--mantle);
    color: var(--text);
    /* Visible underline so the active tab is findable when the bar
       wraps to multiple rows. Color matches the nav-tab indicator
       in App.svelte for consistency. */
    box-shadow: inset 0 -2px 0 var(--blue);
  }
  .tab.closed .label {
    color: var(--overlay0);
    font-style: italic;
  }
  .label {
    background: transparent;
    border: 0;
    color: inherit;
    padding: 0.4rem 0.6rem;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font: inherit;
    min-width: 0;
    /* Truncate long names so the .tab cap above actually clips
       cleanly; flex children need the explicit overflow chain. */
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .label > :not(.dot):not(.bcast) {
    /* The actual title text - wrap it in the flex layout and let
       it ellipsis. The dot + broadcast badge stay fixed-size. */
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .tab-icon {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    margin-right: 0.25rem;
  }
  .tab-label-segs {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .seg-sep { color: var(--surface1); padding: 0 0.05rem; }
  .seg-more { color: var(--overlay1); font-size: 0.72rem; margin-left: 0.15rem; }
  /* Yellow tint for SFTP folder marker - matches the openSftp pane
     toolbar button colour. */
  :global(.tab-label-segs .sftp-marker) { color: var(--yellow); }
  .uptime {
    color: var(--overlay0);
    font-size: 0.7rem;
    margin-left: 0.3rem;
    font-variant-numeric: tabular-nums;
  }
  .group-chip {
    color: var(--on-accent);
    font-size: 0.62rem;
    font-weight: 600;
    padding: 0 0.35rem;
    border-radius: 999px;
    line-height: 1.4;
    margin-right: 0.25rem;
    max-width: 7rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .close {
    background: transparent;
    border: 0;
    color: var(--overlay0);
    cursor: pointer;
    padding: 0.4rem 0.5rem;
    font: inherit;
  }
  .close:hover { color: var(--red); }
  .dot {
    width: 7px; height: 7px;
    border-radius: 50%;
    display: inline-block;
  }
  .rec-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--red);
    margin-right: 0.15rem;
    flex-shrink: 0;
    animation: rec-pulse 1.6s ease-in-out infinite;
  }
  @keyframes rec-pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.35; }
  }
  .mcp-badge {
    display: inline-flex;
    align-items: center;
    color: var(--blue);
    margin-right: 0.15rem;
    flex-shrink: 0;
  }
  .share-badge {
    display: inline-flex;
    align-items: center;
    color: var(--green);
    font-size: 0.7rem;
    margin-right: 0.15rem;
    flex-shrink: 0;
  }
  /* The banner stays a grid child even when inactive - display:none would
     remove it from the grid, shifting .term-area into the middle `auto` row
     (0 height) instead of the `1fr` row, which blanked the terminal. So it
     collapses via height/padding/border, keeping the row count stable. */
  .control-guest-banner {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.8rem;
    background: var(--red);
    color: var(--base);
    font-size: 0.82rem;
    font-weight: 600;
    height: 0;
    padding: 0;
    overflow: hidden;
    visibility: hidden;
  }
  .control-guest-banner.shown {
    height: auto;
    padding: 0.3rem 0.6rem;
    visibility: visible;
  }
  .control-guest-banner button {
    background: var(--base);
    color: var(--red);
    border: none;
    border-radius: 5px;
    padding: 0.15rem 0.7rem;
    cursor: pointer;
    font-weight: 600;
  }
  .share-badge.control {
    color: var(--red);
    animation: rec-pulse 1.6s ease-in-out infinite;
  }
  /* A small ring marking the tab a guest is currently looking at. Distinct
     from the filled share dot (a guest is attached) - this is a hollow ring
     (a guest's gaze). No emoji, per house style. */
  .guest-watching {
    display: inline-block;
    width: 7px;
    height: 7px;
    border: 1.5px solid var(--sky, #89dceb);
    border-radius: 50%;
    margin-right: 0.2rem;
    flex-shrink: 0;
  }
  .bcast {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    color: var(--peach);
    margin-right: 0.15rem;
  }
  .bcast.partial { opacity: 0.55; }
  .bcast-groups {
    font-size: 0.62rem;
    line-height: 1;
    color: var(--peach);
    background: color-mix(in oklab, var(--peach) 18%, transparent);
    padding: 0.05rem 0.25rem;
    border-radius: 6px;
    max-width: 8ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tabbar-end {
    margin-left: auto;
    display: flex;
    align-items: center;
    padding: 0 0.3rem;
    border-left: 1px solid var(--surface0);
  }
  .bcast-btn {
    background: transparent; color: var(--subtext0); border: 0;
    padding: 0.3rem 0.45rem;
    cursor: pointer; border-radius: 3px;
    display: inline-flex; align-items: center; gap: 0.25rem;
  }
  .bcast-btn:hover { background: var(--surface0); color: var(--text); }
  .bcast-btn.active {
    background: var(--peach);
    color: var(--on-accent);
  }
  .bcast-btn.active:hover { background: #f9c89a; }
  .bcount { font-size: 0.7rem; font-weight: 600; }
  .term-area {
    position: relative;
    overflow: hidden;
  }
  .tab-content {
    position: absolute;
    inset: 0;
    visibility: hidden;
    pointer-events: none;
  }
  .tab-content.active {
    visibility: visible;
    pointer-events: auto;
  }
</style>
