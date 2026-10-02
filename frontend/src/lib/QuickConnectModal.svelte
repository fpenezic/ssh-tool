<script lang="ts">
  import { quickConnect, type QuickTarget } from "./quickConnect.svelte";
  import { tree, credentials } from "./stores.svelte";
  import { clickOutside } from "./clickOutside";
  import { errMsg } from "./connectErrors";

  let target = $state("");
  let folderId = $state("");
  let credentialId = $state("");
  let busy = $state(false);
  let error = $state("");
  let inputEl = $state<HTMLInputElement | null>(null);
  let pick = $state(-1);
  let focused = $state(false);

  $effect(() => {
    if (quickConnect.open) {
      target = quickConnect.initial;
      folderId = "";
      credentialId = "";
      error = "";
      busy = false;
      pick = -1;
      setTimeout(() => inputEl?.focus(), 0);
    }
  });

  // "Customers/Acme/Prod" for every folder, sorted, for the inherit picker.
  function pathOf(id: string | null): string {
    const parts: string[] = [];
    let f = tree.folderById(id);
    let guard = 0;
    while (f && guard++ < 1000) {
      parts.unshift(f.name);
      f = tree.folderById(f.parent_id ?? null);
    }
    return parts.join(" / ");
  }
  const folderOptions = $derived(
    tree.folders.map((f) => ({ id: f.id, path: pathOf(f.id) })).sort((a, b) => a.path.localeCompare(b.path)),
  );
  const credOptions = $derived(
    credentials.list.filter((c) => c.kind !== "api_token").sort((a, b) => a.name.localeCompare(b.name)),
  );

  const suggestions = $derived.by(() => {
    if (!focused) return [];
    const q = target.trim().toLowerCase();
    return quickConnect.history.filter((h) => h.target.toLowerCase().includes(q) && h.target !== target.trim()).slice(0, 8);
  });

  function apply(h: QuickTarget) {
    target = h.target;
    folderId = h.folderId && tree.folderById(h.folderId) ? h.folderId : "";
    credentialId = h.credentialId && credentials.list.some((c) => c.id === h.credentialId) ? h.credentialId : "";
    pick = -1;
  }

  async function submit() {
    if (busy || !target.trim()) return;
    busy = true;
    error = "";
    try {
      const ok = await quickConnect.connect({
        target: target.trim(),
        folderId: folderId || undefined,
        credentialId: credentialId || undefined,
      });
      if (ok) quickConnect.close();
    } catch (e) {
      error = errMsg(e);
    } finally {
      busy = false;
    }
  }

  function onInputKey(e: KeyboardEvent) {
    const n = suggestions.length;
    if (e.key === "ArrowDown" && n) {
      e.preventDefault();
      pick = (pick + 1) % n;
    } else if (e.key === "ArrowUp" && n) {
      e.preventDefault();
      pick = pick <= 0 ? n - 1 : pick - 1;
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (pick >= 0 && pick < n) apply(suggestions[pick]);
      else submit();
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") { e.preventDefault(); quickConnect.close(); }
  }
</script>

{#if quickConnect.open}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="overlay" onkeydown={onKey} role="dialog" aria-modal="true" tabindex="-1">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" use:clickOutside={{ onOutside: () => { if (!busy) quickConnect.close(); } }}>
    <h3>Quick connect</h3>
    <p class="hint">Not saved. Use "Save as connection" on the tab to keep it.</p>

    <label class="fld">
      <span>Host</span>
      <div class="inp-wrap">
        <input
          bind:this={inputEl}
          bind:value={target}
          class="inp"
          placeholder="user@host or user@host:port"
          spellcheck="false"
          autocomplete="off"
          disabled={busy}
          onkeydown={onInputKey}
          oninput={() => (pick = -1)}
          onfocus={() => (focused = true)}
          onblur={() => (focused = false)}
        />
        {#if suggestions.length}
          <div class="suggest" role="listbox">
            {#each suggestions as h, i (h.target)}
              <!-- mousedown: a click would land after the input's blur closed the list -->
              <button
                type="button"
                role="option"
                aria-selected={i === pick}
                class:picked={i === pick}
                onmousedown={(e) => { e.preventDefault(); apply(h); }}
              >
                <span class="t">{h.target}</span>
                {#if h.folderId && tree.folderById(h.folderId)}
                  <span class="via">via {tree.folderById(h.folderId)?.name}</span>
                {/if}
                <span
                  class="forget"
                  role="presentation"
                  title="Forget"
                  onmousedown={(e) => { e.preventDefault(); e.stopPropagation(); quickConnect.forget(h.target); }}
                >×</span>
              </button>
            {/each}
          </div>
        {/if}
      </div>
    </label>

    <label class="fld">
      <span>Inherit from folder</span>
      <select bind:value={folderId} disabled={busy}>
        <option value="">None - connect directly</option>
        {#each folderOptions as f (f.id)}
          <option value={f.id}>{f.path}</option>
        {/each}
      </select>
    </label>
    <p class="sub">Uses the folder's jump host, credential and network profile.</p>

    <label class="fld">
      <span>Credential</span>
      <select bind:value={credentialId} disabled={busy}>
        <option value="">{folderId ? "Inherited from the folder" : "Agent / keys, else ask for a password"}</option>
        {#each credOptions as c (c.id)}
          <option value={c.id}>{c.name} ({c.kind})</option>
        {/each}
      </select>
    </label>

    {#if error}<div class="err">{error}</div>{/if}

    <div class="row">
      <button onclick={() => quickConnect.close()} disabled={busy}>Cancel</button>
      <button class="primary" onclick={submit} disabled={busy || !target.trim()}>
        {busy ? "Connecting..." : "Connect"}
      </button>
    </div>
  </div>
</div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    /* Tool-window tier: host key and login prompts (9550) open above it. */
    z-index: 9000;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(440px, calc(100vw - 32px));
    padding: 1.1rem 1.3rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.5);
    display: flex; flex-direction: column; gap: 0.6rem;
  }
  h3 { margin: 0; font-size: 1rem; }
  .hint { margin: -0.3rem 0 0.2rem; font-size: 0.78rem; color: var(--subtext0); }
  .sub { margin: -0.35rem 0 0; font-size: 0.72rem; color: var(--overlay1); }
  .fld { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--subtext0); }
  .inp-wrap { position: relative; }
  .inp, select {
    width: 100%; box-sizing: border-box;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px;
    padding: 0.4rem 0.6rem; font: inherit; font-size: 0.88rem;
    outline: none;
  }
  .inp:focus, select:focus { border-color: var(--blue); }
  .suggest {
    position: absolute; top: calc(100% + 3px); left: 0; right: 0;
    z-index: 2;
    display: flex; flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface1); border-radius: 4px;
    box-shadow: 0 4px 12px rgba(0,0,0,0.35);
    padding: 3px;
  }
  .suggest button {
    display: flex; align-items: center; gap: 0.6rem;
    background: transparent; border: 0; color: var(--text);
    font: inherit; font-size: 0.82rem; text-align: left;
    padding: 0.25rem 0.5rem; border-radius: 3px; cursor: pointer;
  }
  .suggest button:hover, .suggest button.picked { background: var(--surface0); }
  .suggest .t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .suggest .via { color: var(--overlay1); font-size: 0.72rem; }
  .suggest .forget { color: var(--overlay1); padding: 0 0.2rem; }
  .suggest .forget:hover { color: var(--red); }
  .err {
    font-size: 0.8rem; color: var(--red);
    background: color-mix(in srgb, var(--red) 10%, transparent);
    border-radius: 4px; padding: 0.4rem 0.6rem;
    white-space: pre-wrap; word-break: break-word;
  }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.2rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.35rem 0.8rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.6; cursor: default; }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover:not(:disabled) { background: var(--sapphire); }
</style>
