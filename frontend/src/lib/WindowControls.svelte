<script lang="ts">
  // Minimise / maximise / close for the frameless main window (compact
  // title bar on Windows and Linux). Fixed to the top-right corner above
  // every overlay, so the window stays closable behind the vault gate or
  // a modal. Close goes through the same WindowClosing hook as the native
  // button (quit confirm, close-to-tray).
  import { Window } from "@wailsio/runtime";
  import { onMount } from "svelte";
  import { IconMinus, IconMaximize, IconRestore, IconX } from "./iconMap";

  let maximised = $state(false);

  async function refresh() {
    try { maximised = await Window.IsMaximised(); } catch { /* ignore */ }
  }

  onMount(() => {
    void refresh();
    const onResize = () => void refresh();
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  });
</script>

<div class="wctl" role="group" aria-label="Window">
  <button onclick={() => Window.Minimise()} title="Minimise" aria-label="Minimise">
    <IconMinus size={14} />
  </button>
  <button
    onclick={async () => { await Window.ToggleMaximise(); void refresh(); }}
    title={maximised ? "Restore" : "Maximise"}
    aria-label={maximised ? "Restore" : "Maximise"}
  >
    {#if maximised}<IconRestore size={12} />{:else}<IconMaximize size={12} />{/if}
  </button>
  <button class="close" onclick={() => Window.Close()} title="Close" aria-label="Close">
    <IconX size={15} />
  </button>
</div>

<style>
  .wctl {
    position: fixed;
    top: 0;
    right: 0;
    z-index: 10001;
    display: flex;
    height: var(--titlebar-h, 32px);
    --wails-draggable: no-drag;
  }
  button {
    width: 44px;
    height: 100%;
    display: grid;
    place-items: center;
    background: transparent;
    border: 0;
    color: var(--overlay1);
    cursor: default;
  }
  button:hover { background: var(--surface0); color: var(--text); }
  button.close:hover { background: #c42b1c; color: #fff; }
</style>
