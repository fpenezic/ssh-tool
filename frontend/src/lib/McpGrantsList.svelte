<script lang="ts">
  // Lists the SSH sessions currently shared with the LLM (MCP bridge) and lets
  // the user revoke each. Kept live via the mcp_grants_changed event the
  // backend emits on every share/unshare/session-close.
  import { onMount, onDestroy } from "svelte";
  import { api, type McpGrantInfo } from "./api";
  import { EventsOn } from "./wailsRuntime";

  let grants = $state<McpGrantInfo[]>([]);
  let unsub: (() => void) | null = null;

  async function reload() {
    try { grants = (await api.mcpListGrants()) ?? []; } catch { /* ignore */ }
  }

  onMount(() => {
    reload();
    unsub = EventsOn("mcp_grants_changed", (data: any) => {
      grants = (data as McpGrantInfo[]) ?? [];
    });
  });
  onDestroy(() => { if (unsub) unsub(); });

  async function unshare(sessionId: string) {
    try { await api.mcpUnshareSession(sessionId); } catch { /* ignore */ }
    await reload();
  }
</script>

{#if grants.length === 0}
  <p class="hint">No sessions are shared right now.</p>
{:else}
  <ul class="grants">
    {#each grants as g (g.session_id)}
      <li>
        <div class="meta">
          <span class="name">{g.name || g.session_id}</span>
          <span class="host">{g.hostname}</span>
          <span class="level {g.level}">{g.level === "read-run" ? "read + run" : "read only"}</span>
        </div>
        <button class="revoke" onclick={() => unshare(g.session_id)}>Stop sharing</button>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .grants { list-style: none; padding: 0; margin: 0.4rem 0 0; }
  .grants li {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.4rem 0.5rem; border-radius: 4px;
  }
  .grants li:hover { background: var(--surface0); }
  .meta { display: flex; align-items: center; gap: 0.5rem; flex: 1; min-width: 0; }
  .name { font-weight: 600; color: var(--text); }
  .host { color: var(--overlay0); font-size: 0.8rem; }
  .level {
    font-size: 0.68rem; padding: 0.05rem 0.4rem; border-radius: 999px;
    background: var(--surface1); color: var(--subtext0);
  }
  .level.read-run { background: var(--peach); color: var(--on-accent); }
  .revoke {
    background: transparent; border: 1px solid var(--surface1);
    color: var(--red); border-radius: 3px; padding: 0.15rem 0.5rem;
    cursor: pointer; font: inherit; font-size: 0.75rem;
  }
  .revoke:hover { background: var(--surface1); }
</style>
