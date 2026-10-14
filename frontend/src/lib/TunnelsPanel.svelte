<script lang="ts">
  // Tunnels panel (status bar). Every tunnel-only connection with its state,
  // start / stop, the reconnect countdown, the last error and its forwards.
  // State comes from the shared tunnels store (tunnels_changed event); while
  // the panel is open it also polls, for forward byte counters and the
  // "up for" / "next try in" clocks.
  import { tunnels } from "./tunnels.svelte.ts";
  import { api, type TunnelInfo, type TunnelForwardInfo } from "./api";
  import { view } from "./stores.svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconTunnel, IconPlay, IconStop, IconTerminal, IconX, IconExternalLink } from "./iconMap";

  interface Props {
    // Listening port forwards across every session (tunnels and terminal
    // tabs alike) - the status bar's forwards count, shown here instead.
    forwardCount: number;
    onClose: () => void;
  }
  let { forwardCount, onClose }: Props = $props();

  let filter = $state("");
  let expanded = $state<Set<string>>(new Set());
  let now = $state(Math.floor(Date.now() / 1000));

  $effect(() => {
    void tunnels.refresh();
    const poll = setInterval(() => {
      now = Math.floor(Date.now() / 1000);
      void tunnels.refresh();
    }, 2000);
    return () => clearInterval(poll);
  });

  const shown = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return tunnels.list;
    return tunnels.list.filter((t) =>
      t.name.toLowerCase().includes(q) ||
      t.hostname.toLowerCase().includes(q) ||
      t.folder_path.toLowerCase().includes(q) ||
      t.forwards.some((f) => String(f.local_port).includes(q) || f.remote_host.toLowerCase().includes(q)),
    );
  });

  // Rows grouped under their folder path, in the order the backend sorted.
  const groups = $derived.by(() => {
    const out: { path: string; items: TunnelInfo[] }[] = [];
    for (const t of shown) {
      const last = out[out.length - 1];
      if (last && last.path === t.folder_path) last.items.push(t);
      else out.push({ path: t.folder_path, items: [t] });
    }
    return out;
  });

  const stoppedShown = $derived(shown.filter((t) => !tunnels.running(t)));
  const runningShown = $derived(shown.filter((t) => tunnels.running(t)));

  function toggleExpand(id: string) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expanded = next;
  }

  function dur(secs: number): string {
    secs = Math.max(0, secs);
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
    return `${Math.floor(secs / 86400)}d ${Math.floor((secs % 86400) / 3600)}h`;
  }

  function bytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }

  function stateText(t: TunnelInfo): string {
    switch (t.state) {
      case "connected":
        return `up ${dur(now - t.since)}`;
      case "connecting":
        return "connecting...";
      case "reconnecting":
        if (t.next_retry > now) return `reconnecting, try ${t.attempt} in ${t.next_retry - now}s`;
        return `reconnecting, try ${t.attempt}...`;
      case "error":
        return "failed";
      default:
        return "stopped";
    }
  }

  function fwdSummary(t: TunnelInfo): string {
    if (t.forwards.length === 0) return "no forwards";
    if (t.state !== "connected") return `${t.forwards.length} forward${t.forwards.length === 1 ? "" : "s"}`;
    const up = t.forwards.filter((f) => f.state === "listening").length;
    return `${up}/${t.forwards.length} listening`;
  }

  function fwdBroken(t: TunnelInfo): boolean {
    return t.state === "connected" && t.forwards.some((f) => f.state === "error" || (f.auto_start && f.state !== "listening"));
  }

  function fwdLabel(f: TunnelForwardInfo): string {
    const local = `${f.local_addr || "127.0.0.1"}:${f.local_port || "auto"}`;
    if (f.kind === "dynamic") return `${local}  SOCKS`;
    if (f.kind === "remote") return `remote :${f.remote_port}  ->  ${local}`;
    return `${local}  ->  ${f.remote_host}:${f.remote_port}`;
  }

  async function fwdToggle(t: TunnelInfo, f: TunnelForwardInfo) {
    try {
      if (f.state === "listening") await api.forwardsStop(f.id);
      else if (t.session_id) await api.forwardsStart(f.id, t.session_id);
    } catch (e) {
      toast.err(errMsg(e));
    }
    void tunnels.refresh();
  }

  function startAll() {
    for (const t of stoppedShown) void tunnels.start(t.connection_id);
  }
  function stopAll() {
    for (const t of runningShown) void tunnels.stop(t.connection_id);
  }

  function reveal(t: TunnelInfo) {
    view.reveal("connection", t.connection_id);
    onClose();
  }
  function openTerminal(t: TunnelInfo) {
    void connectionActions.connectOne(t.connection_id);
    onClose();
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") onClose();
  }
</script>

<svelte:window onkeydown={onKey} />

<div class="panel" role="dialog" aria-label="Tunnels">
  <div class="head">
    <div class="title">
      <IconTunnel size={13} />
      <span>Tunnels</span>
      <span class="count">{tunnels.upCount}/{tunnels.list.length} up</span>
      <span class="count" title="Listening port forwards in all sessions, terminal tabs included">- {forwardCount} forward{forwardCount === 1 ? "" : "s"} listening</span>
    </div>
    <div class="head-actions">
      <button disabled={stoppedShown.length === 0} onclick={startAll} title="Start every stopped tunnel shown">
        <IconPlay size={11} /> Start all
      </button>
      <button disabled={runningShown.length === 0} onclick={stopAll} title="Stop every running tunnel shown">
        <IconStop size={11} /> Stop all
      </button>
      <button class="close" onclick={onClose} aria-label="Close"><IconX size={12} /></button>
    </div>
  </div>

  {#if tunnels.list.length > 6}
    <input class="filter" placeholder="Filter by name, host, folder or port" bind:value={filter} />
  {/if}

  <div class="list">
    {#if tunnels.list.length === 0}
      <div class="empty">
        No tunnels yet. Set <b>Tunnel only</b> on a connection (or a whole folder) in its settings: it then runs without a shell or tab, holding just its port forwards, and shows up here.
      </div>
    {:else if shown.length === 0}
      <div class="empty">Nothing matches the filter.</div>
    {/if}

    {#each groups as g (g.path)}
      {#if g.path}<div class="group">{g.path}</div>{/if}
      {#each g.items as t (t.connection_id)}
        {@const open = expanded.has(t.connection_id)}
        <div class="tunnel" class:open>
          <div class="row">
            <button class="main" onclick={() => toggleExpand(t.connection_id)} title={open ? "Hide forwards" : "Show forwards"}>
              <span class="dot st-{t.state}"></span>
              <span class="name">{t.name}</span>
              {#if t.autostart}<span class="chip" title="Starts when the app starts">auto</span>{/if}
              <span class="state st-{t.state}">{stateText(t)}</span>
              <span class="fwd-sum" class:bad={fwdBroken(t)}>{fwdSummary(t)}</span>
            </button>
            {#if tunnels.running(t)}
              <button class="act" onclick={() => tunnels.stop(t.connection_id)} title="Stop this tunnel"><IconStop size={11} /></button>
            {:else}
              <button class="act go" onclick={() => tunnels.start(t.connection_id)} title={t.state === "error" ? "Try again" : "Start this tunnel"}><IconPlay size={11} /></button>
              {#if t.state === "error"}
                <button class="act" onclick={() => tunnels.stop(t.connection_id)} title="Clear the error (the tunnel stays stopped)"><IconX size={11} /></button>
              {/if}
            {/if}
          </div>
          {#if t.error && t.state !== "connected"}
            <div class="err" title={t.error}>{t.error}</div>
          {/if}
          {#if open}
            <div class="detail">
              <div class="host">{t.hostname}</div>
              {#if t.forwards.length === 0}
                <div class="muted">This connection has no port forwards yet. Add them under Port forwards in its settings.</div>
              {/if}
              {#each t.forwards as f (f.id)}
                <div class="fwd">
                  <span class="fdot fst-{f.state || 'off'}" title={f.state || "not running"}></span>
                  <span class="kind">{f.kind === "local" ? "L" : f.kind === "remote" ? "R" : "D"}</span>
                  <span class="flabel" title={f.description || fwdLabel(f)}>{fwdLabel(f)}</span>
                  {#if f.description}<span class="fdesc">{f.description}</span>{/if}
                  {#if f.state === "listening"}
                    <span class="traffic" title="Bytes in / out">{bytes(f.bytes_in)} / {bytes(f.bytes_out)}</span>
                  {:else if f.error}
                    <span class="ferr" title={f.error}>{f.error}</span>
                  {:else if !f.auto_start}
                    <span class="muted small" title="Not auto-start: starts only when you start it here">manual</span>
                  {/if}
                  {#if t.state === "connected"}
                    <button class="act small" onclick={() => fwdToggle(t, f)} title={f.state === "listening" ? "Stop this forward" : "Start this forward"}>
                      {#if f.state === "listening"}<IconStop size={10} />{:else}<IconPlay size={10} />{/if}
                    </button>
                  {/if}
                </div>
              {/each}
              <div class="links">
                <button class="link" onclick={() => reveal(t)}><IconExternalLink size={10} /> Show in tree</button>
                <button class="link" onclick={() => openTerminal(t)}><IconTerminal size={10} /> Open terminal</button>
              </div>
            </div>
          {/if}
        </div>
      {/each}
    {/each}
  </div>
</div>

<style>
  .panel {
    background: var(--base, #1e1e2e);
    color: var(--text, #cdd6f4);
    border: 1px solid var(--surface1, #45475a);
    border-radius: 8px;
    width: 32rem;
    max-width: calc(100vw - 1rem);
    padding: 0.6rem 0.7rem;
    box-shadow: 0 8px 30px rgba(0, 0, 0, 0.45);
    display: flex;
    flex-direction: column;
    max-height: 70vh;
    font-size: 0.78rem;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
    gap: 0.5rem;
  }
  .title { display: flex; align-items: center; gap: 0.4rem; font-weight: 600; font-size: 0.85rem; }
  .count { color: var(--subtext0); font-weight: 400; font-size: 0.75rem; }
  .head-actions { display: flex; gap: 0.35rem; align-items: center; }
  button {
    background: var(--surface0, #313244);
    border: 1px solid var(--surface1, #45475a);
    color: var(--text, #cdd6f4);
    border-radius: 5px;
    padding: 0.2rem 0.5rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.75rem;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.45; cursor: default; }
  .close { border: none; background: transparent; padding: 0.2rem; }
  .filter {
    width: 100%;
    box-sizing: border-box;
    margin-bottom: 0.45rem;
    background: var(--mantle);
    border: 1px solid var(--surface1);
    border-radius: 5px;
    color: var(--text);
    padding: 0.3rem 0.5rem;
    font: inherit;
  }
  .list { overflow-y: auto; min-height: 0; }
  .empty { color: var(--overlay1); padding: 0.5rem 0.2rem; line-height: 1.45; }
  .group {
    color: var(--overlay1);
    font-size: 0.66rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 0.45rem 0.2rem 0.2rem;
  }
  .tunnel { border-radius: 5px; }
  .tunnel.open { background: var(--mantle); }
  .row { display: flex; align-items: center; gap: 0.3rem; }
  .main {
    flex: 1;
    min-width: 0;
    background: transparent;
    border: 0;
    padding: 0.3rem 0.35rem;
    justify-content: flex-start;
    text-align: left;
    gap: 0.45rem;
  }
  .main:hover:not(:disabled) { background: var(--surface0); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--overlay0); flex-shrink: 0; }
  .dot.st-connected { background: var(--green); }
  .dot.st-connecting, .dot.st-reconnecting { background: var(--yellow); animation: pulse 1.4s ease-in-out infinite; }
  .dot.st-error { background: var(--red); }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
  .name { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .chip {
    font-size: 0.6rem;
    border: 1px solid var(--surface2);
    color: var(--subtext0);
    border-radius: 999px;
    padding: 0 0.35rem;
    flex-shrink: 0;
  }
  .state { color: var(--subtext0); white-space: nowrap; margin-left: auto; }
  .state.st-connected { color: var(--green); }
  .state.st-connecting, .state.st-reconnecting { color: var(--yellow); }
  .state.st-error { color: var(--red); }
  .fwd-sum { color: var(--overlay1); white-space: nowrap; font-size: 0.7rem; min-width: 5.5rem; text-align: right; }
  .fwd-sum.bad { color: var(--peach); }
  .act { padding: 0.2rem 0.35rem; flex-shrink: 0; }
  .act.go { color: var(--green); }
  .act.small { padding: 0.1rem 0.3rem; }
  .err {
    color: var(--red);
    font-size: 0.7rem;
    padding: 0 0.4rem 0.3rem 1.25rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .detail { padding: 0.1rem 0.4rem 0.45rem 1.25rem; display: flex; flex-direction: column; gap: 0.2rem; }
  .host { color: var(--overlay1); font-family: var(--mono, monospace); font-size: 0.7rem; margin-bottom: 0.1rem; }
  .fwd { display: flex; align-items: center; gap: 0.4rem; min-height: 1.3rem; }
  .fdot { width: 6px; height: 6px; border-radius: 50%; background: var(--surface2); flex-shrink: 0; }
  .fdot.fst-listening { background: var(--green); }
  .fdot.fst-error { background: var(--red); }
  .kind {
    font-size: 0.6rem;
    font-weight: 700;
    color: var(--crust);
    background: var(--overlay1);
    border-radius: 3px;
    padding: 0 0.22rem;
    flex-shrink: 0;
  }
  .flabel { font-family: var(--mono, monospace); font-size: 0.72rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .fdesc { color: var(--subtext0); font-size: 0.7rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .traffic, .ferr { margin-left: auto; font-size: 0.68rem; white-space: nowrap; }
  .traffic { color: var(--overlay1); }
  .ferr { color: var(--red); overflow: hidden; text-overflow: ellipsis; max-width: 12rem; }
  .muted { color: var(--overlay1); }
  .muted.small { margin-left: auto; font-size: 0.68rem; }
  .links { display: flex; gap: 0.4rem; margin-top: 0.25rem; }
  .link { background: transparent; border: 0; color: var(--blue); padding: 0.1rem 0.2rem; font-size: 0.7rem; }
  .link:hover:not(:disabled) { background: var(--surface0); }
</style>
