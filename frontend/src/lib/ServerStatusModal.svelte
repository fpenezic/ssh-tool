<!--
  System status popup - a read-only "at a glance" health view of the remote
  host of the focused SSH session. Opened from the status-bar stats readout;
  same source as that readout (SshServerStats side-channel probe), just the
  full field set. Shows load / CPU, memory + swap, every real (non-pseudo)
  partition, and logged-in users by name.

  Opens instantly with the last poll's data (passed as `initial`), then
  re-probes once for freshness; the manual refresh button re-probes again.
  Strictly display-only - no actions, no writes.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import { api, type ServerStats, type DiskTopResult, type ProcInfo, type UnitInfo } from "./api";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { toast } from "./toast.svelte";
  import { logtail } from "./logtailStore.svelte";
  import { focusSessionTerminal } from "./paneFocus";
  import { errMsg } from "./connectErrors";
  import { failedIgnore } from "./failedIgnore.svelte";
  import { IconCpu, IconMemory, IconDisk, IconUsers, IconRefresh, IconHost, IconChevronRight, IconChevronDown, IconAlertTriangle, IconFolder } from "./iconMap";
  import { focusActivePane } from "./paneFocus";
  import { level, levelVar } from "./serverStatsLevel";

  interface Props {
    initial: ServerStats;
    connName: string;
    sessionId: string;
    // A local shell: stats come from this machine, and there is no du
    // (Windows has none, and a local disk is one Explorer away anyway).
    local?: boolean;
    // Which tab to open on; the status bar's "failed" chip opens Services.
    initialTab?: Tab;
    // The saved connection behind the session: ignored failed units are
    // remembered per connection.
    connectionId?: string;
    onClose: () => void;
  }
  type Tab = "overview" | "processes" | "services";
  let { initial, connName, sessionId, local = false, initialTab = "overview", connectionId = "", onClose: onCloseProp }: Props = $props();
  // svelte-ignore state_referenced_locally
  let tab = $state<Tab>(initialTab);
  // A WSL or Linux local shell gets Processes and Services read-only (no
  // kill, no restart: that shell is right there). PowerShell, cmd and
  // macOS stay on Overview.
  // svelte-ignore state_referenced_locally
  let sysinfo = $state(!local);
  onMount(() => {
    if (!local) return;
    api.localSysinfo(sessionId)
      .then((v) => { sysinfo = !!v; if (!v) tab = "overview"; else if (tab !== "overview") selectTab(tab); })
      .catch(() => { sysinfo = false; tab = "overview"; });
  });

  // The modal steals keyboard focus; hand it back to the terminal on close so
  // the user can keep typing without re-clicking the pane. Every close path
  // (Esc, backdrop, the X button) routes through here.
  function onClose() {
    onCloseProp();
    focusActivePane();
  }

  // Seed from the last poll's snapshot so the modal paints instantly, then
  // refresh() replaces it. Intentionally captures `initial` once - it is a
  // point-in-time snapshot, not a live prop we track.
  // svelte-ignore state_referenced_locally
  let stats = $state<ServerStats>(initial);
  let refreshing = $state(false);
  let refreshErr = $state("");

  onMount(() => {
    // One freshness re-probe on open; keep `initial` if it fails.
    refresh();
  });

  async function refresh() {
    if (refreshing) return;
    refreshing = true;
    refreshErr = "";
    try {
      const s = await (local ? api.localHostStats(sessionId) : api.sshServerStats(sessionId));
      if (s && s.ok) stats = s;
      else refreshErr = "Host returned no readable stats.";
      if (tab === "processes") void loadProcs();
      if (tab === "services") void loadUnits();
    } catch (e: any) {
      refreshErr = "Probe failed (session may have closed).";
    } finally {
      refreshing = false;
    }
  }

  // Per-mount "more" panel: inode use plus an on-demand du. du walks every
  // inode (minutes on a slow disk or a tree of small files), so it only runs
  // on the button, once, and the host caps it at 20s.
  let expanded = $state<Record<string, boolean>>({});
  let topDirs = $state<Record<string, DiskTopResult | "loading" | { error: string }>>({});
  async function loadTopDirs(mount: string) {
    topDirs[mount] = "loading";
    try {
      topDirs[mount] = await api.sshDiskTopDirs(sessionId, mount);
    } catch (e: any) {
      topDirs[mount] = { error: errMsg(e) };
    }
  }

  // ---- Processes tab ----
  let procBy = $state<"cpu" | "mem">("cpu");
  // Row count is a per-viewer preference; storage can be missing or
  // throw (private window), so it only ever falls back to 10.
  const PROC_LIMITS = [5, 10, 20];
  function readProcLimit(): number {
    try {
      const n = parseInt(localStorage.getItem("sysstat_proc_limit") ?? "", 10);
      return PROC_LIMITS.includes(n) ? n : 10;
    } catch {
      return 10;
    }
  }
  let procLimit = $state(readProcLimit());
  function pickProcLimit(n: number) {
    procLimit = n;
    try { localStorage.setItem("sysstat_proc_limit", String(n)); } catch { /* not remembered */ }
    void loadProcs();
  }
  let procs = $state<ProcInfo[] | null>(null);
  let procErr = $state("");
  let procLoading = $state(false);
  let procMenu = $state<number | null>(null);
  async function loadProcs() {
    procLoading = true;
    procErr = "";
    try {
      procs = (await (local ? api.localTopProcesses : api.sshTopProcesses)(sessionId, procBy, procLimit)) ?? [];
    } catch (e: any) {
      procErr = errMsg(e);
    } finally {
      procLoading = false;
    }
  }
  function sortProcs(by: "cpu" | "mem") {
    procBy = by;
    void loadProcs();
  }

  // ---- Services tab ----
  // svelte-ignore state_referenced_locally
  let svcState = $state<"failed" | "running" | "all">(initial.failed_units > 0 ? "failed" : "running");
  // Failed units that still count here (not ignored on this host).
  const failedCount = $derived(failedIgnore.effective(connectionId, stats.failed_unit_names).length);
  let units = $state<UnitInfo[] | null>(null);
  let svcErr = $state("");
  let svcLoading = $state(false);
  let svcFilter = $state("");
  let openUnit = $state<string | null>(null);
  let unitLogs = $state<Record<string, string[] | { error: string }>>({});
  const shownUnits = $derived(
    (units ?? []).filter((u) => !svcFilter || (u.unit + " " + u.description).toLowerCase().includes(svcFilter.toLowerCase())),
  );
  async function loadUnits() {
    svcLoading = true;
    svcErr = "";
    try {
      units = (await (local ? api.localServices : api.sshServices)(sessionId, svcState)) ?? [];
    } catch (e: any) {
      svcErr = errMsg(e);
    } finally {
      svcLoading = false;
    }
  }
  function pickSvcState(st: "failed" | "running" | "all") {
    svcState = st;
    openUnit = null;
    void loadUnits();
  }
  async function toggleUnit(u: string) {
    openUnit = openUnit === u ? null : u;
    if (openUnit && !unitLogs[u]) {
      try {
        unitLogs[u] = (await (local ? api.localUnitLog : api.sshUnitLog)(sessionId, u)) ?? [];
      } catch (e: any) {
        unitLogs[u] = { error: errMsg(e) };
      }
    }
  }
  function quote(v: string): string {
    return `'${v.replace(/'/g, `'\\''`)}'`;
  }
  // Typed, not run: the user reads the line and presses Enter. Same rule as
  // the SFTP pane's "cd here".
  async function statusInTerminal(u: string) {
    await typeInTerminal(`systemctl status ${quote(u)} --no-pager`);
  }
  // The scan cut off at 20s: hand the same du to the terminal without the
  // cap, so whoever wants the full picture can wait for it there.
  function duInTerminal(mount: string) {
    return typeInTerminal(`nice -n 19 du -xh -d 2 -- ${quote(mount)} 2>/dev/null | sort -rh | head -n 20`);
  }
  async function typeInTerminal(cmd: string) {
    const line = `\u0015${cmd}`;
    try {
      const b64 = btoa(String.fromCharCode(...new TextEncoder().encode(line)));
      await (local ? api.localShellWrite(sessionId, b64) : api.sshWrite(sessionId, b64));
      onClose();
      if (!focusSessionTerminal(sessionId)) toast.push("ok", "Typed into the terminal - press Enter to run it");
    } catch (e: any) {
      toast.err(errMsg(e));
    }
  }
  function tailUnit(u: string) {
    onClose();
    logtail.open(sessionId, { unit: u });
  }

  // A process needs a moment to exit after SIGTERM, and a restarted unit
  // to settle; re-read the list after that instead of immediately.
  function afterAction(kind: "signal" | "service") {
    setTimeout(() => {
      if (kind === "signal") void loadProcs();
      else { unitLogs = {}; void loadUnits(); void refresh(); }
    }, 700);
  }

  // Kept in sync with sudoPasswordPrefix in app.go.
  const SUDO_PROMPT_PREFIX = "sudo-password-required: ";

  // One confirm, then the action; a sudo password is asked for only when
  // root is needed and the connection's own password did not work.
  async function runAction(kind: "signal" | "service", pid: number, unit: string, verb: string, what: string, danger: boolean) {
    procMenu = null;
    const ok = await showConfirm({
      title: what,
      message: kind === "signal"
        ? `Send SIG${verb} to PID ${pid} on ${stats.hostname || connName}?`
        : `Run systemctl ${verb} ${unit} on ${stats.hostname || connName}?`,
      okLabel: verb === "KILL" ? "Kill" : verb === "TERM" ? "Terminate" : verb[0].toUpperCase() + verb.slice(1),
      danger,
    });
    if (!ok) return;
    let password = "";
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        await api.sshSystemAction(sessionId, kind, pid, unit, verb, password);
        toast.push("ok", kind === "signal" ? `SIG${verb} sent to ${pid}` : `${unit}: ${verb} done`);
        afterAction(kind);
        return;
      } catch (e: any) {
        const msg = errMsg(e);
        if (!msg.includes(SUDO_PROMPT_PREFIX) && !/wrong password/.test(msg)) {
          toast.err(msg);
          // A failed kill is often a process that already exited: the list
          // should stop showing it either way.
          afterAction(kind);
          return;
        }
        const pw = await showPrompt(
          /wrong password/.test(msg) ? "Wrong password - sudo password again:" : "This needs root. sudo password:",
          { password: true },
        );
        if (pw === null) return;
        password = pw;
      }
    }
  }

  function selectTab(t: Tab) {
    tab = t;
    if (t === "processes" && !procs && !procLoading) void loadProcs();
    if (t === "services" && !units && !svcLoading) void loadUnits();
  }
  onMount(() => {
    // A local shell opens its tab once localSysinfo has answered.
    if (tab !== "overview" && !local) selectTab(tab);
  });

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") onClose();
  }

  // ---- formatting helpers ----
  function fmtBytesKB(kb: number): string {
    if (!kb || kb <= 0) return "0";
    const mb = kb / 1024;
    if (mb < 1024) return `${Math.round(mb)} MiB`;
    const gib = mb / 1024;
    if (gib < 100) return `${(Math.round(gib * 10) / 10).toFixed(1)} GiB`;
    return `${Math.round(gib)} GiB`;
  }

  function fmtUptime(sec: number): string {
    if (!sec || sec <= 0) return "";
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const parts: string[] = [];
    if (d > 0) parts.push(`${d}d`);
    if (h > 0) parts.push(`${h}h`);
    if (m > 0 || parts.length === 0) parts.push(`${m}m`);
    return parts.join(" ");
  }

  // Bar colour by saturation; thresholds live in serverStatsLevel.ts so the
  // status bar icons turn at the same points.
  const cpuColor = (f: number) => levelVar[level("cpu", f)];
  const memColor = (f: number) => levelVar[level("mem", f)];
  const swapColor = (f: number) => levelVar[level("swap", f)];
  const diskColor = (f: number) => levelVar[level("disk", f)];
  function pctWidth(frac: number): string {
    return `${Math.min(100, Math.max(0, frac * 100))}%`;
  }

  const memUsedKB = $derived(
    stats.mem_total_kb > 0 ? stats.mem_total_kb - stats.mem_avail_kb : 0,
  );
  const memFrac = $derived(
    stats.mem_total_kb > 0 ? memUsedKB / stats.mem_total_kb : 0,
  );
  const swapUsedKB = $derived(
    stats.swap_total_kb > 0 ? stats.swap_total_kb - stats.swap_free_kb : 0,
  );
  const swapFrac = $derived(
    stats.swap_total_kb > 0 ? swapUsedKB / stats.swap_total_kb : 0,
  );
  // Load per core: 1.0 = fully utilised. Saturate the bar at that.
  const loadFrac = $derived(stats.ncpu > 0 ? stats.load1 / stats.ncpu : 0);

  const parts = $derived(
    [...(stats.partitions ?? [])].sort((a, b) => a.mount.localeCompare(b.mount)),
  );
  // who -q lists a name per session, so the same user shows up once per
  // open session. Collapse to unique names with a session count, preserving
  // first-seen order.
  const users = $derived.by(() => {
    const order: string[] = [];
    const counts = new Map<string, number>();
    for (const n of stats.user_names ?? []) {
      if (!counts.has(n)) order.push(n);
      counts.set(n, (counts.get(n) ?? 0) + 1);
    }
    return order.map((name) => ({ name, count: counts.get(name) ?? 1 }));
  });
  const uptimeStr = $derived(fmtUptime(stats.uptime_sec));
</script>

<svelte:window onkeydown={onKey} />

<div
  class="backdrop"
  role="button"
  tabindex="-1"
  onclick={onClose}
  onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") onClose(); }}
></div>
<div class="modal" class:wide={tab !== "overview"} role="dialog" aria-labelledby="sysstat-title">
  <header>
    <div class="title-row">
      <h2 id="sysstat-title">
        <IconHost size={14} />
        {stats.hostname || connName || "System status"}
      </h2>
      <div class="head-actions">
        <button
          class="icon-btn"
          class:spinning={refreshing}
          onclick={refresh}
          disabled={refreshing}
          title="Refresh"
          aria-label="Refresh"
        >
          <IconRefresh size={14} />
        </button>
        <button class="x" onclick={onClose} aria-label="Close">×</button>
      </div>
    </div>
    <div class="sub-row">
      {#if connName}<span class="conn">{connName}</span>{/if}
      {#if stats.kernel}<span class="meta">{stats.kernel}</span>{/if}
      {#if uptimeStr}<span class="meta">up {uptimeStr}</span>{/if}
    </div>
    {#if refreshErr}<div class="warn">{refreshErr}</div>{/if}
    {#if sysinfo}
      <div class="tabs" role="tablist">
        <button role="tab" aria-selected={tab === "overview"} class:active={tab === "overview"} onclick={() => selectTab("overview")}>Overview</button>
        <button role="tab" aria-selected={tab === "processes"} class:active={tab === "processes"} onclick={() => selectTab("processes")}>Processes</button>
        <button role="tab" aria-selected={tab === "services"} class:active={tab === "services"} onclick={() => selectTab("services")}>
          Services{#if failedCount > 0}<span class="tab-bad">{failedCount}</span>{/if}
        </button>
      </div>
    {/if}
  </header>

  <div class="body">
  {#if tab === "processes"}
    <div class="tab-tools">
      <span class="dim">Top</span>
      {#each PROC_LIMITS as n (n)}
        <button class="seg-btn" class:on={procLimit === n} onclick={() => pickProcLimit(n)}>{n}</button>
      {/each}
      <span class="dim">by</span>
      <button class="seg-btn" class:on={procBy === "cpu"} onclick={() => sortProcs("cpu")}>CPU</button>
      <button class="seg-btn" class:on={procBy === "mem"} onclick={() => sortProcs("mem")}>Memory</button>
    </div>
    {#if procErr}
      <div class="warn">{procErr}</div>
    {:else if !procs}
      <div class="bar-cap">Loading…</div>
    {:else}
      <div class="ptable">
        <span class="th">PID</span><span class="th">Command</span><span class="th num">CPU</span><span class="th num">Mem</span><span class="th">User</span><span class="th"></span>
        {#each procs as p (p.pid)}
          <span class="dim">{p.pid}</span>
          <span class="cmd" title={p.command}>{p.command}</span>
          <span class="num" style={level("cpuPct", p.cpu / 100) !== "ok" ? `color: ${levelVar[level("cpuPct", p.cpu / 100)]}` : ""}>{p.cpu.toFixed(0)}%</span>
          <span class="num" style={level("mem", p.mem / 100) !== "ok" ? `color: ${levelVar[level("mem", p.mem / 100)]}` : ""}>{p.mem.toFixed(1)}%</span>
          <span class="user" title={p.user}>{p.user}</span>
          <span class="menu-cell">
            <button class="icon-btn" aria-label="Actions for {p.pid}" onclick={() => (procMenu = procMenu === p.pid ? null : p.pid)}>⋯</button>
            {#if procMenu === p.pid}
              <div class="pmenu" role="menu">
                <button role="menuitem" onclick={() => { navigator.clipboard.writeText(String(p.pid)); procMenu = null; }}>Copy PID</button>
                <button role="menuitem" onclick={() => { navigator.clipboard.writeText(p.command); procMenu = null; }}>Copy command line</button>
                {#if !local}
                <div class="msep"></div>
                <button role="menuitem" onclick={() => runAction("signal", p.pid, "", "TERM", "Terminate process", false)}>Terminate (SIGTERM)…</button>
                <button role="menuitem" class="bad" onclick={() => runAction("signal", p.pid, "", "KILL", "Kill process", true)}>Kill (SIGKILL)…</button>
                {/if}
              </div>
            {/if}
          </span>
        {/each}
      </div>
      <div class="bar-cap">A process of another user is signalled through sudo.</div>
    {/if}
  {:else if tab === "services"}
    <div class="tab-tools">
      <button class="seg-btn" class:on={svcState === "failed"} onclick={() => pickSvcState("failed")}>Failed</button>
      <button class="seg-btn" class:on={svcState === "running"} onclick={() => pickSvcState("running")}>Running</button>
      <button class="seg-btn" class:on={svcState === "all"} onclick={() => pickSvcState("all")}>All</button>
      <input class="filter" type="search" placeholder="Filter units" bind:value={svcFilter} />
    </div>
    {#if svcErr}
      <div class="warn">{svcErr}</div>
    {:else if !units}
      <div class="bar-cap">Loading…</div>
    {:else if shownUnits.length === 0}
      <div class="bar-cap">{svcState === "failed" ? "No failed services." : "Nothing matches."}</div>
    {:else}
      <div class="units">
        {#each shownUnits as u (u.unit)}
          {@const ignoredIn = u.active === "failed" && connectionId ? failedIgnore.ignoredIn(connectionId, u.unit) : []}
          {@const ignored = ignoredIn.length > 0}
          <div class="unit" class:open={openUnit === u.unit} class:ignored>
            <button class="unit-row" onclick={() => toggleUnit(u.unit)}>
              <span class="udot" class:failed={u.active === "failed" && !ignored} class:active={u.active === "active"}></span>
              <span class="uname">{u.unit}</span>
              <span class="ustate">{u.active}/{u.sub}{#if ignored}{` · ignored ${ignoredIn[0].isFolder ? `in ${ignoredIn[0].label}` : "on this host"}`}{/if}</span>
              <span class="udesc" title={u.description}>{u.description}</span>
            </button>
            {#if openUnit === u.unit}
              {@const lg = unitLogs[u.unit]}
              <div class="unit-body">
                {#if !lg}
                  <div class="bar-cap">Loading journal…</div>
                {:else if "error" in lg}
                  <div class="warn">{lg.error}</div>
                {:else}
                  <pre class="ulog">{lg.length ? lg.join("\n") : "(no journal lines)"}</pre>
                {/if}
                <div class="unit-actions">
                  {#if !local}
                  <button class="du-btn" onclick={() => runAction("service", 0, u.unit, "restart", "Restart service", false)}>Restart…</button>
                  {#if u.active === "active"}
                    <button class="du-btn" onclick={() => runAction("service", 0, u.unit, "stop", "Stop service", true)}>Stop…</button>
                  {:else}
                    <button class="du-btn" onclick={() => runAction("service", 0, u.unit, "start", "Start service", false)}>Start…</button>
                  {/if}
                  <button class="du-btn" onclick={() => tailUnit(u.unit)}>Open log tail</button>
                  {/if}
                  {#if u.active === "failed" && connectionId}
                    {#if ignored}
                      <button class="more" title={`Ignored in: ${ignoredIn.map((x) => x.label).join(", ")}. Stop ignoring clears it there, for every host it covers.`}
                        onclick={() => failedIgnore.unignore(connectionId, u.unit)}>Stop ignoring</button>
                    {:else}
                      <span class="ign" title="Ignored units stay listed but do not light the status bar's failed warning. A folder covers every host below it.">
                        Ignore on
                        {#each failedIgnore.scopesOf(connectionId) as sc (sc.scope)}
                          <button class="scope" title={sc.isFolder ? `Every host in ${sc.label}` : "Only this connection"} onclick={() => failedIgnore.set(sc.scope, u.unit, true)}>{#if sc.isFolder}<IconFolder size={12} />{:else}<IconHost size={12} />{/if}{sc.label}</button>
                        {/each}
                      </span>
                    {/if}
                  {/if}
                  <button class="more" onclick={() => statusInTerminal(u.unit)}>Status in terminal</button>
                </div>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  {:else}
    <!-- CPU / load -->
    <section>
      {#if stats.cpu_pct >= 0}
        <div class="sec-head"><IconCpu size={13} /><span>CPU</span>
          {#if stats.ncpu > 0}<span class="sec-note">{stats.ncpu} core{stats.ncpu === 1 ? "" : "s"}</span>{/if}
        </div>
        <div class="bar">
          <div class="fill" style="width: {pctWidth(stats.cpu_pct / 100)}; background: {levelVar[level('cpuPct', stats.cpu_pct / 100)]};"></div>
        </div>
        <div class="bar-cap">{Math.round(stats.cpu_pct)}% busy</div>
      {:else}
        <div class="sec-head"><IconCpu size={13} /><span>CPU load</span>
          {#if stats.ncpu > 0}<span class="sec-note">{stats.ncpu} core{stats.ncpu === 1 ? "" : "s"}</span>{/if}
        </div>
        <div class="load-nums">
          <span title="1 minute">{stats.load1.toFixed(2)}</span>
          <span class="dim" title="5 minutes">{stats.load5.toFixed(2)}</span>
          <span class="dim" title="15 minutes">{stats.load15.toFixed(2)}</span>
          <span class="dim label">1 / 5 / 15 min</span>
        </div>
        {#if stats.ncpu > 0}
          <div class="bar">
            <div class="fill" style="width: {pctWidth(loadFrac)}; background: {cpuColor(loadFrac)};"></div>
          </div>
          <div class="bar-cap">{Math.round(loadFrac * 100)}% of {stats.ncpu} core{stats.ncpu === 1 ? "" : "s"}</div>
        {/if}
      {/if}
    </section>

    <!-- Memory -->
    {#if stats.mem_total_kb > 0}
      <section>
        <div class="sec-head"><IconMemory size={13} /><span>Memory</span>
          <span class="sec-note">{Math.round(stats.mem_used_pct)}%</span>
        </div>
        <div class="bar">
          <div class="fill" style="width: {pctWidth(memFrac)}; background: {memColor(memFrac)};"></div>
        </div>
        <div class="bar-cap">{fmtBytesKB(memUsedKB)} / {fmtBytesKB(stats.mem_total_kb)} used</div>
        {#if stats.swap_total_kb > 0}
          <div class="sec-head sub"><span>Swap</span>
            <span class="sec-note">{Math.round(swapFrac * 100)}%</span>
          </div>
          <div class="bar">
            <div class="fill" style="width: {pctWidth(swapFrac)}; background: {swapColor(swapFrac)};"></div>
          </div>
          <div class="bar-cap">{fmtBytesKB(swapUsedKB)} / {fmtBytesKB(stats.swap_total_kb)} used</div>
        {/if}
      </section>
    {/if}

    <!-- Storage -->
    {#if parts.length > 0}
      <section>
        <div class="sec-head"><IconDisk size={13} /><span>Storage</span>
          <span class="sec-note">{parts.length} filesystem{parts.length === 1 ? "" : "s"}</span>
        </div>
        {#each parts as p (p.mount)}
          <div class="part">
            <div class="part-head">
              <span class="mount">{p.mount}</span>
              <span class="fs">{p.fs}</span>
              <span class="part-pct">{Math.round(p.used_pct)}%</span>
            </div>
            <div class="bar">
              <div class="fill" style="width: {pctWidth(p.used_pct / 100)}; background: {diskColor(p.used_pct / 100)};"></div>
            </div>
            <div class="bar-cap part-cap">
              <span>{fmtBytesKB(p.used_kb)} / {fmtBytesKB(p.size_kb)} used · {fmtBytesKB(p.avail_kb)} free</span>
              {#if !local || p.inode_pct >= 0}<button class="more" onclick={() => (expanded[p.mount] = !expanded[p.mount])}>
                {#if expanded[p.mount]}<IconChevronDown size={11} />{:else}<IconChevronRight size={11} />{/if}More
              </button>{/if}
            </div>
            {#if p.inode_pct >= 0 && level("disk", p.inode_pct / 100) !== "ok" && !expanded[p.mount]}
              <div class="bar-cap inode-alert" style="color: {diskColor(p.inode_pct / 100)};">
                Inodes {Math.round(p.inode_pct)}% used - many small files can fill a disk that still shows free space
              </div>
            {/if}
            {#if expanded[p.mount]}
              {@const td = topDirs[p.mount]}
              <div class="more-body">
                <div class="bar-cap">
                  {#if p.inode_pct >= 0}
                    Inodes: <span style="color: {level('disk', p.inode_pct / 100) === 'ok' ? 'inherit' : diskColor(p.inode_pct / 100)};">{Math.round(p.inode_pct)}% used</span>
                  {:else}
                    Inodes: not reported by {p.fs.startsWith("/dev/") ? "this filesystem" : p.fs}
                  {/if}
                </div>
                {#if local}
                  <!-- no du for a local shell -->
                {:else if !td}
                  <button class="du-btn" onclick={() => loadTopDirs(p.mount)} title="Runs du -x two levels deep on the host, capped at 20s">
                    Find largest directories
                  </button>
                {:else if td === "loading"}
                  <div class="bar-cap">Scanning {p.mount}… (up to 20s)</div>
                {:else if "error" in td}
                  <div class="warn">{td.error}</div>
                {:else}
                  {#if td.timed_out}
                    <div class="du-cut" title={"du reads every file's metadata, so it is slow on trees with millions of small files (mail spools, caches, container layers, backup sets), on network or overloaded storage, and when the disk is already busy.\nThe list below only has what was counted in time - the missing part may be the biggest."}>
                      <IconAlertTriangle size={14} />
                      <div>
                        <strong>Scan stopped after 20s - sizes are a lower bound.</strong>
                        <div>Large or slow trees may be missing from the list. Run du in the terminal without the limit to see everything.</div>
                        <button class="du-btn" onclick={() => duInTerminal(p.mount)} title="Types the command into the terminal; press Enter to run it">Type du into the terminal</button>
                      </div>
                    </div>
                  {:else if td.partial}<div class="bar-cap">{td.reason}</div>{/if}
                  <div class="dirs">
                    {#each td.dirs ?? [] as d (d.path)}
                      <span class="dir-size">{fmtBytesKB(d.size_kb)}</span><span class="dir-path" title={d.path}>{d.path}</span>
                    {:else}
                      <span class="bar-cap">No directories found.</span>
                    {/each}
                  </div>
                  <button class="du-btn" onclick={() => loadTopDirs(p.mount)}>Scan again</button>
                {/if}
              </div>
            {/if}
          </div>
        {/each}
      </section>
    {/if}

    <!-- Users -->
    {#if stats.users >= 0}
      <section>
        <div class="sec-head"><IconUsers size={13} /><span>Logged-in users</span>
          <span class="sec-note">{stats.users}</span>
        </div>
        {#if users.length > 0}
          <div class="chips">
            {#each users as u (u.name)}
              <span class="chip">
                {u.name}{#if u.count > 1}<span class="chip-count">×{u.count}</span>{/if}
              </span>
            {/each}
          </div>
        {:else}
          <div class="bar-cap">No named sessions reported.</div>
        {/if}
      </section>
    {/if}

    {#if !stats.ok}
      <p class="hint">This host answered but returned no readable metrics.</p>
    {/if}
  {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.5);
    z-index: 9000;
  }
  .modal {
    position: fixed;
    top: 50%; left: 50%;
    transform: translate(-50%, -50%);
    width: min(560px, 92vw);
    max-height: 82vh;
    display: flex; flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    z-index: 9001;
    overflow: hidden;
  }
  header {
    padding: 0.75rem 1rem 0.55rem;
    border-bottom: 1px solid var(--surface0);
    background: var(--mantle);
  }
  .title-row {
    display: flex; align-items: center; justify-content: space-between;
  }
  h2 {
    font-size: 1rem; margin: 0; color: var(--text);
    display: inline-flex; align-items: center; gap: 0.4rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .head-actions { display: inline-flex; align-items: center; gap: 0.15rem; flex-shrink: 0; }
  .icon-btn {
    background: transparent; border: 0; color: var(--subtext0);
    cursor: pointer; padding: 0.2rem; border-radius: 3px;
    display: inline-flex; align-items: center;
  }
  .icon-btn:hover:not(:disabled) { background: var(--surface0); color: var(--text); }
  .icon-btn:disabled { opacity: 0.6; cursor: default; }
  .icon-btn.spinning :global(svg) { animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .x {
    background: transparent; border: 0; color: var(--subtext0);
    font-size: 1.4rem; line-height: 1; cursor: pointer;
    padding: 0 0.3rem; border-radius: 3px;
  }
  .x:hover { background: var(--surface0); color: var(--text); }
  .sub-row {
    margin-top: 0.35rem;
    display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap;
    font-size: 0.76rem; color: var(--subtext0);
  }
  .sub-row .conn { color: var(--text); }
  .sub-row .meta {
    color: var(--overlay1);
    font-family: ui-monospace, monospace;
    font-size: 0.72rem;
  }
  .warn {
    margin-top: 0.35rem;
    color: var(--yellow);
    font-size: 0.74rem;
  }
  .tabs { display: flex; gap: 0.2rem; margin-top: 0.55rem; margin-bottom: -0.56rem; }
  .tabs button {
    background: none; border: 0; border-bottom: 2px solid transparent;
    color: var(--subtext0); font: inherit; font-size: 0.78rem;
    padding: 0.3rem 0.6rem; cursor: pointer;
  }
  .tabs button.active { color: var(--text); border-bottom-color: var(--blue); }
  .tab-bad { margin-left: 0.3rem; color: var(--red); font-weight: 600; }
  .tab-tools { display: flex; align-items: center; gap: 0.35rem; margin: 0.4rem 0 0.6rem; }
  .dim { color: var(--subtext0); }
  .seg-btn {
    background: none; border: 1px solid var(--surface1); color: var(--subtext0);
    border-radius: 4px; font: inherit; font-size: 0.74rem; padding: 0.1rem 0.55rem; cursor: pointer;
  }
  .seg-btn.on { background: var(--surface0); color: var(--text); border-color: var(--surface2); }
  .filter {
    margin-left: auto; background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px; font: inherit; font-size: 0.74rem;
    padding: 0.15rem 0.45rem; width: 9rem;
  }
  .modal.wide { width: min(760px, 94vw); }
  .ptable {
    display: grid; grid-template-columns: auto minmax(0, 1fr) auto auto minmax(0, 6rem) auto;
    gap: 0.35rem 0.7rem; align-items: center; font-size: 0.82rem;
  }
  .ptable .th { color: var(--subtext0); font-size: 0.74rem; }
  .ptable .num { text-align: right; font-variant-numeric: tabular-nums; }
  .ptable .cmd, .ptable .user { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ptable .cmd { font-family: ui-monospace, monospace; }
  .menu-cell { position: relative; }
  .pmenu {
    position: absolute; right: 0; top: 100%; z-index: 2; min-width: 12rem;
    background: var(--mantle); border: 1px solid var(--surface1); border-radius: 6px;
    padding: 0.25rem; box-shadow: 0 8px 24px rgba(0,0,0,0.35);
    display: flex; flex-direction: column;
  }
  .pmenu button {
    background: none; border: 0; text-align: left; color: var(--text);
    font: inherit; font-size: 0.76rem; padding: 0.3rem 0.5rem; border-radius: 4px; cursor: pointer;
  }
  .pmenu button:hover { background: var(--surface0); }
  .pmenu button.bad { color: var(--red); }
  .msep { height: 1px; background: var(--surface0); margin: 0.2rem 0.3rem; }
  .units { display: flex; flex-direction: column; gap: 0.15rem; }
  .unit-row {
    width: 100%; display: grid; grid-template-columns: auto auto auto minmax(0, 1fr);
    align-items: center; gap: 0.5rem; background: none; border: 0; color: var(--text);
    font: inherit; font-size: 0.82rem; padding: 0.3rem 0.35rem; border-radius: 4px; cursor: pointer; text-align: left;
  }
  .unit-row:hover, .unit.open .unit-row { background: var(--surface0); }
  .ign { display: inline-flex; align-items: center; gap: 0.3rem; flex-wrap: wrap; color: var(--subtext0); font-size: 0.74rem; }
  /* Text actions next to the unit buttons: coloured so they read as links. */
  .unit-actions .more { color: var(--sapphire); }
  .unit-actions .more:hover { color: var(--text); text-decoration: underline; }
  .scope {
    display: inline-flex; align-items: center; gap: 0.25rem; cursor: pointer;
    background: var(--surface0); color: var(--sapphire); border: 1px solid var(--surface1);
    border-radius: 10px; padding: 0.08rem 0.5rem; font: inherit; font-size: 0.74rem;
  }
  .scope:hover { background: var(--surface1); color: var(--text); }
  .unit.ignored .uname, .unit.ignored .udesc { color: var(--overlay1); }
  .udot { width: 7px; height: 7px; border-radius: 50%; background: var(--overlay0); }
  .udot.active { background: var(--green); }
  .udot.failed { background: var(--red); }
  .uname { font-family: ui-monospace, monospace; }
  .ustate { color: var(--subtext0); font-size: 0.7rem; }
  .udesc { color: var(--subtext0); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .unit-body { padding: 0.35rem 0.5rem 0.5rem 1.2rem; }
  .ulog {
    margin: 0; padding: 0.4rem 0.5rem; background: var(--mantle); border-radius: 4px;
    font-size: 0.7rem; white-space: pre-wrap; word-break: break-word; color: var(--subtext1, var(--text));
    max-height: 9rem; overflow: auto;
  }
  .unit-actions { display: flex; gap: 0.4rem; align-items: center; flex-wrap: wrap; }
  .body {
    padding: 0.4rem 1rem 0.9rem;
    overflow-y: auto;
    flex: 1; min-height: 0;
    color: var(--text);
    font-size: 0.82rem;
  }
  section {
    padding: 0.7rem 0;
    border-bottom: 1px solid var(--surface0);
  }
  section:last-child { border-bottom: 0; }
  .sec-head {
    display: flex; align-items: center; gap: 0.35rem;
    color: var(--subtext1);
    font-size: 0.76rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin-bottom: 0.4rem;
  }
  .sec-head.sub {
    margin-top: 0.6rem;
    text-transform: none;
    letter-spacing: 0;
    color: var(--subtext0);
  }
  .sec-note { margin-left: auto; color: var(--overlay1); font-variant-numeric: tabular-nums; }
  .load-nums {
    display: flex; align-items: baseline; gap: 0.6rem;
    font-variant-numeric: tabular-nums;
    font-size: 1.05rem;
    color: var(--text);
    margin-bottom: 0.45rem;
  }
  .load-nums .dim { color: var(--overlay1); font-size: 0.9rem; }
  .load-nums .label {
    margin-left: auto; font-size: 0.68rem; text-transform: uppercase;
    letter-spacing: 0.04em; color: var(--overlay0);
  }
  .bar {
    height: 8px;
    border-radius: 4px;
    background: var(--surface0);
    overflow: hidden;
  }
  .bar .fill {
    height: 100%;
    border-radius: 4px;
    transition: width 0.2s ease;
  }
  .bar-cap {
    margin-top: 0.25rem;
    color: var(--subtext0);
    font-size: 0.72rem;
    font-variant-numeric: tabular-nums;
  }
  .part { margin-top: 0.55rem; }
  .part-cap { display: flex; align-items: center; gap: 0.5rem; }
  .more {
    margin-left: auto;
    display: inline-flex; align-items: center; gap: 0.1rem;
    background: none; border: none; padding: 0;
    color: var(--overlay1); font-size: 0.7rem; cursor: pointer;
  }
  .more:hover { color: var(--text); }
  .more-body {
    margin-top: 0.3rem; padding: 0.35rem 0.5rem;
    border-left: 2px solid var(--surface1);
  }
  .du-cut {
    display: flex; gap: 0.45rem; align-items: flex-start; margin: 0.3rem 0 0.45rem;
    padding: 0.45rem 0.6rem; border-radius: 5px; font-size: 0.76rem; cursor: help;
    background: color-mix(in srgb, var(--yellow) 14%, transparent); color: var(--text);
    border: 1px solid color-mix(in srgb, var(--yellow) 45%, transparent);
  }
  .du-cut :global(svg) { color: var(--yellow); flex-shrink: 0; margin-top: 0.1rem; }
  .du-cut strong { color: var(--yellow); font-weight: 600; }
  .du-cut .du-btn { margin-top: 0.35rem; cursor: pointer; }
  .du-btn {
    margin-top: 0.35rem;
    background: var(--surface0); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px;
    font-size: 0.72rem; padding: 0.15rem 0.5rem; cursor: pointer;
  }
  .du-btn:hover { background: var(--surface1); }
  .dirs {
    display: grid; grid-template-columns: auto 1fr; gap: 0.1rem 0.6rem;
    margin-top: 0.3rem; font-size: 0.72rem;
  }
  .dir-size { color: var(--subtext0); text-align: right; font-variant-numeric: tabular-nums; }
  .dir-path {
    font-family: ui-monospace, monospace; color: var(--text);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .part:first-of-type { margin-top: 0; }
  .part-head {
    display: flex; align-items: baseline; gap: 0.5rem;
    margin-bottom: 0.25rem;
  }
  .part-head .mount {
    font-family: ui-monospace, monospace;
    color: var(--text);
    font-size: 0.78rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    max-width: 16rem;
  }
  .part-head .fs {
    color: var(--overlay0);
    font-size: 0.7rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    max-width: 12rem;
  }
  .part-head .part-pct {
    margin-left: auto; color: var(--subtext0);
    font-variant-numeric: tabular-nums;
  }
  .chips { display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    background: var(--surface0);
    color: var(--text);
    border-radius: 999px;
    padding: 0.1rem 0.5rem;
    font-size: 0.74rem;
    font-family: ui-monospace, monospace;
  }
  .chip-count {
    color: var(--subtext0);
    font-size: 0.66rem;
    font-variant-numeric: tabular-nums;
  }
  .dim { color: var(--overlay0); }
  .hint { color: var(--subtext0); font-style: italic; margin: 0.5rem 0 0; }
</style>
