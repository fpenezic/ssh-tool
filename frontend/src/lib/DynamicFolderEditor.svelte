<script lang="ts">
  // Editor for a dynamic-inventory folder. Creates a new dynamic
  // folder when `existingFolderId` is null, edits an existing one
  // when set. Provider-specific config is kept generic in the IPC
  // (map[string]any), so adding hetzner / aws later only needs a new
  // sub-form here and a provider impl on the backend.
  //
  // Inherit-cascade settings (port / username / jump / credential /
  // notes / tags) are NOT edited here - the folder is a regular
  // folder under the hood. Use the standard folder editor for that.

  import { api } from "./api";
  import { errMsg } from "./connectErrors";
  import { withTakeover } from "./connectionActions.svelte";
  import { tree, selection, credentials } from "./stores.svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import { clickOutside } from "./clickOutside";
  import { IconX } from "./iconMap";
  import { toast } from "./toast.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { networkProfiles } from "./networkProfiles.svelte";

  // Populate the Network dropdowns (cached; refreshes on tunnel events).
  $effect(() => { networkProfiles.load().catch(() => {}); });

  type Props = {
    parentId: string | null;
    existingFolderId: string | null;
    onClose: () => void;
  };
  let { parentId, existingFolderId, onClose }: Props = $props();

  const isEdit = $derived(!!existingFolderId);

  // ----- form state -----
  type ProviderId =
    | "proxmox"
    | "hetzner"
    | "digitalocean"
    | "linode"
    | "vultr"
    | "scaleway"
    | "aws_ec2"
    | "ansible";

  let name = $state("");
  let provider = $state<ProviderId>("proxmox");
  let refreshSeconds = $state<number>(300);

  // Per-provider hostname-source vocabulary. Keys must match the
  // strings the backend accepts (see *.go pickXHostname switches).
  const HOSTNAME_OPTIONS: Record<string, { value: string; label: string }[]> = {
    hetzner: [
      { value: "name",         label: "Server name (resolved via DNS)" },
      { value: "public_ipv4",  label: "Public IPv4" },
      { value: "private_ipv4", label: "First private network IPv4" },
    ],
    digitalocean: [
      { value: "name",         label: "Droplet name" },
      { value: "public_ipv4",  label: "Public IPv4" },
      { value: "private_ipv4", label: "Private (VPC) IPv4" },
    ],
    linode: [
      { value: "label",        label: "Instance label" },
      { value: "public_ipv4",  label: "Public IPv4" },
      { value: "private_ipv4", label: "Private IPv4" },
    ],
    vultr: [
      { value: "label",        label: "Instance label" },
      { value: "public_ipv4",  label: "Main public IPv4" },
      { value: "private_ipv4", label: "Internal IPv4" },
    ],
    scaleway: [
      { value: "name",         label: "Server name" },
      { value: "public_ipv4",  label: "Public IPv4" },
      { value: "private_ipv4", label: "Private IPv4" },
    ],
    aws_ec2: [
      { value: "name_tag",     label: "Name tag" },
      { value: "public_ipv4",  label: "Public IPv4" },
      { value: "private_ipv4", label: "Private IPv4" },
      { value: "public_dns",   label: "Public DNS" },
    ],
  };

  let hostnameSource = $state<string>("name");
  // Region/zone for providers that scope by it. Scaleway requires
  // zone; AWS requires region; others ignore.
  let regionOrZone = $state<string>("");

  // proxmox-specific
  let baseURL = $state("");
  // Token now lives in the credentials vault as kind=api_token.
  // The folder config just stores a reference to it.
  let tokenCredentialId = $state<string>("");
  let insecureSkipVerify = $state(false);
  // Optional user+password credential used for the node-shell console
  // (PVE's vncshell rejects API tokens - it needs a real realm login
  // ticket). Empty = node consoles unavailable; guest consoles still
  // work via the API token. A password-kind credential whose name is
  // the PVE username (e.g. "fpenezic@ldap").
  let vncCredentialId = $state<string>("");

  const passwordCreds = $derived(
    credentials.list.filter((c) => c.kind === "password"),
  );

  // Inline "create new token" UI (shown when user clicks "+").
  let newTokenOpen = $state(false);
  let newTokenName = $state("");
  let newTokenID = $state("");
  let newTokenSecret = $state("");
  let newTokenSaving = $state(false);
  let newTokenErr = $state<string | null>(null);

  const apiTokenCreds = $derived(
    credentials.list.filter((c) => c.kind === "api_token"),
  );

  let includeHosts = $state(true);
  let includeGuests = $state(true);
  let hideStopped = $state(false);
  let tagWhitelist = $state("");
  let tagBlacklist = $state("");

  // ansible-specific
  let ansiblePath = $state("");
  let ansibleHostPattern = $state("");
  let ansibleGroupPattern = $state("");
  let ansibleNameFrom = $state<"inventory_hostname" | "ansible_host">("inventory_hostname");
  // Per-folder jump credential applied to every hop parsed out of
  // ansible_ssh_common_args. Targets typically can't auth to the
  // bastion with their own creds, so this is the only way the SSH
  // layer learns who to log in as on the jump host.
  let ansibleJumpCredentialId = $state<string>("");

  // SSH-capable credentials (password / key / agent / opkssh).
  // Used for the jump-host picker; api_token / vault refs aren't
  // valid SSH auth on their own.
  const sshCreds = $derived(
    credentials.list.filter(
      (c) => c.kind === "password" || c.kind === "key" || c.kind === "agent" || c.kind === "opkssh",
    ),
  );

  // Network profile (userspace WireGuard) the provider API is fetched
  // through. "" = follow the folder's own Network setting (the
  // backend resolves the inheritance), "__direct__" = explicitly no
  // tunnel, otherwise a profile id. SSH connects to the entries
  // always follow the folder's Network setting.
  let networkProfileId = $state<string>("");

  let saving = $state(false);
  let err = $state<string | null>(null);
  let info = $state<{ lastPulled: number | null; lastError: string } | null>(null);

  $effect(() => {
    if (!existingFolderId) return;
    void (async () => {
      try {
        const f = tree.folders.find((x) => x.id === existingFolderId);
        if (f) name = f.name;
        const d = await api.dynamicFolderGet(existingFolderId);
        if (!d) return;
        provider = (d.provider as any) || "proxmox";
        refreshSeconds = d.refresh_seconds || 300;
        const cfg = d.config ?? {};
        baseURL = String(cfg.base_url ?? "");
        tokenCredentialId = String(cfg.api_token_credential_id ?? "");
        vncCredentialId = String(cfg.vnc_credential_id ?? "");
        insecureSkipVerify = !!cfg.insecure_skip_verify;
        if (typeof cfg.hostname_source === "string") {
          hostnameSource = cfg.hostname_source;
        } else {
          hostnameSource = HOSTNAME_OPTIONS[provider]?.[0]?.value ?? "name";
        }
        regionOrZone = String(cfg.region ?? cfg.zone ?? "");
        includeHosts = cfg.include_hosts !== false;
        includeGuests = cfg.include_guests !== false;
        hideStopped = !!cfg.hide_stopped;
        tagWhitelist = Array.isArray(cfg.tag_whitelist) ? cfg.tag_whitelist.join(", ") : "";
        tagBlacklist = Array.isArray(cfg.tag_blacklist) ? cfg.tag_blacklist.join(", ") : "";
        ansiblePath = String(cfg.path ?? "");
        ansibleHostPattern = String(cfg.host_pattern ?? "");
        ansibleGroupPattern = String(cfg.group_pattern ?? "");
        ansibleNameFrom = (cfg.name_from === "ansible_host" ? "ansible_host" : "inventory_hostname") as any;
        ansibleJumpCredentialId = String(cfg.jump_credential_id ?? "");
        networkProfileId = String(cfg.network_profile_id ?? "");
        info = { lastPulled: d.last_pulled_at ?? null, lastError: d.last_error ?? "" };
      } catch (e: any) {
        err = String(e);
      }
    })();
  });

  function parseTagList(s: string): string[] {
    return s.split(/[,\n]/).map((t) => t.trim()).filter(Boolean);
  }

  function buildConfig(): Record<string, any> {
    if (provider === "proxmox") {
      return {
        base_url: baseURL.trim(),
        api_token_credential_id: tokenCredentialId,
        vnc_credential_id: vncCredentialId,
        network_profile_id: networkProfileId,
        insecure_skip_verify: insecureSkipVerify,
        include_hosts: includeHosts,
        include_guests: includeGuests,
        hide_stopped: hideStopped,
        tag_whitelist: parseTagList(tagWhitelist),
        tag_blacklist: parseTagList(tagBlacklist),
      };
    }
    if (provider === "ansible") {
      return {
        path: ansiblePath.trim(),
        host_pattern: ansibleHostPattern.trim(),
        group_pattern: ansibleGroupPattern.trim(),
        name_from: ansibleNameFrom,
        jump_credential_id: ansibleJumpCredentialId,
        // No hide_stopped concept for static inventory; tag filters
        // still apply because the backend filter pipeline runs on
        // every provider uniformly.
        tag_whitelist: parseTagList(tagWhitelist),
        tag_blacklist: parseTagList(tagBlacklist),
      };
    }
    // Generic cloud-token provider shape. Hostname source vocabulary
    // is per-provider; region/zone is only meaningful for AWS EC2
    // and Scaleway, ignored elsewhere.
    const cfg: Record<string, any> = {
      api_token_credential_id: tokenCredentialId,
      network_profile_id: networkProfileId,
      hostname_source: hostnameSource,
      include_hosts: false,
      include_guests: true,
      hide_stopped: hideStopped,
      tag_whitelist: parseTagList(tagWhitelist),
      tag_blacklist: parseTagList(tagBlacklist),
    };
    if (provider === "aws_ec2") cfg.region = regionOrZone.trim();
    if (provider === "scaleway") cfg.zone = regionOrZone.trim();
    return cfg;
  }

  async function createInlineToken() {
    newTokenErr = null;
    if (!newTokenName.trim() || !newTokenSecret) {
      newTokenErr = "Name and secret are required";
      return;
    }
    newTokenSaving = true;
    try {
      const res = await api.credentialsCreate({
        kind: "api_token",
        name: newTokenName.trim(),
        api_token_id: newTokenID.trim(),
        api_token_secret: newTokenSecret,
      } as any);
      await credentials.load();
      if (res?.credential) tokenCredentialId = res.credential.id;
      newTokenOpen = false;
      newTokenName = "";
      newTokenID = "";
      newTokenSecret = "";
    } catch (e: any) {
      newTokenErr = errMsg(e);
    } finally {
      newTokenSaving = false;
    }
  }

  async function save() {
    err = null;
    if (!name.trim()) { err = "Name is required"; return; }
    if (provider === "proxmox" && !baseURL.trim()) { err = "Base URL is required"; return; }
    if (provider === "aws_ec2" && !regionOrZone.trim()) { err = "Region is required (e.g. eu-central-1)"; return; }
    if (provider === "scaleway" && !regionOrZone.trim()) { err = "Zone is required (e.g. fr-par-1)"; return; }
    if (provider === "ansible" && !ansiblePath.trim()) { err = "Inventory file path is required"; return; }
    // Ansible uses a local file, no API token needed.
    if (provider !== "ansible" && !tokenCredentialId) { err = "Pick (or create) an API token credential"; return; }
    saving = true;
    try {
      const cfg = buildConfig();
      if (!isEdit) {
        await api.dynamicFolderCreate({
          parent_id: parentId,
          name: name.trim(),
          settings: {},
          provider,
          config: cfg,
          refresh_seconds: refreshSeconds,
        });
      } else {
        // Token is a credential reference now; nothing to re-confirm
        // on edit. Secret rotation goes through the Credentials view.
        await api.dynamicFolderUpdate({
          folder_id: existingFolderId!,
          provider,
          config: cfg,
          refresh_seconds: refreshSeconds,
        });
      }
      await tree.load();
      if (existingFolderId) selection.select({ kind: "folder", id: existingFolderId });
      onClose();
    } catch (e: any) {
      err = errMsg(e);
    } finally {
      saving = false;
    }
  }

  let converting = $state(false);
  async function convertToStatic() {
    if (!existingFolderId || converting) return;
    const ok = await showConfirm({
      title: "Convert dynamic folder to static?",
      message:
        "Every current host becomes a regular connection inside this folder. " +
        "The link to the provider is removed - future inventory refreshes stop, " +
        "and new hosts added at the source won't appear here.\n\n" +
        "This is irreversible. Existing pinned connections are kept untouched.",
      okLabel: "Convert",
      danger: true,
    });
    if (!ok) return;
    converting = true;
    err = null;
    try {
      const created = await api.convertDynamicFolderToStatic(existingFolderId);
      toast.ok(`Converted to static. ${created} connection${created === 1 ? "" : "s"} created.`);
      void tree.load();
      onClose();
    } catch (e: any) {
      err = errMsg(e);
      toast.err("Convert failed: " + err);
    } finally {
      converting = false;
    }
  }

  async function refreshNow() {
    if (!existingFolderId) return;
    err = null;
    // A manual refresh may need the folder's network profile tunnel. If
    // that profile is live on another machine, offer a take-over
    // (withTakeover) instead of a raw "network profile ... busy" error,
    // then retry the refresh once the tunnel is free.
    const outcome = await withTakeover(() => api.dynamicFolderRefreshNow(existingFolderId!));
    if (!outcome.ok && outcome.cancelled) return; // user declined - quiet
    if (!outcome.ok) {
      err = errMsg(outcome.error);
      return;
    }
    // Manager fires event → store reloads. Just refresh the
    // local info panel.
    const d = await api.dynamicFolderGet(existingFolderId);
    if (d) info = { lastPulled: d.last_pulled_at ?? null, lastError: d.last_error ?? "" };
  }

  function providerLabel(p: ProviderId): string {
    switch (p) {
      case "proxmox": return "Proxmox VE";
      case "hetzner": return "Hetzner Cloud";
      case "digitalocean": return "DigitalOcean";
      case "linode": return "Linode";
      case "vultr": return "Vultr";
      case "scaleway": return "Scaleway";
      case "aws_ec2": return "AWS EC2";
      case "ansible": return "Ansible inventory";
    }
  }

  function providerTokenHint(p: ProviderId): string {
    switch (p) {
      case "hetzner":
        return "Hetzner Cloud Console → Security → API tokens. Read-only is enough.";
      case "digitalocean":
        return "Cloud → API → Tokens & Keys → Personal access tokens. Read scope is enough.";
      case "linode":
        return "Cloud Manager → My profile → API tokens. Read-only Linodes scope is enough.";
      case "vultr":
        return "Account → API → Personal Access Token. Read access is enough.";
      case "scaleway":
        return "Console → Identity & Access Management → API keys. Read access on Instances.";
      case "aws_ec2":
        return "Use an IAM access key with ec2:DescribeInstances. Token id = access key, secret = secret access key.";
      default:
        return "Stored in the vault as kind api_token.";
    }
  }

  function fmtPulled(ts: number | null): string {
    if (!ts) return "never";
    const delta = Math.floor(Date.now() / 1000 - ts);
    if (delta < 60) return `${delta}s ago`;
    if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
    return `${Math.floor(delta / 3600)}h ago`;
  }
</script>

<div class="overlay" role="presentation">
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    use:clickOutside={{ onOutside: onClose }}
    onkeydown={(e) => { if (e.key === "Escape") onClose(); }}
  >
    <header>
      <h2>{isEdit ? "Edit dynamic folder" : "New dynamic folder"}</h2>
      <button class="close" onclick={onClose} title="Close (Esc)">
        <IconX size={14} />
      </button>
    </header>

    <div class="form">
      <label>
        <span class="lbl">Name</span>
        <input bind:value={name} placeholder="prod proxmox" />
      </label>

      <label>
        <span class="lbl">Provider</span>
        <select
          value={provider}
          onchange={(e) => {
            provider = (e.target as HTMLSelectElement).value as ProviderId;
            const opts = HOSTNAME_OPTIONS[provider];
            if (opts && !opts.find((o) => o.value === hostnameSource)) {
              hostnameSource = opts[0].value;
            }
          }}
        >
          <option value="ansible">Ansible inventory</option>
          <option value="aws_ec2">AWS EC2</option>
          <option value="digitalocean">DigitalOcean</option>
          <option value="hetzner">Hetzner Cloud</option>
          <option value="linode">Linode (Akamai)</option>
          <option value="proxmox">Proxmox VE</option>
          <option value="scaleway">Scaleway</option>
          <option value="vultr">Vultr</option>
        </select>
      </label>

      {#if provider === "proxmox"}
        <fieldset>
          <legend>Proxmox API</legend>
          <label>
            <span class="lbl">Base URL</span>
            <input bind:value={baseURL} placeholder="https://pve.example.com:8006" />
            <span class="hint">If your cluster sits behind a load balancer, use the LB URL - `/cluster/resources` returns the whole cluster regardless of which node answers.</span>
          </label>
          <label>
            <span class="lbl">API token credential</span>
            <div class="token-pick">
              <select bind:value={tokenCredentialId}>
                <option value="">- select a credential -</option>
                {#each apiTokenCreds as c (c.id)}
                  <option value={c.id}>{c.name}{c.config?.token_id ? ` (${c.config.token_id})` : ""}</option>
                {/each}
              </select>
              <button type="button" class="token-add" onclick={() => (newTokenOpen = !newTokenOpen)}>
                {newTokenOpen ? "Cancel" : "+ New"}
              </button>
            </div>
            <span class="hint">
              Lives in the credentials vault as kind <code>api_token</code>.
              Same token can back several dynamic folders. Rotate the
              secret through Credentials → that token; nothing here to
              re-enter on edit.
            </span>
          </label>
          {#if newTokenOpen}
            <fieldset class="inline-new">
              <legend>New API token credential</legend>
              <label>
                <span class="lbl">Name</span>
                <input bind:value={newTokenName} placeholder="proxmox prod" />
              </label>
              <label>
                <span class="lbl">Token id</span>
                <input bind:value={newTokenID} placeholder="sshtool@pve!inventory" />
              </label>
              <label>
                <span class="lbl">Token secret</span>
                <PasswordInput bind:value={newTokenSecret} autocomplete="off" />
              </label>
              {#if newTokenErr}<div class="err">{newTokenErr}</div>{/if}
              <button type="button" class="primary" disabled={newTokenSaving} onclick={createInlineToken}>
                {newTokenSaving ? "Saving…" : "Save credential"}
              </button>
            </fieldset>
          {/if}
          <label>
            <span class="lbl">VNC console login (optional)</span>
            <select bind:value={vncCredentialId}>
              <option value="">- none (guest consoles only) -</option>
              {#each passwordCreds as c (c.id)}
                <option value={c.id}>{c.name}{c.default_username ? ` (${c.default_username})` : ""}</option>
              {/each}
            </select>
            <span class="hint">
              A user+password credential (a real PVE realm login like
              <code>user@ldap</code>, NOT the API token). Needed only for
              <strong>node (host) consoles</strong> - PVE rejects API
              tokens there. Guest VM/LXC consoles work without it. The
              credential name should be the PVE username.
            </span>
          </label>
          <label class="check">
            <input type="checkbox" bind:checked={insecureSkipVerify} />
            <span>Skip TLS verification (self-signed certs)</span>
          </label>
          <label>
            <span class="lbl">Network (API access)</span>
            <select bind:value={networkProfileId}>
              <option value="">(same as the folder's Network setting)</option>
              <option value="__direct__">Direct - no tunnel</option>
              {#each networkProfiles.list as np (np.id)}
                <option value={np.id}>via {np.name} ({np.kind === "netbird" ? "NetBird" : "WireGuard"})</option>
              {/each}
            </select>
            <span class="hint">
              How the provider API itself is fetched. By default it
              follows the folder's Network setting (set it in the
              folder's detail pane), so a VPN-only Proxmox needs no
              extra config here.
            </span>
          </label>
        </fieldset>

        <fieldset>
          <legend>What to include</legend>
          <label class="check">
            <input type="checkbox" bind:checked={includeHosts} />
            <span>Hosts (PVE nodes)</span>
          </label>
          <label class="check">
            <input type="checkbox" bind:checked={includeGuests} />
            <span>Guests (VMs + LXC containers)</span>
          </label>
          <label class="check">
            <input type="checkbox" bind:checked={hideStopped} />
            <span>Hide stopped guests</span>
          </label>
          <label>
            <span class="lbl">Tag whitelist (comma-separated)</span>
            <input bind:value={tagWhitelist} placeholder="prod, staging" />
            <span class="hint">If set, only entries with at least one of these tags survive.</span>
          </label>
          <label>
            <span class="lbl">Tag blacklist (comma-separated)</span>
            <input bind:value={tagBlacklist} placeholder="deprecated" />
            <span class="hint">Always excludes entries with any of these tags.</span>
          </label>
        </fieldset>
      {:else if provider === "ansible"}
        <fieldset>
          <legend>Ansible inventory</legend>
          <label>
            <span class="lbl">Inventory file path</span>
            <div class="path-pick">
              <input bind:value={ansiblePath} placeholder="/home/me/infra/inventory/hosts.yml" />
              <button type="button" class="path-browse" onclick={async () => {
                try {
                  const p = await api.pickAnsibleInventoryFile();
                  if (p) ansiblePath = p;
                } catch (e) { console.warn("file picker failed", e); }
              }}>Browse…</button>
            </div>
            <span class="hint">Absolute path on this machine. Both INI (<code>hosts.ini</code>) and YAML (<code>hosts.yml</code>) formats are parsed; format is picked from the file extension.</span>
          </label>
          <label>
            <span class="lbl">Host pattern (optional)</span>
            <input bind:value={ansibleHostPattern} placeholder="web* or *.prod" />
            <span class="hint">Fnmatch glob. Empty = all hosts.</span>
          </label>
          <label>
            <span class="lbl">Group pattern (optional)</span>
            <input bind:value={ansibleGroupPattern} placeholder="prod_* or webservers" />
            <span class="hint">Fnmatch glob - keeps only hosts belonging to at least one matching group.</span>
          </label>
          <label>
            <span class="lbl">Display name</span>
            <select bind:value={ansibleNameFrom}>
              <option value="inventory_hostname">Inventory hostname</option>
              <option value="ansible_host">ansible_host value</option>
            </select>
          </label>
          <label>
            <span class="lbl">Jump host credential</span>
            <select bind:value={ansibleJumpCredentialId}>
              <option value="">- none (jump chain inherits target creds, usually wrong) -</option>
              {#each sshCreds as c (c.id)}
                <option value={c.id}>{c.name} ({c.kind})</option>
              {/each}
            </select>
            <span class="hint">
              Applied to every hop parsed out of
              <code>ansible_ssh_common_args</code>. Target hosts almost
              always have different credentials than the bastion, so
              without this the jump connect step will fail authentication.
            </span>
          </label>
          <span class="hint">
            Every Ansible group the host belongs to becomes a tag.
            <code>ansible_user</code>, <code>ansible_port</code>, and
            <code>ansible_host</code> become per-host overrides at
            connect time. <code>ansible_ssh_common_args</code>
            <code>ProxyJump</code> / <code>ProxyCommand=ssh -W</code>
            is parsed and applied as the jump host chain.
          </span>
        </fieldset>

        <fieldset>
          <legend>Filters</legend>
          <label>
            <span class="lbl">Tag whitelist (comma-separated)</span>
            <input bind:value={tagWhitelist} placeholder="prod, staging" />
            <span class="hint">If set, only hosts whose group tags include at least one of these survive.</span>
          </label>
          <label>
            <span class="lbl">Tag blacklist (comma-separated)</span>
            <input bind:value={tagBlacklist} placeholder="deprecated" />
            <span class="hint">Always excludes hosts whose group tags include any of these.</span>
          </label>
        </fieldset>
      {:else}
        <fieldset>
          <legend>{providerLabel(provider)} API</legend>
          <label>
            <span class="lbl">API token credential</span>
            <div class="token-pick">
              <select bind:value={tokenCredentialId}>
                <option value="">- select a credential -</option>
                {#each apiTokenCreds as c (c.id)}
                  <option value={c.id}>{c.name}{c.config?.token_id ? ` (${c.config.token_id})` : ""}</option>
                {/each}
              </select>
              <button type="button" class="token-add" onclick={() => (newTokenOpen = !newTokenOpen)}>
                {newTokenOpen ? "Cancel" : "+ New"}
              </button>
            </div>
            <span class="hint">{providerTokenHint(provider)}</span>
          </label>
          {#if newTokenOpen}
            <fieldset class="inline-new">
              <legend>New API token credential</legend>
              <label>
                <span class="lbl">Name</span>
                <input bind:value={newTokenName} placeholder={providerLabel(provider).toLowerCase() + " prod"} />
              </label>
              <label>
                <span class="lbl">
                  {provider === "aws_ec2" ? "Access key ID (required)" : "Token id (optional)"}
                </span>
                <input bind:value={newTokenID} placeholder={provider === "aws_ec2" ? "AKIA…" : ""} />
              </label>
              <label>
                <span class="lbl">
                  {provider === "aws_ec2" ? "Secret access key" : "Token secret"}
                </span>
                <PasswordInput bind:value={newTokenSecret} autocomplete="off" />
              </label>
              {#if newTokenErr}<div class="err">{newTokenErr}</div>{/if}
              <button type="button" class="primary" disabled={newTokenSaving} onclick={createInlineToken}>
                {newTokenSaving ? "Saving…" : "Save credential"}
              </button>
            </fieldset>
          {/if}
        </fieldset>

        <fieldset>
          <legend>Network (API access)</legend>
          <label>
            <span class="lbl">Fetch the provider API through</span>
            <select bind:value={networkProfileId}>
              <option value="">(same as the folder's Network setting)</option>
              <option value="__direct__">Direct - no tunnel</option>
              {#each networkProfiles.list as np (np.id)}
                <option value={np.id}>via {np.name} ({np.kind === "netbird" ? "NetBird" : "WireGuard"})</option>
              {/each}
            </select>
            <span class="hint">
              By default the fetch follows the folder's Network setting
              (folder detail pane); pick Direct to keep a public API
              off an inherited VPN.
            </span>
          </label>
        </fieldset>

        {#if provider === "aws_ec2" || provider === "scaleway"}
          <fieldset>
            <legend>{provider === "aws_ec2" ? "Region" : "Zone"}</legend>
            <label>
              <span class="lbl">{provider === "aws_ec2" ? "AWS region" : "Scaleway zone"}</span>
              <input
                bind:value={regionOrZone}
                placeholder={provider === "aws_ec2" ? "eu-central-1" : "fr-par-1"}
              />
              <span class="hint">
                One folder per {provider === "aws_ec2" ? "region" : "zone"} - the API
                scopes listings to a single one.
              </span>
            </label>
          </fieldset>
        {/if}

        <fieldset>
          <legend>Hostname source</legend>
          <p class="hint" style="margin: 0;">
            Pick what the SSH layer should connect to for each entry.
          </p>
          {#each HOSTNAME_OPTIONS[provider] ?? [] as opt (opt.value)}
            <label class="check">
              <input type="radio" name="host-src" value={opt.value} bind:group={hostnameSource} />
              <span>{opt.label}</span>
            </label>
          {/each}
        </fieldset>

        <fieldset>
          <legend>Filter</legend>
          <label class="check">
            <input type="checkbox" bind:checked={hideStopped} />
            <span>Hide stopped instances</span>
          </label>
          <label>
            <span class="lbl">Tag whitelist (comma-separated)</span>
            <input bind:value={tagWhitelist} placeholder="env=prod, role=web" />
            <span class="hint">
              Tags arrive as plain strings; key=value providers
              (Hetzner labels, AWS EC2 tags) match the formatted form.
            </span>
          </label>
          <label>
            <span class="lbl">Tag blacklist (comma-separated)</span>
            <input bind:value={tagBlacklist} placeholder="env=dev" />
          </label>
        </fieldset>
      {/if}

      <fieldset>
        <legend>Refresh</legend>
        <label>
          <span class="lbl">Auto-refresh interval (seconds)</span>
          <input type="number" min="0" step="30" bind:value={refreshSeconds} />
          <span class="hint">0 disables the timer. Manual refresh still works.</span>
        </label>
        {#if info}
          <div class="info-row">
            <span>Last refreshed: <strong>{fmtPulled(info.lastPulled)}</strong></span>
            <button onclick={refreshNow} type="button">Refresh now</button>
          </div>
          {#if info.lastError}<div class="err">Last error: {info.lastError}</div>{/if}
        {/if}
      </fieldset>

      {#if err}<div class="err">{err}</div>{/if}

      <div class="actions">
        {#if isEdit}
          <button
            onclick={convertToStatic}
            disabled={converting}
            type="button"
            class="convert-btn"
            title="Snapshot every host into a regular connection and drop the provider link. Irreversible."
          >
            {converting ? "Converting…" : "Convert to static…"}
          </button>
        {/if}
        <button onclick={onClose} type="button">Cancel</button>
        <button class="primary" disabled={saving} onclick={save} type="button">
          {saving ? "Saving…" : isEdit ? "Save" : "Create"}
        </button>
      </div>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(17, 17, 27, 0.6);
    z-index: 1000;
    display: flex; align-items: center; justify-content: center;
  }
  .modal {
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    width: min(680px, 92vw);
    max-height: 90vh;
    overflow-y: auto;
    padding: 1rem 1.2rem;
    box-shadow: 0 8px 30px rgba(0,0,0,0.5);
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.8rem;
  }
  header h2 { margin: 0; font-size: 1rem; }
  .close { background: transparent; border: 0; color: var(--subtext0); cursor: pointer; padding: 0.15rem 0.35rem; border-radius: 3px; }
  .close:hover { background: var(--surface0); color: var(--text); }

  .form { display: flex; flex-direction: column; gap: 0.7rem; }
  .form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; }
  .form label .lbl { color: var(--subtext0); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .form label input[type="number"],
  .form label input:not([type]),
  .form label select {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.35rem 0.5rem;
    font: inherit;
    font-size: 0.85rem;
  }
  .form label.check { flex-direction: row; align-items: center; gap: 0.45rem; }
  .form .hint { color: var(--overlay0); font-size: 0.72rem; line-height: 1.4; }
  fieldset {
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.6rem 0.8rem;
    margin: 0;
    display: flex; flex-direction: column; gap: 0.5rem;
  }
  fieldset legend { color: var(--subtext0); font-size: 0.72rem; text-transform: uppercase; padding: 0 0.3rem; }

  .info-row { display: flex; align-items: center; justify-content: space-between; font-size: 0.8rem; }
  .info-row button {
    background: var(--surface0); color: var(--text);
    border: 0; border-radius: 3px;
    padding: 0.25rem 0.55rem;
    font: inherit; font-size: 0.78rem;
    cursor: pointer;
  }

  .err {
    background: color-mix(in oklab, var(--red) 12%, var(--bg-panel));
    border-left: 3px solid var(--red);
    color: var(--red);
    padding: 0.5rem 0.7rem;
    border-radius: 0 3px 3px 0;
    font-size: 0.8rem;
  }

  .actions { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 0.4rem; }
  .actions button {
    background: var(--surface0); color: var(--text);
    border: 0; border-radius: 3px;
    padding: 0.4rem 0.8rem;
    font: inherit; font-size: 0.85rem;
    cursor: pointer;
  }
  .actions button:hover { background: var(--surface1); }
  .actions button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  .actions button.primary:hover { background: var(--lavender); }
  .actions .convert-btn {
    margin-right: auto;
    background: var(--surface1);
    color: var(--text);
    border: 1px solid var(--overlay0);
  }
  .actions .convert-btn:hover {
    background: var(--maroon);
    color: var(--on-accent);
    border-color: var(--maroon);
  }

  .token-pick { display: flex; gap: 0.4rem; align-items: center; }
  .token-pick select { flex: 1; }
  .path-pick { display: flex; gap: 0.4rem; align-items: center; }
  .path-pick input { flex: 1; }
  .path-browse {
    padding: 0.3rem 0.7rem;
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    cursor: pointer;
    font: inherit;
    font-size: 0.8rem;
    color: var(--text);
  }
  .path-browse:hover { background: var(--surface1); }
  .token-add {
    background: var(--surface0); color: var(--text);
    border: 0; border-radius: 3px;
    padding: 0.35rem 0.65rem;
    font: inherit; font-size: 0.78rem;
    cursor: pointer;
    white-space: nowrap;
  }
  .token-add:hover { background: var(--surface1); }
  .inline-new {
    background: var(--crust);
  }
  .inline-new .primary {
    background: var(--blue); color: var(--on-accent); font-weight: 600;
    border: 0; border-radius: 3px;
    padding: 0.4rem 0.8rem;
    cursor: pointer;
    font: inherit; font-size: 0.85rem;
    align-self: flex-start;
  }
  .inline-new .primary:hover { background: var(--lavender); }
</style>
