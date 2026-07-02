<script lang="ts">
  import { confirmModal } from "./confirmModal.svelte.ts";
  import { clickOutside } from "./clickOutside";

  let okBtn = $state<HTMLButtonElement | null>(null);

  $effect(() => {
    if (confirmModal.pending) {
      setTimeout(() => okBtn?.focus(), 0);
    }
  });

  function onConfirm() { confirmModal.confirm(); }
  function onCancel()  { confirmModal.cancel(); }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter")  { e.preventDefault(); onConfirm(); }
    if (e.key === "Escape") { e.preventDefault(); onCancel(); }
  }
</script>

{#if confirmModal.pending}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="overlay" onkeydown={onKey} role="dialog" aria-modal="true" tabindex="-1">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" use:clickOutside={{ onOutside: onCancel }} onkeydown={(e) => e.stopPropagation()}>
    <h2>{confirmModal.pending.title}</h2>
    <p class="msg">{confirmModal.pending.message}</p>
    <div class="row">
      <button onclick={onCancel}>{confirmModal.pending.cancelLabel}</button>
      <button
        class={confirmModal.pending.danger ? "danger" : "primary"}
        bind:this={okBtn}
        onclick={onConfirm}
      >
        {confirmModal.pending.okLabel}
      </button>
    </div>
  </div>
</div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    /* Above UpdateModal (z 9001) so the "restart and install"
       confirm sits on top of the update sheet that opened it. */
    z-index: 9500;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(460px, 92vw);
    padding: 1.1rem 1.3rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.5);
    display: flex; flex-direction: column; gap: 0.7rem;
  }
  h2 { margin: 0; font-size: 1rem; color: var(--text); }
  .msg { margin: 0; font-size: 0.88rem; color: var(--subtext1); line-height: 1.5; white-space: pre-line; }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.3rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.9rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--sapphire); }
  button.danger { background: var(--red); color: var(--on-accent); font-weight: 600; }
  button.danger:hover { background: var(--maroon); }
</style>
