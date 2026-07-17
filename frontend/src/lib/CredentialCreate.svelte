<script lang="ts">
  import { credentials, selection } from "./stores.svelte";
  import { errMsg } from "./connectErrors";
  import { toast } from "./toast.svelte";
  import { copyText } from "./clipboard";
  import { api, type CredentialCreateInput, type BitwardenCipherInfo } from "./api";
  import PasswordStrengthMeter from "./PasswordStrengthMeter.svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import { clickOutside } from "./clickOutside";

  interface Props {
    onClose: () => void;
    // When set, the new credential lands inside this folder. Wired
    // from the credential-folder header "+ Credential" action so
    // creating from inside a folder doesn't drop the credential at
    // the root.
    defaultFolderId?: string | null;
  }
  let { onClose, defaultFolderId = null }: Props = $props();

  let kind = $state<"password" | "key_generate" | "key_import_paste" | "key_file_ref" | "agent" | "opkssh" | "api_token" | "keepass" | "bitwarden" | "infisical">("password");
  let name = $state("");
  let hint = $state("");
  let defaultUser = $state("");
  // Optional expiry date (YYYY-MM-DD) for time-limited secrets - API
  // tokens, setup / auth keys. Empty = no expiry.
  let expiresDate = $state("");
  let tagsRaw = $state("");

  let password = $state("");

  let keyType = $state<"ed25519" | "rsa" | "ecdsa">("ed25519");
  let bits = $state<number | undefined>(undefined);
  let comment = $state("");
  let passphrase = $state("");

  let privateText = $state("");
  let importPass = $state("");

  let keyPath = $state("");
  let fileRefPass = $state("");

  let agentSocket = $state("");
  let agentFp = $state("");

  let opksshBasename = $state("id_ecdsa");
  let opksshConfigYaml = $state("");
  let opksshMaxAge = $state<number | undefined>(168);
  let opksshRefresh = $state<number | undefined>(60);

  let apiTokenID = $state("");
  let apiTokenSecret = $state("");

  // keepass reference
  let kpDatabases = $state<{ id: string; name: string }[]>([]);
  let kpDbId = $state("");
  let kpEntries = $state<{ uuid: string; title: string; group: string; fields: string[]; is_key_field: (f: string) => boolean }[]>([]);
  let kpEntryUuid = $state("");
  let kpField = $state("password");
  let kpFieldOptions = $state<string[]>([]);
  let kpLoadingEntries = $state(false);
  let kpErr = $state<string | null>(null);

  async function loadKeepassDatabases() {
    try {
      const list = await api.keepassList();
      kpDatabases = list.map((d) => ({ id: d.id, name: d.name }));
    } catch (e) {
      kpErr = errMsg(e);
    }
  }

  async function loadKeepassEntries() {
    kpEntryUuid = "";
    kpEntries = [];
    kpFieldOptions = [];
    if (!kpDbId) return;
    kpLoadingEntries = true;
    kpErr = null;
    try {
      const tree = await api.keepassBrowse(kpDbId);
      const flat: typeof kpEntries = [];
      const walk = (groups: typeof tree) => {
        for (const g of groups || []) {
          for (const e of g.entries || []) {
            const attach = e.attachments || [];
            const custom = e.custom_keys || [];
            const fields = [
              ...(e.has_pass ? ["password"] : []),
              ...custom,
              ...attach,
            ];
            flat.push({
              uuid: e.uuid,
              title: e.title || "(untitled)",
              group: e.group_path,
              fields,
              is_key_field: (f: string) => attach.includes(f) || (f !== "password" && custom.includes(f)),
            });
          }
          walk(g.groups || []);
        }
      };
      walk(tree);
      kpEntries = flat;
    } catch (e) {
      kpErr = errMsg(e);
    } finally {
      kpLoadingEntries = false;
    }
  }

  function onKeepassEntryChange() {
    const entry = kpEntries.find((e) => e.uuid === kpEntryUuid);
    kpFieldOptions = entry ? entry.fields : [];
    kpField = kpFieldOptions[0] || "password";
  }

  function keepassFieldIsKey(): boolean {
    const entry = kpEntries.find((e) => e.uuid === kpEntryUuid);
    return entry ? entry.is_key_field(kpField) : false;
  }

  // Load the database list the first time the user switches to the KeePass kind.
  let kpLoaded = false;
  $effect(() => {
    if (kind === "keepass" && !kpLoaded) {
      kpLoaded = true;
      loadKeepassDatabases();
    }
  });

  // bitwarden reference
  let bwServers = $state<{ id: string; name: string }[]>([]);
  let bwServerId = $state("");
  let bwItems = $state<{ id: string; title: string; group: string; fields: string[]; is_key_field: (f: string) => boolean }[]>([]);
  let bwCipherId = $state("");
  let bwField = $state("password");
  let bwFieldOptions = $state<string[]>([]);
  let bwLoadingItems = $state(false);
  let bwErr = $state<string | null>(null);

  async function loadBitwardenServers() {
    try {
      const list = await api.bitwardenList();
      bwServers = list.map((s) => ({ id: s.id, name: s.name }));
    } catch (e) {
      bwErr = errMsg(e);
    }
  }

  async function loadBitwardenItems() {
    bwCipherId = "";
    bwItems = [];
    bwFieldOptions = [];
    if (!bwServerId) return;
    bwLoadingItems = true;
    bwErr = null;
    try {
      const tree = await api.bitwardenBrowse(bwServerId);
      const flat: typeof bwItems = [];
      for (const g of tree || []) {
        const push = (c: BitwardenCipherInfo, path: string) => {
          const custom = c.custom_keys || [];
          const fields = [
            ...(c.has_password ? ["password"] : []),
            ...(c.is_ssh_key ? ["privatekey"] : []),
            ...(c.username ? ["username"] : []),
            ...custom,
          ];
          flat.push({
            id: c.id,
            title: c.name || "(untitled)",
            group: path,
            fields,
            is_key_field: (f: string) => f === "privatekey",
          });
        };
        for (const c of g.ciphers || []) push(c, g.name);
        for (const col of g.collections || []) {
          for (const c of col.ciphers || []) push(c, g.name + " / " + col.name);
        }
      }
      bwItems = flat;
    } catch (e) {
      bwErr = errMsg(e);
    } finally {
      bwLoadingItems = false;
    }
  }

  function onBitwardenItemChange() {
    const item = bwItems.find((i) => i.id === bwCipherId);
    bwFieldOptions = item ? item.fields : [];
    bwField = bwFieldOptions[0] || "password";
  }

  function bitwardenFieldIsKey(): boolean {
    return bwField === "privatekey";
  }

  let bwLoaded = false;
  $effect(() => {
    if (kind === "bitwarden" && !bwLoaded) {
      bwLoaded = true;
      loadBitwardenServers();
    }
  });

  // infisical reference
  let infServers = $state<{ id: string; name: string }[]>([]);
  let infServerId = $state("");
  // Flat list of secrets across the tree: each carries the project + environment
  // + folder path + key needed to build the ref.
  let infItems = $state<{
    id: string; // synthetic: projectId|environment|path|key
    projectId: string; environment: string; path: string; key: string;
    title: string; group: string; isKey: boolean;
  }[]>([]);
  let infItemId = $state("");
  let infLoadingItems = $state(false);
  let infErr = $state<string | null>(null);

  async function loadInfisicalServers() {
    try {
      const list = await api.infisicalList();
      infServers = list.map((s) => ({ id: s.id, name: s.name }));
    } catch (e) {
      infErr = errMsg(e);
    }
  }

  async function loadInfisicalItems() {
    infItemId = "";
    infItems = [];
    if (!infServerId) return;
    infLoadingItems = true;
    infErr = null;
    try {
      const tree = await api.infisicalBrowse(infServerId);
      const flat: typeof infItems = [];
      for (const g of tree || []) {
        for (const env of g.environments || []) {
          for (const e of env.entries || []) {
            const p = e.path && e.path !== "/" ? e.path + " · " : "";
            flat.push({
              id: `${g.project_id}|${env.slug}|${e.path}|${e.key}`,
              projectId: g.project_id,
              environment: env.slug,
              path: e.path || "/",
              key: e.key,
              title: p + e.key,
              group: g.name + " / " + env.name,
              isKey: e.is_key,
            });
          }
        }
      }
      infItems = flat;
    } catch (e) {
      infErr = errMsg(e);
    } finally {
      infLoadingItems = false;
    }
  }

  function selectedInfItem() {
    return infItems.find((i) => i.id === infItemId) || null;
  }

  let infLoaded = false;
  $effect(() => {
    if (kind === "infisical" && !infLoaded) {
      infLoaded = true;
      loadInfisicalServers();
    }
  });

  let busy = $state(false);
  let err = $state<string | null>(null);
  let resultPub = $state<string | null>(null);
  let resultFp = $state<string | null>(null);

  function tags(): string[] {
    return tagsRaw.split(",").map((s) => s.trim()).filter(Boolean);
  }

  async function browsePuttyKey() {
    try {
      const path = await api.pickPuttyKeyFile();
      if (path) keyPath = path;
    } catch (e) {
      err = errMsg(e);
    }
  }

  async function submit() {
    if (!name.trim()) {
      err = "Name is required";
      return;
    }
    busy = true;
    err = null;
    try {
      const base = {
        name,
        hint: hint || undefined,
        tags: tags(),
        default_username: defaultUser || undefined,
        folder_id: defaultFolderId ?? undefined,
        // <input type=date> gives YYYY-MM-DD in local time; store the
        // start of that day as a unix timestamp. Empty = no expiry.
        expires_at: expiresDate ? Math.floor(new Date(expiresDate + "T00:00:00").getTime() / 1000) : undefined,
      };
      let input: CredentialCreateInput;
      switch (kind) {
        case "password":
          input = { ...base, kind: "password", password } as CredentialCreateInput;
          break;
        case "key_generate":
          input = {
            ...base,
            kind: "key_generate",
            params: {
              key_type: keyType,
              bits: bits ?? undefined,
              comment: comment || name,
              passphrase: passphrase || undefined,
            },
          } as CredentialCreateInput;
          break;
        case "key_import_paste":
          input = {
            ...base,
            kind: "key_import_paste",
            private_openssh: privateText,
            passphrase: importPass || undefined,
          } as CredentialCreateInput;
          break;
        case "key_file_ref":
          input = {
            ...base,
            kind: "key_file_ref",
            key_path: keyPath,
            passphrase: fileRefPass || undefined,
          } as CredentialCreateInput;
          break;
        case "agent":
          input = {
            ...base,
            kind: "agent",
            socket_path: agentSocket || undefined,
            fingerprint: agentFp || undefined,
          } as CredentialCreateInput;
          break;
        case "opkssh":
          input = {
            ...base,
            kind: "opkssh",
            key_basename: opksshBasename,
            opkssh_config_yaml: opksshConfigYaml,
            max_cert_age_hours: opksshMaxAge ?? undefined,
            min_remaining_before_refresh_minutes: opksshRefresh ?? undefined,
          } as CredentialCreateInput;
          break;
        case "api_token":
          if (!apiTokenSecret) {
            err = "Token secret is required";
            busy = false;
            return;
          }
          input = {
            ...base,
            kind: "api_token",
            api_token_id: apiTokenID,
            api_token_secret: apiTokenSecret,
          } as CredentialCreateInput;
          break;
        case "keepass":
          if (!kpDbId || !kpEntryUuid) {
            err = "Pick a KeePass database and entry";
            busy = false;
            return;
          }
          input = {
            ...base,
            kind: "keepass",
            keepass_db_id: kpDbId,
            keepass_entry_uuid: kpEntryUuid,
            keepass_field: kpField,
            keepass_is_key: keepassFieldIsKey(),
          } as CredentialCreateInput;
          break;
        case "bitwarden":
          if (!bwServerId || !bwCipherId) {
            err = "Pick a Bitwarden server and item";
            busy = false;
            return;
          }
          input = {
            ...base,
            kind: "bitwarden",
            bitwarden_server_id: bwServerId,
            bitwarden_cipher_id: bwCipherId,
            bitwarden_field: bwField,
            bitwarden_is_key: bitwardenFieldIsKey(),
          } as CredentialCreateInput;
          break;
        case "infisical": {
          const item = selectedInfItem();
          if (!infServerId || !item) {
            err = "Pick an Infisical server and secret";
            busy = false;
            return;
          }
          input = {
            ...base,
            kind: "infisical",
            infisical_server_id: infServerId,
            infisical_project_id: item.projectId,
            infisical_environment: item.environment,
            infisical_secret_path: item.path,
            infisical_key: item.key,
            infisical_is_key: item.isKey,
          } as CredentialCreateInput;
          break;
        }
      }
      const result = await api.credentialsCreate(input);
      await credentials.load();
      if (result.credential) {
        selection.select({ kind: "credential", id: result.credential.id });
      }
      resultPub = result.public_key ?? null;
      resultFp = result.fingerprint ?? null;
      toast.ok(`Credential "${name}" created`);
      if (!resultPub) {
        onClose();
      }
    } catch (e: any) {
      err = errMsg(e);
      toast.err(`Create failed: ${err}`);
    } finally {
      busy = false;
    }
  }

  function copyPub() {
    if (resultPub) copyText(resultPub, { label: "Public key" });
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1" onkeydown={(e) => { if (e.key === "Escape") onClose(); }}>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document" use:clickOutside={{ onOutside: onClose }} onkeydown={(e) => e.stopPropagation()}>
    <header>
      <h1>New credential</h1>
      <button class="close" onclick={onClose}>✕</button>
    </header>

    {#if resultPub}
      <div class="success">
        <p><strong>Credential created.</strong></p>
        {#if resultFp}<p>Fingerprint: <code>{resultFp}</code></p>{/if}
        <p>Public key (deploy this to servers):</p>
        <pre>{resultPub}</pre>
        <div class="row">
          <button onclick={copyPub}>Copy to clipboard</button>
          <button class="primary" onclick={onClose}>Done</button>
        </div>
      </div>
    {:else}
      <div class="form">
        <label>
          Kind
          <select bind:value={kind}>
            <option value="password">Password</option>
            <option value="key_generate">SSH key - generate new</option>
            <option value="key_import_paste">SSH key - paste existing</option>
            <option value="key_file_ref">SSH key - reference file on disk</option>
            <option value="agent">SSH agent</option>
            <option value="opkssh">opkssh profile</option>
            <option value="api_token">API token (proxmox, hetzner, …)</option>
            <option value="keepass">From KeePass database</option>
            <option value="bitwarden">From Bitwarden server</option>
            <option value="infisical">From Infisical server</option>
          </select>
        </label>
        <label>Name *
          <input bind:value={name} placeholder="e.g. Production deploy" />
        </label>
        <label>Hint
          <input bind:value={hint} placeholder="optional description" />
        </label>
        <label>Default username
          <input bind:value={defaultUser} placeholder="optional, used as suggestion" />
        </label>
        <label>Tags
          <input bind:value={tagsRaw} placeholder="comma,separated" />
        </label>
        {#if kind === "api_token" || kind === "password" || kind === "key_generate" || kind === "key_import_paste" || kind === "key_file_ref"}
          <label>Expires <span class="hint inline">(optional - warns you before a time-limited token / key lapses)</span>
            <input type="date" bind:value={expiresDate} />
          </label>
        {/if}

        {#if kind === "password"}
          <label>Password
            <PasswordInput bind:value={password} />
          </label>
          <PasswordStrengthMeter {password} />
        {:else if kind === "key_generate"}
          <label>Key type
            <select bind:value={keyType}>
              <option value="ed25519">Ed25519 (recommended)</option>
              <option value="rsa">RSA</option>
              <option value="ecdsa">ECDSA</option>
            </select>
          </label>
          {#if keyType === "rsa"}
            <label>RSA bits
              <select bind:value={bits}>
                <option value={2048}>2048</option>
                <option value={3072}>3072</option>
                <option value={4096}>4096</option>
              </select>
            </label>
          {:else if keyType === "ecdsa"}
            <label>ECDSA curve
              <select bind:value={bits}>
                <option value={256}>P-256</option>
                <option value={384}>P-384</option>
                <option value={521}>P-521</option>
              </select>
            </label>
          {/if}
          <label>Comment
            <input bind:value={comment} placeholder="user@host (default: name)" />
          </label>
          <label>Passphrase
            <PasswordInput bind:value={passphrase} placeholder="optional" />
          </label>
        {:else if kind === "key_import_paste"}
          <label>Private key (OpenSSH or PuTTY .ppk)
            <textarea bind:value={privateText} rows="6" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----...  or  PuTTY-User-Key-File-3: ..."></textarea>
          </label>
          <label>Passphrase
            <PasswordInput bind:value={importPass} placeholder="if key is encrypted" />
          </label>
          <p class="hint-text">A PuTTY .ppk is converted to OpenSSH and stored in the vault.</p>
        {:else if kind === "key_file_ref"}
          <label>Key path
            <div class="path-row">
              <input bind:value={keyPath} placeholder="~/.ssh/id_ed25519  or  a .ppk" />
              <button type="button" onclick={browsePuttyKey}>Browse .ppk…</button>
            </div>
          </label>
          <label>Passphrase
            <PasswordInput bind:value={fileRefPass} placeholder="if key is encrypted" />
          </label>
          <p class="hint-text">
            An OpenSSH/PEM file stays on disk (we store the path + passphrase only).
            A <strong>.ppk</strong> is converted to OpenSSH and stored in the vault.
          </p>
        {:else if kind === "agent"}
          <label>Socket path
            <input bind:value={agentSocket} placeholder="leave empty for $SSH_AUTH_SOCK" />
          </label>
          <label>Specific key fingerprint
            <input bind:value={agentFp} placeholder="optional" />
          </label>
        {:else if kind === "opkssh"}
          <label>Key basename
            <input bind:value={opksshBasename} placeholder="id_ecdsa" />
          </label>
          <label>Provider YAML
            <textarea
              bind:value={opksshConfigYaml}
              rows="12"
              placeholder="Paste your opkssh provider YAML:&#10;&#10;default_provider: microsoft&#10;providers:&#10;  - alias: microsoft&#10;    issuer: https://login.microsoftonline.com/...&#10;    client_id: ...&#10;    scopes: openid profile email&#10;    redirect_uris:&#10;      - http://localhost:10001/login-callback"
            ></textarea>
          </label>
          <label>Max cert age (hours)
            <input type="number" bind:value={opksshMaxAge} />
          </label>
          <label>Refresh window (minutes before expiry)
            <input type="number" bind:value={opksshRefresh} />
          </label>
        {:else if kind === "api_token"}
          <label>Token id
            <input bind:value={apiTokenID} placeholder="e.g. sshtool@pve!inventory" />
          </label>
          <label>Token secret *
            <PasswordInput bind:value={apiTokenSecret} mono />
          </label>
          <p class="hint">
            For external integrations like the dynamic-inventory
            providers (proxmox, hetzner). Not SSH auth material -
            it's stored exactly as you enter it and handed to the
            integration that asks for it by credential id.
          </p>
        {:else if kind === "keepass"}
          {#if kpDatabases.length === 0}
            <p class="hint">
              No KeePass databases registered. Add one in Settings → KeePass
              first, then come back here.
            </p>
          {:else}
            <label>KeePass database
              <select bind:value={kpDbId} onchange={loadKeepassEntries}>
                <option value="">Select a database…</option>
                {#each kpDatabases as d (d.id)}
                  <option value={d.id}>{d.name}</option>
                {/each}
              </select>
            </label>
            {#if kpLoadingEntries}
              <p class="hint">Loading entries… (unlocks the .kdbx)</p>
            {:else if kpDbId}
              <label>Entry
                <select bind:value={kpEntryUuid} onchange={onKeepassEntryChange}>
                  <option value="">Select an entry…</option>
                  {#each kpEntries as e (e.uuid)}
                    <option value={e.uuid}>{e.group ? e.group + " / " : ""}{e.title}</option>
                  {/each}
                </select>
              </label>
              {#if kpEntryUuid && kpFieldOptions.length > 0}
                <label>Field
                  <select bind:value={kpField}>
                    {#each kpFieldOptions as f}
                      <option value={f}>{f}</option>
                    {/each}
                  </select>
                </label>
                <p class="hint">
                  {keepassFieldIsKey()
                    ? "Resolved as a private key at connect time."
                    : "Resolved as a password at connect time."}
                  The secret is read from KeePass on connect and never copied
                  into this app.
                </p>
              {/if}
            {/if}
          {/if}
          {#if kpErr}<p class="hint" style="color:var(--danger,#e66)">{kpErr}</p>{/if}
        {:else if kind === "bitwarden"}
          {#if bwServers.length === 0}
            <p class="hint">
              No Bitwarden servers registered. Add one in Settings - Bitwarden
              first, then come back here.
            </p>
          {:else}
            <label>Bitwarden server
              <select bind:value={bwServerId} onchange={loadBitwardenItems}>
                <option value="">Select a server…</option>
                {#each bwServers as s (s.id)}
                  <option value={s.id}>{s.name}</option>
                {/each}
              </select>
            </label>
            {#if bwLoadingItems}
              <p class="hint">Loading items… (syncs and unlocks the vault)</p>
            {:else if bwServerId}
              <label>Item
                <select bind:value={bwCipherId} onchange={onBitwardenItemChange}>
                  <option value="">Select an item…</option>
                  {#each bwItems as i (i.id)}
                    <option value={i.id}>{i.group ? i.group + " / " : ""}{i.title}</option>
                  {/each}
                </select>
              </label>
              {#if bwCipherId && bwFieldOptions.length > 0}
                <label>Field
                  <select bind:value={bwField}>
                    {#each bwFieldOptions as f}
                      <option value={f}>{f}</option>
                    {/each}
                  </select>
                </label>
                <p class="hint">
                  {bitwardenFieldIsKey()
                    ? "Resolved as a private key at connect time."
                    : "Resolved as a password at connect time."}
                  The secret is read from the server on connect and never copied
                  into this app.
                </p>
              {/if}
            {/if}
          {/if}
          {#if bwErr}<p class="hint" style="color:var(--danger,#e66)">{bwErr}</p>{/if}
        {:else if kind === "infisical"}
          {#if infServers.length === 0}
            <p class="hint">
              No Infisical servers registered. Add one in Settings - Infisical
              first, then come back here.
            </p>
          {:else}
            <label>Infisical server
              <select bind:value={infServerId} onchange={loadInfisicalItems}>
                <option value="">Select a server…</option>
                {#each infServers as s (s.id)}
                  <option value={s.id}>{s.name}</option>
                {/each}
              </select>
            </label>
            {#if infLoadingItems}
              <p class="hint">Reading secrets…</p>
            {:else if infServerId}
              <label>Secret
                <select bind:value={infItemId}>
                  <option value="">Select a secret…</option>
                  {#each infItems as i (i.id)}
                    <option value={i.id}>{i.group} / {i.title}</option>
                  {/each}
                </select>
              </label>
              {#if infItemId}
                <p class="hint">
                  {selectedInfItem()?.isKey
                    ? "Resolved as a private key at connect time."
                    : "Resolved as a password at connect time."}
                  The value is read from the server on connect and never copied
                  into this app.
                </p>
              {/if}
            {/if}
          {/if}
          {#if infErr}<p class="hint" style="color:var(--danger,#e66)">{infErr}</p>{/if}
        {/if}

        {#if err}<div class="err">{err}</div>{/if}

        <div class="row">
          <button onclick={onClose}>Cancel</button>
          <button class="primary" disabled={busy} onclick={submit}>
            {busy ? "Creating…" : "Create"}
          </button>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex; align-items: center; justify-content: center; z-index: 50;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(560px, 92vw); max-height: 90vh; overflow: auto;
    box-shadow: 0 10px 40px rgba(0, 0, 0, 0.5);
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.8rem 1rem;
    border-bottom: 1px solid var(--surface0);
  }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  .close {
    background: transparent; border: 0; color: var(--overlay0);
    cursor: pointer; font: inherit; padding: 0.2rem 0.5rem;
  }
  .close:hover { color: var(--red); }
  .form {
    padding: 1rem;
    display: flex; flex-direction: column; gap: 0.6rem;
  }
  label {
    display: flex; flex-direction: column; gap: 0.25rem;
    font-size: 0.8rem; color: var(--subtext0);
  }
  input, textarea, select {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  input:focus, textarea:focus, select:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  textarea { font-family: ui-monospace, Menlo, monospace; font-size: 0.78rem; }
  .hint-text { color: var(--overlay0); font-size: 0.75rem; margin: 0; }
  .path-row { display: flex; gap: 0.4rem; align-items: stretch; }
  .path-row input { flex: 1; min-width: 0; }
  .path-row button { flex-shrink: 0; white-space: nowrap; }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.4rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.85rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover:not(:disabled) { background: var(--lavender); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .err {
    color: var(--red); background: var(--crust);
    padding: 0.5rem 0.75rem; border-radius: 4px;
    border-left: 3px solid var(--red); font-size: 0.85rem;
  }
  .success { padding: 1rem; }
  .success pre {
    background: var(--crust); padding: 0.6rem 0.8rem;
    border-radius: 4px; font-size: 0.78rem;
    overflow: auto; word-break: break-all; white-space: pre-wrap;
  }
  code {
    font-size: 0.8rem; background: var(--crust);
    padding: 0.1rem 0.3rem; border-radius: 3px;
  }
</style>
