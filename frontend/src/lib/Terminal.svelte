<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { EventsOn } from "./wailsRuntime";
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { WebglAddon } from "@xterm/addon-webgl";
  import { CanvasAddon } from "@xterm/addon-canvas";
  import { SearchAddon } from "@xterm/addon-search";
  import { WebLinksAddon } from "@xterm/addon-web-links";
  import "@xterm/xterm/css/xterm.css";
  import { api } from "./api";
  import { sessions } from "./stores.svelte";
  import { terminalPrefs } from "./terminalPrefs.svelte";
  import { copyPastePrefs } from "./copyPastePrefs.svelte";
  import { toast } from "./toast.svelte";
  import { broadcast } from "./broadcast.svelte";
  import { keyBarMods } from "./keyBarMods.svelte";
  import type { TerminalTheme } from "./themes";

  // Strip the metadata fields (id, name, isLight) before handing to xterm
  // so its ITheme type-checks cleanly.
  function themeToXterm(t: TerminalTheme) {
    const { id, name, isLight, ...colors } = t;
    return colors;
  }
  import PasteGuard from "./PasteGuard.svelte";
  import TermKeyBar from "./TermKeyBar.svelte";
  import { isMobile } from "./platform";

  interface Props {
    sessionId: string;
    active: boolean;
  }
  let { sessionId, active }: Props = $props();

  // Whether this session is a local PTY (vs SSH). Drives which set
  // of write/resize/scrollback IPCs we call. Looked up at call sites
  // so it tracks store updates if the session shape changes.
  function isLocal(sid: string): boolean {
    return sessions.tabs.find((t) => t.sessionId === sid)?.kind === "local";
  }
  function writeIPC(sid: string, b64: string): Promise<unknown> {
    return isLocal(sid)
      ? api.localShellWrite(sid, b64)
      : api.sshWrite(sid, b64);
  }
  function resizeIPC(sid: string, cols: number, rows: number): Promise<unknown> {
    return isLocal(sid)
      ? api.localShellResize(sid, cols, rows)
      : api.sshResize(sid, cols, rows);
  }
  function scrollbackIPC(sid: string) {
    return isLocal(sid)
      ? api.localShellGetScrollback(sid)
      : api.sshGetScrollback(sid);
  }

  let host: HTMLDivElement;
  // term/fit are $state so the $effects below re-run once xterm is
  // constructed (onMount is async + awaits display layout, so the
  // effects' first pass typically sees them null and would otherwise
  // drop their work on the floor - silent blank terminal, no resize
  // wired up).
  let term: Terminal | null = $state(null);
  let fit: FitAddon | null = $state(null);
  let search: SearchAddon | null = null;
  let webLinks: WebLinksAddon | null = null;

  // Search bar state. Open/close toggled by Ctrl+F (or Cmd+F on mac).
  let searchOpen = $state(false);
  let searchQuery = $state("");
  let searchEl: HTMLInputElement | undefined = $state();
  let webgl: WebglAddon | null = null;
  let canvas: CanvasAddon | null = null;
  let resizeObs: ResizeObserver | null = null;
  let onSearchEvent: ((e: Event) => void) | null = null;

  // ---------- broadcast input gate ----------
  //
  // xterm's onData fires for BOTH user input (keystrokes, paste) AND
  // terminal report responses (DA1, cursor-position / DSR, OSC 10-11
  // colour queries) that the REMOTE app - vim, less, tmux - requests. We
  // must write all of it to THIS session's PTY (the remote asked for the
  // report), but we must NOT broadcast the report responses to other
  // members: they never asked, so the bytes land in their shells as
  // garbage ("^[[>0;276;0c", "rgb:1111/...", ran as commands).
  //
  // userInput is set right before genuine user input reaches the PTY
  // (onKey, key-bar sendKeys, paste) and consumed by the very next
  // onData. Report responses are emitted by the parser during term.write
  // of remote output, with no preceding user action, so userInput is
  // false for them -> written locally, never broadcast.
  let userInput = false;

  // ---------- paste guard ----------
  //
  // We intercept the browser-level `paste` event on the terminal host
  // BEFORE xterm gets to handle it. If the clipboard text contains a
  // newline (real multi-line, not a trailing CR), we pop a modal asking
  // the user to confirm. A per-session "don't ask again" toggle lets
  // power users opt out for the duration.

  let pendingPasteText = $state<string | null>(null);
  let sessionPasteOptOut = $state(false);

  function isMultilinePaste(text: string): boolean {
    // Strip a single trailing newline (very common when copying terminal
    // lines that include the final \n) - that one alone shouldn't trigger.
    const trimmed = text.replace(/\n$/, "");
    return /\n/.test(trimmed);
  }

  function sendPaste(text: string) {
    // xterm `paste()` handles bracketed-paste mode (if the remote supports
    // it) and CR/LF normalization. Prefer it over raw write so the remote
    // sees a clean pasted block.
    if (term) {
      // A paste is user input -> let the resulting onData broadcast (see
      // the userInput gate). term.paste fires onData synchronously.
      userInput = true;
      term.paste(text);
    } else {
      const bytes = enc.encode(text);
      writeIPC(sessionId, toB64(bytes)).catch(console.warn);
    }
  }

  function onHostPaste(e: ClipboardEvent) {
    const text = e.clipboardData?.getData("text") ?? "";
    if (!text) return;
    if (sessionPasteOptOut || !isMultilinePaste(text)) {
      // Let xterm handle the normal paste flow. We don't preventDefault,
      // so the focused textarea inside xterm receives the paste - which
      // fires onData (one event for the whole block). Mark it
      // user-originated so it still broadcasts.
      userInput = true;
      return;
    }
    // Multi-line paste needing confirmation.
    e.preventDefault();
    e.stopPropagation();
    pendingPasteText = text;
  }

  function confirmPaste(remember: boolean) {
    const text = pendingPasteText;
    pendingPasteText = null;
    if (remember) sessionPasteOptOut = true;
    if (text != null) sendPaste(text);
    // Refocus xterm so the user can keep typing.
    setTimeout(() => term?.focus(), 0);
  }
  function cancelPaste() {
    pendingPasteText = null;
    setTimeout(() => term?.focus(), 0);
  }

  // ---------- IO bridge ----------

  const enc = new TextEncoder();
  function toB64(bytes: Uint8Array): string {
    let s = "";
    for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s);
  }
  function fromB64(b64: string): Uint8Array {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  // sendKeys writes a raw string to the PTY exactly as if typed (used by the
  // on-screen key bar on touch devices). Mirrors the onData path: write to
  // this session, fan out to broadcast peers, and keep focus on the
  // terminal so the soft keyboard stays up.
  function sendKeys(data: string) {
    if (!data) return;
    const bytes = enc.encode(data);
    writeIPC(sessionId, toB64(bytes)).catch(console.warn);
    if (broadcast.hasInAnyGroup(sessionId) && broadcast.totalMembers() > 1) {
      broadcast.fanOut(data, sessionId).catch(console.warn);
    }
    term?.focus();
  }

  // Strip DECRQM (Request Mode) and DECRPM (Report Mode) CSI
  // sequences from a byte stream. Format:
  //   ESC [ ? <params> $ p      (DECRQM, ESC=0x1b)
  //   ESC [ ? <params> $ y      (DECRPM, less common but same shape)
  // 8-bit CSI variant (0x9b) handled too.
  //
  // Reason for the strip: xterm 6.x has an open bug in its
  // requestMode handler that throws when certain params land -
  // the throw fires from inside an async parser callback so
  // try/catch around term.write doesn't catch it. The sequence
  // is a query the remote sends to detect terminal features;
  // dropping it just means the remote falls back to safe defaults.
  function stripDECRQM(data: Uint8Array): Uint8Array {
    // Quick scan: nothing to strip if no ESC/0x9b present.
    let found = false;
    for (let i = 0; i < data.length; i++) {
      const b = data[i];
      if (b === 0x1b || b === 0x9b) { found = true; break; }
    }
    if (!found) return data;

    const out = new Uint8Array(data.length);
    let oi = 0;
    let i = 0;
    while (i < data.length) {
      const b = data[i];
      let csiStart = -1;
      // 7-bit CSI: ESC [
      if (b === 0x1b && i + 1 < data.length && data[i + 1] === 0x5b) {
        csiStart = i + 2;
      }
      // 8-bit CSI: 0x9b
      else if (b === 0x9b) {
        csiStart = i + 1;
      }
      if (csiStart < 0) {
        out[oi++] = b;
        i++;
        continue;
      }
      // Only intercept DECRQM/DECRPM: CSI ? <params> $ [py]
      if (csiStart >= data.length || data[csiStart] !== 0x3f /* ? */) {
        out[oi++] = b;
        i++;
        continue;
      }
      // Walk to terminator: $ followed by p or y. Stop after
      // 64 bytes to avoid consuming the rest of the stream on
      // malformed input.
      let j = csiStart + 1;
      const maxJ = Math.min(j + 64, data.length - 1);
      let stripped = false;
      while (j < maxJ) {
        if (data[j] === 0x24 /* $ */ && (data[j + 1] === 0x70 /* p */ || data[j + 1] === 0x79 /* y */)) {
          // Skip the whole sequence: from `b` (i) through j+1.
          i = j + 2;
          stripped = true;
          break;
        }
        // Params are digits, semicolons, and the leading '?'. If
        // we hit anything else this isn't DECRQM/DECRPM - bail.
        const c = data[j];
        if (!((c >= 0x30 && c <= 0x39) || c === 0x3b)) {
          break;
        }
        j++;
      }
      if (!stripped) {
        out[oi++] = b;
        i++;
      }
    }
    return out.subarray(0, oi);
  }

  // ---------- copy / paste handlers ----------

  // announce=true fires a "Copied" toast - used for explicit copy
  // (Ctrl+Shift+C / Cmd+C / right-click). The Linux auto-copy-on-select
  // path passes false: it fires on every drag-select, so a toast there
  // would be constant noise.
  async function copySelection(announce = false): Promise<boolean> {
    if (!term) return false;
    const sel = term.getSelection();
    if (!sel) return false;
    try {
      await navigator.clipboard.writeText(sel);
      if (announce) toast.ok("Copied");
      return true;
    } catch (e) {
      console.warn("clipboard write failed", e);
      return false;
    }
  }

  async function pasteFromClipboard() {
    try {
      const text = await navigator.clipboard.readText();
      if (!text) return;
      if (!sessionPasteOptOut && isMultilinePaste(text)) {
        pendingPasteText = text;
      } else {
        sendPaste(text);
      }
    } catch (e) {
      console.warn("clipboard read failed", e);
    }
  }

  // Broadcast the search bar's open/closed state so the pane header's
  // search button (PaneNode) can reflect it as an active toggle, staying in
  // sync however the bar is closed (button, Esc, or the ✕).
  function emitSearchState() {
    window.dispatchEvent(new CustomEvent("terminal:searchstate", {
      detail: { sessionId, open: searchOpen },
    }));
  }
  function openSearch() {
    searchOpen = true;
    emitSearchState();
    setTimeout(() => searchEl?.focus(), 0);
  }
  function closeSearch() {
    searchOpen = false;
    search?.clearDecorations();
    emitSearchState();
    setTimeout(() => term?.focus(), 0);
  }
  function findNext() {
    if (!search || !searchQuery) return;
    search.findNext(searchQuery, { decorations: searchDecorations() });
  }
  function findPrev() {
    if (!search || !searchQuery) return;
    search.findPrevious(searchQuery, { decorations: searchDecorations() });
  }
  function searchDecorations() {
    // Reuse the active theme's accent for the match highlight so it
    // reads against any background.
    const t = terminalPrefs.theme;
    return {
      matchBackground: t.yellow,
      matchBorder: t.yellow,
      matchOverviewRuler: t.yellow,
      activeMatchBackground: t.red,
      activeMatchBorder: t.red,
      activeMatchColorOverviewRuler: t.red,
    };
  }

  // attachCustomKeyEventHandler runs BEFORE xterm processes a key.
  // Return false to swallow (xterm won't see it). True passes through.
  // We swallow shortcuts that we handle ourselves so xterm doesn't
  // also send them as input bytes.
  function customKeyHandler(e: KeyboardEvent): boolean {
    if (e.type !== "keydown") return true;

    // Paste guard modal is open: route Enter/Esc/y to its actions
    // here. xterm has its own textarea that swallows keys before any
    // window-level listener sees them; this handler runs first and
    // returning false stops xterm from also writing the key into the
    // PTY (newline, etc).
    if (pendingPasteText !== null) {
      if (e.key === "Enter" || e.key === "y") {
        confirmPaste(false);
        return false;
      }
      if (e.key === "Escape") {
        cancelPaste();
        return false;
      }
      // Swallow everything else while the modal is up - keystrokes
      // should not slip through to the shell behind it.
      return false;
    }

    const mode = copyPastePrefs.mode;

    // Ctrl+Shift+F (or Cmd+Shift+F on mac) opens the in-app search
    // bar over the scrollback. Plain Ctrl+F belongs to the remote
    // shell (bash readline forward-char, vim page-forward, less
    // forward) - intercepting it locked users out of vim. F3 still
    // jumps next/prev match while the bar is open via its own
    // keydown handler.
    const findKey = (e.key === "f" || e.key === "F");
    const modFind = mode === "mac"
      ? (e.metaKey && e.shiftKey && !e.ctrlKey)
      : (e.ctrlKey && e.shiftKey);
    if (findKey && modFind) {
      openSearch();
      return false;
    }
    if (e.key === "F3") {
      if (e.shiftKey) findPrev(); else findNext();
      return false;
    }

    // Ctrl+Shift+L (Cmd+Shift+L on mac) - force a clean redraw.
    // Escape hatch for the rare WebGL atlas ghosting a full-screen TUI
    // can leave behind; clears the glyph cache and repaints. Plain
    // Ctrl+L stays the shell's clear-screen.
    const redrawKey = (e.key === "l" || e.key === "L");
    const modRedraw = mode === "mac"
      ? (e.metaKey && e.shiftKey && !e.ctrlKey)
      : (e.ctrlKey && e.shiftKey);
    if (redrawKey && modRedraw) {
      clearWebglAtlas();
      term?.refresh(0, (term?.rows ?? 1) - 1);
      return false;
    }

    // Global app shortcuts that the window-level keydown listener
    // handles. xterm's textarea would otherwise swallow these
    // before they bubble up. We don't run the action here; we
    // just refuse the keypress so the global listener can fire.
    const ctrlOrMeta = e.ctrlKey || e.metaKey;
    if (ctrlOrMeta && !e.altKey) {
      // Ctrl+K - quick palette
      if (!e.shiftKey && e.key.toLowerCase() === "k") return false;
      // Ctrl+Shift+P - snippet palette
      if (e.shiftKey && e.key.toLowerCase() === "p") return false;
      // Ctrl+Tab / Ctrl+Shift+Tab - tab cycle
      if (e.key === "Tab") return false;
      // Ctrl+1..9 - tab jump (unshifted only; shifted forms are
      // shell signals on some terminals)
      if (!e.shiftKey && /^[1-9]$/.test(e.key)) return false;
      // Ctrl+Shift+W - close tab, Ctrl+Shift+T - reopen tab
      if (e.shiftKey && (e.key.toLowerCase() === "w" || e.key.toLowerCase() === "t")) return false;
    }

    // Ctrl+Shift+C - copy (Windows + Linux)
    if ((mode === "windows" || mode === "linux") &&
        e.ctrlKey && e.shiftKey && (e.key === "C" || e.key === "c")) {
      copySelection(true);
      return false;
    }
    // Ctrl+Shift+V - paste (Windows + Linux)
    if ((mode === "windows" || mode === "linux") &&
        e.ctrlKey && e.shiftKey && (e.key === "V" || e.key === "v")) {
      pasteFromClipboard();
      return false;
    }
    // Cmd+C / Cmd+V - copy/paste (Mac)
    if (mode === "mac" && e.metaKey && !e.ctrlKey && !e.shiftKey) {
      if (e.key === "c" || e.key === "C") {
        copySelection(true);
        return false;
      }
      if (e.key === "v" || e.key === "V") {
        pasteFromClipboard();
        return false;
      }
    }
    // Everything else - let xterm handle it. Plain Ctrl+C falls through
    // here and xterm sends it as SIGINT (0x03), which is exactly what
    // we want in all three modes.
    return true;
  }

  function onSelectionChange() {
    // Linux convention: selecting text auto-copies. Closest we get to
    // the X primary selection without a real primary clipboard.
    if (copyPastePrefs.mode !== "linux") return;
    if (!term?.hasSelection()) return;
    copySelection();
  }

  async function onContextMenu(e: MouseEvent) {
    e.preventDefault();
    const mode = copyPastePrefs.mode;
    if (mode === "windows") {
      // Smart toggle: selection -> copy + clear; no selection -> paste.
      if (term?.hasSelection()) {
        await copySelection(true);
        term?.clearSelection();
      } else {
        await pasteFromClipboard();
      }
      return;
    }
    // linux + mac: right-click pastes (no smart toggle).
    await pasteFromClipboard();
  }

  function onMouseDown(e: MouseEvent) {
    // Middle-click paste, Linux mode only.
    if (e.button !== 1) return;
    if (copyPastePrefs.mode !== "linux") return;
    e.preventDefault();
    pasteFromClipboard();
  }

  // Focus the terminal as soon as its host element is actually visible
  // AND has been laid out (non-zero size). Up to ~600ms of polling -
  // beyond that we give up; the user can click to focus manually.
  //
  // The currently-focused element (typically the Connect button in
  // DetailPane) is blurred first because some webviews refuse to move
  // focus across hidden→visible boundaries while another control owns it.
  function focusWhenVisible() {
    let tries = 0;
    const tick = () => {
      if (!term) return;
      const ready = host
        && host.offsetParent !== null
        && host.clientWidth > 0
        && host.clientHeight > 0;
      if (ready) {
        const active = document.activeElement as HTMLElement | null;
        if (active && active !== document.body && !host.contains(active)) {
          active.blur();
        }
        term.focus();
        return;
      }
      if (tries++ < 30) setTimeout(tick, 20);
    };
    tick();
  }

  async function notifyResize() {
    if (!term || !fit) return;
    // Two-pass fit. First call sizes the canvas; xterm then commits
    // the row/col change on the next frame, which is when the WebGL
    // renderer recomputes its cellHeight. A second fit after that
    // commit lets fit() re-measure against the now-correct cell
    // geometry and trim the last row if it would clip.
    fit.fit();
    await new Promise<void>((r) => requestAnimationFrame(() => r()));
    if (!term || !fit) return;
    fit.fit();
    // After fit, sometimes the proposed rows leave the bottom row
    // visually clipped because host height isn't an exact multiple
    // of cellHeight (descender pokes past). Probe the fit addon's
    // own dimensions: if host has less than half a cell of
    // remainder, drop one row. The cost is a single row of unused
    // screen space; the win is no descender clip even at awkward
    // window heights.
    const proposed = fit.proposeDimensions();
    if (proposed && host) {
      const style = getComputedStyle(host);
      const padTop = parseFloat(style.paddingTop) || 0;
      const padBot = parseFloat(style.paddingBottom) || 0;
      const usable = host.clientHeight - padTop - padBot;
      // Prefer the renderer's REAL cell height (CSS px). The old
      // fontSize*lineHeight estimate diverges from the WebGL cell -
      // which rounds to device pixels - so at certain font sizes /
      // DPI the remainder math was wrong and the bottom row stayed
      // clipped, looking like the status bar overlapped the last
      // terminal line. _core is private API; fall back to the
      // estimate if the shape changes in an xterm upgrade.
      const coreCellH = (term as any)._core?._renderService?.dimensions?.css?.cell?.height;
      const estimate = (term.options.lineHeight ?? 1.2) * (term.options.fontSize ?? 13);
      const exact = typeof coreCellH === "number" && coreCellH > 0;
      const lineH = exact ? coreCellH : estimate;
      const remainder = usable - (proposed.rows * lineH);
      // With the exact cell height a tiny remainder is fine (the
      // glyph box ends inside the host); only shed when the canvas
      // would actually poke past. With the estimate keep the old
      // conservative 60%-of-a-row margin.
      const threshold = exact ? 1 : lineH * 0.6;
      if (remainder < threshold) {
        try {
          term.resize(proposed.cols, Math.max(1, proposed.rows - 1));
        } catch { /* xterm guards; ignore */ }
      }
    }
    const { cols, rows } = term;
    // The WebGL glyph atlas caches rasterised cells at the current
    // dimensions. After a resize / font-size change the cell box
    // differs but stale atlas entries can survive, and a full-screen
    // TUI that repaints the whole grid each tick (htop, btop) then
    // ghosts the header over the body until a clean redraw. Clearing
    // the atlas here forces re-rasterisation at the new size. Only on
    // resize, so no per-frame cost.
    clearWebglAtlas();
    try {
      await resizeIPC(sessionId, cols, rows);
    } catch (e) {
      console.warn("resize failed", e);
    }
  }

  // clearWebglAtlas drops the WebGL renderer's cached glyph atlas so
  // the next paint re-rasterises every cell. No-op when WebGL is off
  // or the addon lacks the method (older xterm). Also exposed via the
  // Ctrl+L redraw shortcut for the rare in-place corruption case.
  function clearWebglAtlas() {
    try {
      (webgl as any)?.clearTextureAtlas?.();
      (canvas as any)?.clearTextureAtlas?.();
    } catch { /* best effort */ }
  }

  // Resolve when the host element is actually laid out (visible + has
  // non-zero size). We DO NOT call term.open() until then, because
  // attaching xterm to a display:none / 0×0 host bakes a zero-sized
  // canvas into its renderer that no later fit() or resize event can
  // un-bake - the symptom is "terminal stays blank until you click it".
  function waitForHostLayout(): Promise<void> {
    return new Promise((resolve) => {
      const tick = () => {
        const ready = host
          && host.offsetParent !== null
          && host.clientWidth > 0
          && host.clientHeight > 0;
        if (ready) { resolve(); return; }
        setTimeout(tick, 20);
      };
      tick();
    });
  }

  onMount(async () => {
    // Make sure we've loaded the persisted zoom before constructing the
    // first terminal; otherwise the initial render flickers at 13px.
    await terminalPrefs.load();
    await copyPastePrefs.load();

    // Wait until the pane is actually visible before attaching xterm.
    await waitForHostLayout();

    term = new Terminal({
      fontFamily: terminalPrefs.fontFamily,
      fontSize: terminalPrefs.fontSize,
      lineHeight: 1.2,
      cursorBlink: true,
      theme: themeToXterm(terminalPrefs.theme),
      scrollback: terminalPrefs.scrollback,
      allowProposedApi: true,
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    search = new SearchAddon();
    term.loadAddon(search);
    // WebLinks: click URLs in scrollback to open the system browser via
    // Wails' BrowserOpenURL. The handler runs on the original click,
    // not a synthetic event, so we just route URL -> backend.
    // WebLinksAddon: open URL only when the user holds Ctrl (or Cmd
    // on Mac). Plain click in the terminal is for selection /
    // shell - accidentally launching a browser when clicking near
    // a URL was annoying. Same convention as VS Code's terminal.
    webLinks = new WebLinksAddon((e, url) => {
      if (!(e.ctrlKey || e.metaKey)) return;
      api.openURL(url);
    });
    term.loadAddon(webLinks);
    term.open(host);

    term.attachCustomKeyEventHandler(customKeyHandler);
    term.onSelectionChange(onSelectionChange);

    // WebGL is off by default on mobile: the Android WebView's WebGL glyph
    // atlas is flaky (garbled / overlapping text - "hieroglyphs" - after
    // resizes or broadcast paints), and the DOM/canvas renderer is reliable
    // and plenty fast on a phone. Desktop still uses WebGL unless the user
    // disabled it.
    // Renderer selection. xterm 6's DEFAULT (no renderer addon) is the DOM
    // renderer, which on this beta drops repaints under fast bursts of large
    // output (e.g. `ls -l /var/log` several times) - the buffer is correct
    // (cursor/baseY track fine) but the canvas lags, so the screen shows
    // stale/overlapping rows. So we always load an explicit GPU/2D renderer:
    // WebGL by default; the Canvas addon when WebGL is disabled (the Android
    // WebView's WebGL atlas is flaky, and a user who hit that turns it off).
    // Canvas avoids both the DOM repaint bug and the WebGL atlas bug.
    if (!terminalPrefs.disableWebgl && !isMobile) {
      try {
        webgl = new WebglAddon();
        webgl.onContextLoss(() => {
          // On context loss, drop WebGL and fall back to Canvas (not DOM).
          webgl?.dispose(); webgl = null;
          try { canvas = new CanvasAddon(); term?.loadAddon(canvas); } catch { /* ignore */ }
        });
        term.loadAddon(webgl);
      } catch (e) {
        console.warn("WebGL renderer unavailable, falling back to canvas", e);
        try { canvas = new CanvasAddon(); term.loadAddon(canvas); } catch { /* ignore */ }
      }
    } else {
      // WebGL off (disabled, or mobile): use Canvas rather than letting xterm
      // fall back to its DOM renderer.
      try {
        canvas = new CanvasAddon();
        term.loadAddon(canvas);
      } catch (e) {
        console.warn("Canvas renderer unavailable, using DOM", e);
      }
    }

    fit.fit();

    // onKey fires only for genuine key presses (not pastes, not terminal
    // report responses). Mark the input as user-originated so the very next
    // onData is allowed to broadcast. See the userInput comment above.
    term.onKey(() => { userInput = true; });

    term.onData((raw) => {
      // Consume the user-input marker: true only if this onData was
      // triggered by a real key press / paste / key-bar, false if it's a
      // terminal report response the remote app requested.
      const fromUser = userInput;
      userInput = false;
      // Apply any latched Ctrl/Alt from the on-screen key bar (mobile): a
      // letter typed on the soft keyboard while "Ctrl" is latched becomes
      // its control byte here, since it bypasses the bar's own buttons.
      // No-op on desktop (the latch is only ever set by the mobile bar).
      const data = keyBarMods.apply(raw);
      const bytes = enc.encode(data);
      // Always write to this session - xterm's onData is the source of
      // truth for what the user typed here AND for the report responses
      // the remote app asked for (both must reach this PTY).
      writeIPC(sessionId, toB64(bytes)).catch(console.warn);
      // Broadcast fans the keystroke to every other member, SSH or local
      // PTY alike (see BroadcastFanOut in app.go which handles both pools).
      // ONLY user-originated input is fanned out: a terminal report
      // response (DA1 / cursor pos / OSC colour) must not leak into peers'
      // shells as garbage. hasInAnyGroup: works for sessions that live only
      // in a named (non-default) broadcast group.
      if (fromUser && broadcast.hasInAnyGroup(sessionId) && broadcast.totalMembers() > 1) {
        broadcast.fanOut(data, sessionId).catch(console.warn);
      }
    });

    // Debounce ResizeObserver storm: dragging a window edge fires
    // it pixel-by-pixel, which spammed sshResize and made the
    // double-fit race with itself. A 50 ms tail window collapses
    // the storm into one fit+IPC per drag-pause without making
    // the resize feel laggy.
    let resizePending: ReturnType<typeof setTimeout> | null = null;
    resizeObs = new ResizeObserver(() => {
      if (!active) return;
      if (resizePending) clearTimeout(resizePending);
      resizePending = setTimeout(() => {
        resizePending = null;
        notifyResize();
      }, 50);
    });
    resizeObs.observe(host);

    // Attach the paste interceptor in capture phase so we fire BEFORE
    // xterm's own textarea handles the paste.
    host.addEventListener("paste", onHostPaste, { capture: true });
    // Ctrl+wheel zoom - capture phase + non-passive so preventDefault
    // works against the webview's page-zoom default.
    host.addEventListener("wheel", onWheel, { capture: true, passive: false });
    // Right-click + middle-click handlers. Capture phase so we beat
    // xterm's internal mouse handling. Middle-click paste is Linux-only;
    // contextmenu fires for right-click and we route by mode.
    host.addEventListener("contextmenu", onContextMenu, { capture: true });
    host.addEventListener("mousedown", onMouseDown, { capture: true });
    // Pinch-to-zoom (touch). Capture + non-passive on move so we can
    // preventDefault the WebView's own pinch page-zoom.
    host.addEventListener("pointerdown", onPointerDown, { capture: true });
    host.addEventListener("pointermove", onPointerMove, { capture: true, passive: false });
    host.addEventListener("pointerup", onPointerUp, { capture: true });
    host.addEventListener("pointercancel", onPointerUp, { capture: true });

    // Toggle the scrollback search from the pane header's search button. The
    // button lives in PaneNode (a different component), so it signals us via
    // a window CustomEvent keyed by sessionId - lighter than a shared store
    // for a one-shot trigger. Clicking the button again closes the bar.
    onSearchEvent = (e: Event) => {
      const ce = e as CustomEvent<{ sessionId: string }>;
      if (ce.detail?.sessionId !== sessionId) return;
      if (searchOpen) closeSearch(); else openSearch();
    };
    window.addEventListener("terminal:search", onSearchEvent);

    // Focus the terminal on mount. Tricky because connectOne mounts the
    // tab while the Terminal view is still display:none, then flips the
    // view - so a single focus() right here is a no-op (can't focus a
    // hidden element). We poll the host's offsetParent for visibility
    // and fire focus once it shows up.
    if (active) focusWhenVisible();

    // The "connected" event was emitted before this component subscribed,
    // and the initial shell prompt may have been missed. Force a resize
    // so the shell gets SIGWINCH and redraws the prompt. We stagger two
    // SIGWINCHes: a slow MOTD/banner may push the prompt past the first
    // resize, so the second one catches that case.
    if (active) {
      setTimeout(() => notifyResize(), 50);
      setTimeout(() => notifyResize(), 600);
    }
  });

  // Re-subscribe pty_output/state/debug when sessionId changes. This is
  // critical for the Reconnect flow: PaneNode's reconnectLeaf swaps the
  // pane's sessionId after sshConnect succeeds, but onMount fires only
  // once - without re-subscribing here, the Terminal stays bound to
  // the dead session and silently shows nothing.
  //
  // Subscribe-then-snapshot avoids the missing-prompt race: bytes written
  // by the shell BETWEEN our snapshot call and our subscribe call would
  // otherwise vanish - they aren't in the snapshot and arrived before
  // the listener was wired. Instead we subscribe first, buffer chunks,
  // fetch the snapshot, and use its cum watermark to dedupe.
  //
  // Svelte 5's $effect can re-run on unrelated state churn (e.g. parent
  // mutating paneTabs.tabs when the active pane changes). Guard with a
  // ref tracking the wired sessionId so we don't re-fetch the snapshot
  // and replay it - that's what caused the buffer-doubling-on-click
  // bug. Real sessionId changes (reconnect) DO get through because
  // wiredSid is closed over by the cleanup return.
  // Track the wired sessionId + its live unsubs OUTSIDE the $effect.
  // Svelte 5 $effect can fire on unrelated state churn (parent
  // mutating paneTabs.tabs on every click), and its cleanup *always*
  // runs before the next pass. If we held the unsubs inside the
  // effect body, an idle rerun would tear down the listeners. Keep
  // them in module-scope vars, only swap on a real sessionId change.
  let wiredSid: string | null = null;
  let wiredUnsubs: (() => void)[] = [];
  function unwire() {
    for (const fn of wiredUnsubs) fn();
    wiredUnsubs = [];
    wiredSid = null;
  }
  onDestroy(unwire);

  $effect(() => {
    const sid = sessionId;
    if (!term) return;
    if (sid === wiredSid) {
      // Idle rerun - listeners are already wired. Skip without
      // touching cleanup so they stay alive.
      return;
    }
    if (wiredSid !== null) {
      // Real sessionId change (reconnect). Drop the old listeners
      // before wiring the new ones.
      unwire();
    }
    wiredSid = sid;
    const t = term;

    let watermark: number | null = null;
    let snapshotWritten = false;
    const buffered: Array<{ data: Uint8Array; cum: number }> = [];

    function writeChunk(data: Uint8Array, cum: number) {
      // Strip DECRQM (CSI ? ... $ p) before write. xterm 6.x has an
      // open bug in its requestMode handler that throws a stale `s`
      // reference for some param values; the throw lands in an
      // async callback so try/catch around term.write doesn't catch
      // it. The sequence is purely a *query* from the remote (vim,
      // less, tmux use it to detect feature support) - discarding
      // it just means the remote falls back to defaults. The PTY
      // response would normally be queued anyway, so dropping it
      // is safe.
      const clean = stripDECRQM(data);
      try {
        if (cum === 0 || watermark === null) {
          t.write(clean);
          return;
        }
        if (cum <= watermark) return;
        const start = clean.length - (cum - watermark);
        t.write(start > 0 ? clean.subarray(start) : clean);
      } catch (err) {
        console.warn("[term] write threw, dropping chunk", err);
      }
    }

    function flushIfReady() {
      if (watermark === null) return;
      // Subsequent flushes (live events arriving after snapshot) just
      // write the buffered chunks; the snapshot is only prepended once.
      if (!snapshotWritten) snapshotWritten = true;
      for (const c of buffered) writeChunk(c.data, c.cum);
      buffered.length = 0;
    }

    // Live-output reassembly. Two problems to solve at once:
    //
    // 1. ORDER. Wails v3's EventProcessor.Emit dispatches each event on its
    //    OWN `go func()` (pkg/application/events.go), so pty_output events
    //    do NOT necessarily reach the webview in the order the backend
    //    emitted them - even though the backend now emits from a single
    //    pump under a lock. Out-of-order delivery on a big `ll` burst landed
    //    content on the wrong rows (prompt rendered mid-listing). We cannot
    //    fix this on the Go side; the only total order we have is `cum`, the
    //    backend's cumulative byte count through the end of each chunk.
    // 2. COALESCING. Writing each chunk with its own term.write raced
    //    xterm's async parser against resize/refresh; one contiguous write
    //    per frame keeps the screen consistent.
    //
    // So: hold incoming chunks keyed by their byte range [start,end) (start
    // = cum - rawLen, end = cum), and per animation frame write the maximal
    // CONTIGUOUS run starting at writeCum. A chunk whose start is past
    // writeCum (a gap: a reordered predecessor hasn't arrived yet) stays
    // pending until the gap fills, then flushes in a later frame. This is
    // robust across frames, not just within one. writeCum starts at the
    // snapshot watermark.
    type Pending = { start: number; end: number; clean: Uint8Array };
    let pending: Pending[] = [];
    let writeCum = -1; // -1 until first live chunk seeds it from watermark
    let rafPending = 0;
    // How many consecutive frames we've been stalled behind the same gap.
    // A reorder is resolved within a frame or two; if a gap persists past
    // GAP_FRAME_BUDGET frames the "missing" chunk is genuinely not coming
    // (shouldn't happen with Wails delivery, but never hang): force-flush
    // the held chunks in order, accepting the gap, so output degrades to the
    // old behaviour instead of freezing the terminal.
    let gapFrames = 0;
    const GAP_FRAME_BUDGET = 4;
    function queueLive(data: Uint8Array, cum: number) {
      const clean = stripDECRQM(data);
      if (cum === 0) {
        // Banner sentinel (pre-session) - no ordering info; write as-is at
        // the front of the stream.
        pending.push({ start: -1, end: -1, clean });
      } else {
        // Offsets from the RAW decoded length (cum counts raw bytes); the
        // DECRQM strip only changes content, not the stream position.
        pending.push({ start: cum - data.length, end: cum, clean });
      }
      if (!rafPending) rafPending = requestAnimationFrame(flushLive);
    }
    function flushLive() {
      rafPending = 0;
      if (pending.length === 0) return;
      if (writeCum < 0) writeCum = watermark ?? 0;
      // cum order; sentinel (start<0) sorts first.
      pending.sort((a, b) => a.start - b.start);

      // Force-flush mode: we've waited long enough for a missing chunk that
      // we now write the held run in order regardless of gaps.
      const force = gapFrames >= GAP_FRAME_BUDGET;

      const ready: Uint8Array[] = [];
      const leftover: Pending[] = [];
      for (const p of pending) {
        if (p.start < 0) {
          // Banner sentinel: always writable, doesn't move writeCum.
          ready.push(p.clean);
          continue;
        }
        if (p.end <= writeCum) {
          // Fully covered by what we've already written (snapshot overlap or
          // a duplicate redelivery) - drop.
          continue;
        }
        if (p.start <= writeCum || force) {
          // Contiguous/overlapping (or forced past a stuck gap) - trim any
          // overlapped prefix and write. Overlap is normally just the
          // snapshot boundary, where the strip doesn't touch the overlapped
          // bytes, so a byte-count trim on the cleaned buffer is correct.
          const overlap = writeCum - p.start;
          ready.push(overlap > 0 ? p.clean.subarray(overlap) : p.clean);
          writeCum = Math.max(writeCum, p.end);
        } else {
          // Gap: a lower-offset chunk hasn't arrived yet. Hold this and
          // everything after it (sorted) for a later frame.
          leftover.push(p);
        }
      }
      pending = leftover;
      // Track how long we've been stalled behind a gap so a never-arriving
      // chunk can't freeze output (see GAP_FRAME_BUDGET). Reset once drained.
      gapFrames = pending.length > 0 ? gapFrames + 1 : 0;
      // If we're still holding chunks behind a gap, keep a frame scheduled so
      // the moment the missing chunk lands (or the budget expires) we drain.
      if (pending.length > 0 && !rafPending) {
        rafPending = requestAnimationFrame(flushLive);
      }
      if (ready.length === 0) return;

      let total = 0;
      for (const c of ready) total += c.length;
      const merged = new Uint8Array(total);
      let off = 0;
      for (const c of ready) { merged.set(c, off); off += c.length; }
      const big = total > 1000;
      try {
        t.write(merged, () => {
          // Force a full repaint of the visible rows after a big write. The
          // GPU/canvas renderer can otherwise sit on a stale backbuffer when
          // a burst scrolls many rows at once. Cheap - only on >1000B
          // flushes, not per-keystroke echo.
          if (big && term) {
            try { term.refresh(0, term.rows - 1); } catch { /* ignore */ }
          }
        });
      } catch (err) {
        console.warn("[term] write threw", err);
      }
    }

    const unO = EventsOn(`pty_output:${sid}`, (payload: { b64: string; cum: number }) => {
      const data = fromB64(payload.b64);
      const cum = payload.cum ?? 0;
      if (watermark === null) {
        buffered.push({ data, cum });
      } else {
        queueLive(data, cum);
      }
    });

    (async () => {
      try {
        const snap = await scrollbackIPC(sid);
        if (snap.b64) {
          try { t.write(stripDECRQM(fromB64(snap.b64))); }
          catch (err) { console.warn("[term] snapshot write threw", err); }
        }
        watermark = snap.cum ?? 0;
      } catch {
        watermark = 0;
      }
      flushIfReady();
      // Force a repaint after the initial dump - WebGL renderer can
      // otherwise sit on a stale backbuffer until the user clicks.
      requestAnimationFrame(() => {
        fit?.fit();
        t.refresh(0, t.rows - 1);
      });
    })();

    const unD = EventsOn(`session_debug:${sid}`, (line: string) => {
      term?.writeln(`\x1b[33m[debug] ${line}\x1b[0m`);
    });
    const unS = EventsOn(`session_state:${sid}`, (p: any) => {
      if (p.state === "connecting") sessions.setStatus(sid, "connecting");
      else if (p.state === "auth_in_progress") sessions.setStatus(sid, "connecting", p.hint);
      else if (p.state === "connected") {
        sessions.setStatus(sid, "connected");
        setTimeout(() => notifyResize(), 50);
      }
      else if (p.state === "disconnected") {
        sessions.setStatus(sid, "disconnected", p.reason);
        term?.writeln(`\r\n\x1b[33m[session closed: ${p.reason}]\x1b[0m`);
      } else if (p.state === "error") {
        sessions.setStatus(sid, "error", p.message);
        term?.writeln(`\r\n\x1b[31m[error: ${p.message}]\x1b[0m`);
      }
    });

    // After a reconnect we want the new session's prompt drawn: refit so
    // the shell sees the right cols/rows and emits a prompt redraw.
    setTimeout(() => notifyResize(), 50);

    // Cancel any pending coalesce frame on teardown so we don't write into
    // a torn-down terminal after a sessionId swap / unmount.
    const cancelRaf = () => {
      if (rafPending) { cancelAnimationFrame(rafPending); rafPending = 0; }
      pending = [];
      gapFrames = 0;
    };
    wiredUnsubs = [unO, unD, unS, cancelRaf];
    // Do NOT return a cleanup function from this $effect - that would
    // run on every rerun. Cleanup is in unwire() above, called from
    // onDestroy or on a real sessionId swap.
  });

  $effect(() => {
    if (active && term && fit) {
      requestAnimationFrame(() => { fit?.fit(); focusWhenVisible(); });
    }
  });

  // React to global font-size changes (Ctrl+wheel on any other terminal,
  // or the settings page when that lands). Refit after each change so
  // the cols/rows recalculate against the new cell metrics.
  $effect(() => {
    const size = terminalPrefs.fontSize;
    if (!term || !fit) return;
    if (term.options.fontSize === size) return;
    term.options.fontSize = size;
    requestAnimationFrame(() => {
      fit?.fit();
      // The glyph atlas was rasterised at the old cell size; without
      // dropping it the new size paints over stale glyphs (the garbled /
      // overlapping "hieroglyph" text). Clear it so every cell re-renders.
      clearWebglAtlas();
      if (active) notifyResize();
    });
  });

  // Toggling broadcast adds an inset border + badge to .term-wrap, which
  // has been observed to leave the WebGL atlas in a corrupted state on some
  // GPUs (the "hieroglyph" text). Drop the atlas on any broadcast
  // membership change so the next paint is clean. Cheap - fires only on
  // toggle, not per frame.
  $effect(() => {
    // Touch the reactive broadcast state so this effect tracks it.
    void broadcast.hasInAnyGroup(sessionId);
    void broadcast.totalMembers();
    if (term) requestAnimationFrame(() => clearWebglAtlas());
  });

  // React to theme changes. xterm rerenders on options.theme assignment.
  $effect(() => {
    const t = terminalPrefs.theme;
    if (!term) return;
    term.options.theme = themeToXterm(t);
  });

  // React to font family changes - applies live to every open terminal.
  $effect(() => {
    const ff = terminalPrefs.fontFamily;
    if (!term || !fit) return;
    if (term.options.fontFamily === ff) return;
    term.options.fontFamily = ff;
    requestAnimationFrame(() => {
      fit?.fit();
      if (active) notifyResize();
    });
  });

  // React to scrollback limit changes. xterm needs the option set; we
  // also clear the buffer to avoid the old (potentially larger) ring
  // sticking around as a dangling allocation.
  $effect(() => {
    const sb = terminalPrefs.scrollback;
    if (!term) return;
    if (term.options.scrollback === sb) return;
    term.options.scrollback = sb;
  });

  // Ctrl+wheel zoom. Capture phase + preventDefault so the browser
  // doesn't try to page-zoom the whole webview behind our backs.
  function onWheel(e: WheelEvent) {
    if (!e.ctrlKey) return;
    e.preventDefault();
    e.stopPropagation();
    const dir = e.deltaY > 0 ? -1 : 1;
    terminalPrefs.bumpFontSize(dir);
  }

  // ---------- pinch-to-zoom (touch font size) ----------
  // Two-finger pinch adjusts the terminal font size, the touch analog of
  // Ctrl+wheel. We track the two active pointers, and each time their
  // distance crosses a per-step threshold we bump the font by one. The
  // step accumulator avoids jitter and keeps one pinch == a few sizes,
  // not dozens.
  const activePointers = new Map<number, { x: number; y: number }>();
  let pinchBaseDist = 0;
  // px of pinch-distance change per 1pt font step.
  const PINCH_STEP_PX = 28;

  function pinchDistance(): number {
    const pts = [...activePointers.values()];
    if (pts.length < 2) return 0;
    const dx = pts[0].x - pts[1].x;
    const dy = pts[0].y - pts[1].y;
    return Math.hypot(dx, dy);
  }

  function onPointerDown(e: PointerEvent) {
    if (e.pointerType !== "touch") return;
    activePointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (activePointers.size === 2) {
      pinchBaseDist = pinchDistance();
    } else if (activePointers.size === 1) {
      // Single-finger tap: focus the terminal so the soft keyboard opens.
      // xterm's own touch focus is unreliable inside the Android WebView;
      // focus its hidden textarea directly within this user gesture so the
      // IME is raised. Falling back to term.focus() if the textarea isn't
      // found.
      const ta = host?.querySelector<HTMLTextAreaElement>("textarea.xterm-helper-textarea");
      if (ta) ta.focus();
      else term?.focus();
    }
  }

  function onPointerMove(e: PointerEvent) {
    if (e.pointerType !== "touch") return;
    if (!activePointers.has(e.pointerId)) return;
    activePointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (activePointers.size !== 2 || pinchBaseDist === 0) return;
    e.preventDefault();
    const dist = pinchDistance();
    const steps = Math.trunc((dist - pinchBaseDist) / PINCH_STEP_PX);
    if (steps !== 0) {
      terminalPrefs.bumpFontSize(steps);
      // Re-base by the consumed steps so a slow pinch keeps stepping.
      pinchBaseDist += steps * PINCH_STEP_PX;
    }
  }

  function onPointerUp(e: PointerEvent) {
    if (e.pointerType !== "touch") return;
    activePointers.delete(e.pointerId);
    if (activePointers.size < 2) pinchBaseDist = 0;
  }

  onDestroy(() => {
    resizeObs?.disconnect();
    host?.removeEventListener("paste", onHostPaste, { capture: true } as any);
    host?.removeEventListener("wheel", onWheel, { capture: true } as any);
    host?.removeEventListener("contextmenu", onContextMenu, { capture: true } as any);
    host?.removeEventListener("mousedown", onMouseDown, { capture: true } as any);
    host?.removeEventListener("pointerdown", onPointerDown, { capture: true } as any);
    host?.removeEventListener("pointermove", onPointerMove, { capture: true } as any);
    host?.removeEventListener("pointerup", onPointerUp, { capture: true } as any);
    host?.removeEventListener("pointercancel", onPointerUp, { capture: true } as any);
    if (onSearchEvent) window.removeEventListener("terminal:search", onSearchEvent);
    webgl?.dispose();
    canvas?.dispose();
    search?.dispose();
    webLinks?.dispose();
    term?.dispose();
  });
</script>

<div class="term-wrap" class:active class:broadcast={broadcast.hasInAnyGroup(sessionId) && broadcast.totalMembers() > 1}>
  {#if broadcast.hasInAnyGroup(sessionId) && broadcast.totalMembers() > 1}
    {@const bcGroups = broadcast.groupsOf(sessionId).map((g) => g === "" ? "default" : g)}
    <span class="bc-label" title={`Broadcasting to: ${bcGroups.join(", ")}`}>
      ⊕ BROADCAST
      {#if bcGroups.length > 0}
        <span class="bc-label-groups">{bcGroups.join(", ")}</span>
      {/if}
    </span>
  {/if}
  {#if searchOpen}
    <div class="search-bar">
      <input
        bind:this={searchEl}
        bind:value={searchQuery}
        placeholder="Find in scrollback…"
        onkeydown={(e) => {
          if (e.key === "Escape") { e.preventDefault(); closeSearch(); }
          else if (e.key === "Enter") {
            e.preventDefault();
            if (e.shiftKey) findPrev(); else findNext();
          }
        }}
      />
      <button onclick={findPrev} title="Previous (Shift+Enter / Shift+F3)">↑</button>
      <button onclick={findNext} title="Next (Enter / F3)">↓</button>
      <button onclick={closeSearch} title="Close (Esc)">✕</button>
    </div>
  {/if}
  <!-- Host background tracks the terminal theme so the padding and
       the sub-row remainder under the last line render as terminal
       colour instead of the app theme's grey strip (obvious on the
       light UI theme above the status bar). -->
  <div class="term-host" bind:this={host} style:background={terminalPrefs.theme.background ?? "transparent"}></div>
  {#if isMobile}
    <TermKeyBar send={sendKeys} />
  {/if}
</div>

{#if pendingPasteText !== null}
  <PasteGuard
    text={pendingPasteText}
    onConfirm={confirmPaste}
    onCancel={cancelPaste}
  />
{/if}

<style>
  .term-wrap {
    width: 100%; height: 100%;
    display: none;
    flex-direction: column;
    min-height: 0;
  }
  .term-wrap.active { display: flex; }
  /* Broadcast indicator - orange 2px inset shadow + small banner at
     the top. Loud enough that you can't accidentally type into a
     fan-out session without seeing it; not so loud that it occludes
     the terminal output. */
  .term-wrap.broadcast {
    box-shadow: inset 0 0 0 2px var(--peach);
    position: relative;
  }
  .bc-label {
    position: absolute;
    top: 0;
    right: 0;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    background: var(--peach);
    color: var(--on-accent);
    font-size: 0.65rem;
    font-weight: 600;
    letter-spacing: 0.08em;
    padding: 0.1rem 0.4rem;
    border-bottom-left-radius: 3px;
    z-index: 10;
    pointer-events: none;
  }
  .bc-label-groups {
    font-weight: 500;
    letter-spacing: 0;
    background: rgba(0, 0, 0, 0.18);
    padding: 0 0.3rem;
    border-radius: 6px;
    max-width: 14ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .term-host {
    flex: 1;
    min-height: 0;
    /* Overflow hidden so the renderer's canvas - which xterm sizes
       in whole-row increments after fit() - can't bleed past the
       host edge when the parent container shrinks to a height
       that isn't an exact multiple of cellHeight. Without this
       the last row painted partial pixels into whatever was
       under the host. */
    overflow: hidden;
    /* Bottom padding was 0.85rem as descender-clip insurance while
       the fit math estimated cell height; now that fit reads the
       renderer's real cell height the canvas never pokes past, so a
       uniform inset is enough. The host background tracks the
       terminal theme, so whatever sliver remains is invisible. */
    padding: 0.5rem;
  }
  /* Force the xterm wrapper inside our host to never extend past
     its container. xterm sets explicit width/height in pixels on
     .xterm based on the host's clientRect at fit() time; if the
     parent then shrinks, those inline dimensions stay set and the
     overflow can clip mid-glyph. max-height: 100% lets the inline
     style act as a hint, not a hard constraint. */
  :global(.term-host .xterm) {
    max-height: 100%;
    max-width: 100%;
  }
  /* xterm's scroll viewport is overflow-y:scroll, so WebView2 paints
     its default (white, theme-ignorant) scrollbar - which shows up as
     a stray arrow in the corner when the host height isn't an exact
     multiple of the row height. Style it: a thin, theme-coloured,
     unobtrusive thumb with no arrow buttons. */
  :global(.term-host .xterm-viewport) {
    scrollbar-width: thin;
    scrollbar-color: var(--surface2) transparent;
  }
  :global(.term-host .xterm-viewport::-webkit-scrollbar) {
    width: 8px;
    height: 8px;
  }
  :global(.term-host .xterm-viewport::-webkit-scrollbar-thumb) {
    background: var(--surface2);
    border-radius: 4px;
  }
  :global(.term-host .xterm-viewport::-webkit-scrollbar-track) {
    background: transparent;
  }
  :global(.term-host .xterm-viewport::-webkit-scrollbar-button) {
    display: none;
    width: 0;
    height: 0;
  }
  .search-bar {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.25rem 0.4rem;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
    flex-shrink: 0;
  }
  .search-bar input {
    flex: 1;
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.2rem 0.4rem;
    font: inherit;
    font-size: 0.78rem;
  }
  .search-bar input:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .search-bar button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.15rem 0.45rem;
    cursor: pointer; font: inherit; font-size: 0.78rem;
  }
  .search-bar button:hover { background: var(--surface1); }
  :global(.xterm) { height: 100%; width: 100%; }
  :global(.xterm-viewport) { background-color: transparent !important; }
</style>
