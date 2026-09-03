<script lang="ts">
  // Modal framing for SftpFileView: a backdrop dialog sized for reading a
  // long file. All of the actual behaviour - load, highlight, edit, save -
  // lives in the shared view; this file is only the overlay, the sizing,
  // and the backdrop-click rule.
  //
  // SftpPane mounts the same view inline under its listing (the default on
  // double-click), which is the mode that lets the user read a file while
  // typing in the terminal pane beside it. This one is for when that split
  // is too tight: it gets the full window.
  import SftpFileView from "./SftpFileView.svelte";

  interface Props {
    sessionId: string;
    path: string;
    name: string;
    onClose: () => void;
  }
  let { sessionId, path, name, onClose }: Props = $props();

  // The view owns the unsaved-changes confirm, so the backdrop routes
  // through it rather than calling onClose directly.
  let view = $state<ReturnType<typeof SftpFileView> | null>(null);

  // True only when the press that started this click landed on the backdrop
  // itself. A text selection that starts inside the modal and ends outside
  // it still fires click on the overlay (the shared ancestor), which was
  // closing the dialog and throwing away the drag - and any unsaved edit.
  let pressedOnOverlay = false;

  function onOverlayMouseDown(e: MouseEvent) {
    pressedOnOverlay = e.target === e.currentTarget;
  }

  function onOverlayClick(e: MouseEvent) {
    const fromBackdrop = pressedOnOverlay && e.target === e.currentTarget;
    pressedOnOverlay = false;
    if (fromBackdrop) view?.requestClose();
  }
</script>

<div
  class="overlay"
  role="presentation"
  onmousedown={onOverlayMouseDown}
  onclick={onOverlayClick}
>
  <div class="shell">
    <SftpFileView
      bind:this={view}
      {sessionId}
      {path}
      {name}
      {onClose}
      chrome="modal"
    />
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(17, 17, 27, 0.6);
    z-index: 1000;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  /* The dialog frame. The view inside is a flex column that fills it. */
  .shell {
    width: min(1000px, 94vw);
    height: 86vh;
    display: flex;
    flex-direction: column;
    border: 1px solid var(--surface0);
    border-radius: 5px;
    box-shadow: 0 8px 30px rgba(0,0,0,0.5);
    overflow: hidden;
  }
  .shell > :global(.fileview) { flex: 1; min-height: 0; }
</style>
