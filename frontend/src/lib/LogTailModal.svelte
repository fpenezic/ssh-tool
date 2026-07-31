<script lang="ts">
  // Live remote log follow (journalctl -f / tail -F) against the active
  // session's target host. Mirrors TcpdumpModal's lifecycle: the backend
  // stream is a goroutine keyed by session that survives this view unmounting,
  // so a detached window re-attaches to it via a ring snapshot + watermark.
  //
  // Simpler than tcpdump: no decode/insights, just lines with a client-side
  // grep/highlight filter. Auth uses the shared root/sudo probe (many logs
  // need root); the sudo password is auto-fed from the connection when saved.

  import { onMount, onDestroy } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api, type TcpdumpProbeResult } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import { clickOutside } from "./clickOutside";
  import { copyText } from "./clipboard";
  import { IconCopy } from "./iconMap";
  import { parseLine, parseQuery, groupLines, suggestQuery, type ParsedLine, type QuerySuggestion } from "./logparse";

  interface LogTailStats {
    source: string;
    lines: number;
    running: boolean;
  }
  interface Props {
    sessionId: string;
    onClose: () => void;
    hidden?: boolean;
    onMinimize?: () => void;
    onStats?: (s: LogTailStats) => void;
    embedded?: boolean;
    onDetach?: () => void;
  }
  let { sessionId, onClose, hidden = false, onMinimize, onStats, embedded = false, onDetach }: Props = $props();

  // ---- start form ----
  type Kind = "journal" | "file";
  let kind = $state<Kind>("journal");
  let unit = $state("");
  let path = $state("");
  let lines = $state(200);

  let probe = $state<TcpdumpProbeResult | null>(null);
  let probeErr = $state<string | null>(null);
  let useSavedPassword = $state(true);
  // Password prompt (sudo). Shown when the backend signals needs_password and
  // we have no saved candidate to auto-feed.
  let pwdPrompt = $state(false);
  let pwd = $state("");

  // ---- live state ----
  let running = $state(false);
  let tailId = $state<string | null>(null);
  let rawLines = $state<string[]>([]);
  let totalLines = $state(0);
  let startErr = $state<string | null>(null);
  // Client-side filter. A bare word still greps (case-insensitive substring),
  // but the box also understands a tiny query language - level>=warn,
  // status=500, -noise, "quoted phrase" (see logparse.parseQuery). The filter
  // never touches the backend stream: the full history stays in rawLines so
  // clearing it restores everything.
  let filter = $state("");
  // "Group similar" collapses repeated lines (same message modulo variables)
  // into one row with a count - the noise killer. Off by default.
  let grouped = $state(false);
  let showHelp = $state(false); // operators cheat-sheet popover
  // Cap the retained lines so a long-running follow can't grow the DOM
  // unbounded. Matches the backend ring cap.
  const RENDER_CAP = 2000;

  // Dedupe watermark for re-attach: on attach we snapshot the ring and drop
  // any live line whose seq <= this.
  let attachWatermark = 0;

  let unsubLine: (() => void) | null = null;
  let unsubEvent: (() => void) | null = null;

  // Autoscroll that sticks to the bottom while following, but yields to the
  // user: if they scroll up to read history, new lines stop yanking them down
  // until they scroll back near the bottom. stuck tracks that intent.
  let linesEl: HTMLDivElement | undefined = $state();
  let stuck = $state(true);
  function onScroll() {
    if (!linesEl) return;
    const gap = linesEl.scrollHeight - linesEl.scrollTop - linesEl.clientHeight;
    stuck = gap < 40; // near enough to the bottom counts as "following"
  }

  const sourceLabel = $derived(kind === "journal" ? (unit || "journal") : (path || "file"));

  // Parse every retained line once; recomputed only when rawLines changes.
  const parsed = $derived<ParsedLine[]>(rawLines.map(parseLine));
  // Compile the filter query once per keystroke, then apply.
  const compiled = $derived(parseQuery(filter));
  const matched = $derived(parsed.filter(compiled));

  // The rendered rows: either grouped (collapsed with counts) or the flat tail,
  // capped so the DOM stays bounded on a long follow.
  const visibleRows = $derived.by(() => {
    if (grouped) {
      const groups = groupLines(matched);
      return groups.length > RENDER_CAP ? groups.slice(groups.length - RENDER_CAP) : groups;
    }
    const flat = matched.map((l) => ({ line: l, count: 1, lastTs: l.ts }));
    return flat.length > RENDER_CAP ? flat.slice(flat.length - RENDER_CAP) : flat;
  });
  const visibleCount = $derived(matched.length);

  $effect(() => {
    onStats?.({ source: sourceLabel, lines: totalLines, running });
  });

  // Stick to the bottom as new lines land, unless the user scrolled up.
  // Depend on totalLines (the cumulative backend counter, always growing) and
  // rawLines - NOT visibleRows.length, which stays flat once the ring is at
  // its cap even as content changes, so the effect would never re-run. The
  // rAF waits out the DOM update so scrollHeight reflects the new lines.
  $effect(() => {
    void totalLines;
    void rawLines;
    void filter;
    void grouped;
    if (!stuck || !linesEl) return;
    requestAnimationFrame(() => {
      if (linesEl && stuck) linesEl.scrollTop = linesEl.scrollHeight;
    });
  });

  onMount(async () => {
    try {
      probe = await api.tcpdumpProbe(sessionId);
    } catch (e: any) {
      probeErr = `Probe failed: ${e?.message ?? e}`;
    }
    // Re-attach to a tail already running for this session (this window just
    // received it via a detach/open).
    try {
      const existing = await api.logtailActiveForSession(sessionId);
      if (existing.tail_id) await attach(existing);
    } catch { /* none - show the start form */ }
  });

  onDestroy(() => {
    cleanupSubs();
    // Deliberately does NOT stop the backend stream: an unmount is a detach.
  });

  function cleanupSubs() {
    unsubLine?.(); unsubLine = null;
    unsubEvent?.(); unsubEvent = null;
  }

  function bind(id: string) {
    unsubLine = EventsOn(`logtail_line_batch:${id}`, (b: any) => {
      const incoming: Array<{ text: string; seq: number }> = b?.lines ?? [];
      const fresh = incoming.filter((l) => l.seq > attachWatermark).map((l) => l.text);
      if (fresh.length) {
        const next = rawLines.concat(fresh);
        rawLines = next.length > RENDER_CAP ? next.slice(next.length - RENDER_CAP) : next;
      }
      totalLines = typeof b?.total === "number" ? b.total : totalLines + incoming.length;
    });
    unsubEvent = EventsOn(`logtail_event:${id}`, (e: any) => {
      const ev = e?.event;
      const msg = e?.msg ?? "";
      if (ev === "needs_password") {
        // If we didn't auto-feed a saved password, ask the user.
        if (!useSavedPassword || !probe?.has_candidate_password) pwdPrompt = true;
      } else if (ev === "started") {
        pwdPrompt = false;
        running = true;
      } else if (ev === "reconnecting") {
        // stream dropped, backend is retrying - keep running=true, note it.
        startErr = msg || "reconnecting...";
      } else if (ev === "ended") {
        running = false;
        if (msg) startErr = msg;
      }
    });
  }

  async function attach(info: { tail_id: string; kind: string; unit: string; path: string }) {
    tailId = info.tail_id;
    kind = (info.kind as Kind) || "journal";
    unit = info.unit || "";
    path = info.path || "";
    try {
      const snap = await api.logtailSnapshot(info.tail_id);
      attachWatermark = snap.cum ?? 0;
      rawLines = (snap.lines ?? []).map((l) => l.text);
      totalLines = snap.cum ?? rawLines.length;
    } catch { /* empty snapshot */ }
    running = true;
    bind(info.tail_id);
  }

  async function start() {
    startErr = null;
    if (kind === "file" && !path.trim()) { startErr = "File path required"; return; }
    try {
      const id = await api.logtailStart({
        session_id: sessionId,
        kind,
        unit: unit.trim(),
        path: path.trim(),
        lines,
        root_user: probe?.root_user ?? false,
        sudo_no_pwd: probe?.sudo_no_pwd ?? false,
        use_saved_password: useSavedPassword,
      });
      tailId = id;
      attachWatermark = 0;
      rawLines = [];
      totalLines = 0;
      bind(id);
      running = true;
    } catch (e: any) {
      startErr = errMsg(e);
    }
  }

  async function submitPassword() {
    if (!tailId || !pwd) return;
    try {
      await api.logtailProvidePassword(tailId, pwd);
      pwd = "";
      pwdPrompt = false;
    } catch (e: any) {
      startErr = errMsg(e);
    }
  }

  function closeCapture() {
    if (tailId) api.logtailStop(tailId).catch(() => {});
    cleanupSubs();
    rawLines = [];
    tailId = null;
    running = false;
    onClose();
  }

  function dismiss() {
    if (hidden) return;
    if (embedded) { onClose(); return; }
    if (onMinimize) onMinimize();
    else closeCapture();
  }

  async function copyAll() {
    try {
      // Copy the raw text of the visible rows (grouped rows copy their
      // representative line, prefixed with the count).
      const text = visibleRows
        .map((r) => (r.count > 1 ? `x${r.count}\t${r.line.raw}` : r.line.raw))
        .join("\n");
      await copyText(text, { label: "Lines" });
    } catch { /* ignore */ }
  }

  // ---- autocomplete ----
  let filterEl = $state<HTMLInputElement | undefined>();
  let suggestions = $state<QuerySuggestion[]>([]);
  let sugIndex = $state(0);

  function refreshSuggestions() {
    const el = filterEl;
    if (!el) { suggestions = []; return; }
    // Sample the tail of the parsed lines so field-value suggestions reflect
    // real data without scanning the whole ring on every keystroke.
    const sample = parsed.length > 300 ? parsed.slice(parsed.length - 300) : parsed;
    suggestions = suggestQuery(filter, el.selectionStart ?? filter.length, sample);
    sugIndex = 0;
  }

  function acceptSuggestion(s: QuerySuggestion) {
    filter = s.insert;
    suggestions = [];
    // Restore focus and put the caret at the end of the inserted token.
    queueMicrotask(() => {
      if (filterEl) {
        filterEl.focus();
        const pos = s.insert.length;
        filterEl.setSelectionRange(pos, pos);
      }
      refreshSuggestions();
    });
  }

  function onFilterKeydown(e: KeyboardEvent) {
    if (suggestions.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      sugIndex = (sugIndex + 1) % suggestions.length;
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      sugIndex = (sugIndex - 1 + suggestions.length) % suggestions.length;
    } else if (e.key === "Enter" || e.key === "Tab") {
      e.preventDefault();
      acceptSuggestion(suggestions[sugIndex]);
    } else if (e.key === "Escape") {
      suggestions = [];
    }
  }
</script>

<div class="overlay" class:hidden class:embedded role="dialog" aria-modal="true" tabindex="-1"
     onkeydown={(e) => { if (e.key === "Escape") dismiss(); }}>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document"
       use:clickOutside={{ onOutside: dismiss, enabled: !embedded }}
       onkeydown={(e) => e.stopPropagation()}>
    <header>
      <strong>log tail</strong>
      <span class="auth-state">
        {#if probe?.root_user}<span class="ok">root</span>
        {:else if probe?.sudo_no_pwd}<span class="ok">sudo</span>
        {:else if probe}<span class="warn">sudo may prompt</span>{/if}
      </span>
      {#if onDetach}
        <button class="detach" onclick={onDetach} title="Open in a separate window - stream keeps running so you can watch it while you type">⧉</button>
      {/if}
      {#if onMinimize}
        <button class="minimize" onclick={onMinimize} title={running ? "Minimise - stream keeps running" : "Minimise"}>-</button>
      {/if}
      <button class="close" onclick={closeCapture} title={running ? "Stop and close" : "Close"}>✕</button>
    </header>

    {#if probeErr}<div class="err">{probeErr}</div>{/if}

    {#if !running && !tailId}
      <div class="startform">
        <div class="kind-row">
          <label><input type="radio" bind:group={kind} value="journal" /> journalctl</label>
          <label><input type="radio" bind:group={kind} value="file" /> tail file</label>
        </div>
        {#if kind === "journal"}
          <label class="fld">
            <span>Unit (blank = whole journal)</span>
            <input bind:value={unit} placeholder="nginx" spellcheck="false" />
          </label>
        {:else}
          <label class="fld">
            <span>Path</span>
            <input bind:value={path} placeholder="/var/log/syslog" spellcheck="false" />
          </label>
        {/if}
        <label class="fld">
          <span>Seed lines</span>
          <input type="number" bind:value={lines} min="0" max="5000" />
        </label>
        {#if probe && !probe.root_user && !probe.sudo_no_pwd && probe.has_candidate_password}
          <label class="chk"><input type="checkbox" bind:checked={useSavedPassword} /> use saved password for sudo</label>
        {/if}
        {#if startErr}<div class="err">{startErr}</div>{/if}
        <button class="primary" onclick={start}>Start</button>
      </div>
    {:else}
      <div class="toolbar">
        <div class="filter-wrap">
          <input
            class="filter"
            bind:this={filterEl}
            bind:value={filter}
            placeholder="filter - grep, or level>=warn status=500 -noise"
            spellcheck="false"
            autocomplete="off"
            oninput={refreshSuggestions}
            onkeydown={onFilterKeydown}
            onfocus={refreshSuggestions}
            onblur={() => setTimeout(() => (suggestions = []), 120)}
          />
          {#if suggestions.length > 0}
            <div class="suggest">
              {#each suggestions as s, i}
                <button
                  type="button"
                  class="sug"
                  class:active={i === sugIndex}
                  onmousedown={(e) => { e.preventDefault(); acceptSuggestion(s); }}
                >
                  <span class="sug-label">{s.label}</span>
                  {#if s.hint}<span class="sug-hint">{s.hint}</span>{/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>
        <label class="chk" title="Collapse repeated lines into one row with a count">
          <input type="checkbox" bind:checked={grouped} /> group
        </label>
        <span class="count">{visibleCount}{filter.trim() ? ` / ${rawLines.length}` : ""} lines · {totalLines} total</span>
        <button class="tb help-btn" onclick={() => (showHelp = !showHelp)} title="Filter operators">?</button>
        <button class="tb" onclick={copyAll} title="Copy visible lines"><IconCopy size={13} /></button>
      </div>
      {#if showHelp}
        <div class="help" use:clickOutside={{ onOutside: () => (showHelp = false) }}>
          <div><code>word</code> substring (case-insensitive)</div>
          <div><code>"a phrase"</code> substring with spaces</div>
          <div><code>-word</code> / <code>!word</code> exclude</div>
          <div><code>level&gt;=warn</code> severity at least (error/warn/info/debug/trace)</div>
          <div><code>level=error</code> exact severity</div>
          <div><code>status=500</code> <code>method=POST</code> match a parsed field</div>
          <div class="help-note">Terms combine with AND. Tab / Enter accepts a suggestion.</div>
        </div>
      {/if}
      {#if startErr}<div class="notice">{startErr}</div>{/if}
      {#if pwdPrompt}
        <div class="pwd">
          <span>sudo password</span>
          <input type="password" bind:value={pwd} onkeydown={(e) => { if (e.key === "Enter") submitPassword(); }} />
          <button onclick={submitPassword}>OK</button>
        </div>
      {/if}
      <div class="lines" bind:this={linesEl} onscroll={onScroll}>
        {#each visibleRows as row}
          <div class="ln level-{row.line.level}">
            {#if row.count > 1}<span class="grp-count">x{row.count}</span>{/if}
            {#if row.line.level !== "none"}<span class="lvl-tag">{row.line.level}</span>{/if}
            <span class="ln-text">{row.line.raw}</span>
          </div>
        {/each}
        {#if visibleRows.length === 0}
          <div class="empty">{filter.trim() ? "No lines match the filter." : "Waiting for log output..."}</div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: flex-start; justify-content: center;
    z-index: 320;
    padding-top: 6vh;
  }
  .overlay.hidden { display: none; }
  .overlay.embedded {
    position: static; background: none; padding: 0; z-index: auto; height: 100%;
    /* Full-height column so the modal (and its flex:1 lines area) get a
       bounded height to scroll within, instead of collapsing to content. */
    display: flex; flex-direction: column; align-items: stretch;
    justify-content: stretch; min-height: 0;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 8px;
    width: min(900px, 95vw); max-height: 85vh;
    display: flex; flex-direction: column; overflow: hidden;
    box-shadow: 0 20px 60px rgba(0,0,0,0.6);
  }
  .overlay.embedded .modal {
    width: 100%; max-width: none; max-height: none; height: 100%;
    flex: 1; min-height: 0;
    border: 0; border-radius: 0; box-shadow: none;
  }
  header {
    display: flex; align-items: center; gap: 0.8rem;
    padding: 0.55rem 0.9rem; background: var(--mantle);
    border-bottom: 1px solid var(--surface0); font-size: 0.9rem;
  }
  .auth-state { flex: 1; font-size: 0.78rem; }
  .auth-state .ok { color: var(--green); }
  .auth-state .warn { color: var(--yellow); }
  .detach, .minimize, .close {
    background: transparent; color: var(--subtext0); border: 0;
    cursor: pointer; font: inherit; padding: 0 0.4rem; line-height: 1;
  }
  .detach { font-size: 1rem; }
  .minimize { font-size: 1.2rem; }
  .detach:hover, .minimize:hover { color: var(--text); }
  .close:hover { color: var(--red); }
  .startform {
    display: flex; flex-direction: column; gap: 0.6rem; padding: 0.9rem;
  }
  .kind-row { display: flex; gap: 1rem; font-size: 0.85rem; }
  .fld { display: flex; flex-direction: column; gap: 0.2rem; font-size: 0.8rem; }
  .fld input {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 4px; padding: 0.35rem 0.5rem;
  }
  .chk { font-size: 0.8rem; display: flex; align-items: center; gap: 0.4rem; }
  .primary {
    align-self: flex-start; background: var(--blue); color: var(--crust);
    border: 0; border-radius: 4px; padding: 0.4rem 1rem; cursor: pointer;
  }
  .toolbar {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.4rem 0.7rem; background: var(--crust);
    border-bottom: 1px solid var(--surface0);
  }
  .filter-wrap { position: relative; flex: 1; }
  .filter {
    width: 100%; box-sizing: border-box; background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 4px; padding: 0.25rem 0.5rem;
    font-size: 0.8rem;
  }
  .suggest {
    position: absolute; top: calc(100% + 2px); left: 0; right: 0; z-index: 10;
    background: var(--mantle); border: 1px solid var(--surface1); border-radius: 4px;
    box-shadow: 0 4px 12px rgba(0,0,0,0.35); max-height: 40vh; overflow-y: auto;
  }
  .sug {
    display: flex; align-items: baseline; gap: 0.6rem; width: 100%;
    background: transparent; border: 0; text-align: left; cursor: pointer;
    padding: 0.28rem 0.55rem; color: var(--text); font-size: 0.78rem;
  }
  .sug.active, .sug:hover { background: var(--surface0); }
  .sug-label { font-family: var(--mono, ui-monospace, monospace); }
  .sug-hint { font-size: 0.68rem; color: var(--overlay1); }
  .chk { display: flex; align-items: center; gap: 0.25rem; font-size: 0.72rem; color: var(--subtext0); white-space: nowrap; cursor: pointer; }
  .help-btn { font-weight: 700; }
  .help {
    position: absolute; z-index: 12; margin: 0.2rem 0.7rem; padding: 0.5rem 0.7rem;
    background: var(--mantle); border: 1px solid var(--surface1); border-radius: 5px;
    box-shadow: 0 4px 12px rgba(0,0,0,0.35); font-size: 0.74rem; line-height: 1.7;
  }
  .help code {
    background: var(--surface0); border-radius: 3px; padding: 0 0.25rem;
    font-family: var(--mono, ui-monospace, monospace);
  }
  .help-note { margin-top: 0.3rem; color: var(--overlay1); font-size: 0.68rem; }
  .count { font-size: 0.72rem; color: var(--overlay1); white-space: nowrap; }
  .tb {
    background: transparent; border: 0; color: var(--subtext0);
    cursor: pointer; padding: 0.1rem 0.3rem;
  }
  .tb:hover { color: var(--text); }
  .notice { padding: 0.3rem 0.7rem; font-size: 0.72rem; color: var(--yellow); }
  .pwd {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.4rem 0.7rem; background: var(--mantle); font-size: 0.8rem;
  }
  .pwd input {
    flex: 1; background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 4px; padding: 0.25rem 0.5rem;
  }
  .pwd button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 4px; padding: 0.25rem 0.7rem; cursor: pointer;
  }
  .lines {
    flex: 1; min-height: 0; overflow-y: auto; padding: 0.4rem 0.7rem;
    font-family: var(--mono, ui-monospace, monospace); font-size: 0.76rem;
    line-height: 1.4; background: var(--base);
  }
  .ln { white-space: pre-wrap; word-break: break-word; }
  /* Severity colours - tinted text, theme-aware. */
  .ln.level-error { color: var(--red); }
  .ln.level-warn { color: var(--yellow); }
  .ln.level-info { color: var(--text); }
  .ln.level-debug { color: var(--overlay1); }
  .ln.level-trace { color: var(--overlay0); }
  .ln.level-none { color: var(--subtext1); }
  .lvl-tag {
    display: inline-block; min-width: 3.4em; margin-right: 0.5rem;
    font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.03em;
    opacity: 0.75; vertical-align: baseline;
  }
  .grp-count {
    display: inline-block; margin-right: 0.4rem; padding: 0 0.3rem;
    background: var(--surface1); color: var(--subtext0); border-radius: 3px;
    font-size: 0.66rem; font-weight: 600;
  }
  .empty { color: var(--overlay0); padding: 1rem 0; text-align: center; }
  .err { color: var(--red); padding: 0.3rem 0.7rem; font-size: 0.8rem; }
</style>
