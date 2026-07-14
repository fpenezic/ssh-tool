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

  // Suppress the guest xterm's own answers to queries embedded in the replayed
  // scrollback (DA1/DSR/OSC). Cleared shortly after the snapshot is written.
  // The backend ready-gate is the real protection; this stops local echo noise.
  let replaying = true;
  let userInput = false;

  const BASE_FONT = 14;
  let fontSize = $state(BASE_FONT);
  let zoom = $state(1);

  const sink: SessionSink = {
    write(data: Uint8Array) {
      term?.write(data);
    },
    clear() {
      term?.clear();
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

  // Fit by FONT SIZE, not CSS transform. A scaled canvas is blurry (it
  // resamples the rasterised glyphs); changing xterm's fontSize re-rasterises
  // crisply. We keep the host's exact cols/rows and pick the largest font that
  // still fits the browser box, times the user's zoom.
  function recomputeScale() {
    if (!host || !term) return;
    const wrap = host.parentElement;
    if (!wrap) return;
    // In a split pane the terminal mounts before flex has assigned the box its
    // size, so clientWidth/Height can be 0 on the first pass. Fitting to 0
    // would collapse the font to its minimum (a blank/tiny terminal - the
    // "split doesn't work" symptom). Skip until there's real space; the
    // ResizeObserver fires recomputeScale again once layout settles.
    if (wrap.clientWidth < 2 || wrap.clientHeight < 2) return;
    const core = (term as any)._core?._renderService?.dimensions?.css?.cell;
    const cellW = core?.width || BASE_FONT * 0.6;
    const cellH = core?.height || BASE_FONT * 1.2;
    const natW = cellW * curCols;
    const natH = cellH * curRows;
    const fit = Math.min(wrap.clientWidth / natW, wrap.clientHeight / natH);
    const targetFont = Math.max(6, Math.min(Math.round(fontSize * fit * zoom), 40));
    if (targetFont !== fontSize) {
      fontSize = targetFont;
      term.options.fontSize = targetFont;
    }
  }

  function zoomIn() {
    zoom = Math.min(zoom * 1.15, 3);
    recomputeScale();
  }
  function zoomOut() {
    zoom = Math.max(zoom / 1.15, 0.4);
    recomputeScale();
  }
  function zoomReset() {
    zoom = 1;
    recomputeScale();
  }

  onMount(() => {
    if (!host) return;
    term = new Terminal({
      cols: curCols,
      rows: curRows,
      fontFamily: "'JetBrains Mono', 'Cascadia Mono', Menlo, monospace",
      fontSize: fontSize,
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
      // Without focus the xterm never receives keystrokes, so onData never
      // fires and nothing reaches the host. Focus on mount and on click.
      term.focus();
      host.addEventListener("click", () => term?.focus());
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
  <div class="term-scale" bind:this={host}></div>
  <div class="zoom-bar">
    <button title="Zoom out" onclick={zoomOut}>-</button>
    <button title="Reset zoom" onclick={zoomReset}>{Math.round(zoom * 100)}%</button>
    <button title="Zoom in" onclick={zoomIn}>+</button>
  </div>
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
  .zoom-bar {
    position: absolute;
    right: 8px;
    bottom: 8px;
    display: flex;
    gap: 2px;
    background: rgba(17, 17, 27, 0.85);
    border: 1px solid #313244;
    border-radius: 6px;
    padding: 2px;
    z-index: 5;
  }
  .zoom-bar button {
    background: transparent;
    border: none;
    color: #cdd6f4;
    cursor: pointer;
    font-size: 0.8rem;
    min-width: 1.9rem;
    padding: 0.15rem 0.3rem;
    border-radius: 4px;
  }
  .zoom-bar button:hover {
    background: #313244;
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
