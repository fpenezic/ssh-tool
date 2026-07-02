<script lang="ts">
  // Renders the bottom-right stack of active toasts. Click dismisses
  // a toast early; otherwise the store TTL handles it.
  import { toast } from "./toast.svelte";
</script>

<div class="host" role="status" aria-live="polite">
  {#each toast.toasts as t (t.id)}
    <button
      class="toast {t.kind}"
      class:actionable={!!t.onClick}
      onclick={() => { t.onClick?.(); toast.dismiss(t.id); }}
      title={t.onClick ? "Click to open" : "Click to dismiss"}
    >
      {#if t.kind === "ok"}<span class="sigil">✓</span>{/if}
      {#if t.kind === "err"}<span class="sigil">!</span>{/if}
      {#if t.kind === "info"}<span class="sigil">i</span>{/if}
      <span class="msg">{t.msg}</span>
    </button>
  {/each}
</div>

<style>
  .host {
    position: fixed;
    bottom: 32px;
    right: 16px;
    z-index: 400;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    pointer-events: none;
  }
  .toast {
    pointer-events: auto;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-left-width: 3px;
    border-radius: 4px;
    padding: 0.5rem 0.8rem;
    font: inherit;
    font-size: 0.8rem;
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45);
    cursor: pointer;
    min-width: 200px;
    max-width: 380px;
    animation: slide-in 0.18s ease-out;
  }
  .toast.ok { border-left-color: var(--green); }
  .toast.err { border-left-color: var(--red); }
  .toast.info { border-left-color: var(--blue); }
  .toast.actionable:hover { border-color: var(--blue); }
  .toast.actionable .msg { text-decoration: underline dotted; text-underline-offset: 3px; }
  .sigil {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    font-weight: 700;
    font-size: 0.75rem;
    flex-shrink: 0;
  }
  .toast.ok .sigil { background: var(--green); color: var(--on-accent); }
  .toast.err .sigil { background: var(--red); color: var(--on-accent); }
  .toast.info .sigil { background: var(--blue); color: var(--on-accent); }
  .msg { white-space: pre-wrap; word-break: break-word; }
  @keyframes slide-in {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }
</style>
