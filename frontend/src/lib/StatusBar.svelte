<script lang="ts">
  // VS Code-style status bar pinned to the bottom of the app window.
  // Compact (22px) so it doesn't eat real estate; surfaces just the
  // things you'd otherwise have to hunt for:
  //   - live session count + breakdown (connected / connecting / err)
  //   - broadcast group size if any
  //   - vault lock state
  //   - currently-focused connection name when on the Terminal tab
  //
  // Click handlers on segments do small things (jump tab, open the
  // broadcast manager). Kept lean - full controls remain in their
  // existing pane toolbars.

  import { sessions, paneTabs, view, tree, mcpShared, shareShared } from "./stores.svelte";
  import SharePanel from "./SharePanel.svelte";
  import { errMsg } from "./connectErrors";
  import { broadcast } from "./broadcast.svelte";
  import { tcpdump } from "./tcpdumpStore.svelte";
  import { IconBroadcast, IconHost, IconFolder, IconTunnel, IconLock, IconActivity, IconRefresh, IconCpu, IconMemory, IconDisk, IconUsers, IconVpn, IconBot } from "./iconMap";
  import McpActivityPanel from "./McpActivityPanel.svelte";
  import { networkProfiles } from "./networkProfiles.svelte";
  import { terminalPrefs } from "./terminalPrefs.svelte";
  import type { ServerStats } from "./api";
  import { syncState } from "./syncState.svelte";
  import { workspaces } from "./workspaces.svelte";
  import { updateCheck } from "./updateCheck.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { onMount, onDestroy } from "svelte";
  import { api } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import UpdateModal from "./UpdateModal.svelte";

  let updateModalOpen = $state(false);

  // Global running-tunnel count, polled on a slow interval. We don't
  // gate this on session count because the badge belongs to the
  // whole app, not the focused pane - even if no terminal is open,
  // a hidden detached window might still hold a session with a live
  // forward. 3s matches the per-pane PaneNode poll cadence.
  let tunnelCount = $state(0);
  let showMcpActivity = $state(false);
  let showSharePanel = $state(false);
  let tunnelTimer: ReturnType<typeof setInterval> | null = null;

  // Vault state tracked here so the pill below can show locked /
  // unlocked + offer a manual Lock now. Synced via VaultStatus on
  // mount and the `vault_locked` event App.svelte already emits.
  let vaultLocked = $state(true);
  let unsubVault: null | (() => void) = null;
  async function refreshVault() {
    try {
      const st = await api.vaultStatus();
      vaultLocked = st?.state !== "unlocked";
    } catch {
      vaultLocked = true;
    }
  }
  async function lockVaultNow() {
    const ok = await showConfirm({
      title: "Lock vault",
      message: "Lock the vault now? You'll be prompted to unlock on the next vault-backed action (new SSH connection, credential edit, …).",
      okLabel: "Lock",
    });
    if (!ok) return;
    try {
      // Forget the sidecar so the next launch also prompts.
      // Without this the auto-unlock kicks back in immediately
      // on the next app start - defeats the point of a manual
      // lock.
      await api.vaultLock(true);
      vaultLocked = true;
      toast.ok("Vault locked");
    } catch (e: any) {
      toast.err(`Lock failed: ${errMsg(e)}`);
    }
  }
  async function refreshTunnels() {
    try {
      const list = (await api.forwardsActive("")) ?? [];
      tunnelCount = list.filter((f) => f.state === "listening").length;
    } catch {
      tunnelCount = 0;
    }
  }

  onMount(() => {
    workspaces.load();
    // Live WG tunnel segment; the store refreshes itself on the
    // network_tunnel_changed event after the first load.
    networkProfiles.load().catch(() => {});
    api.appVersion().then((v) => { version = v.version; }).catch(() => {});
    refreshTunnels();
    tunnelTimer = setInterval(refreshTunnels, 3000);
    refreshVault();
    unsubVault = EventsOn("vault_locked", () => { vaultLocked = true; });
  });
  onDestroy(() => {
    if (tunnelTimer) clearInterval(tunnelTimer);
    unsubVault?.();
  });

  let version = $state<string>("");

  let wsMenuOpen = $state(false);
  let wsBusy = $state(false);
  let wsErr = $state<string | null>(null);

  async function openWorkspace(id: string) {
    wsErr = null;
    wsBusy = true;
    try {
      await workspaces.open(id);
      wsMenuOpen = false;
    } catch (e: any) {
      wsErr = errMsg(e);
    } finally {
      wsBusy = false;
    }
  }
  async function saveCurrentAs() {
    const name = await showPrompt("Workspace name?");
    if (!name?.trim()) return;
    wsErr = null;
    try {
      await workspaces.saveCurrentAs(name.trim());
      wsMenuOpen = false;
    } catch (e: any) {
      wsErr = errMsg(e);
    }
  }
  function manage() {
    wsMenuOpen = false;
    view.setTabSettingsSection("workspaces");
  }

  $effect(() => {
    if (!wsMenuOpen) return;
    function onDoc(e: MouseEvent) {
      const el = (e.target as HTMLElement)?.closest(".ws-wrap");
      if (!el) wsMenuOpen = false;
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  });

  // WireGuard tunnels currently up. Paused profiles can't be running
  // (pausing stops the device), so no extra filter needed.
  const runningVpns = $derived(
    networkProfiles.list.filter((p) => p.status.running),
  );

  const liveCount = $derived(
    sessions.tabs.filter((s) => s.status === "connected").length,
  );
  const connectingCount = $derived(
    sessions.tabs.filter(
      (s) => s.status === "connecting" || s.status === "reconnecting",
    ).length,
  );
  const errorCount = $derived(
    sessions.tabs.filter((s) => s.status === "error").length,
  );
  const totalTabs = $derived(sessions.tabs.length);

  // The connection backing the focused pane on the focused tab.
  const focusedConnName = $derived.by(() => {
    if (view.tab !== "terminal") return "";
    const tabId = paneTabs.activeTabId;
    if (!tabId) return "";
    const leaf = paneTabs.activePane(tabId);
    if (!leaf) return "";
    const s = sessions.tabs.find((x) => x.sessionId === leaf.sessionId);
    if (!s) return "";
    const c = tree.connectionById(s.connectionId);
    return c ? `${c.name} · ${c.hostname}` : s.name;
  });

  // The sessionId of the focused pane, but only for a CONNECTED SSH
  // session on the terminal tab - the only case the server-stats probe can
  // run against. Empty otherwise (so the poll idles).
  const focusedSessionId = $derived.by(() => {
    if (view.tab !== "terminal") return "";
    const tabId = paneTabs.activeTabId;
    if (!tabId) return "";
    const leaf = paneTabs.activePane(tabId);
    if (!leaf?.sessionId) return "";
    const s = sessions.tabs.find((x) => x.sessionId === leaf.sessionId);
    if (!s || s.status !== "connected") return "";
    // Skip local shells / VNC panes - the probe is for remote SSH hosts.
    if (leaf.view && leaf.view !== "terminal") return "";
    return leaf.sessionId;
  });

  // ----- server status (optional, off by default) -----
  //
  // When enabled, probe the focused SSH session's host every 10s for
  // load / memory / disk / users and show it in the bar. We probe ONLY the
  // focused session (not every open one), so nothing runs unless the user
  // has a remote terminal focused with the feature on. Poll teardown mirrors
  // the tunnel poll above.
  let serverStats = $state<ServerStats | null>(null);
  let statsTimer: ReturnType<typeof setInterval> | null = null;
  let statsInFlight = false;

  async function probeServerStats(sid: string) {
    if (!sid || statsInFlight) return;
    statsInFlight = true;
    try {
      const s = await api.sshServerStats(sid);
      // Ignore a stale result if focus moved while the probe was in flight.
      if (sid === focusedSessionId) serverStats = s && s.ok ? s : null;
    } catch {
      if (sid === focusedSessionId) serverStats = null;
    } finally {
      statsInFlight = false;
    }
  }

  // Restart the poll whenever the feature toggles or the focused session
  // changes. $effect re-runs on those reactive reads; the returned cleanup
  // clears the old timer so we never double-poll.
  $effect(() => {
    const on = terminalPrefs.serverStatsEnabled;
    const sid = focusedSessionId;
    if (statsTimer) { clearInterval(statsTimer); statsTimer = null; }
    if (!on || !sid) { serverStats = null; return; }
    // Probe immediately, then every 10s while focus/feature hold.
    serverStats = null;
    probeServerStats(sid);
    statsTimer = setInterval(() => probeServerStats(sid), 10000);
    return () => { if (statsTimer) { clearInterval(statsTimer); statsTimer = null; } };
  });

  function goTerminal() { if (totalTabs > 0) view.setTab("terminal"); }

  // Per-capture rows for the status-bar popover: each running capture in
  // THIS window, resolved to where it lives (tab title + connection
  // name) so the user can see and jump to every one, not just the
  // focused/first. The store is window-local, so a detached capture
  // shows in its own window's bar.
  const tcpdumpRows = $derived.by(() => {
    void tcpdump.membershipVersion;
    void tcpdump.statsVersion;
    return tcpdump.list().map((c) => {
      const tab = paneTabs.findTabForSession(c.sessionId);
      const sess = sessions.tabs.find((s) => s.sessionId === c.sessionId);
      const conn = sess ? tree.connectionById(sess.connectionId) : null;
      const name = conn ? conn.name : (sess?.name ?? c.sessionId.slice(0, 8));
      return {
        sessionId: c.sessionId,
        mode: c.mode,
        stats: c.stats,
        name,
        host: conn?.hostname ?? sess?.hostname ?? "",
        tabTitle: tab?.title ?? "-",
        onActiveTab: tab?.tabId === paneTabs.activeTabId,
        inThisWindow: !!tab,
      };
    });
  });

  const tcpdumpAgg = $derived.by(() => {
    let packets = 0, insights = 0, running = 0;
    for (const r of tcpdumpRows) {
      if (r.stats) {
        packets += r.stats.packets;
        insights += r.stats.insights;
        if (r.stats.running) running++;
      }
    }
    return { count: tcpdumpRows.length, packets, insights, running };
  });

  let tcpdumpMenuOpen = $state(false);

  function fmtCount(n: number): string {
    if (n < 1000) return String(n);
    const k = n / 1000;
    return (k >= 10 ? Math.round(k) : Math.round(k * 10) / 10) + "k";
  }

  // Click a capture row: jump to its tab + pane, then open the modal.
  function gotoCapture(sessionId: string) {
    view.setTab("terminal");
    paneTabs.revealSession(sessionId);
    tcpdump.open(sessionId);
    tcpdumpMenuOpen = false;
  }

  // Clicking the segment: with a single capture, jump straight to it;
  // with several, open the picker so the user can choose which.
  function tcpdumpSegmentClick() {
    if (tcpdumpRows.length === 1) {
      gotoCapture(tcpdumpRows[0].sessionId);
    } else {
      tcpdumpMenuOpen = !tcpdumpMenuOpen;
    }
  }

  // Close the picker on outside click.
  $effect(() => {
    if (!tcpdumpMenuOpen) return;
    function onDoc(e: MouseEvent) {
      const el = (e.target as HTMLElement)?.closest(".td-wrap");
      if (!el) tcpdumpMenuOpen = false;
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  });

  // Pin the Settings target section to About before flipping the
  // view. setTabSettingsSection drives a reactive pendingSection
  // pickup inside Settings so the jump works even when Settings
  // is already mounted (its onMount-only section restore wouldn't
  // re-run otherwise).
  function openAbout() {
    view.setTabSettingsSection("about");
  }

  function openUpdate() {
    // Old behaviour was to open the /releases page in the system
    // browser. We now render the release notes inline so the user
    // doesn't have to leave the app to decide whether to update.
    updateModalOpen = true;
  }
</script>

<footer class="statusbar">
  <div class="ws-wrap">
    <button
      class="seg ws"
      onclick={() => (wsMenuOpen = !wsMenuOpen)}
      title="Workspaces"
    >
      <IconFolder size={11} />
      <span>Workspaces</span>
    </button>
    {#if wsMenuOpen}
      <div class="ws-menu" role="menu">
        {#if wsErr}<div class="ws-err">{wsErr}</div>{/if}
        {#if workspaces.list.length === 0}
          <div class="ws-empty">No workspaces yet.</div>
        {:else}
          {#each workspaces.list as w (w.id)}
            <button
              class="ws-row"
              disabled={wsBusy}
              onclick={() => openWorkspace(w.id)}
              title={w.last_opened_at ? `Last opened ${new Date(w.last_opened_at * 1000).toLocaleString()}` : "Never opened"}
            >
              <span class="ws-name">{w.name}</span>
              {#if w.last_opened_at}
                <span class="ws-meta">{new Date(w.last_opened_at * 1000).toLocaleDateString()}</span>
              {/if}
            </button>
          {/each}
        {/if}
        <div class="ws-sep"></div>
        <button class="ws-action" disabled={paneTabs.tabs.length === 0} onclick={saveCurrentAs}>
          + Save current as…
        </button>
        <button class="ws-action" onclick={manage}>Manage workspaces…</button>
      </div>
    {/if}
  </div>

  <button
    class="seg"
    class:has-error={errorCount > 0}
    onclick={goTerminal}
    title={`Click to jump to Terminal view · ${liveCount} connected · ${connectingCount} connecting · ${errorCount} error · ${totalTabs} total`}
  >
    <span class="seg-label">Sessions</span>
    <span>{liveCount}</span>
    {#if connectingCount > 0}<span class="dim">+{connectingCount}…</span>{/if}
    {#if errorCount > 0}<span class="err">{errorCount}!</span>{/if}
  </button>

  {#if tunnelCount > 0}
    <span class="seg tunnels" title="{tunnelCount} active port forward{tunnelCount === 1 ? "" : "s"}">
      <IconTunnel size={11} />
      <span>{tunnelCount}</span>
    </span>
  {/if}

  {#if runningVpns.length > 0}
    <button
      class="seg vpn"
      onclick={() => view.setTabSettingsSection("network")}
      title={`WireGuard up: ${runningVpns.map((p) => p.name).join(", ")} - click to manage`}
    >
      <IconVpn size={11} />
      <span>{runningVpns.length === 1 ? runningVpns[0].name : runningVpns.length}</span>
    </button>
  {/if}

  {#if mcpShared.size > 0}
    <div class="mcp-anchor">
      <button
        class="seg mcp"
        title="{mcpShared.size} session{mcpShared.size === 1 ? '' : 's'} shared with an LLM - click for activity"
        onclick={() => (showMcpActivity = !showMcpActivity)}
      >
        <IconBot size={11} />
        <span>{mcpShared.size}</span>
      </button>
      {#if showMcpActivity}
        <McpActivityPanel placement="up" onClose={() => (showMcpActivity = false)} />
      {/if}
    </div>
  {/if}

  {#if shareShared.guestCount > 0}
    <div class="mcp-anchor">
      <button
        class="seg share"
        title="{shareShared.guestCount} session{shareShared.guestCount === 1 ? '' : 's'} shared to a browser guest - click to manage"
        onclick={() => (showSharePanel = !showSharePanel)}
      >
        <span class="dot">●</span>
        <span>{shareShared.guestCount}</span>
      </button>
      {#if showSharePanel}
        <div class="share-pop">
          <SharePanel onClose={() => (showSharePanel = false)} />
        </div>
      {/if}
    </div>
  {/if}

  {#if broadcast.totalMembers() > 1}
    <span class="seg bcast" title="{broadcast.totalMembers()} sessions across all broadcast groups">
      <IconBroadcast size={11} />
      <span>{broadcast.totalMembers()}</span>
    </span>
  {/if}

  {#if syncState.remoteAhead}
    <button
      class="seg sync-ahead"
      onclick={() => syncState.quickPull()}
      title={`Sync: newer profile available${syncState.remoteAhead.device ? ` from ${syncState.remoteAhead.device}` : ""} (generation ${syncState.remoteAhead.generation}) - click to pull`}
    >
      <IconRefresh size={11} />
      <span>pull</span>
    </button>
  {/if}

  {#if tcpdumpAgg.count > 0}
    <div class="td-wrap">
      <button
        class="seg tcpdump"
        class:live={tcpdumpAgg.running > 0}
        onclick={tcpdumpSegmentClick}
        title={`${tcpdumpAgg.count} tcpdump capture${tcpdumpAgg.count === 1 ? "" : "s"} · ${tcpdumpAgg.packets} packets${tcpdumpAgg.insights > 0 ? ` · ${tcpdumpAgg.insights} insights` : ""}${tcpdumpAgg.count === 1 ? " - click to open" : " - click to pick"}`}
      >
        <IconActivity size={11} />
        {#if tcpdumpAgg.count > 1}<span class="td-num">{tcpdumpAgg.count}</span>{/if}
        <span>{fmtCount(tcpdumpAgg.packets)}</span>
        {#if tcpdumpAgg.insights > 0}
          <span class="td-alert">{fmtCount(tcpdumpAgg.insights)}</span>
        {/if}
      </button>
      {#if tcpdumpMenuOpen}
        <div class="td-menu" role="menu">
          <div class="td-menu-head">Active captures</div>
          {#each tcpdumpRows as r (r.sessionId)}
            <button class="td-row" onclick={() => gotoCapture(r.sessionId)} title={`${r.name}${r.host ? " · " + r.host : ""} - tab ${r.tabTitle}`}>
              <span class="td-dot" class:live={r.stats?.running}></span>
              <span class="td-row-name">{r.name}</span>
              <span class="td-row-tab">{r.tabTitle}</span>
              <span class="td-row-meta">
                {#if r.stats}
                  <span class="td-row-iface">{r.stats.iface}</span>
                  <span class="td-row-pkts">{fmtCount(r.stats.packets)}</span>
                  {#if r.stats.insights > 0}<span class="td-alert">{fmtCount(r.stats.insights)}</span>{/if}
                {:else}
                  <span class="td-row-iface dim">starting…</span>
                {/if}
                {#if r.mode === "minimized"}<span class="td-bg">bg</span>{/if}
              </span>
            </button>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Vault lock pill withdrawn - user feedback: rare action,
       doesn't justify status-bar real estate. Manual lock is still
       available via the existing auto-lock idle timer + the
       Settings → Vault panel. -->
  {#if false && !vaultLocked}
    <button class="seg vault" onclick={lockVaultNow}>
      <IconLock size={11} />
      <span>Lock vault</span>
    </button>
  {/if}

  <div class="spacer"></div>

  {#if focusedConnName}
    <span class="seg focus" title="Focused pane">
      <IconHost size={11} />
      <span>{focusedConnName}</span>
    </span>
  {/if}

  {#if serverStats}
    <span class="seg stats" title="Server status for the focused session (refreshed every 10s)">
      <span class="stat" title="Load average (1 / 5 / 15 min): {serverStats.load1.toFixed(2)} / {serverStats.load5.toFixed(2)} / {serverStats.load15.toFixed(2)}">
        <IconCpu size={11} />{serverStats.load1.toFixed(2)}
      </span>
      {#if serverStats.mem_used_pct >= 0}
        <span class="stat" title="Memory used">
          <IconMemory size={11} />{Math.round(serverStats.mem_used_pct)}%
        </span>
      {/if}
      {#if serverStats.disk_used_pct >= 0}
        <span class="stat" title="Disk used on /">
          <IconDisk size={11} />{Math.round(serverStats.disk_used_pct)}%
        </span>
      {/if}
      {#if serverStats.users >= 0}
        <span class="stat" title="Logged-in users">
          <IconUsers size={11} />{serverStats.users}
        </span>
      {/if}
    </span>
  {/if}


  {#if updateCheck.available}
    <button
      class="seg update"
      onclick={openUpdate}
      title="A newer release is available - click to view release notes"
    >
      <span>↑ {updateCheck.latest} available</span>
    </button>
  {/if}

  {#if updateModalOpen}
    <UpdateModal onClose={() => (updateModalOpen = false)} />
  {/if}

  {#if version}
    <button
      class="seg version"
      onclick={openAbout}
      title="Click for About"
    >
      <span>{version}</span>
    </button>
  {/if}
</footer>

<style>
  .statusbar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    height: 22px;
    padding: 0 0.6rem;
    background: var(--mantle);
    border-top: 1px solid var(--surface0);
    color: var(--subtext0);
    font-size: 0.7rem;
    font-family: ui-sans-serif, system-ui, sans-serif;
    user-select: none;
  }
  .seg {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0 0.35rem;
    line-height: 1;
    background: transparent;
    border: 0;
    color: inherit;
    font: inherit;
    font-size: 0.7rem;
    border-radius: 2px;
    cursor: default;
  }
  button.seg { cursor: pointer; }
  button.seg:hover { background: var(--surface0); color: var(--text); }
  .seg.has-error { color: var(--yellow); }
  .seg.bcast { color: var(--peach); }
  .seg.vpn { color: var(--mauve, #b675f0); }
  /* Server-status readout: a group of small icon+number stats for the
     focused session, muted so it reads as ambient info. */
  .seg.stats { gap: 0.55rem; color: var(--subtext0); }
  .seg.stats .stat {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    white-space: nowrap;
  }
  .seg.tunnels { color: var(--green); }
  .seg.mcp { color: var(--blue); }
  .seg.share { color: var(--green); }
  .seg.share .dot { font-size: 0.7rem; }
  .mcp-anchor { position: relative; display: inline-flex; }
  .share-pop { position: absolute; bottom: 100%; right: 0; margin-bottom: 0.3rem; z-index: 60; }
  .seg.sync-ahead {
    color: var(--blue);
    cursor: pointer;
    animation: sync-pulse 2.2s ease-in-out infinite;
  }
  @keyframes sync-pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.5; }
  }
  .seg.tcpdump { color: var(--pink); }
  .seg.tcpdump .td-num {
    background: var(--surface1);
    color: var(--text);
    border-radius: 999px;
    padding: 0 0.3rem;
    font-size: 0.6rem;
    font-weight: 700;
  }
  .td-alert {
    background: var(--red);
    color: var(--crust);
    border-radius: 999px;
    padding: 0 0.28rem;
    font-weight: 700;
    font-size: 0.62rem;
  }
  .seg.tcpdump.live > :global(svg) {
    animation: sb-pulse 1.4s ease-in-out infinite;
  }
  @keyframes sb-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.35; } }

  /* tcpdump capture picker - anchored above the segment, like the
     workspace menu. Lists every active capture with where it lives so
     you can jump to each one, not just the focused/first. */
  .td-wrap { position: relative; display: inline-flex; }
  .td-menu {
    position: absolute;
    bottom: 24px;
    left: 0;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    box-shadow: 0 -6px 20px rgba(0,0,0,0.45);
    min-width: 260px;
    max-height: 60vh;
    overflow-y: auto;
    padding: 0.2rem 0;
    z-index: 50;
  }
  .td-menu-head {
    padding: 0.3rem 0.6rem;
    color: var(--overlay0);
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .td-row {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    width: 100%;
    background: transparent;
    color: var(--text);
    border: 0;
    padding: 0.3rem 0.6rem;
    font: inherit;
    font-size: 0.74rem;
    cursor: pointer;
    text-align: left;
  }
  .td-row:hover { background: var(--surface0); }
  .td-dot {
    flex-shrink: 0;
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--overlay0);
  }
  .td-dot.live {
    background: var(--green);
    animation: sb-pulse 1.4s ease-in-out infinite;
  }
  .td-row-name {
    font-weight: 600;
    color: var(--pink);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 9rem;
  }
  .td-row-tab {
    color: var(--overlay1);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 7rem;
  }
  .td-row-meta {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    flex-shrink: 0;
  }
  .td-row-iface { color: var(--subtext0); font-family: ui-monospace, monospace; font-size: 0.68rem; }
  .td-row-iface.dim { color: var(--overlay0); font-style: italic; }
  .td-row-pkts { color: var(--subtext1); font-variant-numeric: tabular-nums; }
  .td-bg {
    color: var(--overlay1);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    padding: 0 0.25rem;
    font-size: 0.6rem;
  }
  .seg.update { color: var(--green); font-weight: 600; }
  .seg.update:hover { background: var(--surface0); }
  .seg.vault { color: var(--yellow); }
  .seg.vault:hover { background: var(--surface0); }
  .seg.focus { color: var(--text); }
  .seg.focus span:last-child {
    max-width: 40ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .seg-label {
    color: var(--overlay0);
    font-size: 0.65rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .seg.version {
    color: var(--overlay1);
    font-family: ui-monospace, monospace;
    font-size: 0.68rem;
  }
  .dim { color: var(--overlay0); }
  .err { color: var(--red); }
  .spacer { flex: 1; }

  /* Workspace popover */
  .ws-wrap { position: relative; }
  .ws-menu {
    position: absolute;
    bottom: 24px;
    left: 0;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    box-shadow: 0 -6px 20px rgba(0,0,0,0.45);
    min-width: 220px;
    max-height: 60vh;
    overflow-y: auto;
    padding: 0.2rem 0;
    z-index: 50;
  }
  .ws-empty {
    padding: 0.4rem 0.6rem;
    color: var(--overlay0);
    font-size: 0.72rem;
    font-style: italic;
  }
  .ws-err {
    padding: 0.35rem 0.6rem;
    color: var(--red);
    font-size: 0.72rem;
    border-bottom: 1px solid var(--surface0);
  }
  .ws-row, .ws-action {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    background: transparent;
    color: var(--text);
    border: 0;
    padding: 0.3rem 0.65rem;
    font: inherit;
    font-size: 0.78rem;
    cursor: pointer;
    text-align: left;
  }
  .ws-row:hover:not(:disabled), .ws-action:hover:not(:disabled) {
    background: var(--surface0);
  }
  .ws-row:disabled, .ws-action:disabled { opacity: 0.5; cursor: not-allowed; }
  .ws-meta { color: var(--overlay0); font-size: 0.7rem; }
  .ws-sep { height: 1px; background: var(--surface0); margin: 0.2rem 0; }
  .ws-action { color: var(--blue); }
</style>
