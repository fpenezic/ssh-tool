<script lang="ts">
  // A browser guest is waiting to join a shared session. The host allows or
  // denies AFTER comparing the fingerprint words with the guest out-of-band.
  // Deny is the default focus - joining a terminal (especially a control share)
  // is not something to wave through.
  interface Props {
    remoteIp: string;
    fingerprint: string; // the words to compare
    level: string; // "read" | "control"
    queueLength: number;
    onRespond: (decision: "allow" | "deny") => void | Promise<void>;
  }
  let { remoteIp, fingerprint, level, queueLength, onRespond }: Props = $props();

  const isControl = $derived(level === "control");

  let denyBtn = $state<HTMLButtonElement>();
  $effect(() => {
    denyBtn?.focus();
  });
</script>

<div class="overlay" role="presentation">
  <div class="modal" role="dialog" aria-modal="true" aria-label="Guest join request">
    <h2>Someone wants to join your session</h2>

    <div class="row">
      <span class="label">From</span>
      <span class="ip">{remoteIp}</span>
    </div>

    <div class="row">
      <span class="label">Access</span>
      <span class="level" class:control={isControl}>
        {isControl ? "Full control - they can type in your terminals" : "Read-only"}
      </span>
    </div>

    <div class="fp-block">
      <div class="fp-title">Check this matches what the guest sees:</div>
      <div class="fp-words">{fingerprint}</div>
      <div class="fp-hint">
        Ask them (phone / chat) to read out their fingerprint. If it doesn't
        match, deny - someone may be intercepting the connection.
      </div>
    </div>

    {#if queueLength > 0}
      <div class="more">{queueLength} more request{queueLength === 1 ? "" : "s"} waiting.</div>
    {/if}

    <div class="actions">
      <button bind:this={denyBtn} class="deny" onclick={() => onRespond("deny")}>Deny</button>
      <button class="allow" class:danger={isControl} onclick={() => onRespond("allow")}>
        {isControl ? "Allow control" : "Allow"}
      </button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 340;
  }
  .modal {
    background: var(--base, #1e1e2e);
    color: var(--text, #cdd6f4);
    border: 1px solid var(--surface1, #45475a);
    border-radius: 10px;
    padding: 1.3rem 1.5rem;
    width: min(30rem, 92vw);
    box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  }
  h2 {
    margin: 0 0 0.9rem;
    font-size: 1.1rem;
  }
  .row {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    margin-bottom: 0.5rem;
  }
  .label {
    width: 4rem;
    color: var(--subtext0, #a6adc8);
    font-size: 0.8rem;
  }
  .ip {
    font-family: monospace;
  }
  .level {
    color: var(--subtext1, #bac2de);
  }
  .level.control {
    color: var(--red, #f38ba8);
    font-weight: 600;
  }
  .fp-block {
    margin: 0.9rem 0;
    padding: 0.8rem;
    background: var(--mantle, #181825);
    border-radius: 8px;
    border: 1px solid var(--surface0, #313244);
  }
  .fp-title {
    font-size: 0.8rem;
    color: var(--subtext0, #a6adc8);
    margin-bottom: 0.4rem;
  }
  .fp-words {
    font-family: monospace;
    font-size: 1.35rem;
    font-weight: 700;
    color: var(--blue, #89b4fa);
    letter-spacing: 0.02em;
  }
  .fp-hint {
    margin-top: 0.5rem;
    font-size: 0.76rem;
    color: var(--overlay1, #7f849c);
  }
  .more {
    font-size: 0.78rem;
    color: var(--subtext0, #a6adc8);
    margin-bottom: 0.6rem;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.6rem;
    margin-top: 0.6rem;
  }
  button {
    padding: 0.45rem 1.1rem;
    border-radius: 6px;
    border: 1px solid var(--surface1, #45475a);
    background: var(--surface0, #313244);
    color: var(--text, #cdd6f4);
    cursor: pointer;
    font-size: 0.9rem;
  }
  button.deny {
    border-color: var(--overlay0, #6c7086);
  }
  button.allow {
    background: var(--blue, #89b4fa);
    color: var(--base, #1e1e2e);
    border-color: var(--blue, #89b4fa);
    font-weight: 600;
  }
  button.allow.danger {
    background: var(--red, #f38ba8);
    border-color: var(--red, #f38ba8);
  }
</style>
