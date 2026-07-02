<script lang="ts">
  // Quick-share modal for exporting one or more connections.
  // Reuses the existing ExportSubtree IPC (no folder roots - just
  // the connection ids in `extra`), so the produced archive is
  // import-compatible with the Settings → Import archive flow.
  //
  // Credentials are NEVER included from this entry point - the
  // user gets a clean shareable file. The Settings export panel
  // still offers the "Include credentials (encrypted)" option for
  // backup / migration use.

  import { api } from "./api";
  import { errMsg } from "./connectErrors";
  import { IconX, IconClipboardCopy, IconFile } from "./iconMap";
  import { clickOutside } from "./clickOutside";

  type Props = {
    connectionIds: string[];
    folderIds?: string[];
    suggestedName: string;
    onClose: () => void;
  };

  let { connectionIds, folderIds = [], suggestedName, onClose }: Props = $props();

  let format = $state<"toml" | "json">("toml");
  let stripNotes = $state(false);
  let stripTags = $state(false);
  let stripColor = $state(false);
  let stripIcon = $state(false);
  let convertAuthRefToInherit = $state(true);
  let body = $state<string | null>(null);
  let err = $state<string | null>(null);
  let busy = $state(false);
  let savedHint = $state<string | null>(null);

  const totalCount = $derived(connectionIds.length + folderIds.length);
  const isFolders = $derived(folderIds.length > 0);

  async function generate() {
    busy = true;
    err = null;
    body = null;
    try {
      const res = await api.exportSubtree({
        roots: folderIds,
        extra: connectionIds,
        format,
        include_credentials: false,
        passphrase: "",
        strip_notes: stripNotes,
        strip_tags: stripTags,
        strip_color: stripColor,
        strip_icon: stripIcon,
        convert_auth_ref_to_inherit: convertAuthRefToInherit,
      });
      body = res.body;
    } catch (e: any) {
      err = errMsg(e);
    } finally {
      busy = false;
    }
  }

  // Regenerate whenever any option flips.
  $effect(() => {
    void format;
    void stripNotes;
    void stripTags;
    void stripColor;
    void stripIcon;
    void convertAuthRefToInherit;
    generate();
  });

  async function copy() {
    if (!body) return;
    await navigator.clipboard.writeText(body);
    savedHint = "Copied";
    setTimeout(() => (savedHint = null), 1500);
  }

  async function save() {
    if (!body) return;
    try {
      const ext = format === "json" ? "json" : "toml";
      const path = await api.saveTextFile(`${suggestedName}.${ext}`, body);
      if (path) {
        savedHint = `Saved to ${path}`;
        setTimeout(() => (savedHint = null), 3000);
      }
    } catch (e: any) {
      err = errMsg(e);
    }
  }
</script>

<div class="overlay" role="presentation">
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    use:clickOutside={{ onOutside: onClose }}
    onkeydown={(e) => { if (e.key === "Escape") onClose(); }}
  >
    <header>
      <h2>
        Export {#if isFolders && connectionIds.length === 0}
          {folderIds.length === 1 ? "folder" : `${folderIds.length} folders`}
        {:else if connectionIds.length === 1 && folderIds.length === 0}
          connection
        {:else}
          {totalCount} items
        {/if}
      </h2>
      <button class="close" onclick={onClose} title="Close (Esc)">
        <IconX size={14} />
      </button>
    </header>
    <p class="hint">
      A portable archive with no credentials. {#if isFolders}Whole folder
      subtrees are included recursively.{/if} The recipient can pull
      it via Settings → Import archive.
    </p>
    <div class="fmt">
      <label>
        <input type="radio" bind:group={format} value="toml" />
        TOML
      </label>
      <label>
        <input type="radio" bind:group={format} value="json" />
        JSON
      </label>
    </div>

    <div class="strip">
      <div class="strip-title">Strip from export</div>
      <label class="check">
        <input type="checkbox" bind:checked={stripNotes} />
        <span>Notes <span class="dim">(connection-level free text)</span></span>
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={stripTags} />
        <span>Tags <span class="dim">(connection + credential labels)</span></span>
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={stripColor} />
        <span>Color tag <span class="dim">(folder + connection overrides)</span></span>
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={stripIcon} />
        <span>Custom icons <span class="dim">(embedded icon images - recipients get default icons)</span></span>
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={convertAuthRefToInherit} />
        <span>
          Convert credential override to inherit
          <span class="dim">(connections fall back to their folder's credential on import - no broken auth_ref)</span>
        </span>
      </label>
    </div>

    {#if err}<div class="err">{err}</div>{/if}
    {#if busy}<div class="busy">Generating…</div>{/if}
    {#if body}
      <pre>{body}</pre>
      <div class="actions">
        <button onclick={copy}>
          <IconClipboardCopy size={13} /> Copy
        </button>
        <button class="primary" onclick={save}>
          <IconFile size={13} /> Save as…
        </button>
        {#if savedHint}<span class="ok-hint">{savedHint}</span>{/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(17, 17, 27, 0.6);
    z-index: 1000;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .modal {
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    width: min(720px, 92vw);
    max-height: 86vh;
    display: flex;
    flex-direction: column;
    padding: 1rem 1.2rem;
    box-shadow: 0 8px 30px rgba(0,0,0,0.5);
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.4rem;
  }
  header h2 {
    margin: 0;
    font-size: 0.95rem;
    color: var(--text);
  }
  .close {
    background: transparent; border: 0; color: var(--subtext0); cursor: pointer;
    padding: 0.15rem 0.35rem; border-radius: 3px;
  }
  .close:hover { background: var(--surface0); color: var(--text); }
  .hint { color: var(--subtext0); font-size: 0.78rem; margin: 0.2rem 0 0.6rem; line-height: 1.5; }
  .fmt {
    display: flex; gap: 0.8rem;
    padding: 0.4rem 0;
    border-top: 1px solid var(--surface0);
    border-bottom: 1px solid var(--surface0);
  }
  .fmt label { display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.82rem; }
  .strip {
    display: flex; flex-direction: column; gap: 0.25rem;
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--surface0);
  }
  .strip-title {
    font-size: 0.72rem;
    color: var(--overlay0);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-bottom: 0.15rem;
  }
  .strip .check { display: inline-flex; align-items: flex-start; gap: 0.4rem; font-size: 0.8rem; cursor: pointer; }
  .strip .check input { margin-top: 0.2rem; accent-color: var(--blue); flex-shrink: 0; }
  .strip .dim { color: var(--overlay0); font-size: 0.74rem; }
  pre {
    flex: 1;
    overflow: auto;
    margin: 0.7rem 0 0;
    padding: 0.6rem 0.8rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    font-size: 0.72rem;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .actions {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    margin-top: 0.6rem;
  }
  .actions button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.3rem 0.65rem;
    cursor: pointer; font: inherit; font-size: 0.82rem;
    display: inline-flex; align-items: center; gap: 0.3rem;
  }
  .actions button:hover { background: var(--surface1); }
  .actions button.primary { background: var(--blue); color: var(--on-accent); }
  .actions button.primary:hover { background: var(--lavender); }
  .ok-hint { color: var(--green); font-size: 0.78rem; margin-left: 0.4rem; }
  .err {
    background: color-mix(in oklab, var(--red) 12%, var(--bg-panel));
    border-left: 3px solid var(--red);
    color: var(--red);
    padding: 0.5rem 0.7rem;
    border-radius: 0 3px 3px 0;
    font-size: 0.8rem;
    margin: 0.5rem 0;
  }
  .busy { color: var(--overlay1); font-style: italic; padding: 0.5rem 0; }
</style>
