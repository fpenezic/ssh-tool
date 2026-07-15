<script lang="ts">
  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { clickOutside } from "./clickOutside";
  import { api, type KeepassGroupInfo, type KeepassEntryInfo } from "./api";

  interface PickResult {
    db_id: string;
    entry_uuid: string;
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

  let dbs = $state<{ id: string; name: string }[]>([]);
  let dbId = $state("");
  let tree = $state<KeepassGroupInfo[]>([]);
  let loading = $state(false);
  let err = $state<string | null>(null);
  let search = $state("");
  // Collapsed group paths (default expanded).
  let collapsed = $state<Record<string, boolean>>({});

  // Selected entry + field.
  let selEntry = $state<KeepassEntryInfo | null>(null);
  let selField = $state("");

  onMount(async () => {
    try {
      const list = await api.keepassList();
      dbs = list.map((d) => ({ id: d.id, name: d.name }));
      if (dbs.length === 1) {
        dbId = dbs[0].id;
        await loadTree();
      }
    } catch (e) {
      err = errMsg(e);
    }
  });

  async function loadTree() {
    selEntry = null;
    selField = "";
    tree = [];
    if (!dbId) return;
    loading = true;
    err = null;
    try {
      tree = await api.keepassBrowse(dbId);
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }

  let refreshing = $state(false);
  // Force a fresh pull from the remote (or re-read the local file), then
  // re-list - so an entry just added in KeePass Desktop shows up.
  async function refreshDb() {
    if (!dbId) return;
    refreshing = true;
    err = null;
    try {
      await api.keepassRefresh(dbId);
      await loadTree();
    } catch (e) {
      err = errMsg(e);
    } finally {
      refreshing = false;
    }
  }

  function fieldsFor(e: KeepassEntryInfo): string[] {
    return [
      ...(e.has_pass ? ["password"] : []),
      ...(e.custom_keys || []),
      ...(e.attachments || []),
    ];
  }

  function isKeyField(e: KeepassEntryInfo, field: string): boolean {
    const attach = e.attachments || [];
    const custom = e.custom_keys || [];
    return attach.includes(field) || (field !== "password" && custom.includes(field));
  }

  function selectEntry(e: KeepassEntryInfo) {
    selEntry = e;
    const fields = fieldsFor(e);
    selField = fields[0] || "password";
  }

  function toggleGroup(path: string) {
    collapsed = { ...collapsed, [path]: !collapsed[path] };
  }

  // Flat list of all entries with their group path, for search mode.
  function flatEntries(groups: KeepassGroupInfo[]): KeepassEntryInfo[] {
    const out: KeepassEntryInfo[] = [];
    const walk = (gs: KeepassGroupInfo[]) => {
      for (const g of gs || []) {
        for (const e of g.entries || []) out.push(e);
        walk(g.groups || []);
      }
    };
    walk(groups);
    return out;
  }

  const searchHits = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return null;
    return flatEntries(tree).filter(
      (e) =>
        e.title.toLowerCase().includes(q) ||
        e.username.toLowerCase().includes(q) ||
        e.group_path.toLowerCase().includes(q),
    );
  });

  function confirm() {
    if (!selEntry || !dbId) return;
    onPick({
      db_id: dbId,
      entry_uuid: selEntry.uuid,
      field: selField,
      is_key: isKeyField(selEntry, selField),
      name: selEntry.title || "KeePass entry",
      username: selEntry.username || "",
    });
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1"
  onkeydown={(e) => { if (e.key === "Escape") onClose(); }}>
  <div class="modal" role="document" use:clickOutside={{ onOutside: onClose }}>
    <header>
      <h1>Pick a KeePass entry</h1>
      <button class="close" onclick={onClose}>✕</button>
    </header>

    {#if dbs.length === 0}
      <p class="hint">
        No KeePass databases registered. Add one in Settings → KeePass first.
      </p>
    {:else}
      <div class="body">
        <label class="db-row">Database
          <select bind:value={dbId} onchange={loadTree}>
            <option value="">Select…</option>
            {#each dbs as d (d.id)}
              <option value={d.id}>{d.name}</option>
            {/each}
          </select>
        </label>

        {#if dbId}
          <div class="search-row">
            <input
              class="search"
              bind:value={search}
              placeholder="Search entries by title / username / group…"
            />
            <button type="button" class="refresh-btn" onclick={refreshDb}
              disabled={refreshing || loading}
              title="Re-read the database (picks up entries just added in KeePass)">
              {refreshing ? "Refreshing…" : "Refresh"}
            </button>
          </div>
        {/if}

        {#if loading}
          <p class="hint">Opening database…</p>
        {:else if err}
          <p class="hint err">{err}</p>
        {:else if dbId}
          <div class="tree">
            {#if searchHits !== null}
              {#if searchHits.length === 0}
                <p class="hint">No matches.</p>
              {:else}
                {#each searchHits as e (e.uuid)}
                  <button
                    class="entry {selEntry?.uuid === e.uuid ? 'sel' : ''}"
                    onclick={() => selectEntry(e)}
                  >
                    <span class="title">{e.title || "(untitled)"}</span>
                    <span class="sub">{e.group_path}{e.username ? " · " + e.username : ""}</span>
                  </button>
                {/each}
              {/if}
            {:else}
              {#each tree as g (g.path)}
                {@render groupNode(g, 0)}
              {/each}
            {/if}
          </div>

          {#if selEntry}
            <div class="field-row">
              <span class="field-label">Field</span>
              <select bind:value={selField}>
                {#each fieldsFor(selEntry) as f}
                  <option value={f}>{f}{isKeyField(selEntry, f) ? " (key)" : ""}</option>
                {/each}
              </select>
              <span class="field-note">
                {isKeyField(selEntry, selField) ? "used as a private key" : "used as a password"}
              </span>
            </div>
          {/if}
        {/if}
      </div>

      <footer>
        <button onclick={onClose}>Cancel</button>
        <button class="primary" disabled={!selEntry} onclick={confirm}>Use this entry</button>
      </footer>
    {/if}
  </div>
</div>

{#snippet groupNode(g: KeepassGroupInfo, depth: number)}
  <div class="group" style="--depth: {depth}">
    <button class="group-head" onclick={() => toggleGroup(g.path)}>
      <span class="chev">{collapsed[g.path] ? "▶" : "▼"}</span>
      {g.name}
    </button>
    {#if !collapsed[g.path]}
      {#each g.entries || [] as e (e.uuid)}
        <button
          class="entry {selEntry?.uuid === e.uuid ? 'sel' : ''}"
          style="--depth: {depth + 1}"
          onclick={() => selectEntry(e)}
        >
          <span class="title">{e.title || "(untitled)"}</span>
          {#if e.username}<span class="sub">{e.username}</span>{/if}
        </button>
      {/each}
      {#each g.groups || [] as sub (sub.path)}
        {@render groupNode(sub, depth + 1)}
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
  .db-row { display: flex; flex-direction: column; gap: 4px; font-size: 0.85rem; }
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
