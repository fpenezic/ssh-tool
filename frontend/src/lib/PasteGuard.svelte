<script lang="ts">
  // Modal shown when the user pastes multi-line content into a terminal.
  // Renders a preview of the text + counts, lets them confirm, cancel, or
  // suppress the warning for the rest of the session.

  import { onMount } from "svelte";
  import { clickOutside } from "./clickOutside";

  interface Props {
    text: string;
    onConfirm: (rememberForSession: boolean) => void;
    onCancel: () => void;
  }
  let { text, onConfirm, onCancel }: Props = $props();

  let rememberForSession = $state(false);
  let confirmBtn: HTMLButtonElement | undefined = $state();

  const lineCount = $derived(text.split("\n").length);
  const byteCount = $derived(new Blob([text]).size);
  const preview = $derived(text.length > 400 ? text.slice(0, 400) + "…" : text);
  const hasShellMetaChars = $derived(/[;&|`$()]/.test(text));

  function confirm() { onConfirm(rememberForSession); }

  // Note: when the paste guard sits on top of a terminal, the actual
  // Enter/Esc handling happens in Terminal.svelte's customKeyHandler
  // (xterm intercepts keys before any window-level listener sees
  // them). This component's onKey is a fallback for non-terminal
  // hosts; it also handles the case where the user tabs into the
  // confirm button and presses Enter natively.
  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    } else if (e.key === "Enter" || e.key === "y") {
      e.preventDefault();
      confirm();
    }
  }

  onMount(() => {
    // Focus the confirm button so Enter activates it natively. xterm
    // is the only contender; pulling focus here strips it from the
    // hidden textarea.
    setTimeout(() => confirmBtn?.focus(), 0);
  });
</script>

<svelte:window onkeydown={onKey} />

<div class="overlay" role="dialog" aria-modal="true" onkeydown={(e) => { if (e.key === "Escape") onCancel(); }} tabindex="-1">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" use:clickOutside={{ onOutside: onCancel }} onkeydown={(e) => e.stopPropagation()}>
    <header>
      <h1>⚠ Paste {lineCount} lines?</h1>
    </header>
    <p class="hint">
      About to send <strong>{lineCount} lines</strong> ({byteCount} bytes) to
      the terminal. Multi-line paste runs every newline-separated command
      immediately on the remote.
      {#if hasShellMetaChars}
        <br/>
        <span class="warn">Contains shell metacharacters (; &amp; | $ ` ()). Review carefully.</span>
      {/if}
    </p>
    <pre class="preview">{preview}</pre>
    <label class="checkbox">
      <input type="checkbox" bind:checked={rememberForSession} />
      <span>Don't ask again for this session</span>
    </label>
    <div class="row">
      <button onclick={onCancel}>Cancel <kbd>Esc</kbd></button>
      <button bind:this={confirmBtn} class="primary" onclick={confirm}>Paste <kbd>Enter</kbd></button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.65);
    display: flex; align-items: center; justify-content: center; z-index: 200;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(640px, 92vw); max-height: 80vh; overflow: auto;
    padding: 1.1rem 1.3rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.5);
  }
  header { margin-bottom: 0.6rem; }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; color: var(--yellow); }
  .hint { font-size: 0.85rem; color: var(--subtext0); line-height: 1.45; }
  .warn { color: var(--red); }
  .preview {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.6rem 0.7rem;
    font-family: ui-monospace, Menlo, monospace;
    font-size: 0.78rem;
    max-height: 280px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
    margin: 0.6rem 0;
  }
  .checkbox {
    display: flex; align-items: center; gap: 0.45rem;
    font-size: 0.8rem; color: var(--subtext0);
    margin: 0.5rem 0 0.8rem;
    cursor: pointer;
  }
  .checkbox input { margin: 0; }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.85rem; border-radius: 3px;
    cursor: pointer; font: inherit;
    display: flex; align-items: center; gap: 0.4rem;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--yellow); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: #f5c977; }
  kbd {
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 2px;
    padding: 0.05rem 0.3rem;
    font-size: 0.7rem;
    color: var(--subtext0);
    font-family: ui-monospace, monospace;
  }
</style>
