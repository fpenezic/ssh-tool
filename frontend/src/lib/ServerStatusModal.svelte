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
  import { api, type ServerStats } from "./api";
  import { IconCpu, IconMemory, IconDisk, IconUsers, IconRefresh, IconHost } from "./iconMap";
  import { focusActiveTerminal } from "./terminalFocus";

  interface Props {
    initial: ServerStats;
    connName: string;
    sessionId: string;
    onClose: () => void;
  }
  let { initial, connName, sessionId, onClose: onCloseProp }: Props = $props();

  // The modal steals keyboard focus; hand it back to the terminal on close so
  // the user can keep typing without re-clicking the pane. Every close path
  // (Esc, backdrop, the X button) routes through here.
  function onClose() {
    onCloseProp();
    focusActiveTerminal();
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
      const s = await api.sshServerStats(sessionId);
      if (s && s.ok) stats = s;
      else refreshErr = "Host returned no readable stats.";
    } catch (e: any) {
      refreshErr = "Probe failed (session may have closed).";
    } finally {
      refreshing = false;
    }
  }

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

  // Bar colour by saturation, with per-metric thresholds (a disk at 75% is
  // fine; a load at 0.75/core or any real swap use is not). green -> yellow
  // -> red. Compares against the yellow/red cut for the metric.
  function barVar(frac: number, warn: number, crit: number): string {
    if (frac >= crit) return "var(--red)";
    if (frac >= warn) return "var(--yellow)";
    return "var(--green)";
  }
  const cpuColor = (f: number) => barVar(f, 0.7, 1.0);
  const memColor = (f: number) => barVar(f, 0.75, 0.9);
  const swapColor = (f: number) => barVar(f, 0.25, 0.6);
  const diskColor = (f: number) => barVar(f, 0.8, 0.92);
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
<div class="modal" role="dialog" aria-labelledby="sysstat-title">
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
  </header>

  <div class="body">
    <!-- CPU / load -->
    <section>
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
            <div class="bar-cap">{fmtBytesKB(p.used_kb)} / {fmtBytesKB(p.size_kb)} used · {fmtBytesKB(p.avail_kb)} free</div>
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
