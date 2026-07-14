<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { Terminal } from "@xterm/xterm";
  import { CanvasAddon } from "@xterm/addon-canvas";
  import type { GuestClient, SessionSink, Level } from "./ws";

  interface Props {
    slot: string;
    cols: number;
    rows: number;
    level: Level;
    client: GuestClient;
  }
  let { slot, cols, rows, level, client }: Props = $props();

  let host = $state<HTMLDivElement>();
  let term: Terminal | null = null;
  let canvas: CanvasAddon | null = null;
  let disconnected = $state(false);
  let disconnectReason = $state("");

  // The guest never resizes the host PTY. It builds xterm at the host's exact
  // cols/rows and CSS-scales the wrapper to fit the viewport (letterbox). So no
  // fit addon and no ResizeObserver here - a resize frame from the host is the
  // only thing that changes the grid.
  // Initial grid from the manifest; thereafter driven only by host size frames
  // (the guest never resizes the host PTY). Reading the props once at mount is
  // intentional - untrack keeps svelte-check from flagging the initial capture.
  let curCols = $state(untrack(() => cols));
  let curRows = $state(untrack(() => rows));
  let scale = $state(1);

  // Suppress the guest xterm's own answers to queries embedded in the replayed
  // scrollback (DA1/DSR/OSC). Cleared shortly after the snapshot is written.
  // The backend ready-gate is the real protection; this stops local echo noise.
  let replaying = true;
  let userInput = false;

  const sink: SessionSink = {
    write(data: Uint8Array) {
      term?.write(data);
    },
    resize(c: number, r: number) {
      curCols = c;
      curRows = r;
      term?.resize(c, r);
      recomputeScale();
    },
    state(state: string, reason: string) {
      disconnected = state === "disconnected" || state === "error";
      disconnectReason = reason;
    },
  };

  function recomputeScale() {
    if (!host || !term) return;
    const wrap = host.parentElement;
    if (!wrap) return;
    // The xterm renders at its natural cell size; scale the whole element so
    // the fixed grid fits the available box, preserving aspect.
    const natW = host.scrollWidth || host.clientWidth || 1;
    const natH = host.scrollHeight || host.clientHeight || 1;
    const availW = wrap.clientWidth;
    const availH = wrap.clientHeight;
    scale = Math.min(availW / natW, availH / natH, 1);
  }

  onMount(() => {
    if (!host) return;
    term = new Terminal({
      cols: curCols,
      rows: curRows,
      fontFamily: "'JetBrains Mono', 'Cascadia Mono', Menlo, monospace",
      fontSize: 14,
      cursorBlink: false,
      // Read-only guests can't type; xterm's stdin is disabled entirely.
      disableStdin: level !== "control",
      scrollback: 5000,
      allowProposedApi: true,
      theme: { background: "#1e1e2e", foreground: "#cdd6f4", cursorAccent: "#1e1e2e" },
    });
    canvas = new CanvasAddon();
    term.loadAddon(canvas);
    term.open(host);

    if (level === "control") {
      term.onKey(() => {
        userInput = true;
      });
      term.onData((raw) => {
        const fromUser = userInput;
        userInput = false;
        // While replaying the snapshot, drop the terminal's OWN answers to
        // embedded queries - they aren't real keystrokes and must not reach
        // the host PTY.
        if (replaying && !fromUser) return;
        client.sendInput(slot, new TextEncoder().encode(raw));
      });
    }

    client.registerSink(slot, sink);
    // The client sends `ready` when it applies the snapshot; registering after
    // (or before) is fine - registerSink is idempotent on the host side.
    client.ready(slot);

    // Clear the replay guard after the snapshot has settled.
    setTimeout(() => {
      replaying = false;
    }, 300);

    recomputeScale();
    const ro = new ResizeObserver(() => recomputeScale());
    if (host.parentElement) ro.observe(host.parentElement);
    resizeObs = ro;
  });

  let resizeObs: ResizeObserver | null = null;

  onDestroy(() => {
    resizeObs?.disconnect();
    canvas?.dispose();
    term?.dispose();
  });
</script>

<div class="term-box">
  <div
    class="term-scale"
    bind:this={host}
    style="transform: scale({scale}); transform-origin: top left;"
  ></div>
  {#if disconnected}
    <div class="term-overlay">
      <div class="msg">Session disconnected</div>
      {#if disconnectReason}<div class="reason">{disconnectReason}</div>{/if}
    </div>
  {/if}
</div>

<style>
  .term-box {
    position: relative;
    width: 100%;
    height: 100%;
    overflow: hidden;
    background: #1e1e2e;
    display: flex;
    align-items: flex-start;
    justify-content: flex-start;
  }
  .term-scale {
    display: inline-block;
  }
  .term-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    background: rgba(30, 30, 46, 0.82);
    color: #cdd6f4;
    gap: 0.4rem;
  }
  .term-overlay .msg {
    font-size: 1.1rem;
    font-weight: 600;
  }
  .term-overlay .reason {
    font-size: 0.85rem;
    color: #a6adc8;
  }
</style>
