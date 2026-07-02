<script lang="ts">
  // In-app log tail. Mounted on first open of the Settings → Logs
  // panel. Pulls a snapshot via AppGetLogs and then listens for
  // "app_log" events to append live entries.

  import { onMount, onDestroy, tick } from "svelte";
  import { api } from "./api";
  import { EventsOn } from "./wailsRuntime";

  let lines = $state<string[]>([]);
  let unsub: (() => void) | null = null;
  let scrollEl: HTMLDivElement | undefined = $state();
  let autoScroll = $state(true);
  let filter = $state("");
  let tailEnabled = $state(true);

  async function toggleTail() {
    const next = !tailEnabled;
    await api.appSetLogTailEnabled(next);
    tailEnabled = next;
  }

  // The filter narrows what gets displayed but never drops anything
  // from the underlying buffer.
  const filtered = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return lines;
    return lines.filter((l) => l.toLowerCase().includes(q));
  });

  async function refresh() {
    lines = await api.appGetLogs();
  }

  onMount(async () => {
    tailEnabled = await api.appGetLogTailEnabled();
    await refresh();
    unsub = EventsOn("app_log", (line: string) => {
      lines = [...lines, line];
      if (autoScroll) {
        tick().then(() => {
          if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
        });
      }
    });
    // Initial scroll to bottom so newest entries are visible.
    await tick();
    if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
    // Clamp on document mouseup: a drag that escapes the box releases the
    // mouse outside it, so the listener has to be global.
    document.addEventListener("mouseup", clampSelectionToBox);
  });

  onDestroy(() => {
    unsub?.();
    document.removeEventListener("mouseup", clampSelectionToBox);
  });

  async function clear() {
    await api.appClearLogs();
    lines = [];
  }

  function onScroll() {
    if (!scrollEl) return;
    const atBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 8;
    autoScroll = atBottom;
  }

  // Keep a drag-selection that STARTS in the log box from escaping it
  // (selecting the path/prose above or below). On mouseup, if the
  // selection is anchored inside the box but its focus end landed
  // outside, clamp the focus back to the box edge so the copy contains
  // only log lines.
  function clampSelectionToBox() {
    if (!scrollEl) return;
    const sel = window.getSelection();
    if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return;
    const anchorNode = sel.anchorNode;
    const focusNode = sel.focusNode;
    if (!anchorNode || !focusNode) return;
    const anchorIn = scrollEl.contains(anchorNode);
    const focusIn = scrollEl.contains(focusNode);
    if (!anchorIn || focusIn) return; // not anchored here, or already contained
    // Selection started in the box and its focus escaped. Keep the anchor
    // where it is and move the focus to the box edge in the drag
    // direction, so the selection still spans every log line up to that
    // edge (including the last one) - extend() preserves the anchor and
    // only relocates the focus, which is more robust than rebuilding the
    // range by hand.
    const escapedUpward =
      (scrollEl.compareDocumentPosition(focusNode) & Node.DOCUMENT_POSITION_PRECEDING) !== 0;
    try {
      if (escapedUpward) {
        // Focus to the very start of the box content.
        sel.extend(scrollEl, 0);
      } else {
        // Focus to the end of the last child (the last log line), past
        // its final text so the whole line is included.
        const last = scrollEl.lastElementChild ?? scrollEl;
        sel.extend(last, last.childNodes.length);
      }
    } catch {
      /* anchor moved out from under us; leave the selection as-is */
    }
  }

  // Ctrl/Cmd+A selects only the log lines, not the whole app GUI.
  function onLogKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && (e.key === "a" || e.key === "A")) {
      if (!scrollEl) return;
      e.preventDefault();
      const sel = window.getSelection();
      if (sel) {
        sel.removeAllRanges();
        const range = document.createRange();
        range.selectNodeContents(scrollEl);
        sel.addRange(range);
      }
    }
  }

  function classOf(line: string): string {
    // Tag obvious error/warn lines so they stand out. log.Printf
    // output we emit doesn't include a level prefix so this is best-
    // effort string matching.
    const l = line.toLowerCase();
    if (l.includes("error") || l.includes(" err ") || l.includes("failed")) return "err";
    if (l.includes("warn")) return "warn";
    if (l.includes("debug")) return "dim";
    return "";
  }
</script>

<div class="log-viewer">
  <div class="toolbar">
    <input
      class="filter"
      type="text"
      placeholder="Filter…"
      bind:value={filter}
    />
    <span class="count">{filtered.length} / {lines.length}</span>
    <button onclick={refresh} title="Reload from buffer">↻</button>
    <button onclick={clear} title="Clear buffer">Clear</button>
    <label class="auto" title="Collect log lines into the ring buffer + emit live events">
      <input type="checkbox" checked={tailEnabled} onchange={toggleTail} />
      <span>Enabled</span>
    </label>
    <label class="auto">
      <input type="checkbox" bind:checked={autoScroll} />
      <span>Auto-scroll</span>
    </label>
  </div>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="scroll selectable"
    bind:this={scrollEl}
    onscroll={onScroll}
    tabindex="0"
    role="log"
    onkeydown={onLogKeydown}
  >
    {#each filtered as l, i (i)}
      <div class="line {classOf(l)}">{l}</div>
    {/each}
    {#if filtered.length === 0}
      <div class="empty">No log lines{filter ? " matching the filter" : " yet"}.</div>
    {/if}
  </div>
</div>

<style>
  .log-viewer {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    height: 360px;
    min-height: 0;
  }
  .toolbar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.4rem;
    padding: 0.4rem 0.5rem;
    border-bottom: 1px solid var(--surface0);
    font-size: 0.78rem;
  }
  .filter {
    flex: 1;
    min-width: 7rem;
    background: var(--crust);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.2rem 0.4rem;
    font: inherit;
    font-size: 0.78rem;
  }
  .filter:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .count { color: var(--overlay0); font-size: 0.72rem; }
  .auto { display: flex; align-items: center; gap: 0.25rem; color: var(--subtext0); }
  .toolbar button {
    background: var(--surface0);
    color: var(--text);
    border: 0;
    border-radius: 3px;
    padding: 0.2rem 0.55rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.78rem;
  }
  .toolbar button:hover { background: var(--surface1); }
  .scroll {
    flex: 1;
    overflow-y: auto;
    padding: 0.3rem 0.5rem;
    font-family: ui-monospace, monospace;
    font-size: 0.72rem;
    line-height: 1.45;
  }
  .line {
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .line.err { color: var(--red); }
  .line.warn { color: var(--yellow); }
  .line.dim { color: var(--overlay0); }
  .empty { color: var(--overlay0); font-style: italic; padding: 0.4rem 0; }
</style>
