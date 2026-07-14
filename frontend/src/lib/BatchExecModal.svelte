<script lang="ts">
  // Multi-host command runner. Takes the current connection multi-
  // selection, fans out a single command via the backend BatchExec
  // (no PTY, no interactive auth), and renders one row per host with
  // status / stdout / stderr / exit code.
  //
  // After a run the user can save the command as a snippet via the
  // existing snippet library, so the next "tail /var/log/whatever
  // across these 12 boxes" is one click.

  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api, type BatchHostResult, type SnippetInput, type Snippet } from "./api";
  import { selection, tree } from "./stores.svelte";
  import { clickOutside } from "./clickOutside";
  import { toast } from "./toast.svelte";

  interface Props {
    onClose: () => void;
    // Optional override host list for callers that target sessions
    // outside the regular connection-multi-select (notably the
    // dynamic-inventory bulk pane in DetailPane). When provided,
    // we use these instead of `selection.selectedConnectionIds()`.
    // Each entry needs connection_id (regular id or "dyn:<entryId>"),
    // name, and hostname - names/hostnames feed the per-row labels
    // since we can't look them up via tree.connectionById for
    // dynamic ids.
    hostsOverride?: Array<{ connection_id: string; name: string; hostname: string }>;
  }
  let { onClose, hostsOverride }: Props = $props();

  let command = $state("");
  let timeoutSeconds = $state(60);
  let cmdEl: HTMLTextAreaElement | undefined = $state();

  let running = $state(false);
  // Whether every result row is expanded. Default true so the user
  // sees all stdout at a glance - typical workflow is "run X on N
  // hosts, eyeball output across the board". The earlier default
  // (only error / non-zero rows open) hid clean output and forced
  // a click per host, which defeats the point of running in batch.
  let expandAll = $state(true);
  let results = $state<BatchHostResult[]>([]);
  let errorMsg = $state<string | null>(null);

  // Snippet save state - visible after the user has run something
  // they want to keep.
  let snippetName = $state("");
  let saveOpen = $state(false);
  let saving = $state(false);
  let saveErr = $state<string | null>(null);
  let savedHint = $state<string | null>(null);

  // Snippet picker. Global snippets ("" = all global). The
  // dropdown loads on first open so we don't fetch every modal
  // mount; once loaded it stays cached for the session.
  let snippetPickerOpen = $state(false);
  let availableSnippets = $state<Snippet[]>([]);
  let snippetsLoaded = $state(false);
  let snippetQuery = $state("");
  async function ensureSnippetsLoaded() {
    if (snippetsLoaded) return;
    try {
      // Global only - batch exec runs across N hosts so per-host
      // snippets don't make sense as a single command source.
      availableSnippets = (await api.snippetsList("")) ?? [];
    } catch (e) {
      console.warn("load snippets for batch picker:", e);
      availableSnippets = [];
    }
    snippetsLoaded = true;
  }
  const filteredSnippets = $derived(() => {
    const q = snippetQuery.trim().toLowerCase();
    if (!q) return availableSnippets;
    return availableSnippets.filter((s) =>
      s.name.toLowerCase().includes(q) ||
      s.body.toLowerCase().includes(q)
    );
  });
  function loadSnippetIntoCommand(snip: Snippet) {
    command = snip.body;
    snippetPickerOpen = false;
    snippetQuery = "";
    setTimeout(() => cmdEl?.focus(), 0);
  }

  const ids = $derived(
    hostsOverride
      ? hostsOverride.map((h) => h.connection_id)
      : selection.selectedConnectionIds()
  );
  // Cache override name/hostname lookups for the optimistic
  // placeholder rows (regular ids fall through to tree lookup).
  const overrideById = $derived(
    new Map((hostsOverride ?? []).map((h) => [h.connection_id, h])),
  );
  const hostsPreview = $derived(
    ids.slice(0, 6).map((id) => {
      const o = overrideById.get(id);
      if (o) return o.name;
      return tree.connectionById(id)?.name ?? id;
    }),
  );

  onMount(() => {
    setTimeout(() => cmdEl?.focus(), 0);
  });

  async function run() {
    if (!command.trim()) return;
    if (ids.length === 0) { errorMsg = "No connections selected"; return; }
    errorMsg = null;
    running = true;
    // Optimistic placeholders so the user sees something the moment
    // they hit Run.
    results = ids.map((id) => {
      const c = tree.connectionById(id);
      return {
        connection_id: id,
        hostname: c?.hostname ?? "",
        name: c?.name ?? id,
        state: "skipped" as const,
        stdout: "",
        stderr: "",
        exit_code: 0,
        duration_ms: 0,
      };
    });
    try {
      const out = await api.batchExec({
        connection_ids: ids,
        command: command.trim(),
        timeout_seconds: timeoutSeconds || 60,
      });
      // Re-order to match request order even if backend returned out of order.
      const byId = new Map(out.map((r) => [r.connection_id, r]));
      results = ids.map((id) => byId.get(id) ?? results.find((r) => r.connection_id === id)!);
    } catch (e: any) {
      errorMsg = errMsg(e);
    } finally {
      running = false;
    }
  }

  function exitColor(r: BatchHostResult): string {
    if (r.state === "error") return "var(--red)";
    if (r.exit_code === 0) return "var(--green)";
    return "var(--yellow)";
  }

  async function saveAsSnippet() {
    saveErr = null;
    if (!snippetName.trim() || !command.trim()) {
      saveErr = "Name and body required";
      return;
    }
    saving = true;
    try {
      const input: SnippetInput = {
        connection_id: null,
        name: snippetName.trim(),
        body: command.trim(),
        tags: ["batch"],
      };
      await api.snippetCreate(input);
      savedHint = "Saved to library";
      setTimeout(() => { savedHint = null; saveOpen = false; snippetName = ""; }, 1800);
    } catch (e: any) {
      saveErr = errMsg(e);
    } finally {
      saving = false;
    }
  }

  // Aggregates for the header.
  const okCount = $derived(results.filter((r) => r.state === "ok" && r.exit_code === 0).length);
  const nonZeroCount = $derived(results.filter((r) => r.state === "ok" && r.exit_code !== 0).length);
  const errCount = $derived(results.filter((r) => r.state === "error").length);

  // A copied batch is nearly always on its way into a chat window, so what
  // matters is that it lands there readable: the host name as a heading and
  // the output inside a code block, which is exactly what a <pre> becomes when
  // Teams / Slack / Outlook paste HTML. Copying plain text loses that - the
  // whole thing arrives as one undifferentiated wall.
  //
  // Only failures carry their status. On a healthy host the exit code and the
  // timing are noise to whoever receives this; on a host that refused or
  // errored, the absence of output IS the message, so say why.
  function statusNote(r: BatchHostResult): string {
    if (r.state === "error") return r.error?.trim() || "failed";
    if (r.state === "skipped") return "skipped";
    if (r.exit_code !== 0) return `exit ${r.exit_code}`;
    return "";
  }

  function hostLabel(r: BatchHostResult): string {
    return r.name && r.name !== r.hostname ? `${r.name} (${r.hostname})` : (r.hostname || r.name);
  }

  function bodyOf(r: BatchHostResult): string {
    const body = [r.stdout, r.stderr].filter((s) => s && s.trim()).join("\n");
    return body.replace(/\s+$/, "");
  }

  const esc = (s: string) =>
    s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

  function resultsAsHtml(): string {
    const out: string[] = [`<p><code>${esc(command.trim())}</code></p>`];
    for (const r of results) {
      const note = statusNote(r);
      out.push(`<p><b>${esc(hostLabel(r))}</b>${note ? ` - ${esc(note)}` : ""}</p>`);
      const body = bodyOf(r);
      if (body) out.push(`<pre>${esc(body)}</pre>`);
    }
    return out.join("\n");
  }

  // Plain-text twin, used as the text/plain flavour of the same clipboard write
  // (and as the fallback when the async clipboard API is unavailable).
  function resultsAsText(): string {
    const parts: string[] = [`$ ${command.trim()}`, ""];
    for (const r of results) {
      const note = statusNote(r);
      parts.push(hostLabel(r) + (note ? ` - ${note}` : ""));
      const body = bodyOf(r);
      // A host that failed says so in its header; "(no output)" underneath
      // would just repeat it. Only a host that SUCCEEDED and printed nothing
      // needs the line, otherwise its block would look truncated.
      if (body) parts.push(body);
      else if (!note) parts.push("(no output)");
      parts.push("");
    }
    return parts.join("\n");
  }

  let copiedHint = $state(false);
  async function copyAll() {
    const text = resultsAsText();
    try {
      // Write both flavours: rich targets pick up text/html and render the
      // code blocks, plain targets (an editor, a shell) get the same content
      // without markup.
      if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
        await navigator.clipboard.write([
          new ClipboardItem({
            "text/html": new Blob([resultsAsHtml()], { type: "text/html" }),
            "text/plain": new Blob([text], { type: "text/plain" }),
          }),
        ]);
      } else {
        await api.clipboardSetText(text);
      }
      copiedHint = true;
      setTimeout(() => { copiedHint = false; }, 1600);
    } catch (e: any) {
      // Rich write can be refused (permissions, unfocused document); plain
      // text through the Go side always works, and is better than nothing.
      try {
        await api.clipboardSetText(text);
        copiedHint = true;
        setTimeout(() => { copiedHint = false; }, 1600);
      } catch {
        toast.err("Copy failed: " + errMsg(e));
      }
    }
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1"
     onkeydown={(e) => { if (e.key === "Escape" && !running) onClose(); }}>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document"
       use:clickOutside={{ onOutside: () => { if (!running) onClose(); } }}
       onkeydown={(e) => e.stopPropagation()}>
    <header>
      <strong>Run command on {ids.length} hosts</strong>
      <button class="close" onclick={onClose} title="Close" disabled={running}>✕</button>
    </header>

    <div class="hosts-preview">
      {hostsPreview.join(", ")}
      {#if ids.length > hostsPreview.length}
        <span class="more">+ {ids.length - hostsPreview.length} more</span>
      {/if}
    </div>

    <div class="cmd-section">
      <label class="cmd-label" for="batch-cmd">Command</label>
      <textarea
        id="batch-cmd"
        bind:this={cmdEl}
        bind:value={command}
        rows="3"
        placeholder="uname -a"
        spellcheck="false"
        onkeydown={(e) => {
          if ((e.ctrlKey || e.metaKey) && e.key === "Enter" && !running) run();
        }}
      ></textarea>
      <div class="cmd-row">
        <label class="num">
          <span>Timeout (s)</span>
          <input type="number" min="1" max="600" bind:value={timeoutSeconds} />
        </label>
        <div class="grow"></div>
        <div class="snippet-picker-wrap">
          <button
            onclick={() => {
              snippetPickerOpen = !snippetPickerOpen;
              if (snippetPickerOpen) ensureSnippetsLoaded();
            }}
            title="Load a saved snippet into the command field"
          >
            {snippetPickerOpen ? "Cancel" : "Load snippet…"}
          </button>
          {#if snippetPickerOpen}
            <div class="snippet-picker" role="listbox">
              <input
                type="text"
                class="snippet-search"
                placeholder="Filter snippets…"
                bind:value={snippetQuery}
                onkeydown={(e) => {
                  if (e.key === "Escape") {
                    snippetPickerOpen = false;
                    e.stopPropagation();
                  } else if (e.key === "Enter" && filteredSnippets().length > 0) {
                    loadSnippetIntoCommand(filteredSnippets()[0]);
                    e.preventDefault();
                    e.stopPropagation();
                  }
                }}
              />
              <div class="snippet-list">
                {#if filteredSnippets().length === 0}
                  <div class="snippet-empty">
                    {snippetsLoaded ? "No matches." : "Loading…"}
                  </div>
                {:else}
                  {#each filteredSnippets() as s (s.id)}
                    <button
                      class="snippet-item"
                      onclick={() => loadSnippetIntoCommand(s)}
                      title={s.body}
                    >
                      <span class="snippet-name">{s.name}</span>
                      <span class="snippet-preview">{s.body.slice(0, 80)}</span>
                    </button>
                  {/each}
                {/if}
              </div>
            </div>
          {/if}
        </div>
        <button onclick={() => (saveOpen = !saveOpen)} disabled={!command.trim()}>
          {saveOpen ? "Cancel save" : "Save as snippet…"}
        </button>
        <button class="primary" onclick={run} disabled={running || !command.trim()}>
          {running ? "Running…" : `Run (${ids.length})`}
        </button>
      </div>
      {#if saveOpen}
        <div class="save-row">
          <input
            placeholder="Snippet name"
            bind:value={snippetName}
            onkeydown={(e) => { if (e.key === "Enter") saveAsSnippet(); }}
          />
          <button onclick={saveAsSnippet} disabled={saving || !snippetName.trim()}>
            {saving ? "…" : "Save"}
          </button>
          {#if savedHint}<span class="ok-pill">{savedHint}</span>{/if}
          {#if saveErr}<span class="err inline">{saveErr}</span>{/if}
        </div>
      {/if}
    </div>

    {#if errorMsg}
      <div class="err">⚠ {errorMsg}</div>
    {/if}

    {#if results.length > 0}
      <div class="summary">
        <span class="pill ok">✓ {okCount}</span>
        {#if nonZeroCount > 0}<span class="pill warn">⚠ {nonZeroCount} non-zero</span>{/if}
        {#if errCount > 0}<span class="pill bad">✕ {errCount}</span>{/if}
        {#if running}<span class="dim">running…</span>{/if}
        <div class="spacer"></div>
        <button
          class="copy-all"
          onclick={copyAll}
          disabled={running}
          title="Copy every host's output, each block labelled with the host it came from"
        >
          {copiedHint ? "✓ Copied" : "Copy all"}
        </button>
        <label class="expand-toggle">
          <input type="checkbox" bind:checked={expandAll} />
          <span>Expand all</span>
        </label>
      </div>
      <div class="results">
        {#each results as r (r.connection_id)}
          <details class="host" open={expandAll || r.state === "error" || (r.state === "ok" && r.exit_code !== 0)}>
            <summary>
              <span class="state-dot" style:background={exitColor(r)}></span>
              <span class="name">{r.name}</span>
              <span class="meta">
                {#if r.state === "error"}
                  error: {r.error}
                {:else if r.state === "skipped"}
                  pending…
                {:else}
                  exit {r.exit_code} · {r.duration_ms} ms
                {/if}
              </span>
            </summary>
            {#if r.stdout}
              <pre class="stdout">{r.stdout}</pre>
            {/if}
            {#if r.stderr}
              <pre class="stderr">{r.stderr}</pre>
            {/if}
            {#if !r.stdout && !r.stderr && r.state === "ok"}
              <div class="empty-out">(no output)</div>
            {/if}
          </details>
        {/each}
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
    padding-top: 5vh;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 8px;
    width: min(900px, 95vw);
    max-height: 88vh;
    display: flex; flex-direction: column;
    overflow: hidden;
    box-shadow: 0 20px 60px rgba(0,0,0,0.6);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.55rem 0.9rem;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.9rem;
  }
  .close {
    background: transparent; color: var(--subtext0); border: 0;
    cursor: pointer; font: inherit; padding: 0 0.4rem;
  }
  .close:hover:not(:disabled) { color: var(--red); }
  .hosts-preview {
    padding: 0.4rem 0.9rem;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.78rem;
    color: var(--subtext0);
  }
  .more { color: var(--overlay0); }
  .cmd-section {
    padding: 0.55rem 0.9rem;
    border-bottom: 1px solid var(--surface0);
  }
  .cmd-label {
    font-size: 0.78rem;
    color: var(--subtext0);
    display: block;
    margin-bottom: 0.25rem;
  }
  textarea {
    width: 100%;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.45rem 0.6rem;
    font-family: ui-monospace, "JetBrains Mono", monospace;
    font-size: 0.85rem;
  }
  .cmd-row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    margin-top: 0.5rem;
  }
  .cmd-row label.num {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--subtext0);
    font-size: 0.78rem;
  }
  .cmd-row input[type="number"] {
    width: 4.5rem;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.25rem 0.4rem;
    font: inherit;
    font-size: 0.82rem;
  }
  .grow { flex: 1; }
  .cmd-row button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.35rem 0.85rem;
    cursor: pointer; font: inherit; font-size: 0.82rem;
  }
  .cmd-row button:hover:not(:disabled) { background: var(--surface1); }
  .cmd-row button:disabled { opacity: 0.5; cursor: not-allowed; }
  .cmd-row button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  .cmd-row button.primary:hover:not(:disabled) { background: var(--lavender); }
  .save-row {
    display: flex; gap: 0.4rem; align-items: center;
    margin-top: 0.5rem;
  }
  .save-row input {
    flex: 1; max-width: 28rem;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.3rem 0.5rem; font: inherit; font-size: 0.82rem;
  }
  .ok-pill {
    color: var(--green); font-size: 0.75rem;
  }

  .err {
    padding: 0.45rem 0.9rem;
    background: color-mix(in oklab, var(--red) 14%, var(--bg-panel));
    color: var(--red);
    font-size: 0.82rem;
    border-bottom: 1px solid var(--surface0);
  }
  .err.inline { padding: 0; background: transparent; border: 0; font-size: 0.72rem; }

  .summary {
    display: flex; gap: 0.5rem; align-items: center;
    padding: 0.4rem 0.9rem;
    border-bottom: 1px solid var(--surface0);
    background: var(--crust);
  }
  .pill {
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.72rem;
    font-weight: 600;
  }
  .pill.ok { background: var(--surface0); color: var(--green); }
  .pill.warn { background: var(--surface0); color: var(--yellow); }
  .pill.bad { background: var(--surface0); color: var(--red); }
  .dim { color: var(--overlay0); font-size: 0.72rem; }
  .summary .spacer { flex: 1; }
  .copy-all {
    font-size: 0.72rem;
    padding: 0.15rem 0.6rem;
    border-radius: 4px;
    border: 1px solid var(--surface1);
    background: var(--surface0);
    color: var(--subtext1);
    cursor: pointer;
  }
  .copy-all:hover:not(:disabled) { background: var(--surface1); color: var(--text); }
  .copy-all:disabled { opacity: 0.5; cursor: default; }
  .expand-toggle {
    display: flex; align-items: center; gap: 0.3rem;
    font-size: 0.72rem; color: var(--subtext0);
    cursor: pointer; user-select: none;
  }
  .expand-toggle input { cursor: pointer; }

  .results {
    flex: 1;
    overflow-y: auto;
    padding: 0.4rem 0.6rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .host {
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
  }
  .host summary {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.35rem 0.55rem;
    cursor: pointer;
    list-style: none;
    /* Dragging a selection across several hosts should pick up the host
       names, not just the output blocks - the output is meaningless to
       whoever you paste it to without them. WebKit treats <summary> as a
       control and drops its text from a range selection unless the text is
       explicitly selectable. */
    -webkit-user-select: text;
    user-select: text;
  }
  .host summary::-webkit-details-marker { display: none; }
  .state-dot {
    width: 9px; height: 9px; border-radius: 50%;
    flex-shrink: 0;
  }
  .host .name { color: var(--text); font-size: 0.82rem; flex: 1; min-width: 0; }
  .host .meta {
    color: var(--overlay1); font-size: 0.72rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  pre.stdout, pre.stderr {
    margin: 0;
    padding: 0.4rem 0.6rem;
    background: var(--mantle);
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 0.78rem;
    white-space: pre-wrap;
    word-break: break-word;
    border-top: 1px solid var(--surface0);
    max-height: 18rem;
    overflow-y: auto;
  }
  pre.stderr { color: var(--red); }
  .empty-out {
    padding: 0.4rem 0.6rem;
    color: var(--overlay0);
    font-size: 0.72rem;
    font-style: italic;
    border-top: 1px solid var(--surface0);
  }

  .snippet-picker-wrap { position: relative; }
  .snippet-picker {
    position: absolute;
    bottom: 100%;
    right: 0;
    margin-bottom: 0.3rem;
    width: 380px;
    max-height: 320px;
    display: flex; flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    box-shadow: 0 4px 14px rgba(0,0,0,0.4);
    z-index: 50;
  }
  .snippet-search {
    background: var(--mantle);
    color: var(--text);
    border: 0;
    border-bottom: 1px solid var(--surface0);
    padding: 0.4rem 0.6rem;
    font: inherit;
    font-size: 0.82rem;
    outline: none;
  }
  .snippet-list {
    overflow-y: auto;
    max-height: 260px;
  }
  .snippet-empty {
    padding: 0.6rem;
    color: var(--overlay0);
    font-size: 0.78rem;
  }
  .snippet-item {
    display: flex; flex-direction: column;
    align-items: flex-start;
    gap: 0.15rem;
    width: 100%;
    padding: 0.4rem 0.6rem;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--surface0);
    color: var(--text);
    cursor: pointer;
    text-align: left;
    font-size: 0.82rem;
  }
  .snippet-item:hover { background: var(--surface0); }
  .snippet-item:last-child { border-bottom: 0; }
  .snippet-name { font-weight: 600; }
  .snippet-preview {
    font-family: ui-monospace, monospace;
    font-size: 0.72rem;
    color: var(--subtext0);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }
</style>
