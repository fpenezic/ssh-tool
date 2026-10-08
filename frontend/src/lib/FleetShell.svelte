<script lang="ts">
  // Shared frame for the Fleet tool dialogs: backdrop, title with the host
  // scope under it, a scrolling body and a footer row. Wide mode is for the
  // report and the diff, which want the room.
  import type { Snippet } from "svelte";
  import { IconMinus } from "./iconMap";

  interface Props {
    title: string;
    sub: string;
    wide?: boolean;
    onClose: () => void;
    children: Snippet;
    footer?: Snippet;
    // Set for long-running tools: a minimise button in the title row, and
    // while minimised nothing is drawn (the caller stays mounted).
    onMinimize?: () => void;
    minimized?: boolean;
    // Backdrop click and Escape; defaults to onClose. A running transfer
    // minimises there, while x (onClose) stops it.
    onDismiss?: () => void;
  }
  let { title, sub, wide = false, onClose, children, footer, onMinimize, minimized = false, onDismiss }: Props = $props();
  const dismiss = () => (onDismiss ?? onClose)();

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape" && !minimized) dismiss();
  }
</script>

<svelte:window onkeydown={onKey} />

{#if !minimized}
<div class="backdrop" role="presentation" onclick={dismiss}></div>
<div class="modal" class:wide role="dialog" aria-labelledby="fleet-title">
  <header>
    <div class="title-row">
      <h2 id="fleet-title">{title}</h2>
      <span class="actions">
        {#if onMinimize}
          <button class="x min" onclick={onMinimize} aria-label="Minimise"
            title="Minimise to the status bar - keeps running, click it there to come back"><IconMinus size={15} /></button>
        {/if}
        <button class="x" onclick={onClose} aria-label="Close">×</button>
      </span>
    </div>
    <div class="sub">{sub}</div>
  </header>
  <div class="body">{@render children()}</div>
  {#if footer}<footer>{@render footer()}</footer>{/if}
</div>
{/if}

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5); z-index: 9000; }
  .modal {
    position: fixed; top: 50%; left: 50%; transform: translate(-50%, -50%);
    width: min(720px, 94vw); max-height: 86vh; z-index: 9001;
    display: flex; flex-direction: column;
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 8px;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.45);
  }
  .modal.wide { width: min(1400px, 96vw); height: 86vh; }
  header { padding: 0.75rem 1rem 0.6rem; border-bottom: 1px solid var(--surface0); background: var(--mantle); border-radius: 8px 8px 0 0; }
  .title-row { display: flex; align-items: center; justify-content: space-between; }
  h2 { margin: 0; font-size: 0.98rem; font-weight: 600; }
  .sub { margin-top: 0.2rem; color: var(--subtext0); font-size: 0.76rem; }
  .x { background: transparent; border: 0; color: var(--subtext0); font-size: 1.4rem; line-height: 1; cursor: pointer; padding: 0 0.3rem; border-radius: 3px; }
  .x:hover { background: var(--surface0); color: var(--text); }
  .actions { display: inline-flex; gap: 0.2rem; }
  .x.min { display: inline-flex; align-items: center; padding: 0.15rem 0.3rem; }
  .body { padding: 0.8rem 1rem; overflow: auto; flex: 1; min-height: 0; font-size: 0.82rem; }
  /* A wide dialog is a report: its body stacks, so a scroll box inside it
     (the facts table) can take the leftover height and keep both of its
     scrollbars on screen instead of at the far end of a long table. */
  .modal.wide .body { display: flex; flex-direction: column; }
  .modal.wide .body > :global(*) { flex-shrink: 0; }
  /* Shared controls for every fleet dialog body and footer. */
  .modal :global(.fbtn) {
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1);
    border-radius: 5px; font: inherit; font-size: 0.8rem; padding: 0.3rem 0.8rem; cursor: pointer;
    display: inline-flex; align-items: center; gap: 0.35rem;
  }
  .modal :global(.fbtn:hover:not(:disabled)) { background: var(--surface1); }
  .modal :global(.fbtn:disabled) { opacity: 0.55; cursor: default; }
  .modal :global(.fbtn.primary) { background: var(--blue); color: var(--crust); border-color: var(--blue); font-weight: 600; }
  .modal :global(.fbtn.danger) { background: none; color: var(--red); border-color: color-mix(in srgb, var(--red) 45%, transparent); }
  .modal :global(.finput), .modal :global(.fselect) {
    background: var(--mantle); color: var(--text); border: 1px solid var(--surface1);
    border-radius: 4px; font: inherit; font-size: 0.8rem; padding: 0.3rem 0.5rem;
  }
  .modal :global(.fmono) { font-family: ui-monospace, monospace; }
  .modal :global(.fdim) { color: var(--subtext0); }
  .modal :global(.fwarn) { color: var(--yellow); }
  .modal :global(.ferr) { color: var(--red); }
  .modal :global(.fok) { color: var(--green); }
  .modal :global(.ftable) { width: 100%; border-collapse: collapse; font-size: 0.78rem; }
  .modal :global(.ftable th) {
    text-align: left; font-weight: 500; color: var(--subtext0); font-size: 0.72rem;
    padding: 0.35rem 0.5rem; border-bottom: 1px solid var(--surface0); position: sticky; top: 0; background: var(--base);
  }
  .modal :global(.ftable td) { padding: 0.32rem 0.5rem; border-bottom: 1px solid var(--surface0); vertical-align: top; }
  .modal :global(.ftable tr.group td) { background: var(--mantle); color: var(--subtext0); font-size: 0.72rem; }
  .modal :global(.chip) { display: inline-block; padding: 0.05rem 0.5rem; border-radius: 10px; background: var(--surface0); font-size: 0.74rem; }
  .modal :global(.chip.warn) { background: color-mix(in srgb, var(--yellow) 18%, transparent); color: var(--yellow); }
  .modal :global(.chip.bad) { background: color-mix(in srgb, var(--red) 18%, transparent); color: var(--red); }
  footer { display: flex; align-items: center; gap: 0.5rem; justify-content: flex-end; padding: 0.6rem 1rem; border-top: 1px solid var(--surface0); }
</style>
