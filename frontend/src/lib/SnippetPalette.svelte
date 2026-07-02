<script lang="ts">
  // Ctrl+Shift+P opens this. Fuzzy-searches snippets visible for the
  // active session's connection (= global + per-connection scope).
  // Enter sends the snippet body to that session via SnippetSendToSession.
  //
  // Body is sent verbatim plus a trailing \n on the backend. The user is
  // responsible for the safety of what they're firing - there's no
  // "confirm before sending multi-line", because snippets are user-
  // authored and the paste guard already exists for clipboard content.

  import { paneTabs, sessions, view } from "./stores.svelte";
  import { errMsg } from "./connectErrors";
  import { api, type Snippet } from "./api";
  import { fuzzyMatch, highlightSegments, type FuzzyMatch } from "./fuzzy";
  import { clickOutside } from "./clickOutside";
  import { IconClipboardCopy } from "./iconMap";
  import { focusActiveTerminal } from "./terminalFocus";

  interface Props {
    onClose: () => void;
  }
  let { onClose }: Props = $props();

  // The session we'll fire into = active leaf of the active tab.
  // Captured at mount so swap mid-search doesn't surprise the user.
  const activeLeaf = paneTabs.activePane(paneTabs.activeTabId ?? "");
  const activeSessionId = activeLeaf?.sessionId ?? "";
  const activeConnId = $derived(
    sessions.tabs.find((t) => t.sessionId === activeSessionId)?.connectionId ?? ""
  );

  let query = $state("");
  let activeIdx = $state(0);
  let inputEl: HTMLInputElement | undefined = $state();
  let listEl: HTMLDivElement | undefined = $state();
  let all = $state<Snippet[]>([]);
  let loadErr = $state<string | null>(null);

  $effect(() => {
    // Refetch when active connection changes.
    const cid = activeConnId;
    api.snippetsList(cid).then(
      (rows) => { all = rows ?? []; },
      (e) => { loadErr = String(e?.message ?? e); },
    );
  });

  $effect(() => { setTimeout(() => inputEl?.focus(), 0); });

  type Hit = { snip: Snippet; match: FuzzyMatch | null; matchAgainst: string };

  const results = $derived<Hit[]>(rank());

  function rank(): Hit[] {
    const q = query.trim();
    if (!q) {
      // Empty query - show all sorted by use_count desc then name.
      return all.map((s) => ({ snip: s, match: null, matchAgainst: s.name }));
    }
    const out: Hit[] = [];
    for (const s of all) {
      const haystacks = [s.name, s.body.slice(0, 200), ...(s.tags ?? [])];
      let best: FuzzyMatch | null = null;
      let bestAgainst = s.name;
      for (const h of haystacks) {
        if (!h) continue;
        const m = fuzzyMatch(q, h);
        if (m && (!best || m.score > best.score)) {
          best = m;
          bestAgainst = h;
        }
      }
      if (best) out.push({ snip: s, match: best, matchAgainst: bestAgainst });
    }
    out.sort((a, b) => (b.match?.score ?? 0) - (a.match?.score ?? 0));
    return out;
  }

  $effect(() => {
    // Reset active row on query change.
    void query;
    activeIdx = 0;
  });

  $effect(() => {
    if (results.length === 0) return;
    if (activeIdx >= results.length) activeIdx = results.length - 1;
    if (activeIdx < 0) activeIdx = 0;
    const row = listEl?.querySelector(`[data-idx="${activeIdx}"]`);
    row?.scrollIntoView({ block: "nearest" });
  });

  async function fire(snip: Snippet) {
    if (!activeSessionId) {
      onClose();
      return;
    }
    try {
      await api.snippetSendToSession(snip.id, activeSessionId);
    } catch (e: any) {
      loadErr = errMsg(e);
      return;
    }
    // Switch to the Terminal view so the user sees the snippet
    // land - the palette can be triggered from Connections /
    // Credentials / Settings now too. Backend fans the payload to
    // every broadcast member; this only changes which pane is on
    // top.
    view.setTab("terminal");
    onClose();
    // Restore focus to the active xterm so the user can keep typing
    // without an extra click. Shared helper handles the rAF timing
    // needed to wait out the modal teardown + view display flip.
    focusActiveTerminal();
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") { onClose(); return; }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = Math.min(results.length - 1, activeIdx + 1);
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = Math.max(0, activeIdx - 1);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      const r = results[activeIdx];
      if (r) fire(r.snip);
      return;
    }
  }
</script>

<svelte:window onkeydown={onKey} />

<div
  class="overlay"
  role="dialog"
  aria-modal="true"
  onkeydown={(e) => { if (e.key === "Escape") onClose(); }}
  tabindex="-1"
>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="modal"
    role="document"
    use:clickOutside={{ onOutside: onClose }}
    onkeydown={(e) => {
      // Handle navigation here so the input's own keystrokes don't
      // get to bubble all the way up to the window listener (and
      // out into the QuickPalette / global shortcut paths). The
      // arrow keys / Enter / Escape are the ones we care about;
      // everything else can propagate so e.g. text input works.
      if (e.key === "ArrowDown" || e.key === "ArrowUp" ||
          e.key === "Enter" || e.key === "Escape") {
        onKey(e);
        e.stopPropagation();
      }
    }}
  >
    <input
      bind:this={inputEl}
      bind:value={query}
      placeholder="Search snippets - Enter to fire into the active terminal"
      spellcheck="false"
      autocomplete="off"
    />
    {#if loadErr}<div class="err">{loadErr}</div>{/if}
    <div class="list" bind:this={listEl}>
      {#if !activeSessionId}
        <div class="empty">No active terminal - open a connection first.</div>
      {:else if results.length === 0 && !loadErr}
        <div class="empty">
          {all.length === 0
            ? "No snippets yet. Add some under Settings → Snippets."
            : "No matches."}
        </div>
      {/if}
      {#each results as r, i (r.snip.id)}
        {@const segs = r.match ? highlightSegments(r.matchAgainst, r.match.positions) : [{ text: r.matchAgainst, match: false }]}
        <div
          class="row"
          class:active={i === activeIdx}
          data-idx={i}
          role="button"
          tabindex="0"
          onclick={() => fire(r.snip)}
          onmousemove={() => (activeIdx = i)}
          onkeydown={(e) => {
            if (e.key === "Enter") { e.preventDefault(); fire(r.snip); }
          }}
        >
          <span class="icon"><IconClipboardCopy size={14} /></span>
          <div class="meta">
            <div class="label">
              {#each segs as s}
                {#if s.match}<mark>{s.text}</mark>{:else}<span>{s.text}</span>{/if}
              {/each}
              {#if r.snip.connection_id}<span class="scope">scoped</span>{/if}
            </div>
            <div class="sub">{r.snip.body.split("\n")[0].slice(0, 100)}{r.snip.body.length > 100 || r.snip.body.includes("\n") ? "…" : ""}</div>
          </div>
          <span class="hint">↵ fire</span>
        </div>
      {/each}
    </div>
    <footer>
      <span><kbd>↑↓</kbd> navigate</span>
      <span><kbd>↵</kbd> send</span>
      <span><kbd>Esc</kbd> close</span>
    </footer>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: flex-start; justify-content: center;
    z-index: 310;
    padding-top: 12vh;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 8px;
    width: min(640px, 92vw);
    max-height: 70vh;
    display: flex; flex-direction: column;
    overflow: hidden;
    box-shadow: 0 20px 60px rgba(0,0,0,0.6);
  }
  input {
    background: var(--mantle); color: var(--text);
    border: 0;
    border-bottom: 1px solid var(--surface0);
    padding: 0.7rem 0.9rem;
    font: inherit;
    outline: none;
  }
  .list {
    overflow-y: auto;
    min-height: 0;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.5rem 0.9rem;
    cursor: pointer;
    border-left: 3px solid transparent;
  }
  .row.active {
    background: var(--surface0);
    border-left-color: var(--blue);
  }
  .icon { color: var(--subtext0); display: inline-flex; }
  .meta { flex: 1; min-width: 0; }
  .label {
    color: var(--text);
    font-size: 0.92rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .label mark {
    background: transparent;
    color: var(--blue);
    font-weight: 600;
  }
  .sub {
    color: var(--overlay0);
    font-size: 0.78rem;
    font-family: ui-monospace, monospace;
    margin-top: 0.1rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .scope {
    font-size: 0.65rem;
    background: var(--surface0);
    color: var(--yellow);
    padding: 0.05rem 0.35rem;
    border-radius: 999px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .hint {
    color: var(--overlay0);
    font-size: 0.7rem;
  }
  .empty, .err {
    padding: 0.8rem 0.9rem;
    color: var(--overlay0);
    font-size: 0.85rem;
  }
  .err { color: var(--red); }
  footer {
    display: flex;
    gap: 0.9rem;
    padding: 0.4rem 0.9rem;
    background: var(--crust);
    border-top: 1px solid var(--surface0);
    font-size: 0.72rem;
    color: var(--overlay0);
  }
  kbd {
    background: var(--surface0);
    color: var(--text);
    padding: 0.02rem 0.3rem;
    border-radius: 3px;
    font-family: ui-monospace, monospace;
    font-size: 0.7rem;
  }
</style>
