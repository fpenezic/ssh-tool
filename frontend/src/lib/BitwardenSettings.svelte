<script lang="ts">
  import { onMount } from "svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import SearchableSelect from "./SearchableSelect.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { api, type BitwardenServerInfo, type CredentialRef } from "./api";

  let servers = $state<BitwardenServerInfo[]>([]);
  let creds = $state<CredentialRef[]>([]);
  let loading = $state(true);
  let syncing = $state<Record<string, boolean>>({});

  // editing === null means the form is closed; "" means a new server.
  let editing = $state<string | null>(null);
  let form = $state({
    name: "",
    serverURL: "",
    apiKeyCredID: "",
    master: "",
  });
  let saving = $state(false);

  // Inline "create API key" mini-form state.
  let showApiForm = $state(false);
  let apiForm = $state({ name: "", clientID: "", clientSecret: "" });
  let creatingApi = $state(false);

  // API-key options: api_token credentials (Bitwarden client_id/secret is stored
  // as an api_token - token_id = client_id, vault secret = client_secret).
  let apiKeyOptions = $derived(
    creds
      .filter((c) => c.kind === "api_token")
      .map((c) => ({ value: c.id, label: c.name })),
  );

  async function load() {
    loading = true;
    try {
      [servers, creds] = await Promise.all([
        api.bitwardenList(),
        api.credentialsList(),
      ]);
    } catch (e) {
      toast.err("Load Bitwarden servers: " + errMsg(e));
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function openNew() {
    editing = "";
    form = { name: "", serverURL: "", apiKeyCredID: "", master: "" };
    showApiForm = false;
  }

  function openEdit(s: BitwardenServerInfo) {
    editing = s.id;
    form = {
      name: s.name,
      serverURL: s.server_url,
      apiKeyCredID: s.api_key_ref,
      master: "",
    };
    showApiForm = false;
  }

  function cancel() {
    editing = null;
  }

  async function createApiKey() {
    if (!apiForm.name.trim() || !apiForm.clientID.trim() || !apiForm.clientSecret.trim()) {
      toast.err("Name, client id and client secret are all required");
      return;
    }
    creatingApi = true;
    try {
      const res = await api.credentialsCreate({
        kind: "api_token",
        name: apiForm.name.trim(),
        api_token_id: apiForm.clientID.trim(),
        api_token_secret: apiForm.clientSecret,
      });
      creds = await api.credentialsList();
      if (res.credential) form.apiKeyCredID = res.credential.id;
      showApiForm = false;
      apiForm = { name: "", clientID: "", clientSecret: "" };
      toast.ok("API key credential created");
    } catch (e) {
      toast.err("Create API key: " + errMsg(e));
    } finally {
      creatingApi = false;
    }
  }

  async function save() {
    if (!form.name.trim()) {
      toast.err("Name is required");
      return;
    }
    if (!form.serverURL.trim()) {
      toast.err("Server URL is required");
      return;
    }
    if (!form.apiKeyCredID) {
      toast.err("Pick or create an API key credential");
      return;
    }
    saving = true;
    try {
      await api.bitwardenSave({
        id: editing || undefined,
        name: form.name.trim(),
        server_url: form.serverURL.trim(),
        api_key_cred_id: form.apiKeyCredID,
        master: form.master,
        // On create, seal the master only if one was typed. On edit, a
        // non-empty field means "replace it"; blank means "keep the current".
        set_master: form.master !== "",
      });
      toast.ok(editing ? "Bitwarden server updated" : "Bitwarden server added");
      editing = null;
      await load();
    } catch (e) {
      toast.err("Save: " + errMsg(e));
    } finally {
      saving = false;
    }
  }

  async function del(s: BitwardenServerInfo) {
    const ok = await showConfirm({
      title: "Remove Bitwarden server",
      message: `Remove "${s.name}"? Credentials referencing it will stop resolving. Nothing on the server is touched.`,
      okLabel: "Remove",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.bitwardenDelete(s.id);
      toast.ok("Removed");
      await load();
    } catch (e) {
      toast.err("Remove: " + errMsg(e));
    }
  }

  async function sync(s: BitwardenServerInfo) {
    syncing = { ...syncing, [s.id]: true };
    try {
      const fresh = await api.bitwardenSync(s.id);
      toast.ok(fresh === "stale"
        ? "Server unreachable - using the cached copy"
        : "Synced from the server");
      await load();
    } catch (e) {
      toast.err("Sync: " + errMsg(e));
    } finally {
      syncing = { ...syncing, [s.id]: false };
    }
  }

  function syncedLabel(s: BitwardenServerInfo): string {
    if (!s.last_synced_at) return "never synced";
    const d = new Date(s.last_synced_at * 1000);
    return "synced " + d.toLocaleString();
  }
</script>

<h2>Bitwarden / Vaultwarden servers</h2>
<p class="hint">
  Read secrets straight out of a Vaultwarden or Bitwarden server at connect
  time, organizations and collections included. The server stays the source of
  truth - ssh-tool never writes to it. Sign-in uses an API key
  (Settings - Security - Keys on the server); the master password is sealed in
  this app's vault and used only to decrypt the fetched vault. Items are fetched
  fresh on unlock and when a cached copy is older than a few minutes; use Sync to
  force a pull.
</p>

{#if loading}
  <p class="hint">Loading…</p>
{:else}
  {#if servers.length === 0}
    <p class="hint">No Bitwarden servers registered yet.</p>
  {:else}
    <div class="bw-list">
      {#each servers as s (s.id)}
        <div class="bw-row">
          <div class="bw-meta">
            <span class="bw-name">{s.name}</span>
            <span class="bw-url">{s.server_url}</span>
            <span class="bw-synced">{syncedLabel(s)}</span>
          </div>
          <div class="bw-actions">
            <button onclick={() => sync(s)} disabled={syncing[s.id]}>
              {syncing[s.id] ? "Syncing…" : "Sync"}
            </button>
            <button onclick={() => openEdit(s)}>Edit</button>
            <button class="danger" onclick={() => del(s)}>Remove</button>
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if editing === null}
    <button class="primary" onclick={openNew}>Add server</button>
  {/if}
{/if}

{#if editing !== null}
  <div class="bw-form">
    <h3>{editing === "" ? "Add Bitwarden server" : "Edit Bitwarden server"}</h3>

    <label>Name
      <input bind:value={form.name} placeholder="e.g. Company Vaultwarden" />
    </label>

    <label>Server URL
      <input bind:value={form.serverURL} placeholder="https://vault.example.com" />
    </label>

    <label>API key
      <div class="api-picker">
        <SearchableSelect
          bind:value={form.apiKeyCredID}
          options={apiKeyOptions}
          placeholder="Pick an API key credential…"
        />
        <button type="button" onclick={() => (showApiForm = !showApiForm)}>
          {showApiForm ? "Cancel" : "Create"}
        </button>
      </div>
    </label>

    {#if showApiForm}
      <div class="api-form">
        <label>Credential name
          <input bind:value={apiForm.name} placeholder="e.g. VW API key" />
        </label>
        <label>Client ID
          <input bind:value={apiForm.clientID} autocomplete="off"
            placeholder="user.xxxxxxxx-xxxx-..." />
        </label>
        <label>Client secret
          <PasswordInput bind:value={apiForm.clientSecret} placeholder="" />
        </label>
        <button class="primary" onclick={createApiKey} disabled={creatingApi}>
          {creatingApi ? "Creating…" : "Create API key"}
        </button>
      </div>
    {/if}

    <label>Master password {#if editing}<span class="hint-inline">(leave blank to keep)</span>{/if}
      <PasswordInput bind:value={form.master} placeholder="Bitwarden master password" />
    </label>
    <p class="hint sub">
      The master password is write-only: it is sealed in the vault and never
      shown again. It never leaves this machine.
    </p>

    <div class="bw-form-actions">
      <button class="primary" onclick={save} disabled={saving}>
        {saving ? "Saving…" : "Save"}
      </button>
      <button onclick={cancel} disabled={saving}>Cancel</button>
    </div>
  </div>
{/if}

<style>
  .bw-list { display: flex; flex-direction: column; gap: 6px; margin: 8px 0; }
  .bw-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 10px; border: 1px solid var(--border, #333);
    border-radius: 6px; gap: 12px;
  }
  .bw-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .bw-name { font-weight: 600; }
  .bw-url { font-size: 0.8rem; opacity: 0.7; overflow: hidden; text-overflow: ellipsis; }
  .bw-synced { font-size: 0.8rem; opacity: 0.7; }
  .bw-actions { display: flex; gap: 6px; flex-shrink: 0; }
  .bw-form {
    margin-top: 12px; padding: 12px; border: 1px solid var(--border, #333);
    border-radius: 6px; display: flex; flex-direction: column; gap: 10px;
    max-width: 520px;
  }
  .bw-form label { display: flex; flex-direction: column; gap: 4px; font-size: 0.85rem; }
  .bw-form input, .bw-form :global(select) { width: 100%; box-sizing: border-box; }
  .api-picker { display: flex; gap: 6px; align-items: stretch; }
  .api-picker :global(.search-select) { flex: 1; min-width: 0; }
  .api-picker button { flex-shrink: 0; white-space: nowrap; }
  .api-form {
    padding: 10px; border: 1px dashed var(--border, #444); border-radius: 6px;
    display: flex; flex-direction: column; gap: 8px;
  }
  .bw-form-actions { display: flex; gap: 8px; margin-top: 4px; }
  .hint-inline { font-weight: 400; opacity: 0.6; font-size: 0.8rem; }
  .hint.sub { margin: 0; font-size: 0.78rem; }
  .danger { color: var(--danger, #e66); }
</style>
