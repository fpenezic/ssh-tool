<script lang="ts">
  import { credentials, selection } from "./stores.svelte";
  import { errMsg } from "./connectErrors";
  import { toast } from "./toast.svelte";
  import { copyText } from "./clipboard";
  import { api, type CredentialCreateInput } from "./api";
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

  let kind = $state<"password" | "key_generate" | "key_import_paste" | "key_file_ref" | "agent" | "opkssh" | "api_token">("password");
  let name = $state("");
  let hint = $state("");
  let defaultUser = $state("");
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

  let busy = $state(false);
  let err = $state<string | null>(null);
  let resultPub = $state<string | null>(null);
  let resultFp = $state<string | null>(null);

  function tags(): string[] {
    return tagsRaw.split(",").map((s) => s.trim()).filter(Boolean);
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
          <label>Private key (OpenSSH format)
            <textarea bind:value={privateText} rows="6" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----..."></textarea>
          </label>
          <label>Passphrase
            <PasswordInput bind:value={importPass} placeholder="if key is encrypted" />
          </label>
        {:else if kind === "key_file_ref"}
          <label>Key path
            <input bind:value={keyPath} placeholder="~/.ssh/id_ed25519" />
          </label>
          <label>Passphrase
            <PasswordInput bind:value={fileRefPass} placeholder="if key is encrypted" />
          </label>
          <p class="hint-text">File stays on disk. We store path + passphrase (in keychain) only.</p>
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
