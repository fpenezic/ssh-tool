<script lang="ts">
  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { clickOutside } from "./clickOutside";
  import {
    api,
    type InfisicalGroupInfo,
    type InfisicalEnvInfo,
    type InfisicalEntryInfo,
  } from "./api";

  interface PickResult {
    server_id: string;
    project_id: string;
    environment: string;
    secret_path: string;
    key: string;
    is_key: boolean;
    name: string;
  }

  interface Props {
    onClose: () => void;
    onPick: (r: PickResult) => void;
  }
  let { onClose, onPick }: Props = $props();

  let servers = $state<{ id: string; name: string }[]>([]);
  let serverId = $state("");
  let tree = $state<InfisicalGroupInfo[]>([]);
  let loading = $state(false);
  let err = $state<string | null>(null);
  let search = $state("");
  // Collapsed node keys (default expanded).
  let collapsed = $state<Record<string, boolean>>({});

  // Selected secret: which project + environment + entry.
  let sel = $state<{
    projectId: string;
    environment: string;
    entry: InfisicalEntryInfo;
  } | null>(null);

  onMount(async () => {
    try {
      const list = await api.infisicalList();
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
    sel = null;
    tree = [];
    if (!serverId) return;
    loading = true;
    err = null;
    try {
      tree = await api.infisicalBrowse(serverId);
    } catch (e) {
      err = errMsg(e);
    } finally {
      loading = false;
    }
  }

  function selectEntry(projectId: string, environment: string, entry: InfisicalEntryInfo) {
    sel = { projectId, environment, entry };
  }

  function isSel(projectId: string, environment: string, entry: InfisicalEntryInfo): boolean {
    return (
      sel !== null &&
      sel.projectId === projectId &&
      sel.environment === environment &&
      sel.entry.path === entry.path &&
      sel.entry.key === entry.key
    );
  }

  function toggle(key: string) {
    collapsed = { ...collapsed, [key]: !collapsed[key] };
  }

  // A label combining folder path and key, e.g. "/cloudflare · password".
  function entryLabel(e: InfisicalEntryInfo): string {
    const p = e.path && e.path !== "/" ? e.path + " · " : "";
    return p + e.key;
  }

  // Flat list of all entries with a project/env path, for search mode.
  interface FlatItem {
    projectId: string;
    projectName: string;
    environment: string;
    envName: string;
    entry: InfisicalEntryInfo;
  }
  function flatItems(groups: InfisicalGroupInfo[]): FlatItem[] {
    const out: FlatItem[] = [];
    for (const g of groups || []) {
      for (const env of g.environments || []) {
        for (const e of env.entries || []) {
          out.push({
            projectId: g.project_id,
            projectName: g.name,
            environment: env.slug,
            envName: env.name,
            entry: e,
          });
        }
      }
    }
    return out;
  }

  const searchHits = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return null;
    return flatItems(tree).filter(
      (it) =>
        it.entry.key.toLowerCase().includes(q) ||
        it.entry.path.toLowerCase().includes(q) ||
        it.projectName.toLowerCase().includes(q) ||
        it.envName.toLowerCase().includes(q),
    );
  });

  function confirm() {
    if (!sel || !serverId) return;
    onPick({
      server_id: serverId,
      project_id: sel.projectId,
      environment: sel.environment,
      secret_path: sel.entry.path || "/",
      key: sel.entry.key,
      is_key: sel.entry.is_key,
      name: sel.entry.key || "Infisical secret",
    });
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1"
  onkeydown={(e) => { if (e.key === "Escape") onClose(); }}>
  <div class="modal" role="document" use:clickOutside={{ onOutside: onClose }}>
    <header>
      <h1>Pick an Infisical secret</h1>
      <button class="close" onclick={onClose}>✕</button>
    </header>

    {#if servers.length === 0}
      <p class="hint">
        No Infisical servers registered. Add one in Settings - Infisical first.
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
              placeholder="Search secrets by key / path / project…"
            />
            <button type="button" class="refresh-btn" onclick={loadTree}
              disabled={loading}
              title="Re-read the tree (picks up secrets just added)">
              {loading ? "Loading…" : "Refresh"}
            </button>
          </div>
        {/if}

        {#if loading}
          <p class="hint">Reading secrets…</p>
        {:else if err}
          <p class="hint err">{err}</p>
        {:else if serverId}
          <div class="tree">
            {#if searchHits !== null}
              {#if searchHits.length === 0}
                <p class="hint">No matches.</p>
              {:else}
                {#each searchHits as it (it.projectId + it.environment + it.entry.path + it.entry.key)}
                  <button
                    class="entry {isSel(it.projectId, it.environment, it.entry) ? 'sel' : ''}"
                    onclick={() => selectEntry(it.projectId, it.environment, it.entry)}
                  >
                    <span class="title">{entryLabel(it.entry)}{it.entry.is_key ? " · SSH key" : ""}</span>
                    <span class="sub">{it.projectName} / {it.envName}</span>
                  </button>
                {/each}
              {/if}
            {:else}
              {#each tree as g (g.project_id)}
                {@render projectNode(g)}
              {/each}
            {/if}
          </div>

          {#if sel}
            <div class="field-row">
              <span class="field-note">
                {sel.entry.is_key ? "used as a private key" : "used as a password"}
              </span>
            </div>
          {/if}
        {/if}
      </div>

      <footer>
        <button onclick={onClose}>Cancel</button>
        <button class="primary" disabled={!sel} onclick={confirm}>Use this secret</button>
      </footer>
    {/if}
  </div>
</div>

{#snippet entryBtn(projectId: string, environment: string, e: InfisicalEntryInfo, depth: number)}
  <button
    class="entry {isSel(projectId, environment, e) ? 'sel' : ''}"
    style="--depth: {depth}"
    onclick={() => selectEntry(projectId, environment, e)}
  >
    <span class="title">{entryLabel(e)}{e.is_key ? " · SSH key" : ""}</span>
    {#if !e.has_value}<span class="sub">(empty)</span>{/if}
  </button>
{/snippet}

{#snippet envNode(projectId: string, env: InfisicalEnvInfo)}
  {@const key = projectId + "/" + env.slug}
  <div class="group" style="--depth: 1">
    <button class="group-head" style="--depth: 1" onclick={() => toggle(key)}>
      <span class="chev">{collapsed[key] ? "▶" : "▼"}</span>
      {env.name}
    </button>
    {#if !collapsed[key]}
      {#each env.entries || [] as e (e.path + "/" + e.key)}
        {@render entryBtn(projectId, env.slug, e, 2)}
      {/each}
    {/if}
  </div>
{/snippet}

{#snippet projectNode(g: InfisicalGroupInfo)}
  <div class="group" style="--depth: 0">
    <button class="group-head" onclick={() => toggle(g.project_id)}>
      <span class="chev">{collapsed[g.project_id] ? "▶" : "▼"}</span>
      {g.name}
    </button>
    {#if !collapsed[g.project_id]}
      {#each g.environments || [] as env (env.slug)}
        {@render envNode(g.project_id, env)}
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
  .field-note { opacity: 0.6; font-size: 0.8rem; }
  .hint { opacity: 0.7; font-size: 0.85rem; padding: 8px 14px; }
  .err { color: var(--danger, #e66); }
</style>
