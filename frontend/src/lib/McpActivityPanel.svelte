<script lang="ts">
  // History of what an LLM (MCP bridge) did: run / type / connect / read, with
  // the command, gate (auto / approved / denied), exit status and (for runs)
  // the captured output, collapsible. Opened per-pane (filtered to one session)
  // or globally from the status bar (all sessions). Live via the mcp_activity
  // event; seeded from McpActivityList.
  import { onMount, onDestroy } from "svelte";
  import { api, type McpActivity } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import { clickOutside } from "./clickOutside";
  import { IconChevronRight } from "./iconMap";

  interface Props {
    // Empty = all sessions (global panel); otherwise filter to this session.
    sessionId?: string;
    // "down" opens below the anchor (toolbar); "up" opens above (status bar).
    placement?: "down" | "up";
    onClose: () => void;
  }
  let { sessionId = "", placement = "down", onClose }: Props = $props();

  // Oldest first, newest at the bottom - reads like a terminal. The IPC
  // returns newest-first, so reverse on load; live events append.
  let items = $state<McpActivity[]>([]);
  let kindFilter = $state<"" | "run" | "type" | "connect" | "read">("");
  let search = $state("");
  let expanded = $state<Set<number>>(new Set());
  let unsub: (() => void) | null = null;
  let listEl = $state<HTMLElement | null>(null);

  function scrollToBottom() {
    // Wait a tick so the new row is in the DOM before measuring.
    requestAnimationFrame(() => { if (listEl) listEl.scrollTop = listEl.scrollHeight; });
  }

  async function load() {
    try {
      const rows = (await api.mcpActivityList(sessionId)) ?? [];
      items = rows.reverse(); // newest-first -> oldest-first
      scrollToBottom();
    } catch { /* ignore */ }
  }

  onMount(() => {
    load();
    unsub = EventsOn("mcp_activity", (e: any) => {
      const a = e as McpActivity;
      if (sessionId && a.session_id !== sessionId) return;
      if (items.some((x) => x.seq === a.seq)) return;
      // Append (newest at the bottom), cap to the backend ring size.
      items = [...items, a].slice(-500);
      scrollToBottom();
    });
  });
  onDestroy(() => { if (unsub) unsub(); });

  const filtered = $derived(
    items.filter((a) => {
      if (kindFilter && a.kind !== kindFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        if (!a.command.toLowerCase().includes(q) && !a.session.toLowerCase().includes(q)) return false;
      }
      return true;
    }),
  );

  function toggle(seq: number) {
    const next = new Set(expanded);
    if (next.has(seq)) next.delete(seq); else next.add(seq);
    expanded = next;
  }

  function fmtTime(ts: number): string {
    return new Date(ts * 1000).toLocaleTimeString(undefined, { hour12: false });
  }

  function gateClass(gate: string): string {
    if (gate === "denied") return "denied";
    if (gate === "approved") return "approved";
    return "auto";
  }
</script>

<div class="pop" class:up={placement === "up"} use:clickOutside={{ onOutside: onClose }}>
  <header>
    <span class="title">LLM activity{sessionId ? " (this session)" : ""}</span>
    <button class="close" title="Close" onclick={onClose}>×</button>
  </header>

  <div class="controls">
    <input class="search" placeholder="Filter…" bind:value={search} />
    <select bind:value={kindFilter}>
      <option value="">all</option>
      <option value="run">run</option>
      <option value="type">type</option>
      <option value="connect">connect</option>
      <option value="read">read</option>
    </select>
  </div>

  {#if filtered.length === 0}
    <div class="empty">No LLM activity{search || kindFilter ? " matches the filter" : " yet"}.</div>
  {:else}
    <ul class="list" bind:this={listEl}>
      {#each filtered as a (a.seq)}
        <li class="row">
          <button
            class="line"
            class:has-output={!!a.output}
            onclick={() => a.output && toggle(a.seq)}
            title={a.output ? "Show output" : ""}
          >
            <span class="ts">{fmtTime(a.ts)}</span>
            {#if a.output}
              <span class="chev" class:open={expanded.has(a.seq)}><IconChevronRight size={11} /></span>
            {:else}
              <span class="chev-spacer"></span>
            {/if}
            <span class="kind {a.kind}">{a.kind}</span>
            {#if !sessionId}<span class="sess">{a.session}</span>{/if}
            <span class="cmd">{a.command}</span>
            {#if a.exit === "error"}<span class="exit err">err</span>
            {:else if a.exit === "ok"}<span class="exit ok">ok</span>{/if}
            <span class="gate {gateClass(a.gate)}">{a.gate}</span>
          </button>
          {#if a.output && expanded.has(a.seq)}
            <pre class="output">{a.output}</pre>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  /* Shared visual shell for both placements. */
  .pop {
    z-index: 200;
    width: min(560px, 96vw);
    max-height: 65vh;
    display: flex;
    flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.45);
    font-size: 0.82rem;
  }
  /* Toolbar (pane): drop below the anchor, aligned to its right edge. */
  .pop:not(.up) {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
  }
  /* Status bar (global): pin to the bottom-right of the window so a
     560px panel never overflows off the left edge of a right-aligned
     status-bar button. */
  .pop.up {
    position: fixed;
    right: 8px;
    bottom: 34px;
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.45rem 0.6rem; border-bottom: 1px solid var(--surface0);
  }
  .title { font-weight: 600; color: var(--text); }
  .close {
    background: transparent; border: 0; color: var(--overlay0);
    font-size: 1.1rem; line-height: 1; cursor: pointer; padding: 0 0.2rem;
  }
  .close:hover { color: var(--text); }
  .controls { display: flex; gap: 0.4rem; padding: 0.4rem 0.6rem; }
  .search {
    flex: 1; background: var(--mantle); border: 1px solid var(--surface1);
    border-radius: 3px; color: var(--text); padding: 0.25rem 0.4rem; font: inherit; font-size: 0.82rem;
  }
  .controls select {
    background: var(--mantle); border: 1px solid var(--surface1);
    border-radius: 3px; color: var(--text); font: inherit; font-size: 0.82rem;
  }
  .empty { padding: 1rem 0.6rem; color: var(--overlay0); text-align: center; }
  .list { list-style: none; margin: 0; padding: 0.2rem; overflow-y: auto; }
  .row { border-radius: 4px; }
  .line {
    display: flex; align-items: center; gap: 0.4rem; width: 100%;
    background: transparent; border: 0; color: var(--text);
    padding: 0.28rem 0.4rem; cursor: default; font: inherit; text-align: left;
  }
  .line.has-output { cursor: pointer; }
  .line.has-output:hover { background: var(--surface0); }
  .ts { color: var(--overlay0); font-variant-numeric: tabular-nums; flex-shrink: 0; }
  .chev { color: var(--overlay0); display: inline-flex; transition: transform 0.1s; flex-shrink: 0; }
  .chev.open { transform: rotate(90deg); }
  .chev-spacer { width: 11px; flex-shrink: 0; }
  .kind {
    flex-shrink: 0; padding: 0.05rem 0.4rem; border-radius: 3px;
    font-size: 0.72rem; font-weight: 600; text-transform: uppercase;
    background: var(--surface1); color: var(--subtext0);
  }
  .kind.run { background: var(--blue); color: var(--base); }
  .kind.type { background: var(--mauve); color: var(--base); }
  .kind.connect { background: var(--green); color: var(--base); }
  .kind.read { background: var(--surface1); color: var(--subtext0); }
  .sess { color: var(--overlay1); flex-shrink: 0; max-width: 8rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cmd {
    flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    font-family: ui-monospace, monospace; font-size: 0.8rem;
  }
  .exit { flex-shrink: 0; font-size: 0.72rem; padding: 0 0.25rem; border-radius: 2px; }
  .exit.ok { color: var(--green); }
  .exit.err { color: var(--red); }
  .gate {
    flex-shrink: 0; font-size: 0.7rem; padding: 0.05rem 0.35rem; border-radius: 999px;
  }
  .gate.auto { background: var(--surface1); color: var(--overlay1); }
  .gate.approved { background: var(--green); color: var(--base); }
  .gate.denied { background: var(--red); color: var(--base); }
  .output {
    margin: 0 0.4rem 0.4rem 2rem; padding: 0.45rem 0.55rem;
    background: var(--crust); border: 1px solid var(--surface1); border-radius: 4px;
    font-family: ui-monospace, monospace; font-size: 0.78rem; color: var(--subtext1);
    white-space: pre-wrap; word-break: break-all; max-height: 30vh; overflow-y: auto;
  }
</style>
