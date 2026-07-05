<script lang="ts">
  import { presenceTakeover as pt } from "./presenceTakeover.svelte.ts";
  import { clickOutside } from "./clickOutside";
  import { IconVpn } from "./iconMap";

  function fmt(sec: number): string {
    const s = Math.max(0, Math.floor(sec));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") { e.preventDefault(); pt.cancel(); }
  }
</script>

{#if pt.open}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="overlay" onkeydown={onKey} role="dialog" aria-modal="true" tabindex="-1">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" use:clickOutside={{ onOutside: () => pt.phase === "asking" && pt.cancel() }} onkeydown={(e) => e.stopPropagation()}>
    <h2><IconVpn size={16} /> Network profile in use</h2>

    {#if pt.phase === "asking"}
      <p class="msg">
        This WireGuard profile's tunnel is live on <strong>{pt.machineName}</strong>.
        Connecting here makes both machines fight for the same identity and
        degrades both. Ask <strong>{pt.machineName}</strong> to disconnect and
        take over here?
      </p>
      <div class="row">
        <button onclick={() => pt.cancel()}>Cancel</button>
        <button onclick={() => pt.connectAnyway()}>Connect anyway</button>
        <button class="primary" onclick={() => pt.takeOver()}>Take over</button>
      </div>

    {:else if pt.phase === "requesting"}
      <p class="msg">
        Asking <strong>{pt.machineName}</strong> to disconnect...
        <br />
        <span class="hint">Usually done within the countdown; then this machine connects automatically.</span>
      </p>
      <div class="countdown">{fmt(pt.remaining)}</div>
      <div class="row">
        <button onclick={() => pt.cancel()}>Cancel</button>
      </div>

    {:else if pt.phase === "timeout"}
      <p class="msg">
        <strong>{pt.machineName}</strong> didn't respond in time - it may be
        offline. Connect anyway (both peers will flap if it's actually up), or
        cancel and try later.
      </p>
      <div class="row">
        <button onclick={() => pt.cancel()}>Cancel</button>
        <button class="primary" onclick={() => pt.connectAnyway()}>Connect anyway</button>
      </div>
    {/if}
  </div>
</div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 9500;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(480px, 92vw);
    padding: 1.1rem 1.3rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.5);
    display: flex; flex-direction: column; gap: 0.7rem;
  }
  h2 {
    margin: 0; font-size: 1rem; color: var(--text);
    display: inline-flex; align-items: center; gap: 0.4rem;
  }
  .msg { margin: 0; line-height: 1.5; color: var(--subtext1); }
  .hint { font-size: 0.82rem; color: var(--overlay1); }
  .countdown {
    align-self: center;
    font-size: 2rem;
    font-variant-numeric: tabular-nums;
    color: var(--mauve, #b675f0);
    font-weight: 600;
  }
  .row {
    display: flex; justify-content: flex-end; gap: 0.5rem;
    margin-top: 0.3rem;
  }
  button {
    padding: 0.35rem 0.9rem;
    border: 1px solid var(--surface1);
    border-radius: 5px;
    background: var(--surface0);
    color: var(--text);
    cursor: pointer;
  }
  button:hover { background: var(--surface1); }
  button.primary {
    background: var(--blue);
    color: var(--on-accent);
    border-color: var(--blue);
  }
</style>
