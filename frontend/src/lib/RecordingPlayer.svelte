<script lang="ts">
  // Asciicast v2 player on a read-only xterm instance. No
  // asciinema-player dependency (GPL; and we already ship xterm).
  //
  // Engine: a 33 ms ticker advances media time by wall-clock delta x
  // speed and flushes every event whose timestamp passed. Seeking
  // resets the terminal and replays everything up to the target in
  // one batched write - xterm chews through megabytes in a frame, so
  // scrubbing stays responsive without keyframe bookkeeping.
  import { onMount, onDestroy } from "svelte";
  import { Terminal } from "@xterm/xterm";
  import { WebglAddon } from "@xterm/addon-webgl";
  import "@xterm/xterm/css/xterm.css";
  import { api } from "./api";
  import { terminalPrefs } from "./terminalPrefs.svelte";
  import { copyText } from "./clipboard";
  import type { TerminalTheme } from "./themes";

  interface Props {
    path: string;
  }
  let { path }: Props = $props();

  type CastEvent = { t: number; code: string; data: string };

  let host: HTMLDivElement;
  let term: Terminal | null = null;
  let events: CastEvent[] = [];
  let headerCols = 80;
  let headerRows = 24;
  let title = $state("");
  let loadError = $state<string | null>(null);
  let loaded = $state(false);

  let duration = $state(0);
  let time = $state(0);
  let playing = $state(false);
  let speed = $state(1);
  let skipIdle = $state(true);
  const IDLE_CAP = 2; // seconds of silence kept when skipIdle is on

  // Playback cursor: index of the next event to apply.
  let idx = 0;
  let ticker: ReturnType<typeof setInterval> | null = null;
  let lastTick = 0;

  function themeToXterm(t: TerminalTheme) {
    const { id, name, isLight, ...colors } = t;
    // xterm paints the glyph under a block cursor in cursorAccent,
    // which defaults to black. Dark themes get away with it (their
    // cursor is light), but on light themes the cursor block is dark
    // too, so the character under a blinking cursor vanished. The
    // theme background is the correct inverse for every scheme.
    return { ...colors, cursorAccent: t.background };
  }

  function fmt(s: number): string {
    const m = Math.floor(s / 60);
    const ss = Math.floor(s % 60);
    return `${m}:${ss.toString().padStart(2, "0")}`;
  }

  function applyEvent(ev: CastEvent) {
    if (!term) return;
    if (ev.code === "o") {
      term.write(ev.data);
    } else if (ev.code === "r") {
      const m = /^(\d+)x(\d+)$/.exec(ev.data);
      if (m) {
        term.resize(parseInt(m[1], 10), parseInt(m[2], 10));
        fitFont();
      }
    }
  }

  // Fit-to-window: the recorded grid is fixed (cols/rows come from
  // the cast file, never from our window), so the only free variable
  // is the font size. Measure the cell box at the preference font
  // once, assume cell dims scale linearly with fontSize (true for
  // monospace raster math), and pick the largest size that fits the
  // host. Capped at the user's preference size - an 80x24 cast in a
  // fullscreen modal shouldn't render comic-book glyphs.
  let baseCellW = 0;
  let baseCellH = 0;
  const baseFont = terminalPrefs.fontSize;

  function measureBaseCell() {
    const cell = (term as any)?._core?._renderService?.dimensions?.css?.cell;
    if (cell?.width > 0 && cell?.height > 0) {
      const scale = (term!.options.fontSize ?? baseFont) / baseFont;
      baseCellW = cell.width / scale;
      baseCellH = cell.height / scale;
    }
  }

  function fitFont() {
    if (!term || !host) return;
    if (baseCellW === 0) measureBaseCell();
    if (baseCellW === 0) return; // renderer not ready yet
    const availW = host.clientWidth - 14; // host padding
    const availH = host.clientHeight - 14;
    if (availW <= 0 || availH <= 0) return;
    const fit = Math.min(
      availW / (baseCellW * term.cols),
      availH / (baseCellH * term.rows),
    );
    const size = Math.max(6, Math.min(baseFont, Math.floor(baseFont * fit * 0.98)));
    if (term.options.fontSize !== size) {
      term.options.fontSize = size;
    }
  }

  function tick() {
    const now = performance.now();
    let dt = ((now - lastTick) / 1000) * speed;
    lastTick = now;
    let target = time + dt;
    // Idle skip: nothing happens until the next event and the gap is
    // long - jump to IDLE_CAP seconds before it instead of waiting.
    if (skipIdle && idx < events.length && events[idx].t - target > IDLE_CAP) {
      target = events[idx].t - IDLE_CAP;
    }
    while (idx < events.length && events[idx].t <= target) {
      applyEvent(events[idx]);
      idx++;
    }
    time = Math.min(target, duration);
    if (idx >= events.length) {
      pause();
      time = duration;
    }
  }

  function play() {
    if (playing || !loaded) return;
    // Replay from the start when the user hits play at the end.
    if (time >= duration && duration > 0) seek(0);
    playing = true;
    lastTick = performance.now();
    ticker = setInterval(tick, 33);
  }

  function pause() {
    playing = false;
    if (ticker !== null) {
      clearInterval(ticker);
      ticker = null;
    }
  }

  function togglePlay() {
    if (playing) pause();
    else play();
  }

  // Seek: reset and batch-replay up to the target. Output between
  // resizes is concatenated into single writes; resizes must apply
  // in order or reflow corrupts the replayed screen.
  function seek(to: number) {
    if (!term) return;
    pause();
    term.reset();
    term.resize(headerCols, headerRows);
    idx = 0;
    let chunk = "";
    while (idx < events.length && events[idx].t <= to) {
      const ev = events[idx];
      if (ev.code === "o") {
        chunk += ev.data;
      } else {
        if (chunk) {
          term.write(chunk);
          chunk = "";
        }
        applyEvent(ev);
      }
      idx++;
    }
    if (chunk) term.write(chunk);
    time = to;
  }

  function onScrub(e: Event) {
    const v = parseFloat((e.target as HTMLInputElement).value);
    const wasPlaying = playing;
    seek(v);
    if (wasPlaying) play();
  }

  onMount(async () => {
    let text: string;
    try {
      text = await api.recordingRead(path);
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
      return;
    }
    const lines = text.split("\n");
    try {
      const h = JSON.parse(lines[0]);
      headerCols = h.width || 80;
      headerRows = h.height || 24;
      title = h.title || "";
    } catch {
      loadError = "Not an asciicast v2 file (bad header line)";
      return;
    }
    // Full-screen apps (htop, vim with mouse=a) enable terminal
    // mouse tracking via DECSET. Replayed verbatim, those put the
    // player's xterm into mouse-reporting mode: it swallows mouse
    // events and text selection stops working. Playback never needs
    // mouse reporting - nothing consumes the reports - so strip the
    // mouse-mode set/reset sequences at parse time. Modes: 9 (X10),
    // 1000-1003 (tracking variants), 1004 (focus reporting),
    // 1005/1006/1015/1016 (encodings).
    const MOUSE_MODES =
      // eslint-disable-next-line no-control-regex
      /\x1b\[\?(?:9|100[0-6]|101[56])(?:;(?:9|100[0-6]|101[56]))*[hl]/g;
    for (let i = 1; i < lines.length; i++) {
      const line = lines[i].trim();
      if (!line) continue;
      try {
        const ev = JSON.parse(line);
        if (Array.isArray(ev) && ev.length >= 3 && typeof ev[0] === "number") {
          let data = String(ev[2]);
          if (ev[1] === "o") data = data.replace(MOUSE_MODES, "");
          events.push({ t: ev[0], code: String(ev[1]), data });
        }
      } catch {
        // Torn last line of a crash-stopped recording - skip.
      }
    }
    duration = events.length > 0 ? events[events.length - 1].t : 0;

    term = new Terminal({
      cols: headerCols,
      rows: headerRows,
      disableStdin: true,
      convertEol: false,
      scrollback: 5000,
      fontFamily: terminalPrefs.fontFamily,
      fontSize: terminalPrefs.fontSize,
      theme: themeToXterm(terminalPrefs.theme),
    });
    term.open(host);
    // Same renderer as the live terminal. The DOM renderer leaves
    // speckle artifacts at downscaled font sizes on fractional-DPI
    // displays (Windows 125% scaling) - exactly the sizes fit-to-
    // window picks in the non-fullscreen modal. Honour the same
    // disable flag and fall back the same way Terminal.svelte does.
    if (!terminalPrefs.disableWebgl) {
      try {
        webgl = new WebglAddon();
        webgl.onContextLoss(() => { webgl?.dispose(); webgl = null; });
        term.loadAddon(webgl);
      } catch (e) {
        console.warn("WebGL renderer unavailable, falling back", e);
      }
    }
    // Selection works out of the box - disableStdin blocks input,
    // not the mouse. Copy via Ctrl+C / Ctrl+Shift+C (Ctrl+C has no
    // SIGINT meaning here - nothing is listening) or right-click.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      if ((e.ctrlKey || e.metaKey) && (e.key === "c" || e.key === "C")) {
        const sel = term?.getSelection();
        if (sel) {
          copyText(sel).catch(() => {});
          return false;
        }
      }
      // Space = play/pause, video-player style.
      if (e.key === " ") {
        togglePlay();
        return false;
      }
      // Let Escape bubble to the modal (back to list / close)
      // instead of xterm eating it.
      if (e.key === "Escape") return false;
      return true;
    });
    loaded = true;
    // Renderer needs a frame before cell dimensions exist; fit then,
    // and refit whenever the host box changes (modal fullscreen
    // toggle, window resize).
    requestAnimationFrame(() => {
      requestAnimationFrame(() => fitFont());
    });
    resizeObs = new ResizeObserver(() => fitFont());
    resizeObs.observe(host);
    play();
  });

  let resizeObs: ResizeObserver | null = null;
  let webgl: WebglAddon | null = null;

  onDestroy(() => {
    pause();
    resizeObs?.disconnect();
    resizeObs = null;
    webgl?.dispose();
    webgl = null;
    term?.dispose();
    term = null;
  });
</script>

<div class="player">
  {#if loadError}
    <p class="error">{loadError}</p>
  {/if}
  <!-- mousedown pauses playback so a selection isn't yanked away by
       the next write; right-click copies the selection in place. -->
  <div
    class="term-host"
    bind:this={host}
    style:background={terminalPrefs.theme.background ?? "transparent"}
    role="presentation"
    onmousedown={() => { if (playing) pause(); }}
    oncontextmenu={(e) => {
      e.preventDefault();
      const sel = term?.getSelection();
      if (sel) {
        copyText(sel).catch(() => {});
        term?.clearSelection();
      }
    }}
  ></div>
  <div class="controls" class:hidden={!loaded}>
    <button class="play" onclick={togglePlay} title={playing ? "Pause" : "Play"}>
      {playing ? "⏸" : "▶"}
    </button>
    <span class="time">{fmt(time)} / {fmt(duration)}</span>
    <input
      class="scrub"
      type="range"
      min="0"
      max={duration}
      step="0.1"
      value={time}
      oninput={onScrub}
    />
    <select bind:value={speed} title="Playback speed">
      <option value={0.5}>0.5x</option>
      <option value={1}>1x</option>
      <option value={2}>2x</option>
      <option value={4}>4x</option>
    </select>
    <label class="idle" title="Jump over output gaps longer than 2 seconds">
      <input type="checkbox" bind:checked={skipIdle} />
      Skip idle
    </label>
  </div>
</div>

<style>
  .player {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-height: 0;
    flex: 1;
  }
  .term-host {
    flex: 1;
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.4rem;
    /* Centre the fixed grid when fit-to-window leaves slack space. */
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .error {
    color: var(--red);
    margin: 0;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-shrink: 0;
  }
  .controls.hidden { visibility: hidden; }
  .play {
    width: 2.2rem;
    text-align: center;
  }
  .time {
    font-variant-numeric: tabular-nums;
    font-size: 0.85rem;
    color: var(--subtext0);
    white-space: nowrap;
  }
  .scrub {
    flex: 1;
    min-width: 0;
  }
  .idle {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.8rem;
    color: var(--subtext0);
    white-space: nowrap;
  }
  select {
    background: var(--surface0);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    padding: 0.15rem 0.3rem;
  }
</style>
