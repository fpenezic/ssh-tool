<script lang="ts">
  import { onMount } from "svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { api, type KeepassDatabaseInfo } from "./api";

  let dbs = $state<KeepassDatabaseInfo[]>([]);
  let loading = $state(true);
  let refreshing = $state<Record<string, boolean>>({});

  // Editor state. editing === null means the form is closed; "" means a new DB.
  let editing = $state<string | null>(null);
  let form = $state({
    name: "",
    source: "local" as "local" | "webdav" | "sftp",
    path: "",
    url: "",
    master: "",
    keyFile: "",
    // remote transport creds
    remoteUser: "",
    remotePass: "",
    remoteHost: "",
    remotePort: "",
    remoteWebdavUser: "",
  });
  let saving = $state(false);
  // Track whether the user actually typed a new secret (so an edit that leaves
  // the field blank doesn't wipe the stored one).
  let masterTouched = $state(false);
  let keyFileTouched = $state(false);
  let remotePassTouched = $state(false);

  async function load() {
    loading = true;
    try {
      dbs = await api.keepassList();
    } catch (e) {
      toast.err("Load KeePass databases: " + errMsg(e));
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function openNew() {
    editing = "";
    form = {
      name: "", source: "local", path: "", url: "", master: "", keyFile: "",
      remoteUser: "", remotePass: "", remoteHost: "", remotePort: "", remoteWebdavUser: "",
    };
    masterTouched = keyFileTouched = remotePassTouched = false;
  }

  function openEdit(db: KeepassDatabaseInfo) {
    editing = db.id;
    const rc = db.remote_config || {};
    form = {
      name: db.name,
      source: db.source,
      path: db.path,
      url: db.url,
      master: "",
      keyFile: "",
      remoteUser: rc.user || "",
      remotePass: "",
      remoteHost: rc.host || "",
      remotePort: rc.port || "",
      remoteWebdavUser: rc.username || "",
    };
    masterTouched = keyFileTouched = remotePassTouched = false;
  }

  function cancel() {
    editing = null;
  }

  async function save() {
    if (!form.name.trim()) {
      toast.err("Name is required");
      return;
    }
    if (form.source === "local" && !form.path.trim()) {
      toast.err("Path to the .kdbx file is required");
      return;
    }
    if (form.source !== "local" && !form.url.trim()) {
      toast.err("Remote URL / path is required");
      return;
    }
    saving = true;
    try {
      const remote_config: Record<string, string> = {};
      if (form.source === "sftp") {
        remote_config.host = form.remoteHost.trim();
        remote_config.user = form.remoteUser.trim();
        if (form.remotePort.trim()) remote_config.port = form.remotePort.trim();
      } else if (form.source === "webdav") {
        if (form.remoteWebdavUser.trim()) remote_config.username = form.remoteWebdavUser.trim();
      }
      await api.keepassSave({
        id: editing || undefined,
        name: form.name.trim(),
        source: form.source,
        path: form.source === "local" ? form.path.trim() : "",
        url: form.source === "local" ? "" : form.url.trim(),
        master: form.master,
        set_master: editing === "" ? form.master !== "" : masterTouched,
        key_file: form.keyFile,
        set_key_file: editing === "" ? form.keyFile !== "" : keyFileTouched,
        remote_config,
        remote_pass: form.remotePass,
        set_remote: editing === "" ? form.remotePass !== "" : remotePassTouched,
      });
      toast.ok(editing ? "KeePass database updated" : "KeePass database added");
      editing = null;
      await load();
    } catch (e) {
      toast.err("Save: " + errMsg(e));
    } finally {
      saving = false;
    }
  }

  async function del(db: KeepassDatabaseInfo) {
    const ok = await showConfirm({
      title: "Remove KeePass database",
      message: `Remove "${db.name}"? Credentials referencing it will stop resolving. The .kdbx file itself is not touched.`,
      okLabel: "Remove",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.keepassDelete(db.id);
      toast.ok("Removed");
      await load();
    } catch (e) {
      toast.err("Remove: " + errMsg(e));
    }
  }

  async function refresh(db: KeepassDatabaseInfo) {
    refreshing = { ...refreshing, [db.id]: true };
    try {
      const fresh = await api.keepassRefresh(db.id);
      toast.ok(fresh === "stale"
        ? "Remote unreachable - using the cached copy"
        : "Refreshed from remote");
      await load();
    } catch (e) {
      toast.err("Refresh: " + errMsg(e));
    } finally {
      refreshing = { ...refreshing, [db.id]: false };
    }
  }

  function fetchedLabel(db: KeepassDatabaseInfo): string {
    if (db.source === "local") return "local file";
    if (!db.last_fetched_at) return "never fetched";
    const d = new Date(db.last_fetched_at * 1000);
    return "fetched " + d.toLocaleString();
  }
</script>

<h2>KeePass databases</h2>
<p class="hint">
  Read secrets straight out of a KeePass .kdbx at connect time. KeePass stays
  the source of truth - ssh-tool never writes to the file. The master password
  and key file are sealed in this app's vault; unlock it once and both open
  together. Remote databases (WebDAV / SFTP) are fetched fresh on unlock and
  when a cached copy is older than a few minutes; use Refresh to force a pull.
</p>

{#if loading}
  <p class="hint">Loading…</p>
{:else}
  {#if dbs.length === 0}
    <p class="hint">No KeePass databases registered yet.</p>
  {:else}
    <div class="kp-list">
      {#each dbs as db (db.id)}
        <div class="kp-row">
          <div class="kp-meta">
            <span class="kp-name">{db.name}</span>
            <span class="kp-src">{db.source}</span>
            <span class="kp-fetched">{fetchedLabel(db)}</span>
          </div>
          <div class="kp-actions">
            {#if db.source !== "local"}
              <button onclick={() => refresh(db)} disabled={refreshing[db.id]}>
                {refreshing[db.id] ? "Refreshing…" : "Refresh"}
              </button>
            {/if}
            <button onclick={() => openEdit(db)}>Edit</button>
            <button class="danger" onclick={() => del(db)}>Remove</button>
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if editing === null}
    <button class="primary" onclick={openNew}>Add database</button>
  {/if}
{/if}

{#if editing !== null}
  <div class="kp-form">
    <h3>{editing === "" ? "Add KeePass database" : "Edit KeePass database"}</h3>

    <label>Name
      <input bind:value={form.name} placeholder="e.g. Team vault" />
    </label>

    <label>Source
      <select bind:value={form.source}>
        <option value="local">Local file</option>
        <option value="webdav">WebDAV</option>
        <option value="sftp">SFTP</option>
      </select>
    </label>

    {#if form.source === "local"}
      <label>Path to .kdbx
        <input bind:value={form.path} placeholder="/home/you/secrets.kdbx" />
      </label>
    {:else if form.source === "webdav"}
      <label>URL
        <input bind:value={form.url} placeholder="https://cloud.example.com/dav/secrets.kdbx" />
      </label>
      <label>WebDAV username (optional)
        <input bind:value={form.remoteWebdavUser} autocomplete="off" />
      </label>
      <label>WebDAV password {#if editing}<span class="hint-inline">(leave blank to keep)</span>{/if}
        <PasswordInput bind:value={form.remotePass} placeholder="" />
      </label>
    {:else}
      <label>Remote host
        <input bind:value={form.remoteHost} placeholder="files.example.com" />
      </label>
      <label>Remote user
        <input bind:value={form.remoteUser} autocomplete="off" />
      </label>
      <label>Port (optional)
        <input bind:value={form.remotePort} placeholder="22" />
      </label>
      <label>Remote path
        <input bind:value={form.url} placeholder="/home/you/secrets.kdbx" />
      </label>
      <label>SFTP password {#if editing}<span class="hint-inline">(leave blank to keep)</span>{/if}
        <PasswordInput bind:value={form.remotePass} placeholder="" />
      </label>
    {/if}

    <label>Master password {#if editing}<span class="hint-inline">(leave blank to keep)</span>{/if}
      <PasswordInput bind:value={form.master} placeholder="KeePass master password" />
    </label>

    <label>Key file contents (optional)
      <textarea bind:value={form.keyFile} rows="2"
        placeholder="paste .keyx / .key file contents if your database uses one"></textarea>
    </label>

    <div class="kp-form-actions">
      <button class="primary" onclick={save} disabled={saving}>
        {saving ? "Saving…" : "Save"}
      </button>
      <button onclick={cancel} disabled={saving}>Cancel</button>
    </div>
  </div>
{/if}

<style>
  .kp-list { display: flex; flex-direction: column; gap: 6px; margin: 8px 0; }
  .kp-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 10px; border: 1px solid var(--border, #333);
    border-radius: 6px; gap: 12px;
  }
  .kp-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .kp-name { font-weight: 600; }
  .kp-src {
    font-size: 0.75rem; text-transform: uppercase; opacity: 0.7;
    letter-spacing: 0.05em;
  }
  .kp-fetched { font-size: 0.8rem; opacity: 0.7; }
  .kp-actions { display: flex; gap: 6px; flex-shrink: 0; }
  .kp-form {
    margin-top: 12px; padding: 12px; border: 1px solid var(--border, #333);
    border-radius: 6px; display: flex; flex-direction: column; gap: 10px;
    max-width: 520px;
  }
  .kp-form label { display: flex; flex-direction: column; gap: 4px; font-size: 0.85rem; }
  .kp-form input, .kp-form select, .kp-form textarea {
    width: 100%; box-sizing: border-box;
  }
  .kp-form-actions { display: flex; gap: 8px; margin-top: 4px; }
  .hint-inline { font-weight: 400; opacity: 0.6; font-size: 0.8rem; }
  .danger { color: var(--danger, #e66); }
</style>
