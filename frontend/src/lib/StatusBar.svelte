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
  import { issues } from "./issues.svelte";
  import IssuesPanel from "./IssuesPanel.svelte";
  import SharePanel from "./SharePanel.svelte";
  import { errMsg } from "./connectErrors";
  import { broadcast } from "./broadcast.svelte";
  import { tcpdump } from "./tcpdumpStore.svelte";
  import { fleet } from "./fleetStore.svelte";
  import { IconBroadcast, IconHost, IconFolder, IconTunnel, IconLock, IconActivity, IconRefresh, IconCpu, IconMemory, IconDisk, IconUsers, IconVpn, IconBot, IconSave, IconUpload, IconDownload } from "./iconMap";
  import McpActivityPanel from "./McpActivityPanel.svelte";
  import { mcpCounterTitle } from "./mcpLevel";
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
  import { vaultState } from "./vaultState.svelte";
  import { EventsOn } from "./wailsRuntime";
  import UpdateModal from "./UpdateModal.svelte";
  import ServerStatusModal from "./ServerStatusModal.svelte";
  import { level, rank, fullestPartition, fullestInodes } from "./serverStatsLevel";
  import { failedIgnore } from "./failedIgnore.svelte";
  import { statusBarPrefs, type StatusBarItem } from "./statusBarPrefs.svelte";

  let updateModalOpen = $state(false);
  let statusModalOpen = $state(false);
  let statusModalTab = $state<"overview" | "services">("overview");

  // Global running-tunnel count, polled on a slow interval. We don't
  // gate this on session count because the badge belongs to the
  // whole app, not the focused pane - even if no terminal is open,
  // a hidden detached window might still hold a session with a live
  // forward. 3s matches the per-pane PaneNode poll cadence.
  let tunnelCount = $state(0);
  let showMcpActivity = $state(false);
  let showIssues = $state(false);
  onMount(() => {
    issues.start();
    return () => issues.stop();
  });
  // Cert status reads the vault, so an unlock is the moment the opkssh
  // side becomes knowable - not the next minute tick.
  $effect(() => {
    if (vaultState.status === "unlocked") void issues.refreshOpkssh();
  });
  let showSharePanel = $state(false);
  let tunnelTimer: ReturnType<typeof setInterval> | null = null;

  let unsubOpenUpdate: null | (() => void) = null;

  // Ask App.svelte to re-show VaultGate. The gate owns the passphrase entry,
  // the auto-unlock probe and the mobile biometric path, so re-showing it is
  // the whole unlock flow - duplicating any of it here would be a second
  // place to keep correct.
  function unlockVaultNow() {
    window.dispatchEvent(new CustomEvent("vault-unlock-now"));
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
    // Slow background poll: the backend emits no vault-lock event, so the
    // store is the single place that reconciles the real state.
    vaultState.start();
    // Clicking the OS update toast raises the window (backend) and emits
    // this so the update dialog opens straight away - the whole point of
    // the notification is to land the user on the update, not just surface
    // the app with nothing shown.
    unsubOpenUpdate = EventsOn("open_update", () => { openUpdate(); });
  });
  onDestroy(() => {
    if (tunnelTimer) clearInterval(tunnelTimer);
    unsubOpenUpdate?.();
    // vaultState is a shared singleton and is deliberately NOT stopped here:
    // a detached window mounts its own StatusBar, and tearing the poll down
    // with one of them would leave the others blind to a later lock.
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
    const trimmed = name?.trim();
    if (!trimmed) return;
    wsErr = null;
    // Reusing an existing name means "save into that one" - the backend's
    // unique constraint would otherwise answer a plain rename attempt with a
    // raw SQL error, which is how this used to dead-end.
    const existing = workspaces.findByName(trimmed);
    try {
      if (existing) {
        const ok = await showConfirm({
          title: "Overwrite workspace",
          message: `"${existing.name}" already exists. Replace it with the current tabs?`,
          okLabel: "Overwrite",
        });
        if (!ok) return;
        await workspaces.overwrite(existing.id, existing.name);
      } else {
        await workspaces.saveCurrentAs(trimmed);
      }
      wsMenuOpen = false;
    } catch (e: any) {
      wsErr = errMsg(e);
    }
  }

  // Save into the workspace that is already open: the everyday case, with no
  // prompt and no confirm. Only offered when one IS open.
  async function saveActive() {
    wsErr = null;
    wsBusy = true;
    try {
      const name = workspaces.active?.name ?? "";
      if (await workspaces.saveActive()) {
        toast.ok(`Workspace "${name}" saved`);
        wsMenuOpen = false;
      }
    } catch (e: any) {
      wsErr = errMsg(e);
    } finally {
      wsBusy = false;
    }
  }

  // Save into a workspace from its row, without opening it first.
  async function saveInto(id: string, name: string, e: MouseEvent) {
    e.stopPropagation();
    const ok = await showConfirm({
      title: "Overwrite workspace",
      message: `Replace "${name}" with the current tabs?`,
      okLabel: "Overwrite",
    });
    if (!ok) return;
    wsErr = null;
    wsBusy = true;
    try {
      await workspaces.overwrite(id, name);
      toast.ok(`Workspace "${name}" saved`);
      wsMenuOpen = false;
    } catch (err: any) {
      wsErr = errMsg(err);
    } finally {
      wsBusy = false;
    }
  }
  function manage() {
    wsMenuOpen = false;
    view.setTabSettingsSection("workspaces");
  }

  // Right-click the bar: pick what it shows. Stays open while ticking.
  void statusBarPrefs.load();
  const shows = (i: StatusBarItem) => statusBarPrefs.shows(i);
  let pickOpen = $state(false);
  let pickX = $state(0);
  const PICK: { key: StatusBarItem; label: string }[] = [
    { key: "workspaces", label: "Workspaces" },
    { key: "sessions", label: "Sessions count" },
    { key: "forwards", label: "Active port forwards" },
    { key: "broadcast", label: "Broadcast members" },
    { key: "focus", label: "Focused host name" },
    { key: "shell", label: "Local shell name" },
    { key: "cpu", label: "CPU / load" },
    { key: "mem", label: "Memory" },
    { key: "disk", label: "Disk" },
    { key: "users", label: "Logged-in users" },
  ];
  function onBarContext(e: MouseEvent) {
    e.preventDefault();
    pickX = Math.min(e.clientX, window.innerWidth - 250);
    pickOpen = true;
  }
  $effect(() => {
    if (!pickOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (!(e.target as HTMLElement)?.closest(".sb-pick")) pickOpen = false;
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") pickOpen = false; };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  });

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

  // The sessionId of the focused pane, but only for a CONNECTED session
  // (SSH or local shell) on the terminal tab - the cases the stats probe can
  // run against. Empty otherwise (so the poll idles).
  const focusedSessionId = $derived.by(() => {
    if (view.tab !== "terminal") return "";
    const tabId = paneTabs.activeTabId;
    if (!tabId) return "";
    const leaf = paneTabs.activePane(tabId);
    if (!leaf?.sessionId) return "";
    const s = sessions.tabs.find((x) => x.sessionId === leaf.sessionId);
    if (!s || s.status !== "connected") return "";
    // Skip VNC and other non-terminal panes.
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
  // Near-limit colouring: same thresholds as the System status bars. The
  // disk readout follows the fullest filesystem, not just / - a small root
  // next to a full data mount is exactly what it must not hide.
  const statsView = $derived.by(() => {
    const s = serverStats;
    if (!s) return null;
    const part = fullestPartition(s);
    const diskPct = part ? part.used_pct : s.disk_used_pct;
    const inode = fullestInodes(s);
    const spaceLvl = diskPct >= 0 ? level("disk", diskPct / 100) : "ok";
    const inodeLvl = inode ? level("disk", inode.inode_pct / 100) : "ok";
    // Inodes only ever speak up as an alarm: they colour the disk icon and
    // take over the tooltip when they are the worse of the two.
    const inodeWins = rank[inodeLvl] > rank[spaceLvl];
    return {
      cpu: s.cpu_pct >= 0 ? level("cpuPct", s.cpu_pct / 100)
        : s.ncpu > 0 ? level("cpu", s.load1 / s.ncpu) : "ok",
      mem: s.mem_used_pct >= 0 ? level("mem", s.mem_used_pct / 100) : "ok",
      diskPct,
      diskTitle: inodeWins && inode
        ? `Inodes ${Math.round(inode.inode_pct)}% used on ${inode.mount} - disk space ${Math.round(diskPct)}% on ${part ? part.mount : "/"}`
        : `Disk used on ${part ? part.mount : "/"} (fullest filesystem)`,
      disk: inodeWins ? inodeLvl : spaceLvl,
    };
  });
  // Failed units worth the chip: off in Settings, or ignored on this host,
  // they do not count.
  const focusedConnId = $derived(sessions.tabs.find((t) => t.sessionId === focusedSessionId)?.connectionId ?? "");
  // Ignore lists key on the saved connection; a local shell has none, so
  // it keys on what it runs ("WSL Ubuntu"), one list per distro/machine.
  const ignoreKey = $derived(
    focusedConnId || (sessions.tabs.find((t) => t.sessionId === focusedSessionId)?.kind === "local" && serverStats?.shell
      ? `local:${serverStats.shell}` : ""),
  );
  const failedShown = $derived(
    serverStats && terminalPrefs.failedUnitsChip && serverStats.failed_units > 0
      ? failedIgnore.effective(ignoreKey, serverStats.failed_unit_names)
      : [],
  );
  let statsInFlight = false;

  async function probeServerStats(sid: string) {
    if (!sid || statsInFlight) return;
    statsInFlight = true;
    try {
      const local = sessions.tabs.find((t) => t.sessionId === sid)?.kind === "local";
      const s = await (local ? api.localHostStats(sid) : api.sshServerStats(sid));
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
    // SSH sessions probe the server, local shells the machine they run on
    // (probeServerStats picks the call).
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

<footer class="statusbar" oncontextmenu={onBarContext}>
  {#if pickOpen}
    <div class="sb-pick" style="left: {pickX}px" role="menu">
      <div class="pick-h">Show in status bar</div>
      {#each PICK as p (p.key)}
        <label class="pick-row"><input type="checkbox" checked={shows(p.key)} onchange={() => statusBarPrefs.toggle(p.key)} />{p.label}</label>
      {/each}
      <label class="pick-row"><input type="checkbox" checked={terminalPrefs.failedUnitsChip} onchange={() => terminalPrefs.setFailedUnitsChip(!terminalPrefs.failedUnitsChip)} />Failed units warning</label>
      <div class="pick-sep"></div>
      <label class="pick-row" title="Polls the focused host every 10s; off = no CPU / memory / disk / users / failed at all"><input type="checkbox" checked={terminalPrefs.serverStatsEnabled} onchange={() => terminalPrefs.setServerStatsEnabled(!terminalPrefs.serverStatsEnabled)} />Host stats (polling)</label>
      <div class="pick-note">Warnings (vault, issues, updates, sharing, VPN) and the version always show.</div>
    </div>
  {/if}
  {#if shows("workspaces")}
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
            <div class="ws-row-wrap" class:active={workspaces.isOpen(w.id)}>
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
              <button
                class="ws-save"
                disabled={wsBusy || paneTabs.tabs.length === 0}
                onclick={(e) => saveInto(w.id, w.name, e)}
                title={workspaces.isOpen(w.id) ? "Save this workspace's tabs into it" : "Overwrite this workspace with all open tabs"}
                aria-label="Overwrite {w.name}"
              ><IconSave size={11} /></button>
            </div>
          {/each}
        {/if}
        <div class="ws-sep"></div>
        {#if workspaces.active}
          <button
            class="ws-action primary"
            disabled={wsBusy || paneTabs.tabs.length === 0}
            onclick={saveActive}
            title="Write the current tabs into this workspace"
          >
            <IconSave size={11} /> Save changes to "{workspaces.active.name}"
          </button>
        {/if}
        <button class="ws-action" disabled={paneTabs.tabs.length === 0} onclick={saveCurrentAs}>
          + Save current as…
        </button>
        <button class="ws-action" onclick={manage}>Manage workspaces…</button>
      </div>
    {/if}
  </div>

  {/if}

  {#if shows("sessions")}
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
  {/if}

  {#if tunnelCount > 0 && shows("forwards")}
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
        class:lvl-run={mcpShared.highestLevel === "read-run"}
        class:lvl-yolo={mcpShared.highestLevel === "read-run-yolo"}
        title={mcpCounterTitle(mcpShared.size, mcpShared.highestLevel)}
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
        class:controlled={shareShared.anyControlled}
        title="{shareShared.guestCount} session{shareShared.guestCount === 1 ? '' : 's'} shared to a browser guest{shareShared.anyControlled ? ' - a guest can type' : ' (view only)'} - click to manage"
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

  {#if broadcast.totalMembers() > 1 && shows("broadcast")}
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

  <!-- A minimised fleet upload / download: progress while it runs, the
       outcome once it ends; a click brings the dialog back. -->
  {#if fleet.transfer && fleet.transferMinimized}
    {@const t = fleet.transferStatus}
    <button
      class="seg transfer"
      class:live={t?.running}
      class:bad={!!t && !t.running && t.failed > 0}
      onclick={() => fleet.restoreTransfer()}
      title={t
        ? `${t.tool === "upload" ? "Upload to" : "Download from"} ${t.hosts} host${t.hosts === 1 ? "" : "s"}: ${t.done} done${t.failed ? `, ${t.failed} failed` : ""}${t.running ? "" : " - finished"}. Click to open.`
        : "Fleet transfer - click to open"}
    >
      {#if fleet.transfer.tool === "upload"}<IconUpload size={11} />{:else}<IconDownload size={11} />{/if}
      {#if t?.running}
        <span>{t.total > 0 ? Math.min(100, Math.round((t.bytes / t.total) * 100)) : 0}%</span>
        <span class="tr-hosts">{t.done}/{t.hosts}</span>
      {:else if t}
        <span>{t.failed ? `${t.failed} failed` : "done"}</span>
      {:else}
        <span>{fleet.transfer.tool}</span>
      {/if}
    </button>
  {/if}

  <!-- Locked-vault pill. The earlier "Lock vault" pill was withdrawn as a
       rare action not worth the space, and that still holds - this is the
       opposite case. The vault can be locked while the app is fully usable
       (VaultGate's "Skip (memory only)", or the idle auto-lock after a skip),
       and until now nothing on screen said so: the user found out when a
       credential reveal or an SSH connect failed. A locked vault is a state
       worth a permanent indicator, and the click is the fix for it. -->
  {#if vaultState.locked}
    <button
      class="seg vault locked"
      onclick={unlockVaultNow}
      title="The vault is locked - stored passwords, keys and API tokens cannot be read. Click to unlock."
    >
      <IconLock size={11} />
      <span>Vault locked</span>
    </button>
  {/if}

  <div class="spacer"></div>

  {#if focusedConnName && shows("focus")}
    <span class="seg focus" title="Focused pane">
      <IconHost size={11} />
      <span>{focusedConnName}</span>
    </span>
  {/if}

  {#if serverStats}
    {#if shows("shell") || shows("cpu") || shows("mem") || shows("disk") || shows("users")}
    <button
      class="seg stats"
      onclick={() => { statusModalTab = "overview"; statusModalOpen = true; }}
      title="Status of the focused session's host (refreshed every 10s) - click for full system status"
    >
      {#if serverStats.shell && shows("shell")}
        <span class="stat shell" title="Local shell">{serverStats.shell}</span>
      {/if}
      {#if shows("cpu") && serverStats.cpu_pct >= 0}
        <span class="stat lvl-{statsView?.cpu}" title="CPU busy across {serverStats.ncpu} cores">
          <IconCpu size={11} />{Math.round(serverStats.cpu_pct)}%
        </span>
      {:else if shows("cpu")}
        <span class="stat lvl-{statsView?.cpu}" title="Load average (1 / 5 / 15 min): {serverStats.load1.toFixed(2)} / {serverStats.load5.toFixed(2)} / {serverStats.load15.toFixed(2)}{serverStats.ncpu > 0 ? ` on ${serverStats.ncpu} cores` : ""}">
          <IconCpu size={11} />{serverStats.load1.toFixed(2)}
        </span>
      {/if}
      {#if serverStats.mem_used_pct >= 0 && shows("mem")}
        <span class="stat lvl-{statsView?.mem}" title="Memory used">
          <IconMemory size={11} />{Math.round(serverStats.mem_used_pct)}%
        </span>
      {/if}
      {#if statsView && statsView.diskPct >= 0 && shows("disk")}
        <span class="stat lvl-{statsView.disk}" title={statsView.diskTitle}>
          <IconDisk size={11} />{Math.round(statsView.diskPct)}%
        </span>
      {/if}
      {#if serverStats.users >= 0 && shows("users")}
        <span class="stat" title="Logged-in users">
          <IconUsers size={11} />{serverStats.users}
        </span>
      {/if}
    </button>
    {/if}
    {#if failedShown.length > 0}
      <button
        class="seg failed-chip"
        onclick={() => { statusModalTab = "services"; statusModalOpen = true; }}
        title="Failed: {failedShown.join(", ")} - click for Services"
      >{failedShown.length} failed</button>
    {/if}
  {/if}

  {#if statusModalOpen && serverStats && focusedSessionId}
    <ServerStatusModal
      initial={serverStats}
      connName={focusedConnName}
      sessionId={focusedSessionId}
      local={sessions.tabs.find((t) => t.sessionId === focusedSessionId)?.kind === "local"}
      initialTab={statusModalTab}
      connectionId={ignoreKey}
      onClose={() => (statusModalOpen = false)}
    />
  {/if}


  <!-- Standing problems with the desktop integrations (a handler
       pointing at a binary that moved, say). They only showed inside
       Settings, which is no help to someone who has not noticed
       anything is wrong yet. -->
  <!-- Standing problems: desktop integrations, credentials about to
       expire, opkssh certs running out. A click opens the list with a
       description and a way to each fix; the count alone said nothing. -->
  {#if issues.items.length > 0}
    <div class="mcp-anchor">
      <button
        class="seg alerts"
        class:error={issues.severity === "error"}
        onclick={() => (showIssues = !showIssues)}
        title="Click to see what needs attention"
      >
        <span>! {issues.items.length === 1 ? "1 issue" : `${issues.items.length} issues`}</span>
      </button>
      {#if showIssues}
        <IssuesPanel onClose={() => (showIssues = false)} />
      {/if}
    </div>
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
  .seg.failed-chip {
    color: var(--red);
    background: color-mix(in srgb, var(--red) 16%, transparent);
    border-radius: 8px;
    padding: 0 0.5rem;
    margin: 0 0.2rem;
    height: 16px;
    align-self: center;
    font-size: 0.7rem;
  }
  .seg.stats .stat.shell { color: var(--overlay1); }
  .seg.stats .stat.lvl-warn { color: var(--yellow); }
  .seg.stats .stat.lvl-crit { color: var(--red); }
  .seg.tunnels { color: var(--green); }
  .seg.mcp { color: var(--blue); }
  /* The counter stands for every shared session at once, so it takes the
     loudest grant among them - same colours as the pane header and tab
     badge, which report a single session each. */
  .seg.mcp.lvl-run { color: var(--yellow); }
  .seg.mcp.lvl-yolo { color: var(--red); }
  .seg.share { color: var(--green); }
  /* A guest who can type is the loud case, same as read-run for MCP. */
  .seg.share.controlled { color: var(--yellow); }
  .seg.share .dot { font-size: 0.7rem; }
  .mcp-anchor { position: relative; display: inline-flex; }
  /* The share segment sits on the LEFT of the status bar (before the spacer),
     so anchor the popover to the left edge - right:0 pushed it off-screen. */
  .share-pop { position: absolute; bottom: 100%; left: 0; margin-bottom: 0.3rem; z-index: 60; }
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
  .seg.transfer { color: var(--blue); cursor: pointer; }
  .seg.transfer.bad { color: var(--red); }
  .seg.transfer .tr-hosts { color: var(--subtext0); }
  .seg.transfer.live > :global(svg) { animation: sb-pulse 1.4s ease-in-out infinite; }
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
  .seg.alerts { color: var(--yellow); font-weight: 600; }
  .seg.alerts:hover { background: var(--surface0); }
  .seg.alerts.error { color: var(--red); }
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
  .sb-pick {
    position: fixed; bottom: 26px; z-index: 5000; min-width: 230px;
    background: var(--base); color: var(--text); border: 1px solid var(--surface1);
    border-radius: 5px; box-shadow: 0 -6px 20px rgba(0,0,0,0.45); padding: 0.35rem 0;
    font-size: 0.78rem;
  }
  .pick-h { padding: 0.2rem 0.75rem 0.35rem; color: var(--subtext0); font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.06em; }
  .pick-row { display: flex; align-items: center; gap: 0.5rem; padding: 0.22rem 0.75rem; cursor: pointer; }
  .pick-row:hover { background: var(--surface0); }
  .pick-sep { height: 1px; background: var(--surface0); margin: 0.3rem 0; }
  .pick-note { padding: 0.3rem 0.75rem 0.15rem; color: var(--overlay1); font-size: 0.7rem; }
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
  .ws-action.primary {
    justify-content: flex-start;
    gap: 0.4rem;
    color: var(--green);
    font-weight: 500;
  }
  /* A row is a flex pair: the name (which opens the workspace) and a save
     button that writes the current tabs into it without opening it first. */
  .ws-row-wrap {
    display: flex;
    align-items: stretch;
  }
  .ws-row-wrap .ws-row { flex: 1; min-width: 0; }
  .ws-row-wrap.active .ws-name { color: var(--green); }
  .ws-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ws-save {
    display: flex;
    align-items: center;
    background: transparent;
    border: 0;
    color: var(--overlay0);
    padding: 0 0.55rem;
    cursor: pointer;
  }
  .ws-save:hover:not(:disabled) { background: var(--surface0); color: var(--green); }
  .ws-save:disabled { opacity: 0.35; cursor: not-allowed; }
  .ws-row-wrap:hover .ws-save { color: var(--subtext0); }
  .ws-row-wrap:hover .ws-save:hover:not(:disabled) { color: var(--green); }
</style>
