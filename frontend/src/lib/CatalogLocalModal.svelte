<script lang="ts">
  // The "Copy to my connections" dialog and the report of what a keep or a
  // copy left empty (catalogLocal).
  import SearchableSelect from "./SearchableSelect.svelte";
  import { catalogLocal } from "./catalogLocal.svelte";
  import { tree, selection, view } from "./stores.svelte";
  import { IconX, IconAlertTriangle, IconHost, IconFolder } from "./iconMap";

  let target = $state("");
  let busy = $state(false);
  let err = $state("");
  function pathOf(id: string): string {
    const parts: string[] = [];
    let cur = tree.folders.find((f) => f.id === id);
    for (let i = 0; cur && i < 32; i++) {
      parts.unshift(cur.name);
      const p = cur.parent_id;
      cur = p ? tree.folders.find((f) => f.id === p) : undefined;
    }
    return parts.join(" / ");
  }
  // Where an emptied row sits (several rows often share a name).
  function whereOf(kind: string, id: string): string {
    const parent = kind === "folders" ? tree.folders.find((f) => f.id === id)?.parent_id : tree.connections.find((c) => c.id === id)?.folder_id;
    return parent ? "in " + pathOf(parent) : "";
  }
  // Only the user's own folders take a copy.
  const targets = $derived([
    ...tree.folders.filter((f) => !f.catalog_id).map((f) => ({ value: f.id, label: pathOf(f.id) })).sort((a, b) => a.label.localeCompare(b.label)),
  ]);
  $effect(() => {
    if (catalogLocal.copy) {
      target = "";
      err = "";
    }
  });
  let repoint = $state(true);
  $effect(() => {
    if (catalogLocal.dynSave) {
      target = "";
      err = "";
      repoint = true;
    }
  });
  async function saveDyn() {
    busy = true;
    err = (await catalogLocal.runDynSave(target, repoint)) ?? "";
    busy = false;
  }
  async function copy() {
    busy = true;
    err = (await catalogLocal.runCopy(target)) ?? "";
    busy = false;
  }
  function open(kind: string, id: string) {
    catalogLocal.report = null;
    view.setTab("connections");
    if (kind === "folders") void selection.selectFolderById(id);
    else void selection.selectConnection(id);
  }
</script>

{#if catalogLocal.copy}
  <div class="overlay" role="presentation">
    <div class="modal" role="dialog" aria-modal="true" tabindex="-1" onkeydown={(e) => { if (e.key === "Escape" && !busy) catalogLocal.copy = null; }}>
      <header>
        <h2>Copy "{catalogLocal.copy.name}" to my connections</h2>
        <button class="close" onclick={() => (catalogLocal.copy = null)} title="Cancel (Esc)" disabled={busy}><IconX size={14} /></button>
      </header>
      <p class="hint">A copy of your own: it will not follow the catalog. Changes there do not reach it, and yours do not reach the catalog. Placeholders you have not bound come over empty and are noted on each connection.</p>
      <span class="label">Into</span>
      <SearchableSelect value={target} options={targets} placeholder="Pick a folder..." emptyLabel="(top level)" onChange={(v: string) => (target = v)} />
      {#if err}<p class="err"><IconAlertTriangle size={13} /> {err}</p>{/if}
      <footer>
        <button onclick={() => (catalogLocal.copy = null)} disabled={busy}>Cancel</button>
        <button class="primary" onclick={copy} disabled={busy}>{busy ? "Copying..." : "Copy"}</button>
      </footer>
    </div>
  </div>
{/if}

{#if catalogLocal.dynSave}
  <div class="overlay" role="presentation">
    <div class="modal" role="dialog" aria-modal="true" tabindex="-1" onkeydown={(e) => { if (e.key === "Escape" && !busy) catalogLocal.dynSave = null; }}>
      <header>
        <h2>Save "{catalogLocal.dynSave.name}" as a connection</h2>
        <button class="close" onclick={() => (catalogLocal.dynSave = null)} title="Cancel (Esc)" disabled={busy}><IconX size={14} /></button>
      </header>
      <p class="hint">A fixed connection with the address, port, user and credential this host has now. It does not follow the inventory: if the address changes, change it by hand. A fixed bastion can go to a catalog; an inventory host cannot.</p>
      <span class="label">Into</span>
      <SearchableSelect value={target} options={targets} placeholder="Pick a folder..." emptyLabel="(top level)" onChange={(v: string) => (target = v)} />
      <label class="label"><input type="checkbox" bind:checked={repoint} disabled={busy} /> Point jump hosts that go through this host at the new connection</label>
      {#if err}<p class="err"><IconAlertTriangle size={13} /> {err}</p>{/if}
      <footer>
        <button onclick={() => (catalogLocal.dynSave = null)} disabled={busy}>Cancel</button>
        <button class="primary" onclick={saveDyn} disabled={busy}>{busy ? "Saving..." : "Save"}</button>
      </footer>
    </div>
  </div>
{/if}

{#if catalogLocal.report}
  {@const r = catalogLocal.report.res}
  <div class="overlay" role="presentation">
    <div class="modal" role="dialog" aria-modal="true" tabindex="-1" onkeydown={(e) => { if (e.key === "Escape") catalogLocal.report = null; }}>
      <header>
        <h2>{catalogLocal.report.title}</h2>
        <button class="close" onclick={() => (catalogLocal.report = null)} title="Close (Esc)"><IconX size={14} /></button>
      </header>
      <p class="hint">{r.connections} connection{r.connections === 1 ? "" : "s"}, {r.folders} folder{r.folders === 1 ? "" : "s"}, {r.forwards} forward{r.forwards === 1 ? "" : "s"}{r.credentials ? `, ${r.credentials} credential${r.credentials === 1 ? "" : "s"} made your own` : ""}.</p>
      {#if r.emptied.length}
        <p class="hint">Left empty: these named a catalog placeholder you had not bound. Set your own before you connect.</p>
        <div class="list">
          {#each r.emptied as e, i (i)}
            <button class="item" onclick={() => open(e.kind, e.id)}>
              {#if e.kind === "folders"}<IconFolder size={12} />{:else}<IconHost size={12} />{/if}
              <strong>{e.name}</strong> <span>{e.field}</span>{#if e.slot}<span class="dim">was "{e.slot}"</span>{/if}<span class="dim where">{whereOf(e.kind, e.id)}</span>
            </button>
          {/each}
        </div>
      {/if}
      {#if r.passwords_skipped}
        <p class="hint">{r.passwords_skipped} stored password{r.passwords_skipped === 1 ? "" : "s"} could not be copied (the vault was locked); set them on the copies.</p>
      {/if}
      {#if r.outside_jumps?.length}
        <p class="hint">Still jumping through a connection outside the copy (the original): {r.outside_jumps.join(", ")}.</p>
      {/if}
      <footer><button class="primary" onclick={() => (catalogLocal.report = null)}>Close</button></footer>
    </div>
  </div>
{/if}

<style>
  .overlay { position: fixed; inset: 0; background: rgba(17, 17, 27, 0.6); z-index: 1000; display: flex; align-items: center; justify-content: center; }
  .modal { background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: var(--r-md); width: min(520px, 94vw); max-height: 86vh; display: flex; flex-direction: column; gap: 0.6rem; padding: 1rem 1.2rem; box-shadow: 0 8px 30px rgba(0,0,0,0.5); }
  header { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  header h2 { margin: 0; font-size: var(--fs-md); overflow-wrap: anywhere; }
  .close { background: transparent; border: 0; color: var(--subtext0); cursor: pointer; padding: 0.15rem 0.35rem; border-radius: var(--r-sm); }
  .hint, .dim { color: var(--subtext0); font-size: var(--fs-sm); margin: 0; line-height: 1.45; }
  .label { font-size: var(--fs-sm); color: var(--subtext1); }
  .list { display: flex; flex-direction: column; gap: 3px; overflow-y: auto; min-height: 0; }
  .item { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; text-align: left; background: var(--mantle); border: 1px solid var(--surface0); border-radius: var(--r-sm); padding: 4px 8px; font-size: var(--fs-sm); color: var(--text); cursor: pointer; }
  .item:hover { border-color: var(--blue); }
  .where { margin-left: auto; }
  .err { color: var(--red); font-size: var(--fs-sm); margin: 0; display: flex; gap: 5px; }
  footer { display: flex; justify-content: flex-end; gap: 0.5rem; border-top: 1px solid var(--surface0); padding-top: 0.5rem; }
  button.primary { background: var(--blue); color: var(--base); border: 0; }
</style>
