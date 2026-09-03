<script lang="ts">
  // Quick view + editor for a remote text file. The whole body of the
  // feature lives here; the two things that mount it decide only how it is
  // framed:
  //
  //   chrome="modal"  - SftpViewModal wraps this in a backdrop dialog.
  //   chrome="inline" - SftpPane docks it under the listing, so the file
  //                     stays readable while the user types in the terminal
  //                     pane next to it (SFTP opens as a split by default).
  //
  // The difference is not cosmetic. A modal owns the keyboard: it takes
  // focus on mount, traps Escape, and closes on a backdrop click. Inline
  // must do none of that - stealing focus would pull every keystroke out of
  // the terminal, which is the exact workflow this mode exists to support.
  // Hence `autofocus` and the Escape handling are gated on chrome, not
  // hardcoded.
  //
  // Binary files are detected client-side and refused with a hint to
  // download instead - decoding a few MB of ELF into a <pre> would only
  // produce replacement characters and a janky scroll.
  import { onMount } from "svelte";
  import { api } from "./api";
  import { errMsg } from "./connectErrors";
  import {
    detectLang,
    detectEol,
    eolFlags,
    highlight,
    prettyJson,
    type Lang,
  } from "./miniHighlight";
  import { toast } from "./toast.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";

  interface Props {
    sessionId: string;
    path: string;
    name: string;
    onClose: () => void;
    /** How this view is framed. Drives focus and Escape behaviour, not
     *  just styling - see the note at the top. */
    chrome?: "modal" | "inline";
    /** Inline only: swap this file out for the modal, which has room for
     *  a long config. Omitted (and the button hidden) in the modal itself. */
    onExpand?: () => void;
  }
  let {
    sessionId,
    path,
    name,
    onClose,
    chrome = "modal",
    onExpand,
  }: Props = $props();
  const isModal = $derived(chrome === "modal");

  // 256 KB matches the backend's own default cap. Bigger files still open,
  // they just arrive truncated with a banner saying so.
  const MAX_BYTES = 256 * 1024;

  let loading = $state(true);
  let error = $state<string | null>(null);
  let content = $state("");
  let truncated = $state(false);
  let size = $state(0);
  let isBinary = $state(false);
  let lang = $state<Lang>("text");
  // Line terminator of the file as read. Shown in the header, and preserved
  // on save: silently rewriting a CRLF file to LF would touch every line.
  let eol = $state<"lf" | "crlf" | "mixed" | "none">("lf");
  // Per-line CRLF flags, only populated for a mixed file: the badge says the
  // file is inconsistent, this says which lines are the odd ones.
  let crlfLines = $state<boolean[]>([]);
  let wrap = $state(false);
  let modalEl = $state<HTMLDivElement | null>(null);
  let bodyEl = $state<HTMLDivElement | null>(null);
  // Pretty-printed JSON, computed once on demand. Null until the user asks
  // (or the document turns out not to be JSON at all).
  let pretty = $state<string | null>(null);
  let formatted = $state(false);

  // Edit mode. `draft` is the textarea's buffer; `content` stays the last
  // saved text so dirty is a plain comparison and Cancel is a discard.
  // modTime is what the file was read at - it goes back to the backend on
  // save, which refuses the write if the file changed meanwhile.
  let editing = $state(false);
  let draft = $state("");
  let saving = $state(false);
  let modTime = $state(0);
  let taEl = $state<HTMLTextAreaElement | null>(null);
  const dirty = $derived(editing && draft !== content);

  /** looksBinary reports whether bytes are better left undecoded. A NUL in
   *  the first few KB is the classic test and costs nothing; UTF-16 text
   *  trips it too, which is the right call here since we decode as UTF-8. */
  function looksBinary(bytes: Uint8Array): boolean {
    const n = Math.min(bytes.length, 8000);
    for (let i = 0; i < n; i++) {
      if (bytes[i] === 0) return true;
    }
    return false;
  }

  function decodeB64(b64: string): Uint8Array {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  // A read that never settles used to leave the modal on "Loading…" for
  // ever: the IPC promise simply never resolved (a stalled SFTP channel, a
  // fifo or device node that blocks on open). Racing it against a timer
  // turns that into a message the user can act on.
  const READ_TIMEOUT_MS = 20000;

  function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error(`timed out after ${ms / 1000}s - the file may be a pipe or device, or the SFTP channel stalled`)),
        ms,
      );
      p.then(
        (v) => { clearTimeout(timer); resolve(v); },
        (e) => { clearTimeout(timer); reject(e); },
      );
    });
  }

  async function load() {
    loading = true;
    error = null;
    try {
      const r = await withTimeout(
        api.sftpReadPreview(sessionId, path, MAX_BYTES),
        READ_TIMEOUT_MS,
      );
      truncated = r.truncated;
      size = r.size;
      modTime = r.mod_time;
      editing = false;
      draft = "";
      const bytes = decodeB64(r.b64);
      if (looksBinary(bytes)) {
        isBinary = true;
        content = "";
        return;
      }
      isBinary = false;
      // fatal:false so one bad byte in an otherwise readable log renders
      // as U+FFFD instead of throwing the whole view away.
      content = new TextDecoder("utf-8", { fatal: false }).decode(bytes);
      eol = detectEol(content);
      // Flags come from the raw text - content is normalised to LF below,
      // which would erase the very thing we want to point at.
      crlfLines = eol === "mixed" ? eolFlags(content) : [];
      // Work with LF internally - the highlighter, the textarea and the
      // diff-free `dirty` check all assume it. The original terminator is
      // restored on save.
      if (eol !== "lf") content = content.replace(/\r\n|\r/g, "\n");
      lang = detectLang(name, content);
      // Only offer Format for a whole document that actually parses. A
      // truncated read never will, which is the right answer: reformatting
      // half a file would just invent structure that isn't there.
      pretty = lang === "json" && !truncated ? prettyJson(content) : null;
      formatted = false;
    } catch (e: any) {
      error = errMsg(e);
    } finally {
      loading = false;
    }
  }

  // onMount, NOT $effect: an effect tracks every $state that load() touches,
  // and load() assigns `formatted = false` on the way out. Clicking Format
  // would then re-run the effect, re-read the file, and reset the toggle -
  // the button would visibly do nothing. The view is created per file (keyed
  // by its mount site), so mounting is the right trigger.
  onMount(() => {
    load();
    // Focus only in modal chrome. There the overlay blocks clicks anyway, so
    // focus has to come inside or keystrokes keep landing in a background
    // terminal's textarea. Inline is the opposite case: the terminal beside
    // us is meant to keep the keyboard, so we take focus only when the user
    // clicks into the view themselves.
    //
    // Focus the scrolling body, not the shell: clicking text inside a
    // tabindex=-1 ancestor refocuses that ancestor and the browser scrolls it
    // into view, snapping a long file back to the top. The body is the
    // element that scrolls, so focusing it is stable, and it gets arrow-key
    // scrolling as a bonus. preventScroll guards the initial focus too.
    if (isModal) {
      setTimeout(() => (bodyEl ?? modalEl)?.focus({ preventScroll: true }), 0);
    }
  });

  const shown = $derived(formatted && pretty !== null ? pretty : content);
  const lines = $derived.by(() => (isBinary || !shown ? [] : highlight(shown, lang)));
  // The editor paints the same tokens under a transparent textarea. A
  // trailing newline would otherwise lose its line, so keep one empty row.
  const draftLines = $derived.by(() => highlight(draft + (draft.endsWith("\n") ? " " : ""), lang));
  const lineCount = $derived(lines.length);

  function fmtSize(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  }

  async function copyAll() {
    try {
      await navigator.clipboard.writeText(shown);
      toast.push("ok", "Copied to clipboard");
    } catch (e) {
      toast.push("err", errMsg(e));
    }
  }

  // Single exit point: the X, Escape, and (in modal chrome) the backdrop
  // all land here so an accidental click cannot throw away unsaved work.
  // Exported so the modal wrapper reuses this check rather than
  // reimplementing it.
  export async function requestClose() {
    if (dirty) {
      const ok = await showConfirm({
        title: "Discard changes",
        message: `Discard your unsaved changes to ${name}?`,
        okLabel: "Discard",
        danger: true,
      });
      if (!ok) return;
    }
    onClose();
  }

  let hlEl = $state<HTMLDivElement | null>(null);

  // The highlight layer scrolls with the textarea. Driven from the
  // textarea's scroll event rather than CSS because the two elements are
  // independently scrollable and only the textarea receives wheel/caret
  // scrolling.
  function syncScroll() {
    if (!taEl || !hlEl) return;
    hlEl.scrollTop = taEl.scrollTop;
    hlEl.scrollLeft = taEl.scrollLeft;
  }

  function startEdit() {
    // Edit the raw file, never the pretty-printed view: saving reformatted
    // JSON would rewrite a file the user only asked to look at.
    draft = content;
    formatted = false;
    editing = true;
    setTimeout(() => taEl?.focus(), 0);
  }

  async function cancelEdit() {
    if (dirty) {
      const ok = await showConfirm({
        title: "Discard changes",
        message: `Discard your unsaved changes to ${name}?`,
        okLabel: "Discard",
        danger: true,
      });
      if (!ok) return;
    }
    editing = false;
    draft = "";
  }

  function utf8ToB64(text: string): string {
    const bytes = new TextEncoder().encode(text);
    let bin = "";
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin);
  }

  async function save() {
    if (saving) return;
    saving = true;
    try {
      // Restore the file's own terminator. A CRLF file stays CRLF; a mixed
      // file is normalised to CRLF rather than left half-and-half, since it
      // is being rewritten wholesale anyway.
      const body =
        eol === "crlf" || eol === "mixed" ? draft.replace(/\n/g, "\r\n") : draft;
      await api.sftpWriteFile(sessionId, path, utf8ToB64(body), modTime);
      content = draft;
      // Refresh only the mod-time baseline. Re-reading the file would
      // reassign the textarea's bound value and send the caret to the end
      // mid-edit, so stat is all we do here.
      try {
        const st = await api.sftpStat(sessionId, path);
        modTime = st.mod_time;
        size = st.size;
      } catch {
        // A failed stat only costs us the conflict baseline; the write
        // itself already succeeded, so don't report it as a save failure.
        // The next save falls back to an unguarded write.
        modTime = 0;
      }
      toast.push("ok", "Saved");
    } catch (e) {
      toast.push("err", errMsg(e));
    } finally {
      saving = false;
    }
  }

  function onKey(e: KeyboardEvent) {
    // Ctrl+S saves from anywhere in the view, including inside the textarea.
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      if (editing && dirty) save();
      return;
    }
    if (e.key === "Escape") {
      // Inline: Escape only backs out of an edit. Closing the pane on
      // Escape would be a trap, because the key reaches us whenever focus
      // happens to sit in the view, and the file is meant to stay open
      // while the user works next to it.
      if (editing) {
        e.preventDefault();
        cancelEdit();
      } else if (isModal) {
        e.preventDefault();
        requestClose();
      }
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="fileview"
  class:inline={!isModal}
  role={isModal ? "dialog" : "group"}
  aria-modal={isModal ? "true" : undefined}
  aria-label="File preview: {name}"
  tabindex="-1"
  bind:this={modalEl}
  onkeydown={onKey}
>
<header>
    <h2 title={path}>{name}</h2>
    <div class="header-actions">
      {#if !isBinary && !loading && !error}
        {#if editing}
          <span class="meta">{dirty ? "unsaved changes" : "no changes"}</span>
          {#if eol === "crlf" || eol === "mixed"}
            <span
              class="eol-badge"
              class:mixed={eol === "mixed"}
              title={eol === "crlf"
                ? "Windows line endings (CRLF) - preserved on save"
                : "Mixed line endings - the file is saved as CRLF"}
            >{eol.toUpperCase()}</span>
          {/if}
          <button class="primary" onclick={save} disabled={!dirty || saving}>
            {saving ? "Saving…" : "Save"}
          </button>
          <button onclick={cancelEdit} disabled={saving}>Cancel</button>
        {:else}
          <span class="meta">{lang !== "text" ? lang : ""} {fmtSize(size)} - {lineCount} lines</span>
          {#if eol === "crlf" || eol === "mixed"}
            <span
              class="eol-badge"
              class:mixed={eol === "mixed"}
              title={eol === "crlf"
                ? "Windows line endings (CRLF). Saving keeps them."
                : "Mixed line endings (both CRLF and LF). Saving normalises the file to CRLF."}
            >{eol.toUpperCase()}</span>
          {/if}
          {#if pretty !== null}
            <button
              class:active={formatted}
              onclick={() => (formatted = !formatted)}
              title="Pretty-print this JSON document"
            >{"{ }"} Format</button>
          {/if}
          <button class:active={wrap} onclick={() => (wrap = !wrap)} title="Toggle line wrap">↩ Wrap</button>
          <button onclick={copyAll} title="Copy contents">Copy</button>
          <!-- Truncated files cannot be edited: saving back would write
               only the part we read and silently drop the rest. -->
          <button
            onclick={startEdit}
            disabled={truncated}
            title={truncated ? "Too large to edit - only part of it was read" : "Edit this file"}
          >Edit</button>
        {/if}
      {/if}
    {#if !isModal && onExpand}
      <button onclick={onExpand} title="Open in a full-size window">⤢</button>
    {/if}
    <button
      class="close"
      onclick={requestClose}
      title={isModal ? "Close (Esc)" : "Close preview"}
    >✕</button>
    </div>
  </header>

  {#if truncated}
    <div class="banner">
      Showing the first {fmtSize(MAX_BYTES)} of {fmtSize(size)} - download the file to see all of it.
    </div>
  {/if}

  <div class="body" bind:this={bodyEl} tabindex="-1">
    {#if loading}
      <div class="msg">Loading…</div>
    {:else if error}
      <div class="msg err">{error}</div>
    {:else if isBinary}
      <div class="msg">
        Binary file ({fmtSize(size)}). Download it instead - previewing it here would only show garbage.
      </div>
    {:else if !content}
      <div class="msg">Empty file.</div>
    {:else if editing}
      <div class="edit-stack">
        <!-- Painted underlay: same font metrics as the textarea above it,
             so the tokens line up with the real glyphs. aria-hidden - the
             textarea is the accessible control. -->
        <div class="code edit-paint" bind:this={hlEl} aria-hidden="true">
          {#each draftLines as line, i (i)}
            <div class="row"><span class="lc">{@html line || "&nbsp;"}</span></div>
          {/each}
        </div>
        <textarea
          class="editor selectable"
          bind:this={taEl}
          bind:value={draft}
          onscroll={syncScroll}
          spellcheck="false"
          autocomplete="off"
          disabled={saving}
        ></textarea>
      </div>
    {:else}
      <!-- `selectable` opts back in to text selection: #app sets
           user-select:none for app chrome (see style.css), and this is
           file content the user is meant to be able to copy out of. -->
      <div class="code selectable" class:wrap>
        <!-- Every line is HTML-escaped by miniHighlight before it gets
             here; that module is the only thing allowed to build these
             strings. See the contract note in miniHighlight.ts. -->
        {#each lines as line, i (i)}
          <div class="row" class:crlf-line={!formatted && crlfLines[i]}>
            <span class="ln" title={!formatted && crlfLines[i] ? "This line ends with CRLF" : undefined}
              >{i + 1}</span
            ><span class="lc">{@html line || "&nbsp;"}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>

<style>
    /* Shared shell. In modal chrome the wrapper sizes us; inline we simply
     fill the slot SftpPane gives us, so no width/height of our own. */
  .fileview {
    background: var(--base);
    color: var(--text);
    display: flex;
    flex-direction: column;
    min-height: 0;
    padding: 1rem 1.2rem;
  }
  .fileview:focus { outline: none; }
  /* Inline sits inside the pane, under the listing: tighter padding and a
     top border instead of a dialog's own frame and shadow. */
  .fileview.inline {
    padding: 0.4rem 0.6rem 0.5rem;
    border-top: 1px solid var(--surface0);
    height: 100%;
  }
  .fileview.inline header { margin-bottom: 0.4rem; }
  .fileview.inline h2 { font-size: 0.85rem; }

  header {
    display: flex; align-items: center; justify-content: space-between;
    gap: 1rem; margin-bottom: 0.6rem;
  }
  h2 {
    margin: 0; font-size: 1rem; font-weight: 600;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .header-actions { display: flex; align-items: center; gap: 0.4rem; flex: 0 0 auto; }
  .meta { color: var(--overlay1); font-size: 0.8rem; margin-right: 0.2rem; }
  .eol-badge {
    font-size: 0.68rem;
    font-weight: 600;
    letter-spacing: 0.02em;
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
    background: color-mix(in srgb, var(--blue) 22%, transparent);
    color: var(--blue);
  }
  /* Mixed endings are a defect in the file, not just a convention. */
  .eol-badge.mixed {
    background: color-mix(in srgb, var(--yellow) 25%, transparent);
    color: var(--yellow);
  }
  .header-actions button.active { background: var(--surface1) !important; }
  .banner {
    background: var(--surface0);
    border-left: 3px solid var(--yellow);
    padding: 0.35rem 0.6rem;
    font-size: 0.85rem;
    margin-bottom: 0.5rem;
  }
  .body {
    flex: 1; overflow: auto;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
  }
  /* Focused for keyboard scrolling; the dialog border is the visible frame,
     so an extra focus ring here would just be noise. */
  .body:focus { outline: none; }

  /* Editor = a transparent textarea over a painted copy of the same text.
     Both layers must share every metric that affects glyph position: font,
     size, line-height, padding, white-space and tab-size. Change one and
     you must change the other, or the caret drifts from the colours. */
  .edit-stack { position: relative; height: 100%; }
  .edit-paint,
  .editor {
    position: absolute; inset: 0;
    box-sizing: border-box;
    margin: 0;
    padding: 0.4rem 0.6rem;
    font-family: var(--font-mono, ui-monospace, "Cascadia Code", Consolas, monospace);
    font-size: 0.82rem;
    line-height: 1.45;
    tab-size: 4;
    white-space: pre;
    overflow-wrap: normal;
  }
  /* Written as .code.edit-paint so it outranks the plain .code rule above
     regardless of source order - .code sets a gutter padding and
     min-width:max-content, both of which would break alignment with the
     textarea and its horizontal scrolling. */
  .code.edit-paint {
    overflow: hidden;
    min-width: 0;
    padding: 0.4rem 0.6rem;
    pointer-events: none;
    background: var(--mantle);
  }
  /* The paint layer has no line-number gutter; its rows are plain text and
     must not add width of their own, or long lines would wrap differently
     from the textarea and the colours would slide off the glyphs. */
  .code.edit-paint .row { display: block; }
  .code.edit-paint .row:hover { background: transparent; }
  .code.edit-paint .lc { padding-right: 0; }
  .editor {
    resize: none;
    border: 0;
    overflow: auto;
    background: transparent;
    /* Transparent text, visible caret: the colours come from the layer
       underneath. */
    color: transparent;
    caret-color: var(--text);
  }
  /* The textarea's own text is transparent, so an opaque selection colour
     would hide the painted layer underneath - the selection looked like a
     grey block with no text in it. A translucent highlight lets the colours
     show through. color:transparent is restated because some engines force
     an opaque default selection foreground. */
  .editor::selection {
    /* color-mix keeps this in step with the active theme's --blue, which
       differs across the light and dark palettes; a hardcoded rgba() would
       be right in only one of them. */
    background: color-mix(in srgb, var(--blue) 30%, transparent);
    color: transparent;
  }
  .editor::-moz-selection {
    background: color-mix(in srgb, var(--blue) 30%, transparent);
    color: transparent;
  }
  .editor:focus { outline: none; }
  .editor:disabled { color: transparent; }
  .header-actions button.primary {
    background: var(--blue) !important;
    color: var(--crust) !important;
  }
  .header-actions button.primary:disabled { opacity: 0.5; }
  .msg { padding: 1rem; color: var(--overlay1); }
  .msg.err { color: var(--red); }

  .code {
    font-family: var(--font-mono, ui-monospace, "Cascadia Code", Consolas, monospace);
    font-size: 0.82rem;
    line-height: 1.45;
    padding: 0.4rem 0;
    min-width: max-content;
  }
  .code.wrap { min-width: 0; }
  .row { display: flex; }
  .ln {
    flex: 0 0 auto;
    width: 3.5em;
    padding-right: 0.8em;
    text-align: right;
    color: var(--surface2);
    position: sticky; left: 0;
    background: var(--mantle);
  }
  /* Line numbers must stay out of a copied selection. `#app .selectable *`
     turns selection back on for everything inside .code, so this needs an
     id-level selector to outrank it - :global because #app lives outside
     this component. */
  :global(#app) .code .ln {
    user-select: none;
    -webkit-user-select: none;
  }
  .lc { white-space: pre; padding-right: 1rem; }
  .code.wrap .lc { white-space: pre-wrap; overflow-wrap: anywhere; }
  .row:hover { background: var(--surface0); }
  /* Mixed-ending files: mark the CRLF lines so the odd ones out are findable
     without diffing. Only the gutter is tinted - colouring the whole row
     would fight the syntax highlighting. */
  .row.crlf-line .ln {
    background: color-mix(in srgb, var(--yellow) 22%, var(--mantle));
    color: var(--yellow);
  }
  .row.crlf-line .ln::after {
    content: "\\r";
    font-size: 0.62rem;
    opacity: 0.85;
    margin-left: 0.15em;
  }

  /* Token colours. Names match the tok-* classes miniHighlight emits. */
  .lc :global(.tok-key)     { color: var(--blue); }
  .lc :global(.tok-str)     { color: var(--green); }
  .lc :global(.tok-num)     { color: var(--peach); }
  .lc :global(.tok-bool)    { color: var(--mauve); }
  .lc :global(.tok-comment) { color: var(--overlay0); font-style: italic; }
  .lc :global(.tok-section) { color: var(--mauve); font-weight: 600; }
  .lc :global(.tok-var)     { color: var(--peach); }
  .lc :global(.tok-kw)      { color: var(--mauve); }
  .lc :global(.tok-time)    { color: var(--overlay1); }
  .lc :global(.tok-lvl-err)  { color: var(--red); font-weight: 600; }
  .lc :global(.tok-lvl-warn) { color: var(--yellow); font-weight: 600; }
  .lc :global(.tok-lvl-info) { color: var(--blue); }
  .lc :global(.tok-lvl-dbg)  { color: var(--overlay1); }
</style>
