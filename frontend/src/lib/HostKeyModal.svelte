<script lang="ts">
  import { IconKeyRound } from "./iconMap";

  interface Props {
    challengeId: string;
    hostname: string;
    port: number;
    keyType: string;
    fingerprint: string;
    status: "unknown" | "changed";
    oldFingerprint?: string;
    keyB64: string;
    onRespond: (accept: boolean, remember: boolean) => void;
    // How many more challenges are queued behind this one. 0 means this
    // is the last; >0 surfaces a "(N more queued)" hint so the user
    // knows what's coming after they respond.
    queueLength?: number;
  }
  let { challengeId, hostname, port, keyType, fingerprint, status, oldFingerprint, keyB64, onRespond, queueLength = 0 }: Props = $props();
</script>

<div class="overlay" role="dialog" aria-modal="true">
  <div class="modal">
    {#if status === "changed"}
      <header class="warn">
        <span class="icon">⚠️</span>
        <h1>Host key changed - possible attack!</h1>
      </header>
      <p class="warn-text">
        The SSH host key for <strong>{hostname}:{port}</strong> has changed since your last connection.
        This could indicate a man-in-the-middle attack. Verify with the server administrator before accepting.
      </p>
      <dl>
        <dt>Old fingerprint</dt><dd class="fp old">{oldFingerprint}</dd>
        <dt>New fingerprint</dt><dd class="fp new">{fingerprint}</dd>
        <dt>Key type</dt><dd>{keyType}</dd>
      </dl>
    {:else}
      <header>
        <span class="icon"><IconKeyRound size={18} /></span>
        <h1>Unknown host key</h1>
      </header>
      <p>
        The authenticity of <strong>{hostname}:{port}</strong> cannot be established.
      </p>
      <dl>
        <dt>Key type</dt><dd>{keyType}</dd>
        <dt>Fingerprint</dt><dd class="fp">{fingerprint}</dd>
      </dl>
      <p class="hint">Verify this fingerprint with the server administrator before accepting.</p>
    {/if}

    {#if queueLength > 0}
      <p class="queue-hint">
        ⓘ <strong>{queueLength}</strong> more host{queueLength === 1 ? "" : "s"} waiting after this one.
      </p>
    {/if}

    <div class="row">
      <button onclick={() => onRespond(false, false)}>Cancel</button>
      <button onclick={() => onRespond(true, false)}>
        {status === "changed" ? "Accept once (risky)" : "Trust once"}
      </button>
      <button class="primary" onclick={() => onRespond(true, true)}>
        {status === "changed" ? "Update & trust" : "Trust & remember"}
      </button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(520px, 92vw); padding: 1.25rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.6);
  }
  header {
    display: flex; align-items: center; gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  header.warn { color: var(--red); }
  .icon { font-size: 1.3rem; }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  p { margin: 0.5rem 0; font-size: 0.875rem; line-height: 1.5; }
  .warn-text { color: var(--red); font-size: 0.875rem; }
  .hint { color: var(--overlay0); font-size: 0.8rem; }
  .queue-hint {
    background: var(--crust);
    border-left: 3px solid var(--blue);
    color: var(--blue);
    font-size: 0.8rem;
    padding: 0.4rem 0.6rem;
    margin-top: 0.8rem;
    border-radius: 3px;
  }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 0.3rem 1rem; margin: 0.75rem 0; font-size: 0.82rem; }
  dt { color: var(--overlay0); }
  dd { margin: 0; }
  .fp { font-family: monospace; font-size: 0.78rem; word-break: break-all; }
  .fp.old { color: var(--red); }
  .fp.new { color: var(--green); }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 1rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.85rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--lavender); }
</style>
