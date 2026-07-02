<script lang="ts">
  import { promptModal } from "./promptModal.svelte.ts";
  import { clickOutside } from "./clickOutside";

  let inputValue = $state("");
  let inputEl = $state<HTMLInputElement | null>(null);

  $effect(() => {
    if (promptModal.pending) {
      inputValue = promptModal.pending.defaultValue;
      // focus after the DOM updates
      setTimeout(() => inputEl?.focus(), 0);
    }
  });

  function onConfirm() { promptModal.confirm(inputValue); }
  function onCancel()  { promptModal.cancel(); }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter")  { e.preventDefault(); onConfirm(); }
    if (e.key === "Escape") { e.preventDefault(); onCancel(); }
  }
</script>

{#if promptModal.pending}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="overlay" onkeydown={onKey} role="dialog" aria-modal="true" tabindex="-1">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" use:clickOutside={{ onOutside: onCancel }} onkeydown={(e) => e.stopPropagation()}>
    <p class="msg">{promptModal.pending.message}</p>
    <input
      bind:this={inputEl}
      bind:value={inputValue}
      class="inp"
      type={promptModal.pending.password ? "password" : "text"}
      autocomplete={promptModal.pending.password ? "current-password" : "off"}
      onkeydown={onKey}
      spellcheck="false"
    />
    <div class="row">
      <button onclick={onCancel}>Cancel</button>
      <button class="primary" onclick={onConfirm}>OK</button>
    </div>
  </div>
</div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    /* Above UpdateModal (z 9001) so prompts triggered from any
       9000-tier sheet still land in front. */
    z-index: 9500;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(380px, 90vw);
    padding: 1.1rem 1.3rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.5);
    display: flex; flex-direction: column; gap: 0.7rem;
  }
  .msg { margin: 0; font-size: 0.9rem; color: var(--subtext0); }
  .inp {
    width: 100%; box-sizing: border-box;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px;
    padding: 0.4rem 0.6rem; font: inherit; font-size: 0.9rem;
    outline: none;
  }
  .inp:focus { border-color: var(--blue); }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.35rem 0.8rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--sapphire); }
</style>
