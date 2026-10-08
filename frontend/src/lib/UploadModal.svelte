<script lang="ts">
  // Upload to many: one local file or directory, sent over SFTP to every
  // host in scope as the login user. No sudo and no post-command - the
  // file lands where that user can write; anything after is done by hand.
  import { onDestroy } from "svelte";
  import { api, type FleetTransferEvent, type FleetTransferHost, type FleetUploadOptions } from "./api";
  import { fleet, fleetHostName, fleetLabels, type FleetOpen } from "./fleetStore.svelte";
  import { EventsOn } from "./wailsRuntime";
  import { toast } from "./toast.svelte";
  import FleetShell from "./FleetShell.svelte";
  import { errMsg } from "./connectErrors";
  import { showConfirm } from "./confirmModal.svelte.ts";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();

  // The last target and policy come back next time: uploads to a fleet
  // tend to go to the same place. Browser storage, a convenience only.
  const PREFS_KEY = "ssh-tool:fleet-upload";
  function loadPrefs(): Partial<FleetUploadOptions> {
    try { return JSON.parse(localStorage.getItem(PREFS_KEY) ?? "{}"); } catch { return {}; }
  }
  const prefs = loadPrefs();

  let localPath = $state("");
  let isDir = $state(false);
  let remoteDir = $state(prefs.remote_dir ?? "~/");
  let existing = $state<FleetUploadOptions["existing"]>(prefs.existing ?? "skip");
  let mode = $state(prefs.mode ?? "");
  let err = $state("");

  let runId = $state("");
  let running = $state(false);
  let hosts = $state<Record<string, FleetTransferHost>>({});
  let finished = $state(false);
  let unsub: (() => void) | null = null;
  onDestroy(() => unsub?.());

  const MODE_OK = /^(|[0-7]{3,4})$/;
  const localName = $derived(localPath.split(/[\\/]/).filter(Boolean).at(-1) ?? "");
  const canStart = $derived(!!localPath && MODE_OK.test(mode.trim()) && !running);

  async function pick(dir: boolean) {
    try {
      const p = dir ? await api.sftpPickUploadDirSource() : await api.sftpPickUploadSource();
      if (p) { localPath = p; isDir = dir; reset(); }
    } catch (e) {
      err = errMsg(e);
    }
  }

  function reset() {
    if (running) return;
    hosts = {};
    finished = false;
    err = "";
  }

  async function start(ids: string[]) {
    err = "";
    finished = false;
    const opts: FleetUploadOptions = { local_path: localPath, remote_dir: remoteDir.trim(), existing, mode: mode.trim() };
    try { localStorage.setItem(PREFS_KEY, JSON.stringify({ remote_dir: opts.remote_dir, existing, mode: opts.mode })); } catch { /* fine */ }
    const next: Record<string, FleetTransferHost> = { ...hosts };
    for (const id of ids) {
      next[id] = {
        connection_id: id, name: fleetHostName(id), hostname: "", state: "queued",
        files_done: 0, files_total: 0, files_skipped: 0, unreadable: 0, bytes: 0, total: 0, duration_ms: 0,
      };
    }
    hosts = next;
    runId = crypto.randomUUID();
    unsub?.();
    unsub = EventsOn(`fleet_upload:${runId}`, (ev: FleetTransferEvent) => {
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
        toast.push(bad ? "err" : "ok", `Upload: ${ok} of ${rs.length} host${rs.length === 1 ? "" : "s"} done${bad ? `, ${bad} failed` : ""}`);
      }
    });
    running = true;
    try {
      await api.fleetUploadStart(runId, ids, opts, fleetLabels(ids));
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
        title: "Stop the upload?",
        message: "Hosts still transferring stop now; what already arrived stays. Use the minimise button to keep it running in the background instead.",
        okLabel: "Stop and close",
        danger: true,
      });
      if (!ok) return;
      cancel();
    }
    fleet.closeTransfer();
  }

  const rows = $derived(scope.ids.map((id) => hosts[id]).filter(Boolean) as FleetTransferHost[]);
  const failedIds = $derived(rows.filter((r) => r.state === "error" || r.state === "cancelled").map((r) => r.connection_id));
  const totals = $derived.by(() => {
    let done = 0, total = 0;
    for (const r of rows) { done += r.bytes; total += r.total; }
    return { done, total };
  });

  function fmtSize(n: number): string {
    if (n < 1024) return `${Math.round(n)} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
  }
  function pct(r: FleetTransferHost): number {
    return r.total > 0 ? Math.min(100, Math.round((r.bytes / r.total) * 100)) : 0;
  }
  const STATE: Record<FleetTransferHost["state"], string> = {
    queued: "queued", connecting: "connecting…", transferring: "uploading", done: "done", error: "error", cancelled: "cancelled",
  };
  const CLS: Record<FleetTransferHost["state"], string> = {
    queued: "fdim", connecting: "fdim", transferring: "", done: "fok", error: "ferr", cancelled: "fwarn",
  };
  function detail(r: FleetTransferHost): string {
    if (r.state === "error") return r.error ?? "";
    if (r.state === "transferring") {
      const files = r.files_total > 1 ? `${r.files_done}/${r.files_total} files · ` : "";
      return `${files}${pct(r)}%${r.current ? ` · ${r.current}` : ""}`;
    }
    if (r.state === "done") {
      if (r.files_skipped > 0 && r.files_skipped === r.files_total) return "already there - skipped";
      const sent = r.files_total - r.files_skipped;
      const skip = r.files_skipped ? `, ${r.files_skipped} skipped` : "";
      return r.files_total > 1 ? `${sent} file${sent === 1 ? "" : "s"} sent${skip}` : fmtSize(r.total);
    }
    return "";
  }

  // Keep the status-bar segment current (it shows while minimised).
  $effect(() => {
    if (!running && !finished) {
      fleet.transferStatus = null;
      return;
    }
    const rs = rows;
    fleet.transferStatus = {
      tool: "upload",
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
  title="Upload file"
  sub={`${scope.label} · ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"} · over SFTP as each host's login user; nothing runs on the host`}
  onClose={() => void close()}
  onDismiss={dismiss}
  onMinimize={() => fleet.minimizeTransfer()}
  minimized={fleet.transferMinimized}
>
  <div class="col">
    <span class="fdim">What to upload</span>
    <div class="pick">
      <button class="fbtn" disabled={running} onclick={() => pick(false)}>Choose file…</button>
      <button class="fbtn" disabled={running} onclick={() => pick(true)}>Choose folder…</button>
      {#if localPath}
        <span class="fmono path" title={localPath}>{localPath}</span>
      {:else}
        <span class="fdim">Nothing chosen yet</span>
      {/if}
    </div>
  </div>

  <div class="col">
    <label for="up-dir" class="fdim">Remote directory (created if missing)</label>
    <input id="up-dir" class="finput fmono" bind:value={remoteDir} disabled={running} oninput={reset}
      spellcheck="false" placeholder="~/  or  /tmp/" />
    {#if localName}
      <span class="fdim small">Lands as <span class="fmono">{(remoteDir.trim() || "~").replace(/\/+$/, "")}/{localName}{isDir ? "/" : ""}</span>
        {#if isDir} (the folder itself, with everything in it){/if}</span>
    {/if}
  </div>

  <div class="grid">
    <div class="col">
      <label for="up-existing" class="fdim">When a file is already there</label>
      <select id="up-existing" class="fselect" bind:value={existing} disabled={running} onchange={reset}>
        <option value="skip">Skip it</option>
        <option value="changed">Replace if changed (size or newer)</option>
        <option value="overwrite">Always replace</option>
      </select>
    </div>
    <div class="col">
      <label for="up-mode" class="fdim">Permissions (optional)</label>
      <input id="up-mode" class="finput fmono" bind:value={mode} disabled={running} spellcheck="false" placeholder="e.g. 0755" />
      {#if !MODE_OK.test(mode.trim())}<span class="ferr small">Octal, like 0644 or 755.</span>{/if}
    </div>
  </div>

  {#if err}<p class="ferr">{err}</p>{/if}

  {#if rows.length > 0}
    {#if running && totals.total > 0}
      <div class="bar"><div style="width: {Math.min(100, (totals.done / totals.total) * 100)}%"></div></div>
    {/if}
    <table class="ftable">
      <thead><tr><th>Host</th><th>State</th><th></th></tr></thead>
      <tbody>
        {#each rows as r (r.connection_id)}
          <tr>
            <td class="fmono" title={r.target ?? r.hostname}>{r.name}</td>
            <td class={CLS[r.state]}>{STATE[r.state]}</td>
            <td class="detail" class:ferr={r.state === "error"} title={r.target ?? ""}>{detail(r)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  {#snippet footer()}
    {#if running}
      <button class="fbtn danger" onclick={cancel}>Cancel upload</button>
    {:else}
      <button class="fbtn" onclick={() => void close()}>{finished ? "Close" : "Cancel"}</button>
      {#if finished && failedIds.length > 0}
        <button class="fbtn" onclick={() => start(failedIds)}>Retry {failedIds.length} failed</button>
      {/if}
      <button class="fbtn primary" disabled={!canStart} onclick={() => start(scope.ids)}>
        {finished ? "Upload again" : `Upload to ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"}`}
      </button>
    {/if}
  {/snippet}
</FleetShell>

<style>
  .col { display: flex; flex-direction: column; gap: 0.3rem; margin-bottom: 0.7rem; min-width: 0; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.8rem; }
  @media (max-width: 560px) { .grid { grid-template-columns: 1fr; } }
  .pick { display: flex; align-items: center; gap: 0.45rem; flex-wrap: wrap; min-width: 0; }
  .path { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; flex: 1; font-size: 0.78rem; }
  .small { font-size: 0.72rem; }
  .detail { max-width: 22rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bar { height: 4px; background: var(--surface0); border-radius: 2px; overflow: hidden; margin-bottom: 0.5rem; }
  .bar > div { height: 100%; background: var(--blue); transition: width 0.2s; }
</style>
