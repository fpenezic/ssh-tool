<script lang="ts">
  // Download from many: the same remote file, folder or pattern from every
  // host in scope, into one local folder with a subfolder per host - e.g.
  // collecting logs. Check first (files and size per host, nothing
  // fetched), then download. Download checks on its own first: under
  // WARN_BYTES it starts straight away, above it shows the sizes and asks
  // once more. A large total is warned about, never refused. "Skip size
  // check" starts without looking.
  import { onDestroy } from "svelte";
  import { api, type FleetTransferEvent, type FleetTransferHost, type FleetDownloadOptions } from "./api";
  import { fleet, fleetHostName, fleetLabels, type FleetOpen } from "./fleetStore.svelte";
  import { EventsOn } from "./wailsRuntime";
  import { toast } from "./toast.svelte";
  import FleetShell from "./FleetShell.svelte";
  import { errMsg } from "./connectErrors";
  import { showConfirm } from "./confirmModal.svelte.ts";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();

  const WARN_BYTES = 500 * 1024 * 1024;
  const PREFS_KEY = "ssh-tool:fleet-download";
  function loadPrefs(): Partial<FleetDownloadOptions & { skip_check: boolean }> {
    try { return JSON.parse(localStorage.getItem(PREFS_KEY) ?? "{}"); } catch { return {}; }
  }
  const prefs = loadPrefs();

  let remotePath = $state(prefs.remote_path ?? "");
  let localDir = $state(prefs.local_dir ?? "");
  let skipCompressed = $state(prefs.skip_compressed ?? false);
  let skipCheck = $state(prefs.skip_check ?? false);
  let err = $state("");

  let scanning = $state(false);
  let scan = $state<FleetTransferHost[] | null>(null);
  let runId = $state("");
  let running = $state(false);
  let finished = $state(false);
  let hosts = $state<Record<string, FleetTransferHost>>({});
  let unsub: (() => void) | null = null;
  onDestroy(() => unsub?.());

  function opts(): FleetDownloadOptions {
    return { remote_path: remotePath.trim(), local_dir: localDir, skip_compressed: skipCompressed };
  }
  // Any change to what is fetched makes the last check stale.
  function invalidate() {
    if (running) return;
    scan = null;
    hosts = {};
    finished = false;
    err = "";
  }

  async function pickDir() {
    try {
      const d = await api.sftpPickDownloadDirDest();
      if (d) localDir = d;
    } catch (e) {
      err = errMsg(e);
    }
  }

  async function check(): Promise<boolean> {
    err = "";
    scanning = true;
    try {
      scan = (await api.fleetDownloadScan(scope.ids, opts(), fleetLabels(scope.ids))) ?? [];
      hosts = {};
      finished = false;
      return true;
    } catch (e) {
      err = errMsg(e);
      return false;
    } finally {
      scanning = false;
    }
  }

  // The Download button. A check already on screen counts as seen, so a
  // large total shown there downloads on this click ("anyway").
  async function download() {
    if (finished) {
      // Again: the last run's sizes may be out of date.
      scan = null;
      hosts = {};
      finished = false;
    }
    if (skipCheck) return start(scope.ids);
    if (!scan) {
      if (!(await check())) return;
      if (scanTotal > WARN_BYTES || scanOk.length === 0) return; // shown, wait for a second click
    }
    if (scanOk.length > 0) return start(scanOk.map((r) => r.connection_id));
  }

  async function start(ids: string[]) {
    err = "";
    finished = false;
    const o = opts();
    try { localStorage.setItem(PREFS_KEY, JSON.stringify({ ...o, skip_check: skipCheck })); } catch { /* fine */ }
    const next = { ...hosts };
    for (const id of ids) {
      next[id] = {
        connection_id: id, name: fleetHostName(id), hostname: "", state: "queued",
        files_done: 0, files_total: 0, files_skipped: 0, unreadable: 0, bytes: 0, total: 0, duration_ms: 0,
      };
    }
    hosts = next;
    runId = crypto.randomUUID();
    unsub?.();
    unsub = EventsOn(`fleet_download:${runId}`, (ev: FleetTransferEvent) => {
      if (ev.host) hosts = { ...hosts, [ev.host.connection_id]: ev.host };
      if (ev.done) {
        for (const r of ev.results ?? []) hosts = { ...hosts, [r.connection_id]: r };
        running = false;
        finished = true;
        unsub?.();
        unsub = null;
        const rs = ev.results ?? [];
        const ok = rs.filter((r) => r.state === "done").length;
        const bad = rs.filter((r) => r.state === "error").length;
        toast.push(bad ? "err" : "ok", `Download: ${ok} of ${rs.length} host${rs.length === 1 ? "" : "s"} done${bad ? `, ${bad} with errors` : ""}`);
      }
    });
    running = true;
    try {
      await api.fleetDownloadStart(runId, ids, o, fleetLabels(ids));
    } catch (e) {
      running = false;
      unsub?.();
      unsub = null;
      err = errMsg(e);
    }
  }

  function cancel() {
    if (running) void api.sftpCancelTransfer(runId);
  }
  // Backdrop click / Escape mid-run minimises: the transfer keeps going and
  // the status bar brings it back. x stops it (after asking) and closes.
  function dismiss() {
    if (running) fleet.minimizeTransfer();
    else fleet.closeTransfer();
  }
  async function close() {
    if (running) {
      const ok = await showConfirm({
        title: "Stop the download?",
        message: "Hosts still transferring stop now; what already arrived stays. Use the minimise button to keep it running in the background instead.",
        okLabel: "Stop and close",
        danger: true,
      });
      if (!ok) return;
      cancel();
    }
    fleet.closeTransfer();
  }
  async function openDir() {
    try { await api.fleetOpenDir(localDir); } catch (e) { toast.err(errMsg(e)); }
  }

  // Rows: the live run when there is one, else the check.
  const rows = $derived(
    Object.keys(hosts).length > 0
      ? (scope.ids.map((id) => hosts[id]).filter(Boolean) as FleetTransferHost[])
      : (scan ?? []),
  );
  const scanOk = $derived((scan ?? []).filter((r) => r.state === "done" && r.files_total > 0));
  const scanTotal = $derived(scanOk.reduce((n, r) => n + r.total, 0));
  const scanFiles = $derived(scanOk.reduce((n, r) => n + r.files_total, 0));
  const failedIds = $derived(rows.filter((r) => r.state === "error" || r.state === "cancelled").map((r) => r.connection_id));
  const live = $derived(Object.keys(hosts).length > 0);
  const totals = $derived.by(() => {
    let done = 0, total = 0;
    for (const r of rows) { done += r.bytes; total += r.total; }
    return { done, total };
  });
  const canCheck = $derived(!!remotePath.trim() && !scanning && !running);
  const canStart = $derived(
    !!localDir && !!remotePath.trim() && !running && !scanning && (finished || !scan || scanOk.length > 0),
  );
  const large = $derived(!!scan && !live && scanTotal > WARN_BYTES);

  function fmtSize(n: number): string {
    if (n < 1024) return `${Math.round(n)} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
  }
  function files(n: number): string {
    return `${n} file${n === 1 ? "" : "s"}`;
  }
  function stateLabel(r: FleetTransferHost): string {
    if (!live) return r.state === "error" ? "error" : r.files_total === 0 ? "nothing" : "ready";
    return { queued: "queued", connecting: "connecting…", transferring: "downloading", done: "done", error: "error", cancelled: "cancelled" }[r.state];
  }
  function stateCls(r: FleetTransferHost): string {
    if (r.state === "error") return "ferr";
    if (r.state === "cancelled") return "fwarn";
    if (live && r.state === "done") return "fok";
    if (!live && r.files_total === 0) return "fdim";
    return r.state === "queued" || r.state === "connecting" ? "fdim" : "";
  }
  function detail(r: FleetTransferHost): string {
    if (r.state === "error" && !live) return r.error ?? "";
    const unread = r.unreadable ? ` · ${r.unreadable} unreadable` : "";
    if (r.state === "transferring") {
      const pct = r.total > 0 ? Math.round((r.bytes / r.total) * 100) : 0;
      return `${r.files_done}/${r.files_total} · ${pct}%${r.current ? ` · ${r.current}` : ""}`;
    }
    if (r.state === "error") return r.error ?? "";
    return `${files(r.files_total)} · ${fmtSize(r.total)}${unread}`;
  }

  // Keep the status-bar segment current (it shows while minimised).
  $effect(() => {
    if (!running && !finished) {
      fleet.transferStatus = null;
      return;
    }
    const rs = rows;
    fleet.transferStatus = {
      tool: "download",
      running,
      hosts: rs.length,
      done: rs.filter((r) => r.state === "done").length,
      failed: rs.filter((r) => r.state === "error" || r.state === "cancelled").length,
      bytes: totals.done,
      total: totals.total,
    };
  });
</script>

<FleetShell
  title="Download files"
  sub={`${scope.label} · ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"} · over SFTP as each host's login user, into one folder per host`}
  onClose={() => void close()}
  onDismiss={dismiss}
  onMinimize={() => fleet.minimizeTransfer()}
  minimized={fleet.transferMinimized}
>
  <div class="col">
    <label for="dl-remote" class="fdim">Remote file, folder or pattern</label>
    <input id="dl-remote" class="finput fmono" bind:value={remotePath} disabled={running} oninput={invalidate}
      spellcheck="false" placeholder="/var/log/nginx/*.log  or  ~/app/logs" />
    <span class="fdim small">A folder comes with everything in it. Patterns: <span class="fmono">*</span>, <span class="fmono">?</span>, <span class="fmono">[..]</span> - not <span class="fmono">**</span> or <span class="fmono">{"{a,b}"}</span>.</span>
  </div>

  <div class="col">
    <span class="fdim">Download into</span>
    <div class="pick">
      <button class="fbtn" disabled={running} onclick={pickDir}>Choose folder…</button>
      {#if localDir}
        <span class="fmono path" title={localDir}>{localDir}</span>
      {:else}
        <span class="fdim">Nothing chosen yet</span>
      {/if}
    </div>
    {#if localDir}<span class="fdim small">Each host gets its own subfolder, named after the connection. Files already there are replaced.</span>{/if}
  </div>

  <label class="check"><input type="checkbox" bind:checked={skipCompressed} disabled={running} onchange={invalidate} />Skip compressed files (.gz, .xz, .zst, .zip, ...)</label>
  <label class="check" title="Download first lists every host's files and their size, and stops to ask when the total is over 500 MB. Skip that to start at once.">
    <input type="checkbox" bind:checked={skipCheck} disabled={running} />Skip size check - start downloading at once
  </label>

  {#if scanning}
    <p class="fdim summary">Checking what to download on {scope.ids.length} host{scope.ids.length === 1 ? "" : "s"} - listing only, nothing is fetched yet…</p>
  {/if}

  {#if err}<p class="ferr">{err}</p>{/if}

  {#if scan && !live}
    {#if scanOk.length === 0}
      <p class="fwarn">Nothing to download on any host.</p>
    {:else}
      <p class:fwarn={scanTotal > WARN_BYTES} class="summary">
        {files(scanFiles)} · {fmtSize(scanTotal)} from {scanOk.length} host{scanOk.length === 1 ? "" : "s"}
        {#if scanTotal > WARN_BYTES} - a large download, it will take a while and fill the disk accordingly.{/if}
      </p>
    {/if}
  {/if}

  {#if rows.length > 0}
    {#if running && totals.total > 0}
      <div class="bar"><div style="width: {Math.min(100, (totals.done / totals.total) * 100)}%"></div></div>
    {/if}
    <table class="ftable">
      <thead><tr><th>Host</th><th>{live ? "State" : "Check"}</th><th></th></tr></thead>
      <tbody>
        {#each rows as r (r.connection_id)}
          <tr>
            <td class="fmono" title={r.target ?? r.hostname}>{r.name}</td>
            <td class={stateCls(r)}>{stateLabel(r)}</td>
            <td class="detail" class:ferr={r.state === "error"} title={r.error ?? r.target ?? ""}>{detail(r)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  {#snippet footer()}
    {#if running}
      <button class="fbtn danger" onclick={cancel}>Cancel download</button>
    {:else}
      <button class="fbtn" onclick={() => void close()}>{finished ? "Close" : "Cancel"}</button>
      {#if finished}
        <button class="fbtn" onclick={openDir}>Open folder</button>
        {#if failedIds.length > 0}
          <button class="fbtn" onclick={() => start(failedIds)}>Retry {failedIds.length} failed</button>
        {/if}
      {/if}
      <button class="fbtn" disabled={!canCheck} onclick={() => void check()}>{scan ? "Check again" : "Check sizes"}</button>
      <button class="fbtn primary" disabled={!canStart} title={!localDir ? "Choose a folder to download into" : ""}
        onclick={() => void download()}>
        {scanning ? "Checking…" : finished ? "Download again" : large ? `Download ${fmtSize(scanTotal)} anyway` : scan ? `Download ${fmtSize(scanTotal)}` : "Download"}
      </button>
    {/if}
  {/snippet}
</FleetShell>

<style>
  .col { display: flex; flex-direction: column; gap: 0.3rem; margin-bottom: 0.7rem; min-width: 0; }
  .pick { display: flex; align-items: center; gap: 0.45rem; flex-wrap: wrap; min-width: 0; }
  .path { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; flex: 1; font-size: 0.78rem; }
  .small { font-size: 0.72rem; }
  .check { display: flex; gap: 0.45rem; align-items: center; margin-bottom: 0.7rem; }
  .summary { margin: 0 0 0.5rem; font-size: 0.8rem; }
  .detail { max-width: 24rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bar { height: 4px; background: var(--surface0); border-radius: 2px; overflow: hidden; margin-bottom: 0.5rem; }
  .bar > div { height: 100%; background: var(--blue); transition: width 0.2s; }
</style>
