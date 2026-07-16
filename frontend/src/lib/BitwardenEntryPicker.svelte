<script lang="ts">
  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { clickOutside } from "./clickOutside";
  import {
    api,
    type BitwardenGroupInfo,
    type BitwardenCollectionInfo,
    type BitwardenCipherInfo,
  } from "./api";

  interface PickResult {
    server_id: string;
    cipher_id: string;
    field: string;
    is_key: boolean;
    name: string;
    username: string;
  }

  interface Props {
    onClose: () => void;
    onPick: (r: PickResult) => void;
  }
  let { onClose, onPick }: Props = $props();

  let servers = $state<{ id: string; name: string }[]>([]);
  let serverId = $state("");
  let tree = $state<BitwardenGroupInfo[]>([]);
  let loading = $state(false);
  let syncing = $state(false);
  let err = $state<string | null>(null);
  let search = $state("");
  // Collapsed node keys (default expanded).
  let collapsed = $state<Record<string, boolean>>({});

  // Selected item + field.
  let selCipher = $state<BitwardenCipherInfo | null>(null);
  let selField = $state("");

  onMount(async () => {
    try {
      const list = await api.bitwardenList();
      servers = list.map((s) => ({ id: s.id, name: s.name }));
      if (servers.length === 1) {
        serverId = servers[0].id;
        await loadTree();
      }
    } catch (e) {
      err = errMsg(e);
    }
  });

  async function loadTree() {
    selCipher = null;
    selField = "";
    tree = [];
    if (!serverId) return;
    loading = true;
    err = null;
    try {
      tree = await api.bitwardenBrowse(serverId);
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }

  // Force a fresh sync, then re-list - so an item just added on the server (or
  // by a teammate in a shared org) shows up.
  async function syncServer() {
    if (!serverId) return;
    syncing = true;
    err = null;
    try {
      await api.bitwardenSync(serverId);
      await loadTree();
    } catch (e) {
      err = errMsg(e);
    } finally {
      syncing = false;
    }
  }

  // The fields a cipher exposes: password / username / SSH key / custom fields.
  function fieldsFor(c: BitwardenCipherInfo): string[] {
    return [
      ...(c.has_password ? ["password"] : []),
      ...(c.is_ssh_key ? ["privatekey"] : []),
      ...(c.username ? ["username"] : []),
      ...(c.custom_keys || []),
    ];
  }

  function isKeyField(field: string): boolean {
    return field === "privatekey";
  }

  function selectCipher(c: BitwardenCipherInfo) {
    selCipher = c;
    const fields = fieldsFor(c);
    selField = fields[0] || "password";
  }

  function toggle(key: string) {
    collapsed = { ...collapsed, [key]: !collapsed[key] };
  }

  // Flat list of all ciphers with a group/collection path, for search mode.
  interface FlatItem {
    cipher: BitwardenCipherInfo;
    path: string;
  }
  function flatItems(groups: BitwardenGroupInfo[]): FlatItem[] {
    const out: FlatItem[] = [];
    for (const g of groups || []) {
      for (const c of g.ciphers || []) out.push({ cipher: c, path: g.name });
      for (const col of g.collections || []) {
        for (const c of col.ciphers || []) out.push({ cipher: c, path: g.name + " / " + col.name });
      }
    }
    return out;
  }

  const searchHits = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return null;
    return flatItems(tree).filter(
      (it) =>
        it.cipher.name.toLowerCase().includes(q) ||
        it.cipher.username.toLowerCase().includes(q) ||
        it.path.toLowerCase().includes(q),
    );
  });

  function confirm() {
    if (!selCipher || !serverId) return;
    onPick({
      server_id: serverId,
      cipher_id: selCipher.id,
      field: selField,
      is_key: isKeyField(selField),
      name: selCipher.name || "Bitwarden item",
      username: selCipher.username || "",
    });
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1"
  onkeydown={(e) => { if (e.key === "Escape") onClose(); }}>
  <div class="modal" role="document" use:clickOutside={{ onOutside: onClose }}>
    <header>
      <h1>Pick a Bitwarden item</h1>
      <button class="close" onclick={onClose}>✕</button>
    </header>

    {#if servers.length === 0}
      <p class="hint">
        No Bitwarden servers registered. Add one in Settings - Bitwarden first.
      </p>
    {:else}
      <div class="body">
        <label class="server-row">Server
          <select bind:value={serverId} onchange={loadTree}>
            <option value="">Select…</option>
            {#each servers as s (s.id)}
              <option value={s.id}>{s.name}</option>
            {/each}
          </select>
        </label>

        {#if serverId}
          <div class="search-row">
            <input
              class="search"
              bind:value={search}
              placeholder="Search items by name / username / collection…"
            />
            <button type="button" class="refresh-btn" onclick={syncServer}
              disabled={syncing || loading}
              title="Re-sync the server (picks up items just added)">
              {syncing ? "Syncing…" : "Sync"}
            </button>
          </div>
        {/if}

        {#if loading}
          <p class="hint">Opening vault…</p>
        {:else if err}
          <p class="hint err">{err}</p>
        {:else if serverId}
          <div class="tree">
            {#if searchHits !== null}
              {#if searchHits.length === 0}
                <p class="hint">No matches.</p>
              {:else}
                {#each searchHits as it (it.cipher.id)}
                  <button
                    class="entry {selCipher?.id === it.cipher.id ? 'sel' : ''}"
                    onclick={() => selectCipher(it.cipher)}
                  >
                    <span class="title">{it.cipher.name || "(untitled)"}{it.cipher.is_ssh_key ? " · SSH key" : ""}</span>
                    <span class="sub">{it.path}{it.cipher.username ? " · " + it.cipher.username : ""}</span>
                  </button>
                {/each}
              {/if}
            {:else}
              {#each tree as g (g.org_id)}
                {@render groupNode(g)}
              {/each}
            {/if}
          </div>

          {#if selCipher}
            <div class="field-row">
              <span class="field-label">Field</span>
              <select bind:value={selField}>
                {#each fieldsFor(selCipher) as f}
                  <option value={f}>{f}{isKeyField(f) ? " (key)" : ""}</option>
                {/each}
              </select>
              <span class="field-note">
                {isKeyField(selField) ? "used as a private key" : "used as a password"}
              </span>
            </div>
          {/if}
        {/if}
      </div>

      <footer>
        <button onclick={onClose}>Cancel</button>
        <button class="primary" disabled={!selCipher} onclick={confirm}>Use this item</button>
      </footer>
    {/if}
  </div>
</div>

{#snippet cipherBtn(c: BitwardenCipherInfo, depth: number)}
  <button
    class="entry {selCipher?.id === c.id ? 'sel' : ''}"
    style="--depth: {depth}"
    onclick={() => selectCipher(c)}
  >
    <span class="title">{c.name || "(untitled)"}{c.is_ssh_key ? " · SSH key" : ""}</span>
    {#if c.username}<span class="sub">{c.username}</span>{/if}
  </button>
{/snippet}

{#snippet collectionNode(col: BitwardenCollectionInfo, groupKey: string)}
  {@const key = groupKey + "/" + col.id}
  <div class="group" style="--depth: 1">
    <button class="group-head" style="--depth: 1" onclick={() => toggle(key)}>
      <span class="chev">{collapsed[key] ? "▶" : "▼"}</span>
      {col.name}
    </button>
    {#if !collapsed[key]}
      {#each col.ciphers || [] as c (c.id)}
        {@render cipherBtn(c, 2)}
      {/each}
    {/if}
  </div>
{/snippet}

{#snippet groupNode(g: BitwardenGroupInfo)}
  <div class="group" style="--depth: 0">
    <button class="group-head" onclick={() => toggle(g.org_id)}>
      <span class="chev">{collapsed[g.org_id] ? "▶" : "▼"}</span>
      {g.name}
    </button>
    {#if !collapsed[g.org_id]}
      {#each g.collections || [] as col (col.id)}
        {@render collectionNode(col, g.org_id)}
      {/each}
      {#each g.ciphers || [] as c (c.id)}
        {@render cipherBtn(c, 1)}
      {/each}
    {/if}
  </div>
{/snippet}

<style>
  .overlay {
    position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5);
    display: flex; align-items: center; justify-content: center; z-index: 1000;
  }
  .modal {
    background: var(--bg, #1e1e2e); border: 1px solid var(--border, #333);
    border-radius: 8px; width: min(560px, 92vw); max-height: 82vh;
    display: flex; flex-direction: column;
  }
  header, footer {
    display: flex; align-items: center; padding: 10px 14px;
    border-bottom: 1px solid var(--border, #333);
  }
  footer { border-bottom: none; border-top: 1px solid var(--border, #333); justify-content: flex-end; gap: 8px; }
  header h1 { font-size: 1rem; margin: 0; flex: 1; }
  .close { background: none; border: none; cursor: pointer; font-size: 1rem; }
  .body { padding: 12px 14px; overflow: hidden; display: flex; flex-direction: column; gap: 10px; min-height: 0; }
  .server-row { display: flex; flex-direction: column; gap: 4px; font-size: 0.85rem; }
  .search-row { display: flex; gap: 6px; align-items: stretch; }
  .search { flex: 1; min-width: 0; box-sizing: border-box; }
  .refresh-btn { flex-shrink: 0; white-space: nowrap; font-size: 0.8rem; }
  .tree { overflow-y: auto; border: 1px solid var(--border, #333); border-radius: 6px; padding: 4px; min-height: 180px; max-height: 40vh; }
  .group-head {
    display: block; width: 100%; text-align: left; background: none; border: none;
    cursor: pointer; font-weight: 600; padding: 4px 6px;
    padding-left: calc(6px + var(--depth, 0) * 14px);
  }
  .chev { display: inline-block; width: 1em; opacity: 0.7; font-size: 0.7rem; }
  .entry {
    display: flex; flex-direction: column; gap: 1px; width: 100%; text-align: left;
    background: none; border: none; cursor: pointer; border-radius: 4px;
    padding: 4px 6px; padding-left: calc(6px + var(--depth, 1) * 14px);
  }
  .entry:hover { background: var(--hover, rgba(255,255,255,0.06)); }
  .entry.sel { background: var(--accent-soft, rgba(120,150,255,0.18)); }
  .entry .title { font-size: 0.9rem; }
  .entry .sub { font-size: 0.75rem; opacity: 0.6; }
  .field-row { display: flex; align-items: center; gap: 8px; font-size: 0.85rem; }
  .field-label { font-weight: 600; }
  .field-note { opacity: 0.6; font-size: 0.8rem; }
  .hint { opacity: 0.7; font-size: 0.85rem; padding: 8px 14px; }
  .err { color: var(--danger, #e66); }
</style>
