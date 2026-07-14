<script lang="ts">
  // Lists active browser shares and their attached guests, with kick / stop.
  // Fed by the share_changed event via a poll on open (mirrors McpGrantsList +
  // the forwards popover pattern).
  import { api, type ShareStatus } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import { toast } from "./toast.svelte";
  import { errMsg } from "./connectErrors";

  interface Props {
    onClose: () => void;
  }
  let { onClose }: Props = $props();

  let shares = $state<ShareStatus[]>([]);

  async function refresh() {
    try {
      shares = (await api.shareActive()) ?? [];
    } catch {
      shares = [];
    }
  }

  $effect(() => {
    refresh();
    const off = EventsOn("share_changed", () => refresh());
    return off;
  });

  async function stop(shareId: string) {
    try {
      await api.shareStop(shareId);
      await refresh();
    } catch (e) {
      toast.err("Stop failed: " + errMsg(e));
    }
  }

  async function kick(shareId: string, ip: string) {
    try {
      await api.shareKick(shareId, ip);
      await refresh();
    } catch (e) {
      toast.err("Kick failed: " + errMsg(e));
    }
  }

  async function stopAll() {
    for (const s of shares) await stop(s.share_id);
  }

  function ago(ts: number): string {
    const secs = Math.max(0, Math.floor(Date.now() / 1000 - ts));
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m`;
    return `${Math.floor(secs / 3600)}h`;
  }
</script>

<div class="panel">
  <div class="head">
    <strong>Browser shares</strong>
    <div class="head-actions">
      {#if shares.length > 0}
        <button class="stop-all" onclick={stopAll}>Stop all</button>
      {/if}
      <button class="close" onclick={onClose} title="Close">✕</button>
    </div>
  </div>

  {#if shares.length === 0}
    <div class="empty">No active shares.</div>
  {:else}
    {#each shares as s (s.share_id)}
      <div class="share">
        <div class="share-head">
          <span class="bind">{s.bind}</span>
          <span class="level" class:control={s.level === "control"}>{s.level}</span>
          <button class="stop" onclick={() => stop(s.share_id)}>Stop</button>
        </div>
        {#if s.guests.length === 0}
          <div class="no-guest">Waiting for a guest…</div>
        {:else}
          {#each s.guests as g (g.remote_ip)}
            <div class="guest">
              <span class="ip">{g.remote_ip}</span>
              <span class="joined">joined {ago(g.joined_at)} ago</span>
              <button class="kick" onclick={() => kick(s.share_id, g.remote_ip)}>Kick</button>
            </div>
          {/each}
        {/if}
      </div>
    {/each}
  {/if}
</div>

<style>
  .panel {
    background: var(--base, #1e1e2e);
    color: var(--text, #cdd6f4);
    border: 1px solid var(--surface1, #45475a);
    border-radius: 8px;
    min-width: 20rem;
    max-width: 26rem;
    padding: 0.6rem 0.8rem;
    box-shadow: 0 8px 30px rgba(0, 0, 0, 0.45);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  .head-actions {
    display: flex;
    gap: 0.4rem;
  }
  button {
    background: var(--surface0, #313244);
    border: 1px solid var(--surface1, #45475a);
    color: var(--text, #cdd6f4);
    border-radius: 5px;
    padding: 0.2rem 0.55rem;
    cursor: pointer;
    font-size: 0.78rem;
  }
  .close {
    border: none;
    background: transparent;
  }
  .empty,
  .no-guest {
    color: var(--overlay1, #7f849c);
    font-size: 0.82rem;
    padding: 0.4rem 0;
  }
  .share {
    border-top: 1px solid var(--surface0, #313244);
    padding: 0.5rem 0;
  }
  .share-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .bind {
    font-family: monospace;
    font-size: 0.82rem;
    flex: 1;
  }
  .level {
    font-size: 0.72rem;
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    background: var(--surface0, #313244);
    color: var(--subtext0, #a6adc8);
  }
  .level.control {
    background: var(--red, #f38ba8);
    color: var(--base, #1e1e2e);
    font-weight: 600;
  }
  .guest {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0 0.25rem 0.6rem;
    font-size: 0.82rem;
  }
  .ip {
    font-family: monospace;
    flex: 1;
  }
  .joined {
    color: var(--overlay1, #7f849c);
    font-size: 0.75rem;
  }
</style>
