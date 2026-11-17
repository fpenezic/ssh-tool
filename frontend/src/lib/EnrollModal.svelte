<script lang="ts">
  // Connect this app to a catalog with a one-time code (PROTOCOL 10.5),
  // from an ssh-tool://catalog/add link or typed in Settings. The code is
  // inspected first (which does not use it up) and the user sees what is
  // being connected: the server's name, the URL exactly as it arrived,
  // whose code it is. Only "Connect" redeems it. A link can come from any
  // local program that claims the scheme, so nothing happens without that
  // click.
  import { api, type CatalogEnrollPreview } from "./api";
  import { catalogs } from "./catalog.svelte";
  import { tree } from "./stores.svelte";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconX, IconUsers, IconAlertTriangle, IconLock } from "./iconMap";

  interface Props { url: string; code: string; onClose: () => void }
  let { url, code, onClose }: Props = $props();

  // A link fills both; the form path may need the user to type them.
  // svelte-ignore state_referenced_locally
  let urlIn = $state(url);
  // svelte-ignore state_referenced_locally
  let codeIn = $state(code);
  // svelte-ignore state_referenced_locally
  const fromLink = !!(url && code);
  let preview = $state<CatalogEnrollPreview | null>(null);
  let err = $state("");
  let busy = $state(false);

  async function inspect() {
    err = "";
    preview = null;
    if (!urlIn.trim() || !codeIn.trim()) return;
    busy = true;
    try {
      preview = await api.catalogEnrollInspect(urlIn.trim(), codeIn.trim());
    } catch (e) {
      err = clean(e);
    } finally {
      busy = false;
    }
  }
  // A link only fills the form: nothing contacts the host it names until
  // the user clicks Check.

  async function confirm(replace: boolean) {
    if (!preview) return;
    busy = true;
    err = "";
    try {
      const c = await api.catalogEnrollConfirm(urlIn.trim(), codeIn.trim(), replace);
      toast.ok(`${c.name}: connected as ${preview.user_name}`);
      await Promise.all([catalogs.load(), tree.load()]);
      onClose();
    } catch (e) {
      err = clean(e);
    } finally {
      busy = false;
    }
  }
  const clean = (e: unknown) => errMsg(e).replace(/^catalog: /, "");
  // An edit after the check: the confirmation no longer matches.
  function edited() {
    preview = null;
    err = "";
  }
</script>

<div class="overlay" role="presentation">
  <div class="modal" role="dialog" aria-modal="true" tabindex="-1"
    onkeydown={(e) => { if (e.key === "Escape" && !busy) onClose(); }}>
    <header>
      <h2><IconUsers size={15} /> Connect to a catalog</h2>
      <button class="close" onclick={onClose} title="Close (Esc)" disabled={busy}><IconX size={14} /></button>
    </header>

    {#if fromLink}
      <p class="hint">A link asked ssh-tool to connect to a catalog. Check what it is before you connect: nothing is sent to it until you do.</p>
      <div class="form">
        <label>Catalog URL <input value={urlIn} disabled /></label>
        <button onclick={inspect} disabled={busy || !urlIn.trim() || !codeIn.trim() || !!preview}>Check</button>
      </div>
    {:else}
      <p class="hint">In the catalog's web page, "Connect ssh-tool" shows a code. Enter the catalog's address and the code.</p>
      <div class="form">
        <label>Catalog URL <input bind:value={urlIn} oninput={edited} placeholder="https://catalog.example.com" disabled={busy} /></label>
        <label>Code <input bind:value={codeIn} oninput={edited} placeholder="XXXXX-XXXXX-XXXXX-XXXXX" disabled={busy} onkeydown={(e) => e.key === "Enter" && inspect()} /></label>
        <button onclick={inspect} disabled={busy || !urlIn.trim() || !codeIn.trim()}>Check</button>
      </div>
    {/if}

    {#if busy && !preview}<p class="hint">Checking...</p>{/if}
    {#if err}<p class="err"><IconAlertTriangle size={13} /> {err}</p>{/if}

    {#if preview}
      <dl class="facts">
        <dt>Catalog</dt><dd><strong>{preview.server_name || "(no name)"}</strong></dd>
        <dt>Address</dt><dd class="url">{preview.base_url}</dd>
        {#if preview.normalised_url}<dt></dt><dd class="dim">used as {preview.normalised_url}</dd>{/if}
        <dt>Account</dt><dd>{preview.user_name}{#if preview.user_email} &lt;{preview.user_email}&gt;{/if}</dd>
        <dt>This device</dt><dd>appears there as "{preview.device_name}"</dd>
      </dl>
      <p class="hint"><IconLock size={11} /> Only connect if you asked for this code yourself and the address is your catalog's. The code works once and expires in {Math.max(0, Math.round(preview.expires_in / 60))} min.</p>

      {#if preview.existing_id}
        <p class="warn"><IconAlertTriangle size={13} /> This catalog is already attached as "{preview.existing_name}"{#if preview.existing_account}, signed in as {preview.existing_account}{/if}. Connecting replaces this machine's session there; the connections stay.</p>
        <footer>
          <button onclick={onClose} disabled={busy}>Cancel</button>
          <button class="primary" onclick={() => confirm(true)} disabled={busy}>{busy ? "Connecting..." : "Replace the session"}</button>
        </footer>
      {:else}
        <footer>
          <button onclick={onClose} disabled={busy}>Cancel</button>
          <button class="primary" onclick={() => confirm(false)} disabled={busy}>{busy ? "Connecting..." : "Connect"}</button>
        </footer>
      {/if}
    {:else if !busy}
      <footer><button onclick={onClose}>Cancel</button></footer>
    {/if}
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(17, 17, 27, 0.6); z-index: 1000; display: flex; align-items: center; justify-content: center; }
  .modal { background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: var(--r-md); width: min(560px, 94vw); max-height: 90vh; overflow: auto; display: flex; flex-direction: column; gap: 0.5rem; padding: 1rem 1.2rem; box-shadow: 0 8px 30px rgba(0,0,0,0.5); }
  header { display: flex; align-items: center; justify-content: space-between; }
  header h2 { margin: 0; font-size: var(--fs-md); display: flex; align-items: center; gap: 6px; }
  .close { background: transparent; border: 0; color: var(--subtext0); cursor: pointer; padding: 0.15rem 0.35rem; border-radius: var(--r-sm); }
  .hint, .dim { color: var(--subtext0); font-size: var(--fs-sm); margin: 0; line-height: 1.45; }
  .err { color: var(--red); font-size: var(--fs-sm); margin: 0; display: flex; gap: 5px; align-items: flex-start; }
  .warn { color: var(--yellow); font-size: var(--fs-sm); margin: 0; display: flex; gap: 5px; align-items: flex-start; }
  .form { display: flex; flex-direction: column; gap: 6px; }
  .form label { display: flex; flex-direction: column; gap: 2px; font-size: var(--fs-sm); }
  .form button { align-self: flex-end; }
  .facts { display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; margin: 0.3rem 0; font-size: var(--fs-sm); }
  .facts dt { color: var(--subtext0); }
  .facts dd { margin: 0; }
  /* The whole URL, never shortened: a look-alike host must be visible. */
  .url { font-family: var(--font-mono, monospace); word-break: break-all; color: var(--text); }
  footer { display: flex; justify-content: flex-end; gap: 0.5rem; border-top: 1px solid var(--surface0); padding-top: 0.5rem; }
  button.primary { background: var(--blue); color: var(--base); border: 0; }
</style>
