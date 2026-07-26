<script lang="ts">
  // Standalone window host for a detached tcpdump capture. Opened by the
  // backend via WindowOpenTcpdump with URL /?tcpdump=<sessionId>. It mounts
  // TcpdumpModal in embedded mode (fills the window, no backdrop) which
  // re-attaches to the capture already running on the backend for this
  // session. Closing the window just detaches the view - the capture keeps
  // running under the main window, so on unmount we tell the main window to
  // bring the entry back (detached -> minimized) via a backend event.

  import { onDestroy } from "svelte";
  import TcpdumpModal from "./TcpdumpModal.svelte";
  import { api } from "./api";

  interface Props {
    sessionId: string;
  }
  let { sessionId }: Props = $props();

  let notified = false;
  function notifyRedocked() {
    if (notified) return;
    notified = true;
    // Best-effort: if this fails the capture still lives, the main window just
    // keeps the entry marked detached until the session ends.
    api.tcpdumpNotifyRedocked(sessionId).catch(() => {});
  }

  // Fires on OS window close (X) as well as programmatic unmount.
  onDestroy(notifyRedocked);

  // onClose fires when the user explicitly stops+closes the capture from
  // inside this window. TcpdumpNotifyRedocked also closes the OS window.
  function onClose() {
    notifyRedocked();
  }
</script>

<div class="tcpdump-window">
  <TcpdumpModal {sessionId} {onClose} embedded />
</div>

<style>
  .tcpdump-window {
    height: 100vh;
    width: 100vw;
    overflow: hidden;
    background: var(--base);
  }
</style>
