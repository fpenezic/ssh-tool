<script lang="ts">
  // Settings - Connections - Catalogs: attach a shared connection catalog,
  // sign in, sync, and bind its slots to your own credentials, network
  // profiles and bastions.
  import { onMount } from "svelte";
  import SearchableSelect from "./SearchableSelect.svelte";
  import CatalogFolderVisibility from "./CatalogFolderVisibility.svelte";
  import CatalogMatchModal from "./CatalogMatchModal.svelte";
  import { showConfirm, showConfirmWithCheckbox } from "./confirmModal.svelte.ts";
  import { catalogLocal } from "./catalogLocal.svelte";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { api, type CatalogInfo, type CatalogSlotInfo } from "./api";
  import { catalogs, catalogStateLabel } from "./catalog.svelte";
  import { credentials, tree } from "./stores.svelte";
  import { networkProfiles } from "./networkProfiles.svelte";
  import { renderMarkdown } from "./markdown";
  import { isMobile } from "./platform";
  import { IconKey, IconVpn, IconBastion, IconRefresh, IconTrash, IconAlertTriangle } from "./iconMap";

  let addURL = $state("");
  let adding = $state(false);
  let busy = $state<Record<string, boolean>>({});
  let open = $state<string | null>(null); // catalog whose slots are shown
  let edit = $state<{ id: string; name: string; interval: number } | null>(null);
  // Writers: a slot being edited, and the add-slot form.
  let slotEdit = $state<{ uuid: string; name: string; doc: string } | null>(null);
  let slotAdd = $state<{ catalog: string; folder: string; type: "cred" | "net" | "jump"; name: string; doc: string } | null>(null);
  const writableFolders = (catalogId: string) => tree.folders.filter((f) => f.catalog_id === catalogId && f.remote_uuid && f.catalog_writable && f.catalog_state !== "removed");
  async function saveSlotEdit(c: CatalogInfo, s: CatalogSlotInfo) {
    if (!slotEdit) return;
    const e = slotEdit;
    await run(c.id, "Save slot", async () => {
      await api.catalogSlotUpdate({ catalog_id: c.id, uuid: s.uuid, type: s.type, name: e.name, kind: s.kind, backend: s.backend,
        external_ref: s.external_ref_json ? JSON.parse(s.external_ref_json) : null, doc_md: e.doc });
      slotEdit = null;
      await catalogs.loadSlots(c.id);
    });
  }
  async function deleteSlot(c: CatalogInfo, s: CatalogSlotInfo) {
    const ok = await showConfirm({ title: "Delete slot", message: `Delete the slot "${s.name}" from the catalog? Everyone's binding of it goes with it.`, okLabel: "Delete", danger: true });
    if (!ok) return;
    await run(c.id, "Delete slot", async () => {
      await api.catalogSlotDelete(c.id, s.uuid);
      await catalogs.loadSlots(c.id);
    });
  }
  async function addSlot(c: CatalogInfo) {
    if (!slotAdd || !slotAdd.name.trim() || !slotAdd.folder) return;
    const a = slotAdd;
    await run(c.id, "New slot", async () => {
      await api.catalogSlotCreate({ catalog_id: c.id, folder_id: a.folder, type: a.type, name: a.name.trim(), doc_md: a.doc });
      slotAdd = null;
      await catalogs.loadSlots(c.id);
    });
  }

  // The slots panel: unbound slots first (grouped by the folder they are
  // defined on); bound ones behind "Show bound", with a filter. A catalog
  // can have hundreds of bound slots, which buried the few still to bind.
  let showBound = $state<Record<string, boolean>>({});
  let boundFilter = $state<Record<string, string>>({});
  let flash = $state("");
  let matching = $state<{ id: string; name: string } | null>(null);
  const needsBinding = (s: CatalogSlotInfo) => s.state === "unbound" && (!s.skipped || !!s.target_gone);
  function groupByFolder(list: CatalogSlotInfo[]): { folder: string; slots: CatalogSlotInfo[] }[] {
    const m = new Map<string, CatalogSlotInfo[]>();
    for (const s of list) {
      const k = s.folder_name || "";
      m.set(k, [...(m.get(k) ?? []), s]);
    }
    return [...m.entries()]
      .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
      .map(([folder, slots]) => ({ folder, slots }));
  }
  function boundShown(c: CatalogInfo): CatalogSlotInfo[] {
    const q = (boundFilter[c.id] ?? "").trim().toLowerCase();
    return (catalogs.slots[c.id] ?? []).filter((s) => !needsBinding(s) &&
      (!q || s.name.toLowerCase().includes(q) || (s.target_name ?? "").toLowerCase().includes(q) || (s.folder_name ?? "").toLowerCase().includes(q)));
  }
  // Open on one slot: the bound list is shown filtered to it when it is
  // bound, and the row is scrolled to and flashed.
  async function focusSlot(catalogId: string, uuid: string) {
    const list = await catalogs.loadSlots(catalogId);
    const s = list.find((x) => x.uuid === uuid);
    if (!s) return;
    if (!needsBinding(s)) {
      showBound = { ...showBound, [catalogId]: true };
      boundFilter = { ...boundFilter, [catalogId]: s.name };
    }
    flash = uuid;
    requestAnimationFrame(() => document.getElementById("slot-" + uuid)?.scrollIntoView({ block: "center" }));
    setTimeout(() => { if (flash === uuid) flash = ""; }, 2500);
  }

  onMount(async () => {
    await catalogs.load();
    if (catalogs.focus) {
      open = catalogs.focus.id;
      if (catalogs.focus.slot) void focusSlot(open, catalogs.focus.slot);
      catalogs.focus = null;
    } else if (catalogs.list.length === 1) {
      open = catalogs.list[0].id;
    }
    if (open) catalogs.loadSlots(open);
    credentials.load();
    networkProfiles.load();
  });

  async function run(id: string, what: string, fn: () => Promise<unknown>) {
    busy = { ...busy, [id]: true };
    try {
      await fn();
    } catch (e) {
      toast.err(`${what}: ${errMsg(e)}`, 8000);
    } finally {
      busy = { ...busy, [id]: false };
      await catalogs.load();
    }
  }

  async function add() {
    const u = addURL.trim();
    if (!u || adding) return;
    adding = true;
    try {
      const c = await api.catalogAdd(u);
      addURL = "";
      open = c.id;
      toast.ok(`${c.name} attached`);
    } catch (e) {
      toast.err("Add catalog: " + errMsg(e), 8000);
    } finally {
      adding = false;
      await catalogs.load();
      if (open) catalogs.loadSlots(open);
    }
  }

  async function remove(c: CatalogInfo) {
    const r = await showConfirmWithCheckbox({
      title: "Remove catalog",
      message: `Remove "${c.name}" from this app? Its connections, your bindings and your local settings on them go. Nothing on the server changes.`,
      checkedMessage: `Remove "${c.name}" from this app and keep its connections as your own local folder, working as they do now? Slots you have not bound become empty fields, noted on each connection. Nothing on the server changes.`,
      checkboxLabel: "Keep its connections as a local folder",
      okLabel: "Remove",
      danger: true,
    });
    if (!r.ok) return;
    if (r.checked) {
      await run(c.id, "Remove", async () => {
        const res = await api.catalogRemoveKeepLocal(c.id);
        await tree.load();
        catalogLocal.announce(`${c.name} kept as a local folder`, res);
      });
      return;
    }
    await run(c.id, "Remove", () => api.catalogRemove(c.id));
    tree.load();
  }

  function syncedLabel(c: CatalogInfo): string {
    if (!c.last_sync_at) return "never synced";
    return "synced " + new Date(c.last_sync_at * 1000).toLocaleString();
  }

  function toggle(id: string) {
    open = open === id ? null : id;
    if (open) catalogs.loadSlots(open);
  }

  // Bind targets per slot type.
  const credOptions = $derived(
    credentials.list
      .filter((c) => !c.id.startsWith("catcred-") && c.kind !== "api_token" && c.kind !== "certificate")
      .map((c) => ({ value: c.id, label: c.name })),
  );
  const netOptions = $derived(networkProfiles.list.map((p) => ({ value: p.id, label: p.name })));
  const jumpOptions = $derived(
    tree.connections
      .filter((c) => c.protocol !== "local" && c.protocol !== "web" && c.catalog_state !== "removed")
      .map((c) => ({ value: c.id, label: c.name + (c.hostname ? ` (${c.hostname})` : "") })),
  );
  function optionsFor(s: CatalogSlotInfo) {
    return s.type === "cred" ? credOptions : s.type === "net" ? netOptions : jumpOptions;
  }
  const typeLabel = { cred: "Credential", net: "Network profile", jump: "Bastion" } as const;

  async function bind(c: CatalogInfo, s: CatalogSlotInfo, target: string) {
    if (!target || target === s.target_id) return;
    await run(c.id, "Bind", () => api.catalogBind(c.id, s.uuid, target));
    await catalogs.loadSlots(c.id);
  }
  async function unbind(c: CatalogInfo, s: CatalogSlotInfo) {
    await run(c.id, "Unbind", () => api.catalogUnbind(c.id, s.uuid));
    await catalogs.loadSlots(c.id);
  }

  async function saveConfig() {
    if (!edit) return;
    const e = edit;
    await run(e.id, "Save", () => api.catalogSetConfig(e.id, e.name, e.interval * 60));
    edit = null;
  }

  function onDocClick(e: MouseEvent) {
    const a = (e.target as HTMLElement | null)?.closest("a[data-md-link]") as HTMLAnchorElement | null;
    if (!a) return;
    e.preventDefault();
    const href = a.getAttribute("href");
    if (href) api.openURL(href);
  }
</script>

{#snippet slotRow(c: CatalogInfo, s: CatalogSlotInfo)}
  <div class="slot" id={"slot-" + s.uuid} class:unbound={needsBinding(s)} class:flash={flash === s.uuid}>
    <div class="slot-head">
      <span class="slot-icon" title={typeLabel[s.type]}>
        {#if s.type === "cred"}<IconKey size={14} />{:else if s.type === "net"}<IconVpn size={14} />{:else}<IconBastion size={14} />{/if}
      </span>
      <span class="slot-name">{s.name}</span>
      <span class="slot-type">{typeLabel[s.type]}</span>
      {#if s.state === "unbound"}
        {#if s.skipped && !s.target_gone}
          <span class="chip" title="Skipped in the connect dialog on this machine; bind it here to use a stored code">skipped - you type the code</span>
        {:else}
          <span class="chip tone-warn">{s.target_gone ? "bound item deleted" : "not bound"}</span>
        {/if}
      {:else if s.state === "external"}
        <span class="chip tone-ok" title="Found in your Bitwarden / Vaultwarden">from {s.target_name}</span>
      {/if}
      <span class="slot-used">{s.used_by} {s.used_by === 1 ? "record" : "records"}{#if s.folder_name} - on {s.folder_name}{/if}</span>
      {#if s.can_write && !isMobile}
        <button class="slot-act" onclick={() => (slotEdit = { uuid: s.uuid, name: s.name, doc: s.doc_md })} title="Rename or document this slot for everyone">Edit</button>
        <button class="slot-act danger" onclick={() => deleteSlot(c, s)} title="Delete this slot from the catalog"><IconTrash size={12} /></button>
      {/if}
    </div>
    {#if slotEdit && slotEdit.uuid === s.uuid}
      <div class="cat-form">
        <label>Name <input bind:value={slotEdit.name} /></label>
        <label>How to get access <span class="hint-inline">(markdown, shown to everyone; never put secrets here)</span><textarea rows="3" bind:value={slotEdit.doc}></textarea></label>
        <div class="cat-form-actions">
          <button onclick={() => (slotEdit = null)}>Cancel</button>
          <button class="primary" disabled={busy[c.id] || !slotEdit.name.trim()} onclick={() => saveSlotEdit(c, s)}>Save for everyone</button>
        </div>
      </div>
    {/if}
    {#if s.doc_md}
      <!-- eslint-disable-next-line svelte/no-at-html-tags -->
      <div class="slot-doc md" role="presentation" onclick={onDocClick}>{@html renderMarkdown(s.doc_md)}</div>
    {/if}
    {#if s.kind === "external" && s.state === "unbound"}
      <p class="hint sub">Names an item in Bitwarden / Vaultwarden. Register a server under External secrets, or bind the slot by hand.</p>
    {/if}
    <div class="slot-bind">
      <SearchableSelect
        value={s.state === "bound" ? (s.target_id ?? "") : ""}
        options={optionsFor(s)}
        placeholder={`Pick a ${typeLabel[s.type].toLowerCase()}...`}
        emptyLabel={s.state === "external" ? "(from Bitwarden)" : "(not bound)"}
        onChange={(v: string) => bind(c, s, v)}
      />
      {#if s.state === "bound"}
        <button onclick={() => unbind(c, s)}>Unbind</button>
      {/if}
    </div>
  </div>
{/snippet}

<h2>Catalogs</h2>
<p class="hint">
  A catalog is a connection tree your team shares from a catalog server. Its
  connections show up in your tree and stay in step with it. Secrets are never
  shared: each slot is bound once to something of your own.
</p>

{#if catalogs.list.length === 0}
  <p class="hint">No catalog attached.</p>
{/if}

<div class="cat-list">
  {#each catalogs.list as c (c.id)}
    {@const st = catalogStateLabel(c)}
    <div class="cat-row">
      <div class="cat-head">
        <button class="cat-toggle" onclick={() => toggle(c.id)} aria-expanded={open === c.id}>
          <span class="cat-name">{c.name}</span>
          <span class="cat-url">{c.base_url}</span>
        </button>
        {#if st.text}<span class="chip tone-{st.tone}">{st.text}</span>{/if}
        <div class="cat-actions">
          {#if c.state === "signing_in"}
            <button onclick={() => run(c.id, "Cancel", () => api.catalogCancelSignIn(c.id))}>Cancel sign-in</button>
          {:else if !isMobile && (c.state === "no_login" || c.state === "signed_out")}
            <button class="primary" disabled={busy[c.id]} onclick={() => run(c.id, "Sign in", () => api.catalogSignIn(c.id))}>Sign in</button>
            <button title="Sign in with a code from the catalog's web page" onclick={() => catalogs.startEnroll(c.base_url, "")}>Code</button>
          {:else if c.state !== "locked" && c.state !== "no_login" && c.state !== "signed_out"}
            <button title="Sync now" disabled={busy[c.id] || c.state === "syncing"} onclick={() => run(c.id, "Sync", () => api.catalogSyncNow(c.id))}>
              <IconRefresh size={14} /> Sync
            </button>
          {/if}
          <button onclick={() => (edit = { id: c.id, name: c.name, interval: Math.round(c.sync_interval_sec / 60) })}>Edit</button>
          <button class="danger" title="Remove catalog" onclick={() => remove(c)}><IconTrash size={14} /></button>
        </div>
      </div>
      <div class="cat-meta">
        {#if c.account_name}signed in as {c.account_name}{" - "}{/if}{syncedLabel(c)}
        {#if c.removed > 0}{" - "}<span class="warn-text"><IconAlertTriangle size={12} /> {c.removed} removed from the catalog</span>{/if}
      </div>
      {#if (c.state === "error" || c.state === "signed_out") && (c.error || c.last_error)}
        <p class="hint warn-note">{c.error || c.last_error}</p>
      {/if}
      {#if isMobile && (c.state === "no_login" || c.state === "signed_out")}
        <p class="hint">Without a sign-in this device gets the catalog's connections through profile sync. <button class="linklike" onclick={() => catalogs.startEnroll(c.base_url, "")}>Sign in with a code</button></p>
      {/if}

      {#if edit && edit.id === c.id}
        <div class="cat-form">
          <label>Name <input bind:value={edit.name} /></label>
          <label>Sync every (minutes) <input type="number" min="1" bind:value={edit.interval} /></label>
          <div class="cat-form-actions">
            <button class="primary" onclick={saveConfig}>Save</button>
            <button onclick={() => (edit = null)}>Cancel</button>
            {#if c.state !== "no_login" && c.state !== "signed_out"}
              <button onclick={() => run(c.id, "Sign out", () => api.catalogSignOut(c.id))}>Sign out</button>
            {/if}
          </div>
        </div>
      {/if}

      {#if open === c.id}
        {@const all = catalogs.slots[c.id] ?? []}
        {@const todo = all.filter(needsBinding)}
        {@const bound = all.length - todo.length}
        <CatalogFolderVisibility catalogId={c.id} rootFolderId={c.root_folder_id} />
        <div class="match">
          <h3>Your own copies</h3>
          <p class="hint sub">Already had these hosts before the catalog? Match pairs your connections with the catalog's by host and port, and moves your credentials, logins, forwards and workspaces onto them.</p>
          <button onclick={() => (matching = { id: c.id, name: c.name })}>Match my connections...</button>
        </div>
        <div class="slots">
          <h3>Slots <span class="hint-inline">{all.length === 0 ? "" : `${all.length} - ${todo.length === 0 ? "all bound" : `${todo.length} to bind`}`}</span></h3>
          {#if all.length === 0}
            <p class="hint">This catalog asks for nothing.</p>
          {:else if todo.length > 0}
            <p class="hint sub">Bind each once to something of your own; every connection that uses it works then. A connection's editor binds its own slots too.</p>
          {/if}
          {#each groupByFolder(todo) as g (g.folder)}
            {#if g.folder}<div class="slot-group">{g.folder}</div>{/if}
            {#each g.slots as s (s.uuid)}{@render slotRow(c, s)}{/each}
          {/each}
          {#if bound > 0}
            <button class="slot-toggle" onclick={() => (showBound = { ...showBound, [c.id]: !showBound[c.id] })} aria-expanded={!!showBound[c.id]}>
              {showBound[c.id] ? "Hide" : "Show"} {bound} bound {bound === 1 ? "slot" : "slots"}
            </button>
            {#if showBound[c.id]}
              <input class="slot-filter" placeholder="Filter by slot, what it is bound to or folder..." value={boundFilter[c.id] ?? ""}
                oninput={(e) => (boundFilter = { ...boundFilter, [c.id]: (e.currentTarget as HTMLInputElement).value })} />
              {@const shown = boundShown(c)}
              {#each shown.slice(0, 200) as s (s.uuid)}{@render slotRow(c, s)}{/each}
              {#if shown.length > 200}<p class="hint sub">{shown.length - 200} more; narrow the filter.</p>{/if}
              {#if shown.length === 0}<p class="hint sub">No bound slot matches.</p>{/if}
            {/if}
          {/if}
          {#if !isMobile && writableFolders(c.id).length > 0}
            {#if slotAdd && slotAdd.catalog === c.id}
              <div class="cat-form">
                <label>On folder
                  <select bind:value={slotAdd.folder}>
                    {#each writableFolders(c.id) as f (f.id)}<option value={f.id}>{f.name}</option>{/each}
                  </select>
                </label>
                <label>Type
                  <select bind:value={slotAdd.type}>
                    <option value="cred">Credential</option>
                    <option value="net">Network</option>
                    <option value="jump">Bastion</option>
                  </select>
                </label>
                <label>Name <input bind:value={slotAdd.name} placeholder="what colleagues bind, e.g. customer-admin" /></label>
                <label>How to get access <span class="hint-inline">(markdown, shown to everyone; never put secrets here)</span><textarea rows="2" bind:value={slotAdd.doc}></textarea></label>
                <div class="cat-form-actions">
                  <button onclick={() => (slotAdd = null)}>Cancel</button>
                  <button class="primary" disabled={busy[c.id] || !slotAdd.name.trim() || !slotAdd.folder} onclick={() => addSlot(c)}>Create for everyone</button>
                </div>
              </div>
            {:else}
              <button class="slot-add" onclick={() => (slotAdd = { catalog: c.id, folder: writableFolders(c.id)[0].id, type: "cred", name: "", doc: "" })}>+ New slot</button>
            {/if}
          {/if}
        </div>
      {/if}
    </div>
  {/each}
</div>

{#if !isMobile}
  <div class="cat-add">
    <input bind:value={addURL} placeholder="https://catalog.example.com" onkeydown={(e) => e.key === "Enter" && add()} />
    <button class="primary" disabled={adding || !addURL.trim()} onclick={add}>{adding ? "Waiting for sign-in..." : "Add catalog"}</button>
    <button onclick={() => catalogs.startEnroll(addURL.trim(), "")}>Add with a code</button>
  </div>
  <p class="hint sub">Adding opens the catalog's sign-in page in your browser. With a code from the catalog's web page ("Connect ssh-tool"), no browser sign-in is needed.</p>
{:else}
  <div class="cat-add">
    <button class="primary" onclick={() => catalogs.startEnroll("", "")}>Add with a code</button>
  </div>
  <p class="hint sub">In the catalog's web page, "Connect ssh-tool" opens this app with a code, or shows one to type here.</p>
{/if}

{#if matching}
  <CatalogMatchModal catalogId={matching.id} catalogName={matching.name} onclose={() => (matching = null)} />
{/if}

<style>
  .cat-list { display: flex; flex-direction: column; gap: 8px; margin: 8px 0; }
  .cat-row { padding: 8px 10px; border: 1px solid var(--border, #333); border-radius: var(--r-md); }
  .cat-head { display: flex; align-items: center; gap: 8px; }
  .cat-toggle { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; background: none; border: none; padding: 0; min-width: 0; flex: 1; text-align: left; cursor: pointer; }
  .cat-name { font-weight: 600; }
  .cat-url { font-size: var(--fs-sm); opacity: 0.7; overflow: hidden; text-overflow: ellipsis; }
  .cat-actions { display: flex; gap: 6px; flex-shrink: 0; }
  .cat-actions button { display: inline-flex; align-items: center; gap: 4px; }
  .cat-meta { font-size: var(--fs-sm); opacity: 0.75; margin-top: 4px; }
  .warn-text { color: var(--yellow); display: inline-flex; align-items: center; gap: 3px; }
  .chip { font-size: var(--fs-sm); padding: 1px 8px; border-radius: 999px; white-space: nowrap; }
  .tone-ok { color: var(--green); border: 1px solid color-mix(in srgb, var(--green) 40%, transparent); }
  .tone-warn { color: var(--yellow); border: 1px solid color-mix(in srgb, var(--yellow) 40%, transparent); }
  .tone-err { color: var(--red); border: 1px solid color-mix(in srgb, var(--red) 40%, transparent); }
  .tone-busy { color: var(--sapphire); border: 1px solid color-mix(in srgb, var(--sapphire) 40%, transparent); }
  .cat-form { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; max-width: 420px; }
  .cat-form label { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-sm); }
  .cat-form-actions { display: flex; gap: 8px; }
  .slots { margin-top: 10px; display: flex; flex-direction: column; gap: 8px; }
  .slots h3 { margin: 0; }
  .slot { padding: 8px; border: 1px solid var(--border, #333); border-radius: var(--r-md); display: flex; flex-direction: column; gap: 6px; }
  .slot.unbound { border-color: color-mix(in srgb, var(--yellow) 45%, var(--border, #333)); }
  .slot.flash { border-color: var(--sapphire); box-shadow: 0 0 0 1px var(--sapphire); }
  .slot-group { font-size: var(--fs-sm); font-weight: 600; opacity: 0.8; margin-top: 4px; }
  .slot-toggle { align-self: flex-start; }
  .slot-filter { max-width: 420px; }
  .slot-head { display: flex; align-items: center; gap: 8px; }
  .slot-icon { display: inline-flex; opacity: 0.8; }
  .slot-name { font-weight: 600; }
  .slot-type, .slot-used { font-size: var(--fs-sm); opacity: 0.65; }
  .slot-used { margin-left: auto; }
  .slot-doc { font-size: var(--fs-sm); padding: 4px 8px; border-left: 2px solid var(--sapphire); opacity: 0.9; }
  .slot-bind { display: flex; gap: 6px; align-items: stretch; max-width: 520px; }
  .slot-bind :global(.search-select) { flex: 1; min-width: 0; }
  .cat-add { display: flex; gap: 6px; max-width: 520px; margin-top: 8px; }
  .cat-add input { flex: 1; }
  .hint-inline { font-weight: 400; opacity: 0.6; font-size: var(--fs-sm); }
  .hint.sub { margin: 0; font-size: var(--fs-sm); }
  .danger { color: var(--danger, #e66); }
  .linklike { background: none; border: none; padding: 0; color: var(--sapphire); cursor: pointer; font-size: inherit; text-decoration: underline; }
  .match { margin-top: 10px; display: flex; flex-direction: column; gap: 6px; align-items: flex-start; }
  .match h3 { margin: 0; }
  .slot-act { padding: 0 6px; font-size: var(--fs-sm); }
  .slot-add { align-self: flex-start; margin-top: 4px; }
</style>
