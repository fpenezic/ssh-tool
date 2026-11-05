<script lang="ts">
  // The server's sessions: who runs what, for how long, what it waits on and
  // which session blocks it. Pick a row to stop its statement or end the
  // session (both ask first; refused on a read-only connection). Seeing
  // other users' sessions takes a monitoring grant on most servers.

  import { onDestroy, onMount } from "svelte";
  import { api, type DbHandleInfo, type DbResult } from "./api";
  import DbResultGrid from "./DbResultGrid.svelte";
  import { errMsg } from "./connectErrors";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { IconRefresh } from "./iconMap";

  interface Props {
    sessionId: string;
    info: DbHandleInfo;
  }
  let { sessionId, info }: Props = $props();

  let result = $state<DbResult | null>(null);
  let err = $state<string | null>(null);
  let loading = $state(false);
  let auto = $state(false);
  let picked = $state<(string | null)[] | null>(null);
  let timer: ReturnType<typeof setInterval> | undefined;

  async function load() {
    if (loading) return;
    loading = true;
    try {
      result = await api.dbActivity(sessionId);
      err = null;
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }
  // Once on open: load reads its own state, so an effect would loop.
  onMount(() => void load());
  $effect(() => {
    clearInterval(timer);
    if (auto) timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  });
  onDestroy(() => clearInterval(timer));

  // SQL Server and Db2 can only end the whole session.
  const canCancel = $derived(!["mssql", "db2"].includes(info.engine));
  const col = (name: string) => (result?.columns ?? []).findIndex((c) => c.name.toLowerCase() === name);
  async function stop(endSession: boolean) {
    if (!picked) return;
    const id = picked[0] ?? "";
    const qi = col("query");
    const q = qi >= 0 ? picked[qi] ?? "" : "";
    const ui = col("user");
    const who = ui >= 0 && picked[ui] ? ` (${picked[ui]})` : "";
    const ok = await showConfirm({
      title: endSession ? "End this session?" : "Stop this statement?",
      message: `${endSession ? "End session" : "Stop what session"} ${id}${who} ${endSession ? "runs and close it" : "runs"} on ${info.name}${info.sensitive ? " - a sensitive connection" : ""}?${q ? `\n\n${q.length > 300 ? q.slice(0, 300) + "..." : q}` : ""}`,
      okLabel: endSession ? "End session" : "Stop",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.dbKill(sessionId, id, endSession);
      toast.ok(endSession ? `Session ${id} ended` : `Asked session ${id} to stop`);
      picked = null;
      await load();
    } catch (e) {
      toast.err(errMsg(e));
    }
  }
</script>

<div class="stab">
  <div class="bar">
    <button title="Read the sessions again" onclick={load} disabled={loading}><IconRefresh size={13} /> Refresh</button>
    <label title="Read them every 3 seconds"><input type="checkbox" bind:checked={auto} /> auto</label>
    <span class="sep"></span>
    {#if canCancel}
      <button disabled={!picked || info.read_only} title={info.read_only ? "Read-only connection" : "Stop the picked session's statement"} onclick={() => stop(false)}>Stop statement</button>
    {/if}
    <button class="danger" disabled={!picked || info.read_only} title={info.read_only ? "Read-only connection" : "End the picked session"} onclick={() => stop(true)}>End session</button>
    <span class="spacer"></span>
    <span class="muted-s">{result ? `${(result.rows ?? []).length} session${(result.rows ?? []).length === 1 ? "" : "s"} besides this one` : ""}</span>
  </div>
  <div class="body">
    {#if err}
      <pre class="err">{err}</pre>
      <p class="muted">Other users' sessions usually take a monitoring grant (pg_monitor, PROCESS, VIEW SERVER STATE, SELECT_CATALOG_ROLE, Db2 monitor authority).</p>
    {:else if result}
      <div class="gridbox"><DbResultGrid {result} exportName={`${info.name}-sessions`} onSelect={(r) => (picked = r)} /></div>
    {:else}
      <p class="muted">Loading...</p>
    {/if}
  </div>
</div>

<style>
  .stab { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 0.35rem; padding: 0.3rem 0.5rem; border-bottom: 1px solid var(--surface0); font-size: 0.78rem; }
  .bar button {
    display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.78rem; padding: 0.2rem 0.55rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; cursor: pointer;
  }
  .bar button:disabled { opacity: 0.45; cursor: default; }
  .bar button.danger:not(:disabled) { border-color: var(--red); }
  .bar label { display: inline-flex; align-items: center; gap: 0.25rem; color: var(--subtext0); }
  .sep { width: 0.5rem; }
  .spacer { flex: 1 1 auto; }
  .muted-s { font-size: 0.74rem; color: var(--subtext0); }
  .body { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
  .gridbox { flex: 1 1 auto; min-height: 0; }
  pre.err { margin: 0.5rem 0.75rem; color: var(--red); white-space: pre-wrap; font-size: 0.78rem; }
  .muted { margin: 0.6rem 0.75rem; font-size: 0.8rem; color: var(--subtext0); }
</style>
