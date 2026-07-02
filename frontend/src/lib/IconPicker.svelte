<script lang="ts">
  // Inline icon control for connection / folder editors. Shows the
  // current icon (via imageCache) plus an "Upload" file picker and a
  // "Clear" button. Upload reads the file as base64 client-side
  // (PNG/SVG only - keep it simple), pushes to ImagesUpload, then
  // calls the right Set* IPC. Parent gets onChange(imageId | null) so
  // it can refresh the row.

  import { api } from "./api";
  import { errMsg } from "./connectErrors";
  import { imageCache } from "./images.svelte";
  import { clickOutside } from "./clickOutside";

  type Props = {
    kind: "folder" | "connection" | "credential";
    targetId: string;
    currentIconId: string | null;
    fallbackEmoji: string;
    onChange?: (imageId: string | null) => void;
  };

  async function setIcon(imageId: string) {
    if (kind === "folder") {
      await api.imagesSetFolder(targetId, imageId);
    } else if (kind === "connection") {
      await api.imagesSetConnection(targetId, imageId);
    } else {
      await api.imagesSetCredential(targetId, imageId);
    }
  }

  let { kind, targetId, currentIconId, fallbackEmoji, onChange }: Props = $props();

  let uploading = $state(false);
  let err = $state<string | null>(null);
  let fileInput: HTMLInputElement | undefined = $state();

  // Picker popover state. Lazy-loads the image list on first open
  // (DBs with hundreds of RDM-imported logos shouldn't pay the cost
  // every time the editor mounts).
  let pickerOpen = $state(false);
  let existing = $state<Array<{ id: string; mime: string; use_count: number }>>([]);
  let pickerLoaded = $state(false);

  async function openPicker() {
    pickerOpen = true;
    if (!pickerLoaded) {
      try {
        existing = await api.imagesList() ?? [];
      } catch (e: any) {
        err = errMsg(e);
      }
      pickerLoaded = true;
    }
    // Preload thumbnails through the same cache the rest of the
    // tree uses, so flipping back to the connection list shows
    // them instantly.
    for (const img of existing) imageCache.ensure(img.id);
  }

  async function pickExisting(imageId: string) {
    err = null;
    try {
      await setIcon(imageId);
      onChange?.(imageId);
      pickerOpen = false;
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  const dataUrl = $derived(currentIconId ? imageCache.peek(currentIconId) : null);
  $effect(() => {
    if (currentIconId) imageCache.ensure(currentIconId);
  });

  async function onFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    // 256KB hard cap - tree icons are 16px; anything bigger is the
    // user picking a screenshot by mistake.
    if (f.size > 256 * 1024) {
      err = "File too large (max 256KB)";
      return;
    }
    const okType = ["image/png", "image/svg+xml", "image/jpeg", "image/webp", "image/gif"];
    if (!okType.includes(f.type)) {
      err = "Unsupported type - PNG/SVG/JPG/WebP/GIF";
      return;
    }
    uploading = true;
    err = null;
    try {
      const b64 = await fileToBase64(f);
      const imageId = await api.imagesUpload(b64, f.type);
      await setIcon(imageId);
      // Preload the new icon so the row shows it instantly.
      imageCache.ensure(imageId);
      onChange?.(imageId);
    } catch (ex: any) {
      err = ex?.message ?? String(ex);
    } finally {
      uploading = false;
      if (fileInput) fileInput.value = "";
    }
  }

  async function clearIcon() {
    err = null;
    try {
      await setIcon("");
      onChange?.(null);
    } catch (ex: any) {
      err = ex?.message ?? String(ex);
    }
  }

  function fileToBase64(f: File): Promise<string> {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => {
        // result is "data:<mime>;base64,<payload>" - strip prefix.
        const s = r.result as string;
        const idx = s.indexOf(",");
        resolve(idx >= 0 ? s.slice(idx + 1) : s);
      };
      r.onerror = () => reject(r.error);
      r.readAsDataURL(f);
    });
  }
</script>

<div class="icon-picker">
  <span class="label">Icon</span>
  <div class="row">
    <div class="preview">
      {#if dataUrl}
        <img src={dataUrl} alt="icon" />
      {:else}
        <span class="emoji">{fallbackEmoji}</span>
      {/if}
    </div>
    <button type="button" disabled={uploading} onclick={() => fileInput?.click()}>
      {uploading ? "…" : "Upload"}
    </button>
    <button type="button" onclick={openPicker}>Choose…</button>
    {#if currentIconId}
      <button type="button" class="danger" onclick={clearIcon}>Clear</button>
    {/if}
    <input
      bind:this={fileInput}
      type="file"
      accept="image/png,image/svg+xml,image/jpeg,image/webp,image/gif"
      hidden
      onchange={onFile}
    />
  </div>
  {#if err}<div class="err">{err}</div>{/if}
</div>

{#if pickerOpen}
  <div class="picker-overlay" role="presentation">
    <div
      class="picker-modal"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      use:clickOutside={{ onOutside: () => (pickerOpen = false) }}
      onkeydown={(e) => { if (e.key === "Escape") pickerOpen = false; }}
    >
      <header>
        <strong>Choose icon</strong>
        <button type="button" class="close" onclick={() => (pickerOpen = false)}>✕</button>
      </header>
      {#if !pickerLoaded}
        <div class="loading">Loading…</div>
      {:else if existing.length === 0}
        <div class="empty">No icons in the library yet - upload one first.</div>
      {:else}
        <div class="grid">
          {#each existing as img (img.id)}
            {@const url = imageCache.peek(img.id)}
            <button
              type="button"
              class="cell"
              class:current={img.id === currentIconId}
              title={`${img.use_count} use${img.use_count === 1 ? "" : "s"}`}
              onclick={() => pickExisting(img.id)}
            >
              {#if url}
                <img src={url} alt="" />
              {:else}
                <span class="ph">…</span>
              {/if}
              {#if img.use_count > 0}
                <span class="badge">{img.use_count}</span>
              {/if}
            </button>
          {/each}
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .icon-picker { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--subtext0); }
  .row { display: flex; align-items: center; gap: 0.4rem; }
  .preview {
    width: 28px; height: 28px;
    display: flex; align-items: center; justify-content: center;
    background: var(--crust); border: 1px solid var(--surface0); border-radius: 4px;
  }
  .preview img { width: 20px; height: 20px; object-fit: contain; }
  .preview .emoji { font-size: 1rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.25rem 0.6rem; cursor: pointer;
    font: inherit; font-size: 0.78rem;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  button.danger:hover { background: var(--red); color: var(--on-accent); }
  .err { color: var(--red); font-size: 0.75rem; }

  /* Picker modal */
  .picker-overlay {
    position: fixed; inset: 0;
    background: rgba(17, 17, 27, 0.6);
    z-index: 1000;
    display: flex; align-items: center; justify-content: center;
  }
  .picker-modal {
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    width: min(560px, 90vw);
    max-height: 80vh;
    display: flex; flex-direction: column;
    padding: 0.9rem 1rem;
    box-shadow: 0 8px 30px rgba(0,0,0,0.5);
  }
  .picker-modal header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.6rem;
  }
  .picker-modal header strong {
    font-size: 0.85rem; color: var(--text);
    text-transform: uppercase; letter-spacing: 0.04em;
  }
  .picker-modal .close {
    background: transparent; border: 0; color: var(--subtext0);
    cursor: pointer; padding: 0.15rem 0.35rem; border-radius: 3px;
    font-size: 0.9rem;
  }
  .picker-modal .close:hover { background: var(--surface0); color: var(--text); }
  .loading, .empty {
    padding: 1.2rem 0.5rem; color: var(--overlay1); text-align: center;
    font-style: italic;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(48px, 1fr));
    gap: 0.4rem;
    overflow-y: auto;
    padding-right: 0.3rem;
  }
  .cell {
    position: relative;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    aspect-ratio: 1 / 1;
    display: flex; align-items: center; justify-content: center;
    cursor: pointer; padding: 0.3rem;
  }
  .cell:hover { background: var(--surface0); border-color: var(--surface1); }
  .cell.current {
    border-color: var(--blue);
    box-shadow: 0 0 0 1px var(--blue) inset;
  }
  .cell img { max-width: 100%; max-height: 100%; object-fit: contain; }
  .cell .ph { color: var(--overlay0); font-size: 0.75rem; }
  .cell .badge {
    position: absolute; bottom: 2px; right: 3px;
    background: var(--surface0); color: var(--subtext0);
    font-size: 0.6rem; padding: 0 0.25rem;
    border-radius: 8px; line-height: 1.2;
  }
</style>
