<script lang="ts">
  import { credentials, tree } from "./stores.svelte";
  import SearchableSelect from "./SearchableSelect.svelte";
  import type { JumpHostOverride, JumpHostSpec } from "./api";

  interface Props {
    value: JumpHostOverride | undefined;
    onChange: (next: JumpHostOverride | undefined) => void;
    // The connection being edited, left out of the bastion picker (a
    // connection cannot jump through itself).
    selfId?: string;
  }
  let { value, onChange, selfId }: Props = $props();

  // A hop is either typed in (hostname, port, user, credential) or a saved
  // connection used as the bastion. A reference keeps one source of truth:
  // the bastion's host, user, credential and its own jump chain are read
  // from that connection at connect time.
  // Saved connections plus every host of the dynamic (inventory) folders.
  // An inventory host is referenced by folder + the provider's own id
  // ("dyn:<folderId>/<externalId>"), which survives refreshes and sync.
  const connOptions = $derived([
    ...tree.connections
      .filter((c) => c.id !== selfId && (c.protocol ?? "ssh") === "ssh")
      .map((c) => ({ value: c.id, label: `${c.name} - ${c.hostname}`, group: folderPath(c.folder_id ?? null) })),
    ...Object.keys(tree.dynamicFolders).flatMap((fid) =>
      (tree.dynamicEntries[fid] ?? [])
        .filter((e) => e.external_id && e.hostname)
        .map((e) => ({
          value: `dyn:${fid}/${e.external_id}`,
          label: `${e.name} - ${e.hostname}`,
          group: `${folderPath(fid)} (inventory)`,
        })),
    ),
  ].sort((a, b) => (a.group ?? "").localeCompare(b.group ?? "") || a.label.localeCompare(b.label)));

  // The picker needs the entries of inventory folders nobody expanded yet.
  // Loaded only once a hop is in saved-connection mode.
  $effect(() => {
    if (!hops.some((h) => h.connection_id !== undefined)) return;
    for (const fid of Object.keys(tree.dynamicFolders)) {
      if (tree.dynamicEntries[fid] === undefined) void tree.loadDynamicEntries(fid);
    }
  });

  function folderPath(id: string | null): string {
    const parts: string[] = [];
    let f = tree.folderById(id);
    let guard = 0;
    while (f && guard++ < 1000) {
      parts.unshift(f.name);
      f = tree.folderById(f.parent_id ?? null);
    }
    return parts.join(" / ") || "(root)";
  }

  function useConnection(i: number) {
    update(i, { connection_id: "", hostname: "", port: undefined, username: undefined, auth_ref: undefined });
  }

  // Back to a typed hop, pre-filled from the connection it pointed at so
  // nothing has to be retyped.
  function useManual(i: number) {
    const ref = hops[i].connection_id ?? "";
    const c = tree.connectionById(ref || null);
    const entry = tree.jumpRefEntry(ref);
    update(i, {
      connection_id: undefined,
      hostname: c?.hostname ?? entry?.hostname ?? "",
      port: c?.overrides?.port ?? 22,
      username: c?.overrides?.username ?? undefined,
      auth_ref: c?.overrides?.auth_ref ?? undefined,
    });
  }

  function hopName(h: JumpHostSpec): string {
    if (h.connection_id) return tree.jumpRefLabel(h.connection_id);
    return h.hostname || "?";
  }

  const hops = $derived(flatten(value));

  function flatten(v: JumpHostOverride | undefined): JumpHostSpec[] {
    if (!v || v.kind !== "chain" || !v.chain) return [];
    const out: JumpHostSpec[] = [];
    let cur: JumpHostSpec | undefined = v.chain;
    while (cur) {
      const nextVia: JumpHostSpec | undefined = cur.via;
      out.push({
        hostname: cur.hostname,
        port: cur.port,
        username: cur.username,
        auth_ref: cur.auth_ref,
        connection_id: cur.connection_id,
      });
      cur = nextVia;
    }
    return out;
  }

  function rebuild(list: JumpHostSpec[]): JumpHostOverride | undefined {
    if (list.length === 0) return undefined;
    let acc: JumpHostSpec | undefined;
    for (let i = list.length - 1; i >= 0; i--) {
      const node: JumpHostSpec = { ...list[i] };
      if (acc) node.via = acc;
      acc = node;
    }
    return { kind: "chain", chain: acc };
  }

  function update(i: number, patch: Partial<JumpHostSpec>) {
    const next = hops.map((h, idx) => (idx === i ? { ...h, ...patch } : h));
    onChange(rebuild(next));
  }
  function addHop() {
    onChange(rebuild([...hops, { hostname: "", port: 22 }]));
  }
  function removeHop(i: number) {
    onChange(rebuild(hops.filter((_, idx) => idx !== i)));
  }
  function moveUp(i: number) {
    if (i === 0) return;
    const next = [...hops];
    [next[i - 1], next[i]] = [next[i], next[i - 1]];
    onChange(rebuild(next));
  }
  function moveDown(i: number) {
    if (i === hops.length - 1) return;
    const next = [...hops];
    [next[i], next[i + 1]] = [next[i + 1], next[i]];
    onChange(rebuild(next));
  }
  function clearAll() { onChange(undefined); }
  function explicitNoJump() { onChange({ kind: "none" }); }
</script>

<div class="jump-chain">
  <div class="header">
    <strong>Jump chain</strong>
    <div class="actions">
      {#if value === undefined}
        <span class="muted">inherited from folder</span>
        <button onclick={addHop} title="Override with a jump chain">+ Add hop</button>
        <button onclick={explicitNoJump} title="Block inherited jumps for this connection">No jump</button>
      {:else if value.kind === "none"}
        <span class="muted">explicit: no jump (override)</span>
        <button onclick={clearAll}>Revert to inherited</button>
      {:else}
        <button onclick={addHop}>+ Add hop</button>
        <button onclick={clearAll}>Revert to inherited</button>
      {/if}
    </div>
  </div>

  {#if value?.kind === "chain"}
    <ol class="hops">
      {#each hops as h, i (i)}
        <li class="hop">
          <div class="hop-head">
            <span class="step">Hop {i + 1}</span>
            <div class="mode" role="group" aria-label="Hop source">
              <button class:on={h.connection_id !== undefined} onclick={() => { if (h.connection_id === undefined) useConnection(i); }}
                title="Use a saved connection as this bastion - its host, user, credential and own jump chain apply">Saved connection</button>
              <button class:on={h.connection_id === undefined} onclick={() => { if (h.connection_id !== undefined) useManual(i); }}
                title="Type the bastion's host, port, user and credential here">Manual</button>
            </div>
            <div class="hop-actions">
              <button onclick={() => moveUp(i)} disabled={i === 0}>▲</button>
              <button onclick={() => moveDown(i)} disabled={i === hops.length - 1}>▼</button>
              <button class="danger" onclick={() => removeHop(i)}>✕</button>
            </div>
          </div>
          {#if h.connection_id !== undefined}
            {@const ref = tree.connectionById(h.connection_id || null)}
            {@const entry = tree.jumpRefEntry(h.connection_id)}
            <div class="row">
              <label class="grow">Bastion connection
                <SearchableSelect
                  value={h.connection_id}
                  options={connOptions}
                  placeholder="Search connections and inventory hosts…"
                  onChange={(v) => update(i, { connection_id: v })}
                />
              </label>
            </div>
            {#if h.connection_id.startsWith("dyn:")}
              {#if entry}
                <p class="ref-note">
                  Inventory host {entry.name} ({entry.hostname}) - address, user, credential and route come from its inventory folder, as of the last refresh.
                </p>
              {:else}
                <p class="ref-note bad">{tree.jumpRefLabel(h.connection_id)} - not in the loaded inventory.</p>
              {/if}
            {:else if h.connection_id && !ref}
              <p class="ref-note bad">That connection was deleted - pick another one or switch to Manual.</p>
            {:else if ref}
              <p class="ref-note">
                {ref.overrides?.username ? `${ref.overrides.username}@` : ""}{ref.hostname}{ref.overrides?.port && ref.overrides.port !== 22 ? `:${ref.overrides.port}` : ""}
                - user, credential and its own jump chain come from the connection.
              </p>
            {/if}
          {:else}
          <div class="row">
            <label class="grow">Hostname
              <input value={h.hostname}
                oninput={(e) => update(i, { hostname: (e.currentTarget as HTMLInputElement).value })}
                placeholder="bastion.example.com" />
            </label>
            <label class="port">Port
              <input type="number" min="1" max="65535"
                value={h.port ?? ""}
                oninput={(e) => {
                  const v = (e.currentTarget as HTMLInputElement).value;
                  update(i, { port: v ? parseInt(v, 10) : undefined });
                }} />
            </label>
          </div>
          <div class="row">
            <label class="grow">Username
              <input value={h.username ?? ""}
                oninput={(e) => update(i, { username: (e.currentTarget as HTMLInputElement).value || undefined })}
                placeholder="(inherit from target)" />
            </label>
            <label class="grow">Credential
              <SearchableSelect
                value={h.auth_ref ?? ""}
                options={credentials.list.map((c) => ({ value: c.id, label: `${c.name} - ${c.kind}` }))}
                placeholder="Search credentials…"
                onChange={(v) => update(i, { auth_ref: v || undefined })}
              />
            </label>
          </div>
          {/if}
        </li>
      {/each}
    </ol>
    <p class="hint">Connect order: {hops.map(hopName).join(" → ")} → target</p>
  {/if}
</div>

<style>
  .jump-chain {
    margin-top: 0.6rem;
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.6rem 0.75rem;
    background: var(--crust);
  }
  .header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.4rem; gap: 0.4rem;
  }
  .header strong {
    font-size: 0.78rem; text-transform: uppercase;
    color: var(--subtext0); letter-spacing: 0.04em;
  }
  .actions { display: flex; gap: 0.4rem; align-items: center; }
  .muted { color: var(--overlay1); font-size: 0.78rem; }
  button {
    background: var(--surface0); color: var(--text);
    border: 0; padding: 0.25rem 0.6rem;
    border-radius: 3px; cursor: pointer;
    font: inherit; font-size: 0.78rem;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.4; cursor: not-allowed; }
  button.danger { color: var(--red); background: transparent; }
  button.danger:hover { background: var(--red); color: var(--on-accent); }
  ol.hops {
    list-style: none; margin: 0; padding: 0;
    display: flex; flex-direction: column; gap: 0.5rem;
  }
  .hop {
    border-left: 3px solid var(--blue);
    background: var(--mantle);
    padding: 0.5rem 0.6rem;
    border-radius: 3px;
  }
  .hop-head {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.4rem;
  }
  .step { font-size: 0.75rem; color: var(--subtext0); }
  .mode { display: flex; gap: 1px; margin-left: auto; margin-right: 0.5rem; }
  .mode button { padding: 0.15rem 0.5rem; font-size: 0.72rem; border-radius: 0; }
  .mode button:first-child { border-radius: 3px 0 0 3px; }
  .mode button:last-child { border-radius: 0 3px 3px 0; }
  .mode button.on { background: var(--surface2); color: var(--text); font-weight: 600; }
  .ref-note { margin: 0.35rem 0 0; font-size: 0.72rem; color: var(--overlay1); }
  .ref-note.bad { color: var(--red); }
  .hop-actions { display: flex; gap: 0.2rem; }
  .row { display: flex; gap: 0.5rem; margin-top: 0.3rem; }
  .grow { flex: 1; }
  .port { width: 5.5rem; }
  label {
    display: flex; flex-direction: column; gap: 0.2rem;
    font-size: 0.74rem; color: var(--subtext0);
  }
  input {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.3rem 0.45rem; font: inherit;
    font-size: 0.82rem;
  }
  input:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .hint {
    margin-top: 0.5rem; font-size: 0.72rem;
    color: var(--overlay1); background: var(--mantle);
    padding: 0.3rem 0.5rem; border-radius: 3px;
  }
</style>
