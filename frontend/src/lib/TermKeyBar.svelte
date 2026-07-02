<script lang="ts">
  // On-screen key accessory bar for the terminal on touch devices. The soft
  // keyboard has no Esc/Tab/Ctrl/Alt/arrows/function keys, which makes a
  // shell unusable on a phone. This bar sends the right byte sequences
  // through the same path as typed input.
  //
  // Ctrl and Alt latch: tap Ctrl, then tap a letter -> sends the control
  // byte (Ctrl+C = 0x03). Alt prefixes ESC. Latches are sticky for one
  // following key (or until tapped off), matching how hardware modifiers
  // feel on a soft keyboard.

  import { keyBarMods } from "./keyBarMods.svelte";

  interface Props {
    // Sends a raw string (already the bytes to write) to the PTY.
    send: (data: string) => void;
  }
  let { send }: Props = $props();

  // Ctrl/Alt latch lives in a shared store so the Terminal's onData can
  // apply it to letters typed on the soft keyboard (which bypass this bar).
  // These getters/setters keep the rest of the component readable.
  const mods = keyBarMods;

  // Send a "normal" key press, applying any latched Ctrl/Alt. ch is the
  // base character (e.g. "c") or a ready escape sequence for nav keys.
  function key(seq: string, isControlChar = false) {
    let out = seq;
    if (mods.ctrl && isControlChar && seq.length === 1) {
      const c = seq.toLowerCase().charCodeAt(0);
      // Ctrl+A..Z -> 1..26; Ctrl+[ \ ] ^ _ map to 27..31; Ctrl+Space -> 0.
      if (c >= 97 && c <= 122) out = String.fromCharCode(c - 96);
      else if (seq === " ") out = "\x00";
    }
    if (mods.alt) out = "\x1b" + out;
    send(out);
    mods.clear();
  }

  // Direct escape-sequence keys (navigation / control) - these ignore the
  // Ctrl latch unless noted, but still honour Alt-prefix.
  const ESC = "\x1b";
  function esc() { key(ESC); }
  function tab() { send(mods.alt ? ESC + "\t" : "\t"); mods.clear(); }

  function nav(seq: string) {
    // Alt+arrow etc.: prefix ESC. Arrows are already ESC sequences; we
    // prepend a second ESC for the Alt-modified form, which most shells
    // read as word-wise movement when bound.
    send(mods.alt ? ESC + seq : seq);
    mods.clear();
  }

  // The key rows. Each entry: label + an action.
  type Key = { label: string; run: () => void; wide?: boolean; toggle?: "ctrl" | "alt"; on?: boolean };

  const row1 = $derived<Key[]>([
    { label: "Esc", run: esc },
    { label: "Tab", run: tab },
    { label: "Ctrl", run: () => (mods.ctrl = !mods.ctrl), toggle: "ctrl", on: mods.ctrl },
    { label: "Alt", run: () => (mods.alt = !mods.alt), toggle: "alt", on: mods.alt },
    { label: "/", run: () => key("/") },
    { label: "-", run: () => key("-") },
    { label: "|", run: () => key("|") },
    { label: "~", run: () => key("~") },
  ]);

  const row2: Key[] = [
    { label: "↑", run: () => nav("\x1b[A") },
    { label: "↓", run: () => nav("\x1b[B") },
    { label: "←", run: () => nav("\x1b[D") },
    { label: "→", run: () => nav("\x1b[C") },
    { label: "Home", run: () => nav("\x1b[H") },
    { label: "End", run: () => nav("\x1b[F") },
    { label: "PgUp", run: () => nav("\x1b[5~") },
    { label: "PgDn", run: () => nav("\x1b[6~") },
  ];

  // sendCtrl sends a control byte directly (Ctrl+letter -> 1..26),
  // regardless of the latch. Used by the dedicated ^C/^D/... buttons which
  // are always "Ctrl + that letter".
  function sendCtrl(ch: string) {
    const c = ch.toLowerCase().charCodeAt(0);
    if (c >= 97 && c <= 122) {
      send(String.fromCharCode(c - 96));
    }
    mods.clear();
  }

  // Control combos most commonly needed in a shell: interrupt, EOF,
  // suspend, clear, reverse-search, line-start/end, kill-line/word.
  const ctrlLetters = ["c", "d", "z", "l", "r", "a", "e", "u", "k", "w"];

  // Tap vs scroll. The bar scrolls horizontally, so firing on pointerdown
  // sent a key whenever the user swiped to reach a button. Instead record
  // the down position and fire the action on pointerup only if the pointer
  // barely moved (a real tap, not a scroll-drag).
  const TAP_SLOP = 10; // px
  let downX = 0;
  let downY = 0;
  let downId = -1;

  function onBtnPointerDown(e: PointerEvent) {
    // Prevent the button from stealing focus: if it does, the xterm
    // textarea blurs and the soft keyboard closes (then reopens on the next
    // focus - the flicker). preventDefault on pointerdown keeps focus where
    // it is while we still get pointerup for tap detection.
    e.preventDefault();
    downId = e.pointerId;
    downX = e.clientX;
    downY = e.clientY;
  }

  function onBtnPointerUp(e: PointerEvent, run: () => void) {
    if (e.pointerId !== downId) return;
    downId = -1;
    const moved = Math.hypot(e.clientX - downX, e.clientY - downY);
    if (moved <= TAP_SLOP) {
      e.preventDefault();
      run();
    }
  }
</script>

<div class="keybar" role="toolbar" aria-label="Terminal keys">
  <div class="krow">
    {#each row1 as k (k.label)}
      <button
        class="kbtn"
        class:toggle={k.toggle}
        class:on={k.on}
        tabindex="-1"
        onpointerdown={onBtnPointerDown}
        onpointerup={(e) => onBtnPointerUp(e, k.run)}
      >{k.label}</button>
    {/each}
  </div>
  <div class="krow">
    {#each row2 as k (k.label)}
      <button
        class="kbtn"
        tabindex="-1"
        onpointerdown={onBtnPointerDown}
        onpointerup={(e) => onBtnPointerUp(e, k.run)}
      >{k.label}</button>
    {/each}
    {#each ctrlLetters as ch (ch)}
      <button
        class="kbtn ctrlletter"
        tabindex="-1"
        onpointerdown={onBtnPointerDown}
        onpointerup={(e) => onBtnPointerUp(e, () => sendCtrl(ch))}
      >^{ch.toUpperCase()}</button>
    {/each}
  </div>
</div>

<style>
  .keybar {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.3rem 0.35rem;
    background: var(--bg-elevated);
    border-top: 1px solid var(--border);
    /* Sits above the soft keyboard; the terminal host shrinks to fit. */
    flex: 0 0 auto;
    user-select: none;
    -webkit-user-select: none;
  }
  .krow {
    display: flex;
    gap: 0.25rem;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .krow::-webkit-scrollbar { display: none; }
  .kbtn {
    flex: 1 0 auto;
    min-width: 2.4rem;
    padding: 0.45rem 0.4rem;
    background: var(--surface0);
    color: var(--text);
    border: 1px solid var(--border-soft);
    border-radius: 5px;
    font: inherit;
    font-size: 0.82rem;
    line-height: 1;
    cursor: pointer;
    touch-action: manipulation;
  }
  .kbtn:active { background: var(--surface1); }
  .kbtn.ctrlletter { color: var(--subtext0); font-family: ui-monospace, monospace; }
  .kbtn.toggle.on {
    background: var(--accent);
    color: var(--on-accent);
    border-color: var(--accent);
  }
</style>
