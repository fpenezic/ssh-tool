<script lang="ts">
  // Recursive component: renders either one Terminal (leaf) or two PaneNodes
  // with a resize handle between them (split).

  import Terminal from "./Terminal.svelte";
  import SftpPane from "./SftpPane.svelte";
  import VncPane from "./VncPane.svelte";
  import PaneNodeSelf from "./PaneNode.svelte";
  import { paneTabs, sessions, drag, tree, view, mcpShared, mcpBridge, type PaneNode } from "./stores.svelte";
  import { api } from "./api";
  import { copyText, copySensitive } from "./clipboard";
  import {
    IconHost, IconUser, IconLock, IconClipboardCopy, IconFolder,
    IconRotateCw, IconSplitH, IconSplitV, IconX, IconBroadcast,
    IconActivity, IconGlobe, IconTunnel, IconSearch, IconSettings, IconVpn,
    IconBot,
  } from "./iconMap";
  import { broadcast } from "./broadcast.svelte";
  import { tcpdump } from "./tcpdumpStore.svelte";
  import { focusActiveTerminal } from "./terminalFocus";
  import HttpModal from "./HttpModal.svelte";
  import TunnelPopover from "./TunnelPopover.svelte";
  import LlmSharePopover from "./LlmSharePopover.svelte";
  import McpActivityPanel from "./McpActivityPanel.svelte";
  import { isMobile } from "./platform";

  interface Props {
    tabId: string;
    node: PaneNode;
  }
  let { tabId, node }: Props = $props();

  const activePaneId = $derived(
    paneTabs.tabs.find((t) => t.tabId === tabId)?.activePaneId ?? ""
  );

  // ----- Resize handle for split nodes -----

  let containerEl: HTMLDivElement | undefined = $state();
  let dragging = $state(false);

  function startDrag(e: PointerEvent) {
    if (node.kind !== "split") return;
    e.preventDefault();
    dragging = true;
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  }
  function onMove(e: PointerEvent) {
    if (!dragging || node.kind !== "split" || !containerEl) return;
    const rect = containerEl.getBoundingClientRect();
    const ratio =
      node.direction === "horizontal"
        ? (e.clientX - rect.left) / rect.width
        : (e.clientY - rect.top) / rect.height;
    paneTabs.setSplitRatio(tabId, node.id, ratio);
  }
  function endDrag(e: PointerEvent) {
    dragging = false;
    try {
      (e.target as HTMLElement).releasePointerCapture(e.pointerId);
    } catch {}
  }

  function activate() {
    if (node.kind !== "pane") return;
    paneTabs.setActivePane(tabId, node.id);
  }

  // ----- Per-leaf controls (split horizontally / vertically / close) -----

  async function splitLeaf(direction: "horizontal" | "vertical") {
    if (node.kind !== "pane") return;
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return;
    // Reuse the same connection that the existing pane uses.
    const existingSession = sessions.tabs.find((s) => s.sessionId === node.sessionId);
    if (!existingSession) return;
    try {
      const r = await api.sshConnect(existingSession.connectionId);
      sessions.add({
        sessionId: r.session_id,
        connectionId: existingSession.connectionId,
        name: existingSession.name,
        hostname: existingSession.hostname,
        status: "connected",
      });
      paneTabs.splitPane(tabId, node.id, direction, r.session_id);
    } catch (e) {
      console.error("split failed", e);
    }
  }

  // ----- Drag-and-drop: accept a connection from the sidebar -----

  type DropZone = "left" | "right" | "top" | "bottom";
  let dropZone = $state<DropZone | null>(null);

  function getDropZone(e: DragEvent): DropZone | null {
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const x = (e.clientX - rect.left) / rect.width;
    const y = (e.clientY - rect.top) / rect.height;
    const edge = 0.3;
    if (x < edge) return "left";
    if (x > 1 - edge) return "right";
    if (y < edge) return "top";
    if (y > 1 - edge) return "bottom";
    return null;
  }

  function onDragOver(e: DragEvent) {
    if (!drag.active || node.kind !== "pane") return;
    // Don't allow a tab to drop onto its own pane.
    if (drag.tabId) {
      const srcTab = paneTabs.tabs.find((t) => t.tabId === drag.tabId);
      if (srcTab && paneTabs.findTabForSession(node.sessionId)?.tabId === srcTab.tabId) return;
    }
    const zone = getDropZone(e);
    dropZone = zone;
    if (zone) e.preventDefault();
  }

  function onDragLeave(e: DragEvent) {
    if (!e.relatedTarget || !(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) {
      dropZone = null;
    }
  }

  async function onDrop(e: DragEvent) {
    e.preventDefault();
    const zone = dropZone;
    dropZone = null;
    if (node.kind !== "pane" || !zone) { drag.end(); return; }
    // Locked tabs (VNC consoles) refuse drops - they own the whole tab.
    if (paneTabs.tabs.find((t) => t.tabId === tabId)?.locked) { drag.end(); return; }

    // Tab-drag (existing session): move into the target pane.
    if (drag.tabId) {
      const sourceTabId = drag.tabId;
      const srcTab = paneTabs.tabs.find((t) => t.tabId === sourceTabId);
      drag.end();
      if (!srcTab) return;
      const srcLeaf = paneTabs.activePane(sourceTabId);
      if (!srcLeaf) return;
      const movedSessionId = srcLeaf.sessionId;
      const direction = (zone === "left" || zone === "right") ? "horizontal" : "vertical";
      const side = (zone === "right" || zone === "bottom") ? "b" : "a";
      paneTabs.splitPane(tabId, node.id, direction, movedSessionId, side);
      paneTabs.removeTab(sourceTabId);
      if (paneTabs.tabs.length === 0) view.setTab("connections");
      return;
    }

    // Sidebar-drag path: new connection, new session.
    if (!drag.connectionId) { drag.end(); return; }
    const connId = drag.connectionId;
    drag.end();
    const conn = tree.connectionById(connId);
    if (!conn) return;

    const targetWasBroadcasting =
      node.kind === "pane" && broadcast.hasInAnyGroup(node.sessionId);

    try {
      const r = await api.sshConnect(connId);
      sessions.add({
        sessionId: r.session_id,
        connectionId: connId,
        name: conn.name,
        hostname: conn.hostname ?? "",
        status: "connected",
      });
      const direction = (zone === "left" || zone === "right") ? "horizontal" : "vertical";
      const side = (zone === "right" || zone === "bottom") ? "b" : "a";
      paneTabs.splitPane(tabId, node.id, direction, r.session_id, side);
      if (targetWasBroadcasting) {
        broadcast.add(r.session_id).catch(console.warn);
      }
    } catch (err) {
      console.error("drop connect failed", err);
    }
  }

  const paneSession = $derived(
    node.kind === "pane" ? sessions.tabs.find((s) => s.sessionId === node.sessionId) : null
  );
  // Local PTY sessions have no SSH channel, so SFTP / tcpdump / HTTP
  // (which routes over SOCKS5 forwards) / broadcast / reconnect /
  // forward listing make no sense. Hide those toolbar groups when
  // this pane is a local shell.
  const isLocalPane = $derived(paneSession?.kind === "local");
  // VNC consoles render noVNC full-bleed: no SSH channel, no PTY, and
  // the tab is locked to a single leaf. Hide SFTP / tcpdump / HTTP /
  // broadcast / split / forwards just like a local pane, plus the
  // split controls (the locked tab refuses them).
  const isVncPane = $derived(node.kind === "pane" && node.view === "vnc");
  const noSshPane = $derived(isLocalPane || isVncPane);

  // VNC console state surfaced from VncPane via $bindable + an onControls
  // callback, so the pane header can render the status pill + control
  // buttons inline (VncPane keeps only the screen). This is the
  // one-directional replacement for the old vncControls global registry:
  // VncPane writes these down its bindings, PaneNode only reads them and
  // invokes the handlers - no $effect republish, no read-back cycle (that
  // cycle froze the WebView; see test7/test8).
  let vncStatus = $state<"connecting" | "connected" | "error" | "disconnected" | "needpass">("connecting");
  let vncScaled = $state(true);
  let vncDotCursor = $state(true);
  let vncCtl = $state<{
    toggleScale: () => void;
    toggleDotCursor: () => void;
    sendCAD: () => void;
    reconnect: () => void;
  } | null>(null);
  // Whether the scrollback search bar is open for this pane's session, so
  // the header search button reflects it as an active toggle. Terminal
  // broadcasts terminal:searchstate on every open/close (button, Esc, ✕).
  let searchActive = $state(false);
  $effect(() => {
    const onState = (e: Event) => {
      const ce = e as CustomEvent<{ sessionId: string; open: boolean }>;
      if (node.kind === "pane" && ce.detail?.sessionId === node.sessionId) {
        searchActive = ce.detail.open;
      }
    };
    window.addEventListener("terminal:searchstate", onState);
    return () => window.removeEventListener("terminal:searchstate", onState);
  });

  const paneStatusColor = $derived(
    paneSession?.status === "connected"    ? "var(--green)" :
    paneSession?.status === "connecting"   ? "var(--yellow)" :
    paneSession?.status === "reconnecting" ? "var(--peach)" :
    paneSession?.status === "error"        ? "var(--red)" : "var(--overlay0)"
  );
  // Color tag resolved through the connection's folder ancestry.
  const paneTagColor = $derived(
    paneSession ? tree.resolveColorForConnection(paneSession.connectionId) : ""
  );

  // ---------- Quick-copy buttons ----------
  let copyHint = $state<string | null>(null);
  let copyHintTimer: ReturnType<typeof setTimeout> | null = null;
  // tcpdump capture lifetime is owned by the window-level tcpdump store
  // (see tcpdumpStore.svelte.ts) and the host mounted in TerminalArea -
  // NOT here. PaneNode is rebuilt on every layout mutation, so anything
  // capture-related mounted in this subtree would die on a split / SFTP
  // / drag. PaneNode only reads the store (for the toolbar chip) and
  // toggles it open. The derived below re-reads on store.version bumps.
  const tcpdumpMode = $derived(
    node.kind === "pane" ? tcpdump.modeOf(node.sessionId) : null,
  );
  const tcpdumpStats = $derived(
    node.kind === "pane" ? tcpdump.statsOf(node.sessionId) : null,
  );
  let showHttp = $state(false);
  let showTunnels = $state(false);
  let showLlmShare = $state(false);
  let showLlmActivity = $state(false);

  // Live count of forwards that are currently listening on this
  // pane's session. Shown as a small badge on the tunnels button so
  // the user can tell at a glance whether something is up. Polled on
  // a slow interval (3s) only while the pane is mounted with an
  // active SSH session - no badge means "no live tunnels", not
  // "data missing". 3s is a deliberate compromise so 8 split panes
  // on different sessions don't fire 8 × 2s requests like the full
  // PortForwards view does; the badge only needs ballpark accuracy.
  let activeForwardCount = $state(0);
  $effect(() => {
    // Reset whenever the session under this pane changes.
    activeForwardCount = 0;
    const sid = paneSession?.kind !== "local" && paneSession?.status === "connected"
      ? paneSession.sessionId
      : "";
    if (!sid) return;
    let cancelled = false;
    async function tick() {
      try {
        const list = (await api.forwardsActive(sid)) ?? [];
        if (cancelled) return;
        activeForwardCount = list.filter((f) => f.state === "listening").length;
      } catch {
        if (cancelled) return;
        activeForwardCount = 0;
      }
    }
    tick();
    const h = setInterval(tick, 3000);
    return () => { cancelled = true; clearInterval(h); };
  });

  // Discover a capture this window didn't start. After a tab detach the
  // session lands in a new window whose tcpdump store is empty, but the
  // backend capture is still running (session-scoped). Ask the backend
  // once on connect; if a capture exists and the store doesn't know it,
  // register it as a background chip so the user sees it's live and can
  // restore/attach. Guarded so we don't re-add one the user just closed.
  $effect(() => {
    const sid = node.kind === "pane" && paneSession?.kind !== "local"
      && paneSession?.status === "connected" ? node.sessionId : "";
    if (!sid) return;
    if (tcpdump.modeOf(sid)) return; // already tracked in this window
    let cancelled = false;
    (async () => {
      try {
        const info = await api.tcpdumpActiveForSession(sid);
        if (cancelled || !info.dump_id) return;
        if (!tcpdump.modeOf(sid)) tcpdump.ensureMinimized(sid);
      } catch { /* no active capture */ }
    })();
    return () => { cancelled = true; };
  });

  function flashCopyHint(msg: string) {
    copyHint = msg;
    if (copyHintTimer) clearTimeout(copyHintTimer);
    copyHintTimer = setTimeout(() => { copyHint = null; }, 2200);
  }

  async function copyField(field: "username" | "hostname" | "ssh") {
    if (!paneSession) return;
    try {
      const info = await api.connectionCopyInfo(paneSession.connectionId);
      let val = "";
      let label = "";
      switch (field) {
        case "username": val = info.username; label = "Username"; break;
        case "hostname": val = info.hostname; label = "Host"; break;
        case "ssh":      val = info.ssh_command; label = "ssh command"; break;
      }
      if (!val) { flashCopyHint(`${label}: (empty)`); return; }
      await copyText(val, { toast: false });
      flashCopyHint(`${label} copied`);
    } catch (e: any) {
      flashCopyHint("Copy failed: " + (e?.message ?? e));
    }
  }

  async function copyPassword() {
    if (!paneSession) return;
    try {
      const pw = await api.connectionRevealPassword(paneSession.connectionId);
      await copySensitive(pw, { toast: false });
      flashCopyHint("Password copied (clears in 30s)");
    } catch (e: any) {
      flashCopyHint("No password: " + (e?.message ?? e));
    }
  }

  // Disconnect this leaf's session and start a fresh one against the
  // same connection. The pane tree's swapSessionId rebinds every leaf
  // that referenced the old id (covers the SFTP-shares-session case).
  function paneBroadcastTitle(): string {
    if (node.kind !== "pane") return "Add to default broadcast group";
    const groups = broadcast.groupsOf(node.sessionId).map((g) => g === "" ? "default" : g);
    if (groups.length === 0) return "Add to default broadcast group";
    return `In broadcast group${groups.length === 1 ? "" : "s"}: ${groups.join(", ")} - click to toggle default group`;
  }

  async function reconnectLeaf() {
    if (node.kind !== "pane") return;
    const oldId = node.sessionId;
    const sess = sessions.tabs.find((s) => s.sessionId === oldId);
    if (!sess) return;
    const conn = tree.connectionById(sess.connectionId);
    if (!conn) return;
    try {
      // Tear down first so the new session doesn't share quirks with
      // the dying one (forwards / SFTP cache live on Session).
      try { await api.sshDisconnect(oldId); } catch {}
      sessions.remove(oldId);
      const r = await api.sshConnect(sess.connectionId);
      sessions.add({
        sessionId: r.session_id,
        connectionId: sess.connectionId,
        name: conn.name,
        hostname: conn.hostname,
        status: "connected",
      });
      paneTabs.swapSessionId(oldId, r.session_id);
    } catch (e) {
      console.error("reconnect failed", e);
    }
  }

  async function closeLeaf() {
    if (node.kind !== "pane") return;
    const sessionId = node.sessionId;
    // If a reconnect is in flight on this session, the X button cancels
    // the retry instead of closing - saves an extra modal and matches
    // the user's mental model (X = "stop whatever is happening").
    const sess = sessions.tabs.find((s) => s.sessionId === sessionId);
    if (sess?.status === "reconnecting") {
      try { await api.sshCancelReconnect(sessionId); } catch {}
      sessions.setStatus(sessionId, "disconnected", "cancelled");
      return;
    }
    // Remove the leaf first; only disconnect the underlying session if no
    // other leaf still references it (an SFTP split shares the session
    // with its sibling terminal pane).
    paneTabs.closePane(tabId, node.id);
    // Whatever pane/tab got promoted to active should also get keyboard
    // focus - without this the focus dies with the unmounted xterm and
    // the user has to click before typing.
    if (paneTabs.tabs.length > 0) focusActiveTerminal();
    if (paneTabs.countLeavesForSession(sessionId) === 0) {
      // Session is going away entirely - tear down any background capture
      // for it too (the overlay lives up in TerminalArea now, keyed by
      // sessionId, so closing a pane doesn't otherwise reach it). If the
      // session survives in a sibling pane (SFTP split), the capture
      // stays - closing one side of a split must not kill it.
      tcpdump.close(sessionId);
      try {
        if (sess?.kind === "local") {
          await api.localShellDisconnect(sessionId);
        } else {
          await api.sshDisconnect(sessionId);
        }
      } catch {}
      sessions.remove(sessionId);
    }
  }
</script>

{#if node.kind === "pane"}
  {@const isActive = node.id === activePaneId}
  <div
    class="pane"
    class:active={isActive}
    class:tagged={!!paneTagColor}
    style:--pane-tag={paneTagColor || "transparent"}
    onclick={activate}
    role="presentation"
    ondragover={onDragOver}
    ondragleave={onDragLeave}
    ondrop={onDrop}
  >
    <div class="pane-title" style:--tag-color={paneTagColor || "transparent"}>
      {#if paneTagColor}<span class="tag-strip"></span>{/if}
      <span class="pane-dot" style="background: {paneStatusColor}"></span>
      <span class="pane-name">{paneSession?.name ?? "…"}</span>
      {#if paneSession?.hostname}
        <span class="pane-host">{paneSession.hostname}</span>
      {/if}
      {#if paneSession?.networkVia}
        <span class="vpn-badge" title="First hop dials through WireGuard profile '{paneSession.networkVia}'">
          <IconVpn size={10} /> {paneSession.networkVia}
        </span>
      {/if}
      {#if paneSession?.status === "reconnecting"}
        <span class="reconnecting" title={paneSession.statusHint}>
          ↻ reconnect {paneSession.reconnectAttempt}/{paneSession.reconnectMaxAttempts}
          {#if paneSession.reconnectDelay !== undefined}
            in {paneSession.reconnectDelay}s
          {/if}
        </span>
      {:else if paneSession?.status === "disconnected"}
        {#if paneSession.statusHint}
          <span class="disc-hint">- {paneSession.statusHint}</span>
        {/if}
        {#if !noSshPane}
          <button
            class="reconnect-inline"
            title="Reconnect (open a fresh SSH session)"
            onclick={(e) => { e.stopPropagation(); reconnectLeaf(); }}
          ><IconRotateCw size={12} /> Reconnect</button>
        {/if}
      {/if}
      <div class="pane-actions">
        <!-- Copy group: SSH only -->
        {#if !noSshPane}
          <div class="action-group">
            <button
              class="cp host"
              title="Copy host"
              onclick={(e) => { e.stopPropagation(); copyField("hostname"); }}
            ><IconHost size={13} /></button>
            <button
              class="cp user"
              title="Copy username"
              onclick={(e) => { e.stopPropagation(); copyField("username"); }}
            ><IconUser size={13} /></button>
            <button
              class="cp pass"
              title="Copy password (clears clipboard after 30s)"
              onclick={(e) => { e.stopPropagation(); copyPassword(); }}
            ><IconLock size={13} /></button>
            <button
              class="cp ssh"
              title="Copy ssh command"
              onclick={(e) => { e.stopPropagation(); copyField("ssh"); }}
            ><IconClipboardCopy size={13} /></button>
          </div>
        {/if}

        <!-- Tools group: SSH only. Hidden on mobile - SFTP split, tcpdump,
             HTTP probe and tunnels are advanced desktop workflows that don't
             fit a phone toolbar. -->
        {#if !noSshPane && !isMobile}
          <div class="action-group">
            {#if node.view !== "sftp"}
              <button
                class="openSftp"
                title="Open SFTP browser on the same session (split right)"
                onclick={(e) => {
                  e.stopPropagation();
                  paneTabs.splitPaneShareSession(tabId, node.id, "horizontal", "sftp", "b");
                }}
              ><IconFolder size={13} /></button>
            {/if}
            <button
              class="tcpdump"
              class:running={tcpdumpMode === "open"}
              class:bg={tcpdumpMode === "minimized"}
              title={tcpdumpMode === "minimized" && tcpdumpStats
                ? `tcpdump on ${tcpdumpStats.iface} running in background - ${tcpdumpStats.packets} packets${tcpdumpStats.insights > 0 ? `, ${tcpdumpStats.insights} insights` : ""} (counts in status bar; click to restore)`
                : "Live tcpdump on this host"}
              onclick={(e) => { e.stopPropagation(); tcpdump.open(node.sessionId); }}
            ><IconActivity size={13} /></button>
            <button
              class="http"
              title="HTTP / SOAP request (routes through this session's SOCKS5 if running)"
              onclick={(e) => { e.stopPropagation(); showHttp = true; }}
            ><IconGlobe size={13} /></button>
            <div class="tunnel-anchor">
              <button
                class="tunnels"
                class:has-active={activeForwardCount > 0}
                title={activeForwardCount > 0
                  ? `${activeForwardCount} active tunnel${activeForwardCount === 1 ? "" : "s"} - toggle / open bookmarks`
                  : "Toggle tunnels / open bookmarks"}
                onclick={(e) => { e.stopPropagation(); showTunnels = !showTunnels; }}
              >
                <IconTunnel size={13} />
              </button>
              {#if showTunnels && paneSession}
                <TunnelPopover
                  connectionId={paneSession.connectionId}
                  sessionId={paneSession.status === "connected" ? paneSession.sessionId : ""}
                  onClose={() => (showTunnels = false)}
                />
              {/if}
            </div>
            <div class="tunnel-anchor">
              {#if mcpBridge.enabled}
              <button
                class="llm-share"
                class:has-active={paneSession?.status === "connected" && mcpShared.has(paneSession.sessionId)}
                title="Share this session with an LLM (MCP)"
                onclick={(e) => { e.stopPropagation(); showLlmShare = !showLlmShare; }}
              >
                <IconBot size={13} />
              </button>
              {/if}
              {#if showLlmShare && paneSession}
                <LlmSharePopover
                  sessionId={paneSession.status === "connected" ? paneSession.sessionId : ""}
                  onClose={() => (showLlmShare = false)}
                  onViewActivity={() => { showLlmShare = false; showLlmActivity = true; }}
                />
              {/if}
              {#if showLlmActivity && paneSession}
                <McpActivityPanel
                  sessionId={paneSession.status === "connected" ? paneSession.sessionId : ""}
                  onClose={() => (showLlmActivity = false)}
                />
              {/if}
            </div>
            <button
              class="edit-conn"
              title="Edit this connection (jump to its settings in the tree)"
              onclick={(e) => {
                e.stopPropagation();
                if (paneSession?.connectionId) view.reveal("connection", paneSession.connectionId);
              }}
            ><IconSettings size={13} /></button>
          </div>

          <!-- Session group: search + broadcast + reconnect -->
          <div class="action-group">
            {#if node.view !== "sftp"}
              <button
                class="search-btn"
                class:active={searchActive}
                title="Search the scrollback (Ctrl+Shift+F)"
                onclick={(e) => {
                  e.stopPropagation();
                  if (node.kind === "pane") {
                    window.dispatchEvent(new CustomEvent("terminal:search", { detail: { sessionId: node.sessionId } }));
                  }
                }}
              ><IconSearch size={13} /></button>
            {/if}
            <button
              class="bcast-btn"
              class:active={node.kind === "pane" && broadcast.hasInAnyGroup(node.sessionId)}
              title={paneBroadcastTitle()}
              onclick={(e) => {
                e.stopPropagation();
                if (node.kind === "pane") broadcast.toggle(node.sessionId);
              }}
            ><IconBroadcast size={13} /></button>
            <button class="reconnect" title="Reconnect (disconnect + open fresh session)" onclick={(e) => { e.stopPropagation(); reconnectLeaf(); }}><IconRotateCw size={13} /></button>
          </div>
        {/if}

        <!-- VNC console group: status pill + console controls, inline in the
             pane header so VncPane keeps the full area for the screen. State
             flows up from VncPane via $bindable; handlers via onControls. No
             reactive republish (the registry version froze the WebView). -->
        {#if isVncPane && vncCtl}
          <span
            class="vnc-pill"
            class:ok={vncStatus === "connected"}
            class:err={vncStatus === "error"}
          >
            {#if vncStatus === "connecting"}Connecting...
            {:else if vncStatus === "connected"}Connected
            {:else if vncStatus === "disconnected"}Disconnected
            {:else if vncStatus === "needpass"}Password required
            {:else}Error{/if}
          </span>
          <div class="action-group">
            <button
              class:active={vncScaled}
              title="Toggle fit-to-window scaling"
              onclick={(e) => { e.stopPropagation(); vncCtl?.toggleScale(); }}
            >{vncScaled ? "Fit" : "1:1"}</button>
            <button
              class:active={vncDotCursor}
              title="Show a dot cursor when the server sends none"
              onclick={(e) => { e.stopPropagation(); vncCtl?.toggleDotCursor(); }}
            >Dot</button>
            <button
              disabled={vncStatus !== "connected"}
              title="Send Ctrl+Alt+Del"
              onclick={(e) => { e.stopPropagation(); vncCtl?.sendCAD(); }}
            >C-A-D</button>
            <button
              title="Reconnect"
              onclick={(e) => { e.stopPropagation(); vncCtl?.reconnect(); }}
            ><IconRotateCw size={13} /></button>
          </div>
        {/if}

        <!-- Layout group. VNC tabs are locked to one full leaf: no splits.
             Splits are hidden on mobile (a phone has no room for side-by-side
             panes); the close button stays. -->
        <div class="action-group">
          {#if !isVncPane && !isMobile}
            <button title="Split right" onclick={(e) => { e.stopPropagation(); splitLeaf("horizontal"); }}><IconSplitH size={13} /></button>
            <button title="Split down"  onclick={(e) => { e.stopPropagation(); splitLeaf("vertical"); }}><IconSplitV size={13} /></button>
          {/if}
          <button title="Close pane"  class="close" onclick={(e) => { e.stopPropagation(); closeLeaf(); }}><IconX size={13} /></button>
        </div>
      </div>
    </div>
    {#if copyHint}
      <div class="copy-hint">{copyHint}</div>
    {/if}
    <div class="term-wrap">
      {#if node.view === "sftp"}
        <SftpPane sessionId={node.sessionId} />
      {:else if node.view === "vnc"}
        <VncPane
          sessionId={node.sessionId}
          bind:status={vncStatus}
          bind:scaled={vncScaled}
          bind:dotCursor={vncDotCursor}
          onControls={(c) => (vncCtl = c)}
        />
      {:else}
        <Terminal sessionId={node.sessionId} active={true} />
      {/if}
    </div>
    {#if dropZone}
      <div class="drop-indicator {dropZone}"></div>
    {/if}
    {#if showHttp && node.kind === "pane"}
      <HttpModal sessionId={node.sessionId} onClose={() => (showHttp = false)} />
    {/if}
  </div>
{:else}
  <div
    class="split"
    class:dragging
    class:horizontal={node.direction === "horizontal"}
    class:vertical={node.direction === "vertical"}
    style:--ratio={node.ratio}
    bind:this={containerEl}
  >
    <div class="child a">
      <PaneNodeSelf {tabId} node={node.a} />
    </div>
    <div
      class="handle"
      role="separator"
      aria-orientation={node.direction === "horizontal" ? "vertical" : "horizontal"}
      onpointerdown={startDrag}
      onpointermove={onMove}
      onpointerup={endDrag}
      onpointercancel={endDrag}
    ></div>
    <div class="child b">
      <PaneNodeSelf {tabId} node={node.b} />
    </div>
  </div>
{/if}

<style>
  .pane {
    display: grid;
    grid-template-rows: 22px 1fr;
    width: 100%;
    height: 100%;
    border: 1px solid transparent;
    box-sizing: border-box;
    position: relative;
  }
  /* Color tag rendered as a 3px left strip on the whole pane -
     mirrors how sidebar tree rows mark environment (prod/staging
     /dev) and keeps the signal visible while you read the
     terminal output. Inset shadow avoids reflow vs. an extra
     border. */
  .pane.tagged {
    box-shadow: inset 3px 0 0 var(--pane-tag);
  }
  .pane.active {
    border-color: var(--blue)55;
  }
  .pane-title {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0 0.4rem;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.73rem;
    min-width: 0;
    user-select: none;
  }
  .pane-dot {
    flex-shrink: 0;
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
  .tag-strip {
    flex-shrink: 0;
    width: 3px;
    align-self: stretch;
    margin: 0 0.1rem 0 -0.2rem;
    background: var(--tag-color);
    border-radius: 2px;
  }
  .pane-name {
    font-weight: 500;
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  /* VNC status pill in the header (connected / connecting / error). */
  .vnc-pill {
    font-size: 0.72rem;
    color: var(--overlay1);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .vnc-pill.ok { color: var(--green); }
  .vnc-pill.err { color: var(--red); }
  .reconnecting {
    color: var(--peach);
    font-size: 0.72rem;
    margin-left: 0.4rem;
    flex-shrink: 0;
  }
  .disc-hint {
    color: var(--red);
    font-size: 0.72rem;
    margin-left: 0.3rem;
  }
  .reconnect-inline {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    margin-left: 0.5rem;
    padding: 0.15rem 0.5rem;
    background: var(--blue);
    color: var(--on-accent);
    border: 0;
    border-radius: 3px;
    cursor: pointer;
    font: inherit;
    font-size: 0.72rem;
    font-weight: 600;
  }
  .reconnect-inline:hover { background: var(--sapphire); }
  .pane-host {
    color: var(--overlay0);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex-shrink: 1;
    min-width: 0;
  }
  .vpn-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    font-size: 0.62rem;
    font-weight: 600;
    padding: 0.02rem 0.4rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--mauve, #b675f0) 22%, transparent);
    color: var(--mauve, #b675f0);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .pane-actions {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-shrink: 0;
  }
  .action-group {
    display: flex;
    gap: 1px;
    padding-right: 0.5rem;
    border-right: 1px solid var(--surface0);
  }
  .action-group:last-child {
    padding-right: 0;
    border-right: 0;
  }
  /* Broadcast button gets an orange tint by default + brighter when
     active so the eye finds it without the icon. */
  .pane-actions button.bcast-btn { color: var(--peach); }
  .pane-actions button.bcast-btn.active {
    background: var(--peach);
    color: var(--on-accent);
  }
  .pane-actions button {
    background: transparent;
    /* Default a touch brighter than before (var(--overlay0) was the bug user
       called out - washed out grey on a near-black bar). Accent
       modifiers below colour each action so the eye can find Host /
       User / Password / Copy without reading the icon. */
    color: var(--subtext0);
    border: none;
    border-radius: 3px;
    width: 20px;
    height: 18px;
    font-size: 0.78rem;
    cursor: pointer;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .pane-actions button:hover {
    background: var(--surface0);
    color: var(--text);
  }
  /* Semantic accents - Catppuccin palette */
  .pane-actions button.cp.host   { color: var(--blue); }
  .pane-actions button.cp.user   { color: var(--mauve); }
  .pane-actions button.cp.pass   { color: var(--peach); }
  .pane-actions button.cp.ssh    { color: var(--green); }
  .pane-actions button.openSftp  { color: var(--yellow); }
  .pane-actions button.reconnect { color: var(--teal); }
  .pane-actions button.tcpdump   { color: var(--pink); }
  .pane-actions button.tcpdump.running { color: var(--on-accent); background: var(--pink); }
  /* Background capture (minimised): a small pulsing green dot in the
     corner marks "a capture runs here" without a number - the packet /
     insight counts live in the bottom status bar now. */
  .pane-actions button.tcpdump.bg { position: relative; color: var(--pink); }
  .pane-actions button.tcpdump.bg::after {
    content: "";
    position: absolute;
    top: 1px; right: 1px;
    width: 5px; height: 5px;
    border-radius: 50%;
    background: var(--green);
    animation: td-pulse 1.4s ease-in-out infinite;
  }
  @keyframes td-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }
  .pane-actions button.http      { color: var(--sapphire); }
  .pane-actions button.tunnels   { color: var(--lavender); }
  .pane-actions button.tunnels.has-active { color: var(--green); }
  .pane-actions button.llm-share.has-active { color: var(--blue); }
  .tunnel-anchor { position: relative; display: inline-flex; }
  .pane-actions button.close:hover {
    background: var(--red);
    color: var(--on-accent);
  }
  .pane-actions button.active {
    background: var(--peach);
    color: var(--on-accent);
  }
  .pane-actions button.active:hover { background: #f9c89a; }
  /* Search toggle active = a calmer blue, so it doesn't read as the
     broadcast (peach) active state. */
  .pane-actions button.search-btn.active {
    background: var(--blue);
    color: var(--on-accent);
  }
  .pane-actions button.cp { font-size: 0.72rem; }
  .copy-hint {
    position: absolute;
    top: 26px;
    right: 6px;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.2rem 0.5rem;
    font-size: 0.72rem;
    color: var(--green);
    z-index: 10;
    box-shadow: 0 2px 6px rgba(0,0,0,0.4);
  }

  .term-wrap {
    position: relative;
    overflow: hidden;
    min-height: 0;
  }

  .drop-indicator {
    position: absolute;
    background: rgba(137, 180, 250, 0.22);
    border: 2px solid var(--blue);
    pointer-events: none;
    z-index: 20;
    border-radius: 3px;
  }
  .drop-indicator.left   { top: 0; left: 0;   width: 50%; height: 100%; }
  .drop-indicator.right  { top: 0; right: 0;  width: 50%; height: 100%; }
  .drop-indicator.top    { top: 0; left: 0;   width: 100%; height: 50%; }
  .drop-indicator.bottom { bottom: 0; left: 0; width: 100%; height: 50%; }

  .split {
    display: grid;
    width: 100%;
    height: 100%;
  }
  .split.horizontal {
    grid-template-columns: var(--ratio_pct) 5px 1fr;
    --ratio_pct: calc(var(--ratio) * 100%);
  }
  .split.vertical {
    grid-template-rows: var(--ratio_pct) 5px 1fr;
    --ratio_pct: calc(var(--ratio) * 100%);
  }
  .child {
    overflow: hidden;
    min-width: 0;
    min-height: 0;
  }
  .handle {
    background: var(--surface0);
    user-select: none;
  }
  .split.horizontal .handle {
    cursor: col-resize;
  }
  .split.vertical .handle {
    cursor: row-resize;
  }
  .handle:hover,
  .split.dragging .handle {
    background: var(--blue);
  }
</style>
