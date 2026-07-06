<script lang="ts">
  import { tree, credentials, selection, sessions, paneTabs, view } from "./stores.svelte";
  import { connectionActions, withTakeover } from "./connectionActions.svelte";
  import { api } from "./api";
  import { IconGlobe, dynamicEntryIcon } from "./iconMap";
  import { explain as explainConnectError, unwrapRaw as unwrapConnectErr } from "./connectErrors";
  import { toast } from "./toast.svelte";
  import { copyText } from "./clipboard";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import PasswordInput from "./PasswordInput.svelte";

  interface Props {
    folderId: string;
    entryId: string;
  }
  let { folderId, entryId }: Props = $props();

  // Pull the last connect-error for this dynamic entry out of the
  // shared connectionActions store. Same source the tree's double-
  // click path writes into, so the panel reflects failures from
  // both the in-pane Connect button AND tree double-click.
  const synthConnId = $derived("dyn:" + entryId);
  const connectErr = $derived(
    connectionActions.lastConnectError[synthConnId]?.message ?? null,
  );

  // Per-attempt override knobs - see DetailPane.svelte for the
  // same UX on regular connections.
  let overrideCredId = $state<string>("");
  let overrideUsername = $state<string>("");
  let overridePassword = $state<string>("");
  let overrideJumpHost = $state<string>("");
  let overrideJumpCredId = $state<string>("");
  let showOverride = $state(false);
  // SshConnect blocks until auth+PTY succeed (can be seconds, longer with an
  // opkssh browser flow). Without a busy state the Connect button looks idle
  // and the user taps again - especially easy to do on mobile where there's
  // no other feedback. Disable + relabel while a connect is in flight.
  let connecting = $state(false);

  // SSH-capable credentials only (password/key/agent/opkssh) for
  // the jump host picker.
  const sshCreds = $derived(
    credentials.list.filter(
      (c) => c.kind === "password" || c.kind === "key" || c.kind === "agent" || c.kind === "opkssh",
    ),
  );

  const folder = $derived.by(() => {
    void tree.version;
    return tree.folderById(folderId);
  });
  const meta = $derived.by(() => {
    void tree.version;
    return tree.dynamicFolders[folderId] ?? null;
  });
  const entry = $derived.by(() => {
    void tree.version;
    const list = tree.dynamicEntries[folderId] ?? [];
    return list.find((e) => e.id === entryId) ?? null;
  });
  const provider = $derived(meta?.provider ?? "");

  // ----- provider-specific decoders -----
  // Proxmox raw is the cluster/resources row (type, node, vmid, tags,
  // maxcpu, maxmem, maxdisk, cpu/mem/disk current, uptime).
  type Facts = Array<{ label: string; value: string; mono?: boolean }>;

  const facts = $derived.by<Facts>(() => {
    if (!entry) return [];
    const out: Facts = [];
    out.push({ label: "Name", value: entry.name });
    out.push({ label: "Hostname", value: entry.hostname, mono: true });
    // Skip Status for providers that don't have one (Ansible static
    // inventory has no running/stopped notion); cloud providers fill
    // it with "running" / "stopped" and benefit from showing it.
    if (entry.status) {
      out.push({ label: "Status", value: entry.status });
    }
    out.push({ label: "Kind", value: humanKind(entry.kind) });
    const r = (entry.raw ?? {}) as any;
    if (provider === "proxmox") {
      if (r.type) out.push({ label: "Resource type", value: r.type, mono: true });
      if (r.node) out.push({ label: "Hosting node", value: r.node, mono: true });
      if (r.vmid) out.push({ label: "VMID", value: String(r.vmid), mono: true });
      if (r.maxcpu) out.push({ label: "vCPUs", value: String(r.maxcpu) });
      if (r.maxmem) out.push({ label: "Memory", value: humanBytes(r.maxmem) });
      if (r.maxdisk) out.push({ label: "Disk", value: humanBytes(r.maxdisk) });
      if (r.uptime) out.push({ label: "Uptime", value: humanDuration(r.uptime) });
    } else if (provider === "hetzner") {
      if (r.id) out.push({ label: "Server ID", value: String(r.id), mono: true });
      if (r?.server_type?.name) out.push({ label: "Server type", value: r.server_type.name, mono: true });
      if (r?.datacenter?.name) out.push({ label: "Datacenter", value: r.datacenter.name });
      if (r?.image?.name) out.push({ label: "Image", value: r.image.name, mono: true });
      if (r?.public_net?.ipv4?.ip) out.push({ label: "Public IPv4", value: r.public_net.ipv4.ip, mono: true });
      if (r?.public_net?.ipv6?.ip) out.push({ label: "Public IPv6", value: r.public_net.ipv6.ip, mono: true });
      if (Array.isArray(r.private_net) && r.private_net.length > 0 && r.private_net[0].ip) {
        out.push({ label: "Private IPv4", value: r.private_net[0].ip, mono: true });
      }
      if (r.created) out.push({ label: "Created", value: r.created });
    } else if (provider === "digitalocean") {
      if (r.id) out.push({ label: "Droplet ID", value: String(r.id), mono: true });
      const v4 = (r.networks?.v4 ?? []) as Array<{ ip_address?: string; type?: string }>;
      const pub = v4.find((n) => n.type === "public")?.ip_address;
      const prv = v4.find((n) => n.type === "private")?.ip_address;
      if (pub) out.push({ label: "Public IPv4", value: pub, mono: true });
      if (prv) out.push({ label: "Private IPv4", value: prv, mono: true });
    } else if (provider === "linode") {
      if (r.id) out.push({ label: "Linode ID", value: String(r.id), mono: true });
      if (r.region) out.push({ label: "Region", value: r.region, mono: true });
      if (Array.isArray(r.ipv4)) {
        const pub = (r.ipv4 as string[]).find((ip) => !ip.startsWith("10.") && !ip.startsWith("192.168.") && !ip.startsWith("172."));
        const prv = (r.ipv4 as string[]).find((ip) => ip.startsWith("10.") || ip.startsWith("192.168.") || ip.startsWith("172."));
        if (pub) out.push({ label: "Public IPv4", value: pub, mono: true });
        if (prv) out.push({ label: "Private IPv4", value: prv, mono: true });
      }
    } else if (provider === "vultr") {
      if (r.id) out.push({ label: "Instance ID", value: String(r.id), mono: true });
      if (r.region) out.push({ label: "Region", value: r.region, mono: true });
      if (r.main_ip && r.main_ip !== "0.0.0.0") out.push({ label: "Main IPv4", value: r.main_ip, mono: true });
      if (r.internal_ip) out.push({ label: "Internal IPv4", value: r.internal_ip, mono: true });
    } else if (provider === "scaleway") {
      if (r.id) out.push({ label: "Server ID", value: String(r.id), mono: true });
      if (r?.public_ip?.address) out.push({ label: "Public IPv4", value: r.public_ip.address, mono: true });
      if (r.private_ip) out.push({ label: "Private IPv4", value: String(r.private_ip), mono: true });
    } else if (provider === "aws_ec2") {
      if (r.InstanceID) out.push({ label: "Instance ID", value: r.InstanceID, mono: true });
      if (r.PublicIP) out.push({ label: "Public IPv4", value: r.PublicIP, mono: true });
      if (r.PrivateIP) out.push({ label: "Private IPv4", value: r.PrivateIP, mono: true });
      if (r.PublicDNS) out.push({ label: "Public DNS", value: r.PublicDNS, mono: true });
    } else if (provider === "ansible") {
      // Ansible payload is { name, groups, vars }. Surface the
      // vars that matter for connect (user/port/host already
      // shown above as Hostname; show the override + any jump
      // chain). The full vars map is in the Raw payload below.
      const vars = (r.vars ?? {}) as Record<string, string>;
      if (vars.ansible_user) out.push({ label: "Ansible user", value: vars.ansible_user, mono: true });
      if (vars.ansible_port) out.push({ label: "Ansible port", value: vars.ansible_port, mono: true });
      const jumpArgs = vars.ansible_ssh_common_args || vars.ansible_ssh_extra_args || "";
      if (jumpArgs) {
        const hops = parseAnsibleJumpHops(jumpArgs);
        if (hops.length > 0) {
          out.push({ label: "Jump via", value: hops.join(" → "), mono: true });
        } else {
          out.push({ label: "SSH args", value: jumpArgs, mono: true });
        }
      }
    }
    return out;
  });

  // Lightweight mirror of the Go-side AnsibleParseJumpHosts. Used
  // purely for display - connect still re-parses on the backend.
  // Recognises ProxyJump=h1,h2 and ProxyCommand=ssh ... -W %h:%p HOST.
  function parseAnsibleJumpHops(args: string): string[] {
    // -J shorthand for -o ProxyJump=. Matches the Go-side
    // AnsibleParseJumpHosts.
    const dj = /(?:^|\s)-J\s+([^\s"']+)/.exec(args);
    if (dj) {
      return dj[1].split(",").map((s) => s.trim()).filter(Boolean);
    }
    const pj = /\bProxyJump\s*=\s*([^\s"']+)/i.exec(args);
    if (pj) {
      return pj[1].split(",").map((s) => s.trim()).filter(Boolean);
    }
    const pc = /\bProxyCommand\s*=\s*(.+?)(?:"|'|$)/i.exec(args);
    if (!pc) return [];
    const cmd = pc[1].trim();
    const tokens = cmd.split(/\s+/);
    if (tokens.length === 0 || !tokens[0].endsWith("ssh")) return [];
    let host = "";
    let hasW = false;
    const takesArg = new Set(["-W","-o","-i","-p","-l","-F","-L","-R","-D","-J","-B","-b","-c","-E","-e","-I","-m","-Q","-S","-w"]);
    for (let i = 1; i < tokens.length; i++) {
      const t = tokens[i];
      if (t === "-W") { hasW = true; i++; continue; }
      if (t.startsWith("-") && takesArg.has(t)) { i++; continue; }
      if (t.startsWith("-")) continue;
      if (!host) host = t;
    }
    return hasW && host ? [host] : [];
  }

  // Current load percentages (Proxmox only - Hetzner doesn't expose
  // live usage in /servers).
  const usage = $derived.by(() => {
    if (provider !== "proxmox") return null;
    const r = (entry?.raw ?? {}) as any;
    const cpu = (typeof r.cpu === "number" && typeof r.maxcpu === "number" && r.maxcpu > 0)
      ? r.cpu * 100 : null;
    const mem = (typeof r.mem === "number" && typeof r.maxmem === "number" && r.maxmem > 0)
      ? (r.mem / r.maxmem) * 100 : null;
    const disk = (typeof r.disk === "number" && typeof r.maxdisk === "number" && r.maxdisk > 0)
      ? (r.disk / r.maxdisk) * 100 : null;
    if (cpu === null && mem === null && disk === null) return null;
    return { cpu, mem, disk };
  });

  let rawOpen = $state(false);
  const rawJSON = $derived(entry?.raw ? JSON.stringify(entry.raw, null, 2) : "");

  function humanKind(k: string): string {
    if (k === "guest_vm") return "VM";
    if (k === "guest_lxc") return "LXC container";
    if (k === "host") return "Host / node";
    if (k === "server") return "Host";
    return k;
  }
  function humanBytes(n: number): string {
    if (!Number.isFinite(n) || n <= 0) return "-";
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    let u = 0;
    while (n >= 1024 && u < units.length - 1) { n /= 1024; u++; }
    return `${n.toFixed(n >= 10 || u === 0 ? 0 : 1)} ${units[u]}`;
  }
  function humanDuration(s: number): string {
    if (!Number.isFinite(s) || s <= 0) return "-";
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }
  function pct(n: number | null): string {
    return n === null ? "-" : `${n.toFixed(n < 10 ? 1 : 0)}%`;
  }
  function pctClass(n: number | null): string {
    if (n === null) return "";
    if (n >= 85) return "high";
    if (n >= 60) return "med";
    return "low";
  }

  async function connect() {
    if (!entry || connecting) return;
    if (entry.status === "stopped") {
      const ok = await showConfirm({
        title: "Host is stopped",
        message:
          `${entry.name} is stopped in the provider.\n\nConnect anyway? ` +
          `(useful if the VM is reachable on another address or the status is stale.)`,
        okLabel: "Connect",
      });
      if (!ok) return;
    }
    connectionActions.clearConnectError(synthConnId);
    const cid = overrideCredId;
    const ouser = overrideUsername;
    const opass = overridePassword;
    const ojumpHost = overrideJumpHost.trim();
    const ojumpCred = overrideJumpCredId;
    overrideCredId = "";
    overrideUsername = "";
    overridePassword = "";
    overrideJumpHost = "";
    overrideJumpCredId = "";
    const hasJump = ojumpHost || ojumpCred;
    const hasAdvanced = cid || ouser || opass;
    connecting = true;
    try {
      // Dynamic hosts route their first hop through the folder's network
      // profile; withTakeover surfaces the take-over dialog if that
      // profile is live on another machine instead of a raw failure.
      const outcome = await withTakeover(() => hasJump
        ? api.sshConnectDynamicWithJumpOverride(folderId, entry.id, cid, ouser, opass, ojumpHost, ojumpCred)
        : hasAdvanced
          ? api.sshConnectDynamicAdvanced(folderId, entry.id, cid, ouser, opass)
          : api.sshConnectDynamic(folderId, entry.id));
      if (!outcome.ok && outcome.cancelled) return; // user declined - quiet
      if (!outcome.ok) {
        connectionActions.recordConnectError(synthConnId, outcome.error);
        return;
      }
      const res = outcome.value;
      sessions.add({
        sessionId: res.session_id,
        connectionId: "dyn:" + entry.id,
        name: entry.name,
        hostname: entry.hostname,
        status: "connected",
      });
      paneTabs.addTab(res.session_id, entry.name);
      view.setTab("terminal");
    } finally {
      connecting = false;
    }
  }

  async function copyHost() {
    if (!entry) return;
    try { await copyText(entry.hostname, { label: "Hostname" }); } catch {}
  }

  async function refreshFolder() {
    try {
      await api.dynamicFolderRefreshNow(folderId);
    } catch (e) {
      console.warn("dyn refresh failed", e);
    }
  }

  let pinning = $state(false);
  async function pinAsConnection() {
    if (!entry || pinning) return;
    const defaultName = entry.name;
    const proposed = await showPrompt(
      "Pin this dynamic host as a permanent connection. Name for the new connection:",
      defaultName,
    );
    if (proposed == null) return;
    const name = proposed.trim() || defaultName;
    pinning = true;
    try {
      const conn = await api.pinDynamicEntry({
        folder_id: folderId,
        entry_id: entry.id,
        target_folder_id: folderId,
        name,
        override_credential_id: overrideCredId || "",
        tags: entry.tags ?? [],
      });
      toast.ok(`Pinned "${name}". Edit it like any other connection.`);
      void tree.load();
      // Select the new connection so the user lands on it.
      if (conn && (conn as any).id) {
        selection.select({ kind: "connection", id: (conn as any).id });
      } else {
        selection.select({ kind: "none" });
      }
    } catch (e: any) {
      toast.err("Pin failed: " + (e?.message || e));
    } finally {
      pinning = false;
    }
  }
</script>

{#if !entry}
  <div class="empty">
    <p>Entry not found. It may have been removed in the latest refresh.</p>
    <button onclick={() => selection.select({ kind: "none" })}>Clear</button>
  </div>
{:else}
  {@const HeadIcon = dynamicEntryIcon(entry.kind)}
  <header class="head">
    <h1>
      <span class="ico"><HeadIcon size={18} /></span>
      {entry.name}
      {#if entry.status}
        <span class="status status-{entry.status}">{entry.status}</span>
      {/if}
    </h1>
    <div class="head-actions">
      <button class="primary" onclick={connect} disabled={connecting}>
        {connecting ? "Connecting…" : overrideCredId ? "Connect (override)" : "Connect"}
      </button>
      {#if provider === "proxmox" && (entry.kind === "guest_vm" || entry.kind === "guest_lxc" || entry.kind === "host")}
        <button
          title={entry.kind === "host"
            ? "Open the Proxmox node shell (needs a VNC console login set on the dynamic folder)"
            : "Open the Proxmox noVNC console for this guest in a new tab"}
          onclick={() => connectionActions.openVncProxmox(folderId, entry.id)}
        >
          Open console
        </button>
      {/if}
      <button
        class="ghost"
        title="Use a different credential just for the next connect attempt"
        onclick={() => (showOverride = !showOverride)}
      >
        {showOverride ? "✕" : "Use different credential…"}
      </button>
      <button onclick={copyHost} title="Copy hostname">Copy host</button>
      <button onclick={refreshFolder} title="Refresh inventory">Refresh</button>
      <button
        onclick={pinAsConnection}
        disabled={pinning}
        title="Promote this host to a permanent connection. Future inventory refreshes will skip its external ID."
      >
        {pinning ? "Pinning…" : "Pin as connection…"}
      </button>
    </div>
  </header>

  {#if showOverride}
    <div class="cred-override">
      <label class="cred-row">
        <span>Credential</span>
        <select bind:value={overrideCredId}>
          <option value="">(use the folder's inherited credential)</option>
          {#each credentials.list as c (c.id)}
            <option value={c.id}>{c.name} ({c.kind})</option>
          {/each}
        </select>
      </label>
      <label class="cred-row">
        <span>Username</span>
        <input
          type="text"
          placeholder="(leave blank to inherit)"
          bind:value={overrideUsername}
          autocomplete="off"
        />
      </label>
      <label class="cred-row">
        <span>Password</span>
        <PasswordInput
          placeholder="(leave blank to use the credential)"
          bind:value={overridePassword}
          autocomplete="off"
        />
      </label>
      <label class="cred-row">
        <span>Jump host</span>
        <input
          type="text"
          placeholder="(leave blank to use parsed ansible_ssh_*_args)"
          bind:value={overrideJumpHost}
          autocomplete="off"
        />
      </label>
      <label class="cred-row">
        <span>Jump credential</span>
        <select bind:value={overrideJumpCredId}>
          <option value="">(use the folder's jump credential)</option>
          {#each sshCreds as c (c.id)}
            <option value={c.id}>{c.name} ({c.kind})</option>
          {/each}
        </select>
      </label>
      <p class="hint inline">
        All fields are independent and reset after the next
        Connect press - nothing is saved. Jump host accepts
        <code>[user@]host[:port]</code> and replaces the entire
        chain parsed from Ansible vars.
      </p>
    </div>
  {/if}

  {#if connectErr}
    {@const friendly = explainConnectError(connectErr)}
    {@const raw = unwrapConnectErr(connectErr)}
    <div class="err connect-err">
      <div class="err-summary">⚠ {friendly.summary}</div>
      {#if friendly.hint}<div class="err-hint">{friendly.hint}</div>{/if}
      {#if friendly.summary !== raw}
        <details class="err-raw">
          <summary>Show raw error</summary>
          <pre>{raw}</pre>
        </details>
      {/if}
      <button class="err-clear" onclick={() => connectionActions.clearConnectError(synthConnId)}>Clear</button>
    </div>
  {/if}

  <p class="sub">
    <span class="dyn-pill"><IconGlobe size={11} /> {provider || "dynamic"}</span>
    {#if folder}
      <span class="muted">from <code class="mono">{folder.name}</code></span>
    {/if}
    <span class="muted">·</span>
    <span class="muted small">read-only - backed by the provider, not the local DB.</span>
  </p>

  <section class="facts-grid">
    {#each facts as f}
      <div class="fact">
        <span class="fact-label">{f.label}</span>
        <span class="fact-value" class:mono={f.mono}>{f.value}</span>
      </div>
    {/each}
  </section>

  {#if entry.tags.length > 0}
    <section class="tags">
      <h3>{provider === "hetzner" ? "Labels" : "Tags"}</h3>
      <div class="tag-row">
        {#each entry.tags as t}
          <span class="tag">{t}</span>
        {/each}
      </div>
    </section>
  {/if}

  {#if usage}
    <section class="usage">
      <h3>Current load</h3>
      <div class="bars">
        <div class="bar-row">
          <span class="bar-label">CPU</span>
          <div class="bar"><div class="bar-fill {pctClass(usage.cpu)}" style="width: {Math.min(100, usage.cpu ?? 0)}%"></div></div>
          <span class="bar-val mono">{pct(usage.cpu)}</span>
        </div>
        <div class="bar-row">
          <span class="bar-label">Memory</span>
          <div class="bar"><div class="bar-fill {pctClass(usage.mem)}" style="width: {Math.min(100, usage.mem ?? 0)}%"></div></div>
          <span class="bar-val mono">{pct(usage.mem)}</span>
        </div>
        <div class="bar-row">
          <span class="bar-label">Disk</span>
          <div class="bar"><div class="bar-fill {pctClass(usage.disk)}" style="width: {Math.min(100, usage.disk ?? 0)}%"></div></div>
          <span class="bar-val mono">{pct(usage.disk)}</span>
        </div>
      </div>
    </section>
  {/if}

  {#if rawJSON}
    <section class="raw">
      <button class="raw-toggle" onclick={() => rawOpen = !rawOpen}>
        {rawOpen ? "▾" : "▸"} Raw provider payload
      </button>
      {#if rawOpen}
        <pre class="raw-pre mono">{rawJSON}</pre>
      {/if}
    </section>
  {/if}
{/if}

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; margin-bottom: 0.25rem; }
  .head h1 { margin: 0; font-size: 1.35rem; display: flex; align-items: center; gap: 0.55rem; flex-wrap: wrap; }
  .head .ico { color: var(--teal); display: inline-flex; }
  .head-actions { display: flex; gap: 0.5rem; flex-wrap: wrap; }
  .head-actions button { background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 3px; padding: 0.35rem 0.8rem; cursor: pointer; }
  .head-actions button:hover { background: var(--surface1); }
  .head-actions button.primary { background: var(--blue); color: var(--on-accent); border-color: var(--blue); font-weight: 600; }
  .head-actions button.primary:hover { filter: brightness(1.08); }

  .sub { color: var(--subtext0); font-size: 0.85rem; margin: 0 0 1.25rem; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
  .sub .muted { color: var(--overlay0); }
  .sub .small { font-size: 0.75rem; }
  .sub code { background: var(--surface0); padding: 0.05rem 0.4rem; border-radius: 2px; color: var(--text); }
  .dyn-pill {
    color: var(--teal);
    background: color-mix(in oklab, var(--teal) 16%, var(--bg-panel));
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 0.1rem 0.45rem;
    border-radius: 2px;
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }

  .status { font-size: 0.7rem; padding: 0.1rem 0.5rem; border-radius: 2px; text-transform: uppercase; letter-spacing: 0.05em; }
  .status-running { color: var(--green); background: color-mix(in oklab, var(--green) 12%, var(--bg-panel)); }
  .status-stopped { color: var(--yellow); background: color-mix(in oklab, var(--yellow) 14%, var(--bg-panel)); }

  .facts-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 0.5rem;
    margin-bottom: 1.5rem;
  }
  .fact {
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.5rem 0.65rem;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .fact-label { color: var(--overlay1); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .fact-value { color: var(--text); font-size: 0.9rem; word-break: break-all; }
  .fact-value.mono { font-family: ui-monospace, monospace; font-size: 0.85rem; }

  .tags { margin-bottom: 1.5rem; }
  .tags h3 { margin: 0 0 0.5rem; font-size: 0.85rem; text-transform: uppercase; color: var(--subtext0); letter-spacing: 0.05em; }
  .tag-row { display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .tag {
    background: var(--surface0);
    color: var(--text);
    padding: 0.15rem 0.55rem;
    border-radius: 2px;
    font-size: 0.78rem;
    font-family: ui-monospace, monospace;
  }

  .usage { margin-bottom: 1.5rem; }
  .usage h3 { margin: 0 0 0.6rem; font-size: 0.85rem; text-transform: uppercase; color: var(--subtext0); letter-spacing: 0.05em; }
  .bars { display: flex; flex-direction: column; gap: 0.4rem; }
  .bar-row { display: grid; grid-template-columns: 60px 1fr 60px; align-items: center; gap: 0.65rem; }
  .bar-label { color: var(--subtext0); font-size: 0.8rem; }
  .bar { background: var(--surface0); height: 6px; border-radius: 3px; overflow: hidden; }
  .bar-fill { height: 100%; background: var(--blue); transition: width 0.3s ease-out; }
  .bar-fill.low { background: var(--green); }
  .bar-fill.med { background: var(--yellow); }
  .bar-fill.high { background: var(--red); }
  .bar-val { color: var(--text); font-size: 0.78rem; text-align: right; }

  .raw { margin-top: 1.5rem; }
  .raw-toggle {
    background: transparent;
    border: none;
    color: var(--subtext0);
    font-size: 0.85rem;
    cursor: pointer;
    padding: 0.25rem 0;
  }
  .raw-toggle:hover { color: var(--text); }
  .raw-pre {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.75rem;
    font-size: 0.78rem;
    color: var(--text);
    max-height: 320px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }
  .mono { font-family: ui-monospace, monospace; }
  .empty { color: var(--subtext0); padding: 2rem 0; }

  .ghost {
    background: transparent;
    border: 1px solid var(--surface1);
    color: var(--subtext0);
  }
  .ghost:hover { background: var(--surface0); color: var(--text); }
  .cred-override {
    margin-bottom: 0.75rem;
    padding: 0.5rem 0.75rem;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    font-size: 0.85rem;
  }
  .cred-override label {
    display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap;
  }
  .cred-row { margin: 0.25rem 0; }
  .cred-row > span:first-child {
    display: inline-block;
    min-width: 80px;
    color: var(--subtext0);
    font-size: 0.78rem;
  }
  .cred-override select,
  .cred-override input {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.25rem 0.4rem;
    font: inherit; font-size: 0.82rem;
    min-width: 220px;
  }
  .hint.inline { display: inline; padding: 0; margin: 0.3rem 0 0; font-size: 0.78rem; color: var(--subtext0); }

  /* Connect-error styles mirror DetailPane.svelte's so the failure
     panel looks identical whether the user clicked Connect on a
     regular connection or a dynamic-inventory entry. */
  .err {
    color: var(--red); background: var(--crust);
    padding: 0.5rem 0.75rem; border-radius: 4px;
    border-left: 3px solid var(--red);
    margin-bottom: 0.75rem;
  }
  .connect-err {
    font-size: 0.85rem; word-break: break-word;
    position: relative;
  }
  .err-summary { font-weight: 600; }
  .err-hint {
    font-weight: 400; color: var(--pink);
    margin-top: 0.2rem; font-size: 0.8rem;
  }
  .err-raw { margin-top: 0.35rem; font-size: 0.75rem; }
  .err-raw summary {
    cursor: pointer; color: var(--subtext0); font-weight: 400; user-select: none;
  }
  .err-raw summary:hover { color: var(--text); }
  .err-raw pre {
    margin: 0.3rem 0 0;
    padding: 0.4rem 0.55rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    color: var(--red);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .err-clear {
    position: absolute; top: 0.4rem; right: 0.5rem;
    background: transparent; border: 1px solid var(--surface1);
    color: var(--subtext0); padding: 0.1rem 0.5rem;
    border-radius: 3px; cursor: pointer; font: inherit;
    font-size: 0.72rem;
  }
  .err-clear:hover { background: var(--surface0); color: var(--text); }
</style>
