<script lang="ts">
  import { onMount } from "svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import SearchableSelect from "./SearchableSelect.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { api, type InfisicalServerInfo, type CredentialRef, type NetworkProfileInfo } from "./api";

  let servers = $state<InfisicalServerInfo[]>([]);
  let creds = $state<CredentialRef[]>([]);
  // Only WireGuard profiles expose an in-process dialer for this HTTP path;
  // Netbird / Tailscale are sidecar-SOCKS only and not offered here.
  let wgProfiles = $state<NetworkProfileInfo[]>([]);
  let loading = $state(true);
  let testing = $state<Record<string, boolean>>({});

  // editing === null means the form is closed; "" means a new server.
  let editing = $state<string | null>(null);
  let form = $state({
    name: "",
    serverURL: "",
    apiKeyCredID: "",
    networkProfileID: "",
  });
  let saving = $state(false);

  // Inline "create API key" mini-form state. Infisical machine-identity
  // credentials are client_id + client_secret, stored as a normal api_token.
  let showApiForm = $state(false);
  let apiForm = $state({ name: "", clientID: "", clientSecret: "" });
  let creatingApi = $state(false);

  let apiKeyOptions = $derived(
    creds
      .filter((c) => c.kind === "api_token")
      .map((c) => ({ value: c.id, label: c.name })),
  );

  async function load() {
    loading = true;
    try {
      const [srvs, cs, profs] = await Promise.all([
        api.infisicalList(),
        api.credentialsList(),
        api.networkProfilesList().catch(() => [] as NetworkProfileInfo[]),
      ]);
      servers = srvs;
      creds = cs;
      wgProfiles = (profs ?? []).filter((p) => p.kind === "wireguard");
    } catch (e) {
      toast.err("Load Infisical servers: " + errMsg(e));
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function openNew() {
    editing = "";
    form = { name: "", serverURL: "", apiKeyCredID: "", networkProfileID: "" };
    showApiForm = false;
  }

  function openEdit(s: InfisicalServerInfo) {
    editing = s.id;
    form = {
      name: s.name,
      serverURL: s.server_url,
      apiKeyCredID: s.api_key_ref,
      networkProfileID: s.network_profile_id,
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
      await api.infisicalSave({
        id: editing || undefined,
        name: form.name.trim(),
        server_url: form.serverURL.trim(),
        api_key_cred_id: form.apiKeyCredID,
        network_profile_id: form.networkProfileID,
      });
      toast.ok(editing ? "Infisical server updated" : "Infisical server added");
      editing = null;
      await load();
    } catch (e) {
      toast.err("Save: " + errMsg(e));
    } finally {
      saving = false;
    }
  }

  async function del(s: InfisicalServerInfo) {
    const ok = await showConfirm({
      title: "Remove Infisical server",
      message: `Remove "${s.name}"? Credentials referencing it will stop resolving. Nothing on the server is touched.`,
      okLabel: "Remove",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.infisicalDelete(s.id);
      toast.ok("Removed");
      await load();
    } catch (e) {
      toast.err("Remove: " + errMsg(e));
    }
  }

  async function testLogin(s: InfisicalServerInfo) {
    testing = { ...testing, [s.id]: true };
    try {
      await api.infisicalTestLogin(s.id);
      toast.ok("Login OK - the API key works");
    } catch (e) {
      toast.err("Test login: " + errMsg(e));
    } finally {
      testing = { ...testing, [s.id]: false };
    }
  }

  function usedLabel(s: InfisicalServerInfo): string {
    if (!s.last_used_at) return "not used yet";
    const d = new Date(s.last_used_at * 1000);
    return "last read " + d.toLocaleString();
  }
</script>

<h2>Infisical servers</h2>
<p class="hint">
  Read secrets straight out of an Infisical server at connect time. The server
  stays the source of truth - ssh-tool never writes to it. Sign-in uses a machine
  identity's Universal Auth key (client id + client secret); Infisical decrypts
  server-side, so there is no master password. Each secret is read fresh on
  connect; a brief outage falls back to the last value seen.
</p>

{#if loading}
  <p class="hint">Loading…</p>
{:else}
  {#if servers.length === 0}
    <p class="hint">No Infisical servers registered yet.</p>
  {:else}
    <div class="inf-list">
      {#each servers as s (s.id)}
        <div class="inf-row">
          <div class="inf-meta">
            <span class="inf-name">{s.name}</span>
            <span class="inf-url">{s.server_url}</span>
            <span class="inf-used">{usedLabel(s)}</span>
          </div>
          <div class="inf-actions">
            <button onclick={() => testLogin(s)} disabled={testing[s.id]}>
              {testing[s.id] ? "Testing…" : "Test login"}
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
  <div class="inf-form">
    <h3>{editing === "" ? "Add Infisical server" : "Edit Infisical server"}</h3>

    <label>Name
      <input bind:value={form.name} placeholder="e.g. Company Infisical" />
    </label>

    <label>Server URL
      <input bind:value={form.serverURL} placeholder="https://infisical.example.com" />
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
          <input bind:value={apiForm.name} placeholder="e.g. Infisical machine identity" />
        </label>
        <label>Client ID
          <input bind:value={apiForm.clientID} autocomplete="off"
            placeholder="xxxxxxxx-xxxx-xxxx-..." />
        </label>
        <label>Client secret
          <PasswordInput bind:value={apiForm.clientSecret} placeholder="" />
        </label>
        <button class="primary" onclick={createApiKey} disabled={creatingApi}>
          {creatingApi ? "Creating…" : "Create API key"}
        </button>
      </div>
    {/if}

    {#if wgProfiles.length > 0}
      <label>Network profile <span class="hint-inline">(optional)</span>
        <select bind:value={form.networkProfileID}>
          <option value="">Direct - no tunnel</option>
          {#each wgProfiles as p (p.id)}
            <option value={p.id}>{p.name}</option>
          {/each}
        </select>
      </label>
      <p class="hint sub">
        Route reads through a WireGuard profile when the server is only reachable
        over the tunnel. Netbird / Tailscale are not offered here.
      </p>
    {/if}

    <div class="inf-form-actions">
      <button class="primary" onclick={save} disabled={saving}>
        {saving ? "Saving…" : "Save"}
      </button>
      <button onclick={cancel} disabled={saving}>Cancel</button>
    </div>
  </div>
{/if}

<style>
  .inf-list { display: flex; flex-direction: column; gap: 6px; margin: 8px 0; }
  .inf-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 10px; border: 1px solid var(--border, #333);
    border-radius: 6px; gap: 12px;
  }
  .inf-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .inf-name { font-weight: 600; }
  .inf-url { font-size: 0.8rem; opacity: 0.7; overflow: hidden; text-overflow: ellipsis; }
  .inf-used { font-size: 0.8rem; opacity: 0.7; }
  .inf-actions { display: flex; gap: 6px; flex-shrink: 0; }
  .inf-form {
    margin-top: 12px; padding: 12px; border: 1px solid var(--border, #333);
    border-radius: 6px; display: flex; flex-direction: column; gap: 10px;
    max-width: 520px;
  }
  .inf-form label { display: flex; flex-direction: column; gap: 4px; font-size: 0.85rem; }
  .inf-form input, .inf-form :global(select) { width: 100%; box-sizing: border-box; }
  .api-picker { display: flex; gap: 6px; align-items: stretch; }
  .api-picker :global(.search-select) { flex: 1; min-width: 0; }
  .api-picker button { flex-shrink: 0; white-space: nowrap; }
  .api-form {
    padding: 10px; border: 1px dashed var(--border, #444); border-radius: 6px;
    display: flex; flex-direction: column; gap: 8px;
  }
  .inf-form-actions { display: flex; gap: 8px; margin-top: 4px; }
  .hint-inline { font-weight: 400; opacity: 0.6; font-size: 0.8rem; }
  .hint.sub { margin: 0; font-size: 0.78rem; }
  .danger { color: var(--danger, #e66); }
</style>
