<script lang="ts">
  // Standalone window host for a detached log tail. Opened by the backend via
  // WindowOpenLogtail with URL /?logtail=<sessionId>. Mounts LogTailModal in
  // embedded mode (fills the window, no backdrop), re-attaching to the stream
  // running on the backend. Closing the window detaches the view - the stream
  // keeps running under the main window, so on unmount we tell the main window
  // to bring the entry back (detached -> minimized).

  import { onDestroy } from "svelte";
  import LogTailModal from "./LogTailModal.svelte";
  import { api } from "./api";

  interface Props {
    sessionId: string;
  }
  let { sessionId }: Props = $props();

  let notified = false;
  function notifyRedocked() {
    if (notified) return;
    notified = true;
    api.logtailNotifyRedocked(sessionId).catch(() => {});
  }

  onDestroy(notifyRedocked);

  function onClose() {
    notifyRedocked();
  }
</script>

<div class="logtail-window">
  <LogTailModal {sessionId} {onClose} embedded />
</div>

<style>
  .logtail-window {
    height: 100vh;
    width: 100vw;
    overflow: hidden;
    background: var(--base);
  }
</style>
