<script lang="ts">
  import { confirmModal } from "./confirmModal.svelte.ts";
  import { clickOutside } from "./clickOutside";
  import { claimKeyboard } from "./paneFocus";

  let okBtn = $state<HTMLButtonElement | null>(null);
  let modalEl = $state<HTMLDivElement | null>(null);
  // The dialog often opens on a click (the unsaved prompt on a row), and
  // the second click of a double-click would land on whatever button sits
  // under the pointer - Discard included. That click carries detail 2 (the
  // OS double-click interval decides, however long it is set), so the
  // buttons that act take only a first click; keyboard activation has
  // detail 0.
  // Not enough on its own: a WebView may restart the count when the element
  // under the pointer changes, and the OS interval can be long. So a click
  // also counts only once the pointer has really moved since the dialog
  // opened (a double-click's second press comes without moving), or a key
  // was pressed. Cancel / Stay is always live.
  let armed = false;
  let origin: { x: number; y: number } | null = null;
  function onMove(e: PointerEvent) {
    if (armed) return;
    if (!origin) { origin = { x: e.clientX, y: e.clientY }; return; }
    if (Math.abs(e.clientX - origin.x) + Math.abs(e.clientY - origin.y) > 4) armed = true;
  }
  function onAnyKey() { armed = true; }
  // Touch and pen do not move a pointer before a tap: a press that comes
  // half a second after the dialog opened is not the tail of the tap or
  // double-tap that opened it.
  let shownAt = 0;
  function onDown(e: PointerEvent) {
    if (e.pointerType !== "mouse" && Date.now() - shownAt >= 500) armed = true;
  }
  function single(e: MouseEvent): boolean { return e.detail <= 1 && armed; }

  // This component stays mounted and renders nothing until asked, so the
  // claim is tied to `pending` rather than to mounting. Returning the
  // release from the effect means it runs whenever pending flips back.
  $effect(() => {
    if (!confirmModal.pending) return;
    return claimKeyboard();
  });

  // Focus moves into the dialog when it opens and back to where it was
  // when it closes.
  $effect(() => {
    if (!confirmModal.pending) return;
    armed = false;
    origin = null;
    shownAt = Date.now();
    window.addEventListener("pointermove", onMove, true);
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onAnyKey, true);
    const before = document.activeElement as HTMLElement | null;
    setTimeout(() => okBtn?.focus(), 0);
    return () => {
      window.removeEventListener("pointermove", onMove, true);
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onAnyKey, true);
      before?.focus?.();
    };
  });

  // Keyboard activation (Enter on the button: detail 0) is never a stray
  // click, and the overlay's Enter passes no event.
  function onConfirm(e?: MouseEvent) { if (!e || e.detail === 0 || single(e)) confirmModal.confirm(); }
  function onCancel()  { confirmModal.cancel(); }
  function onAlt(e: MouseEvent) { if (e.detail === 0 || single(e)) confirmModal.alt(); }

  // Tab stays inside the dialog (it is modal: nothing behind it may take
  // focus or keys).
  function trapTab(e: KeyboardEvent) {
    if (e.key !== "Tab" || !modalEl) return;
    const items = [...modalEl.querySelectorAll<HTMLElement>("button, input, [tabindex]:not([tabindex='-1'])")]
      .filter((el) => !(el as HTMLButtonElement).disabled);
    if (!items.length) return;
    const first = items[0], last = items[items.length - 1];
    const at = document.activeElement;
    if (e.shiftKey && (at === first || !modalEl.contains(at))) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && (at === last || !modalEl.contains(at))) { e.preventDefault(); first.focus(); }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter")  { e.preventDefault(); onConfirm(); }
    if (e.key === "Escape") { e.preventDefault(); onCancel(); }
  }
</script>

{#if confirmModal.pending}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="overlay" onkeydown={onKey} role="dialog" aria-modal="true" tabindex="-1"
  aria-labelledby="confirm-modal-title" aria-describedby="confirm-modal-msg">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" bind:this={modalEl} use:clickOutside={{ onOutside: onCancel, enabled: !confirmModal.pending.modal }} onkeydown={(e) => {
    // Esc answers Cancel / Stay from anywhere in the dialog. Enter stays
    // with the focused button (OK by default), so Tab to another and Enter
    // picks that one.
    if (e.key === "Escape") { e.preventDefault(); onCancel(); }
    trapTab(e);
    e.stopPropagation();
  }}>
    <h2 id="confirm-modal-title">{confirmModal.pending.title}</h2>
    <p class="msg" id="confirm-modal-msg">{confirmModal.pending.message}</p>
    {#if confirmModal.pending.checkboxLabel}
      <label class="optout">
        <input
          type="checkbox"
          checked={confirmModal.pending.checked}
          onchange={(e) => confirmModal.toggleChecked((e.target as HTMLInputElement).checked)}
        />
        <span>{confirmModal.pending.checkboxLabel}</span>
      </label>
    {/if}
    <div class="row">
      <button onclick={onCancel}>{confirmModal.pending.cancelLabel}</button>
      {#if confirmModal.pending.altLabel}
        <button class="danger" onclick={onAlt}>{confirmModal.pending.altLabel}</button>
      {/if}
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
  .optout {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    margin: 0.1rem 0 0.9rem;
    font-size: 0.85rem;
    color: var(--subtext0);
    cursor: pointer;
  }
  .optout input { cursor: pointer; }
  button.danger { background: var(--red); color: var(--on-accent); font-weight: 600; }
  button.danger:hover { background: var(--maroon); }
</style>
