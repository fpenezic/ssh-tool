<script lang="ts">
  import { onMount } from "svelte";
  import {
    api,
    type PortForward,
    type ForwardStatus,
    type Connection,
    type ProxyBookmark,
  } from "./api";
  import { sessions, tree, paneTabs } from "./stores.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte";
  import { copyText } from "./clipboard";
  import { IconGlobe } from "./iconMap";
  import { errMsg } from "./connectErrors";

  interface Props {
    connection: Connection;
  }
  let { connection }: Props = $props();

  let specs = $state<PortForward[]>([]);
  let active = $state<ForwardStatus[]>([]);
  let err = $state<string | null>(null);

  // The active SSH session for this connection (if any). Used to start
  // forwards on demand. If multiple sessions exist for the same connection
  // we use the most recently opened one.
  const activeSession = $derived(
    sessions.tabs
      .filter((t) => t.connectionId === connection.id && t.status === "connected")
      .at(-1)
  );

  async function reload() {
    err = null;
    try {
      specs = (await api.forwardsList(connection.id)) ?? [];
      if (activeSession) {
        active = (await api.forwardsActive(activeSession.sessionId)) ?? [];
      } else {
        active = [];
      }
    } catch (e) {
      err = errMsg(e);
    }
  }

  onMount(() => {
    reload();
    // Light polling of live status while this view is mounted. 2s feels
    // responsive without spamming the backend.
    const t = setInterval(async () => {
      if (activeSession) {
        try {
          active = (await api.forwardsActive(activeSession.sessionId)) ?? [];
        } catch {}
      }
    }, 2000);
    return () => clearInterval(t);
  });

  $effect(() => {
    void connection.id;
    void activeSession?.sessionId;
    reload();
  });

  // ---------- create form ----------

  let showAdd = $state(false);
  let nKind = $state<"local" | "remote" | "dynamic">("local");
  // Bind address: for local/dynamic this is the LOCAL listen address; for
  // remote it's the address bound on the REMOTE host. 127.0.0.1 (loopback)
  // is the safe default for every kind - the user can widen it (e.g. to the
  // host's LAN IP, or 0.0.0.0) when they want the listener reachable from
  // other machines. nLocalAddrTouched tracks whether the user edited it, so
  // switching kind can reset the default without clobbering a custom value.
  let nLocalAddr = $state("127.0.0.1");
  let nLocalAddrTouched = $state(false);
  let nLocalPort = $state<number | undefined>(undefined);
  // Target host (the RemoteHost field). Semantics flip by kind:
  //   local  -L: the host to reach FROM the server (e.g. db.internal, or
  //              127.0.0.1 for a service on the server itself).
  //   remote -R: the host to reach ON THIS machine (e.g. 127.0.0.1 for a
  //              local squid/proxy). Confusingly stored in remote_host.
  // 127.0.0.1 is the most common default for both; the user can change it.
  let nRemoteHost = $state("127.0.0.1");
  let nRemoteHostTouched = $state(false);
  let nRemotePort = $state<number | undefined>(undefined);
  let nAutoStart = $state(false);
  let nDesc = $state("");

  // Reset the address defaults to loopback when the user switches kind,
  // unless they've typed their own value. Keeps both fields meaningful per
  // kind without surprising edits.
  $effect(() => {
    void nKind;
    if (!nLocalAddrTouched) nLocalAddr = "127.0.0.1";
    if (!nRemoteHostTouched) nRemoteHost = "127.0.0.1";
  });

  function resetForm() {
    nKind = "local";
    nLocalAddr = "127.0.0.1";
    nLocalAddrTouched = false;
    nLocalPort = undefined;
    nRemoteHost = "127.0.0.1";
    nRemoteHostTouched = false;
    nRemotePort = undefined;
    nAutoStart = false;
    nDesc = "";
  }

  // Editing an existing forward reuses this same form. null = create mode.
  // The forward's KIND is immutable (local/remote/dynamic change the whole
  // semantics + backend listener), so the kind select is disabled while
  // editing - delete + recreate to change it.
  let editingForwardId = $state<string | null>(null);

  function openEditForward(spec: PortForward) {
    editingForwardId = spec.id;
    nKind = spec.kind;
    nLocalAddr = spec.local_addr ?? "127.0.0.1";
    nLocalAddrTouched = true; // don't let the kind-switch effect clobber it
    nLocalPort = spec.local_port ?? undefined;
    nRemoteHost = spec.remote_host ?? "127.0.0.1";
    nRemoteHostTouched = true;
    nRemotePort = spec.remote_port ?? undefined;
    nAutoStart = spec.auto_start;
    nDesc = spec.description ?? "";
    showAdd = true;
  }

  function cancelForm() {
    showAdd = false;
    editingForwardId = null;
    resetForm();
  }

  async function submitForward() {
    if (editingForwardId) return updateForward();
    return createForward();
  }

  async function createForward() {
    err = null;
    try {
      await api.forwardsCreate({
        connection_id: connection.id,
        kind: nKind,
        local_addr: nLocalAddr || undefined,
        local_port: nLocalPort,
        remote_host: nKind === "dynamic" ? undefined : nRemoteHost || undefined,
        remote_port: nKind === "dynamic" ? undefined : nRemotePort,
        auto_start: nAutoStart,
        description: nDesc,
      });
      showAdd = false;
      resetForm();
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  // Partial update via the backend's clear-flags: an emptied field is
  // cleared (back to inherit/null) rather than left unchanged.
  async function updateForward() {
    if (!editingForwardId) return;
    err = null;
    const isDyn = nKind === "dynamic";
    try {
      await api.forwardsUpdate({
        id: editingForwardId,
        local_addr: nLocalAddr || undefined,
        clear_local_addr: !nLocalAddr,
        local_port: nLocalPort,
        clear_local_port: nLocalPort === undefined,
        // dynamic forwards have no remote host/port - always clear them.
        remote_host: isDyn ? undefined : (nRemoteHost || undefined),
        clear_remote_host: isDyn || !nRemoteHost,
        remote_port: isDyn ? undefined : nRemotePort,
        clear_remote_port: isDyn || nRemotePort === undefined,
        auto_start: nAutoStart,
        description: nDesc,
      });
      cancelForm();
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  async function removeForward(id: string) {
    const ok = await showConfirm({
      title: "Delete forward",
      message: "Delete this forward?",
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.forwardsDelete(id);
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  // ensureSession returns a live session id for this connection,
  // opening one on demand if nothing is connected yet. Mirrors the
  // quick palette: starting a tunnel or opening a bookmark from here no
  // longer requires connecting first - we connect for you and give the
  // session a visible tab. Returns null on failure (already surfaced).
  let connecting = $state(false);
  async function ensureSession(): Promise<string | null> {
    if (activeSession) return activeSession.sessionId;
    connecting = true;
    err = null;
    try {
      const res = await api.sshConnect(connection.id);
      sessions.add({
        sessionId: res.session_id,
        connectionId: connection.id,
        name: connection.name,
        hostname: connection.hostname,
        status: "connected",
      });
      paneTabs.addTab(res.session_id, connection.name);
      return res.session_id;
    } catch (e: any) {
      err = errMsg(e);
      return null;
    } finally {
      connecting = false;
    }
  }

  async function startForward(spec: PortForward) {
    err = null;
    const sid = await ensureSession();
    if (!sid) return;
    try {
      await api.forwardsStart(spec.id, sid);
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  async function stopForward(spec: PortForward) {
    try {
      await api.forwardsStop(spec.id);
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  async function launchBrowser(spec: PortForward, url?: string) {
    err = null;
    const target = url ?? await showPrompt("URL to open:", "https://") ?? "";
    if (!target) return;
    // Make sure the proxy is up: connect if needed, then start the
    // forward if it isn't already listening, then launch the browser.
    const sid = await ensureSession();
    if (!sid) return;
    try {
      if (!statusOf(spec.id) || statusOf(spec.id)?.state !== "listening") {
        await api.forwardsStart(spec.id, sid);
        await reload();
      }
      await api.sshLaunchBrowser(spec.id, target);
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  // Bookmark management - one inline form open at a time (keyed by spec.id).
  // editBmIndex distinguishes add (null) from editing an existing bookmark
  // at that index; the same form markup + state is reused for both.
  let addBookmarkFor = $state<string | null>(null);
  let editBmIndex = $state<number | null>(null);
  let newBmName = $state("");
  let newBmUrl = $state("");

  function openAddBookmark(specId: string) {
    addBookmarkFor = specId;
    editBmIndex = null;
    newBmName = "";
    newBmUrl = "";
  }

  function openEditBookmark(spec: PortForward, index: number) {
    const bm = (spec.bookmarks ?? [])[index];
    if (!bm) return;
    addBookmarkFor = spec.id;
    editBmIndex = index;
    newBmName = bm.name;
    newBmUrl = bm.url;
  }

  async function saveBookmark(spec: PortForward) {
    if (!newBmName.trim() || !newBmUrl.trim()) return;
    const existing = spec.bookmarks ?? [];
    const entry: ProxyBookmark = { name: newBmName.trim(), url: newBmUrl.trim() };
    // Edit replaces in place; add appends.
    const updated: ProxyBookmark[] =
      editBmIndex !== null
        ? existing.map((bm, i) => (i === editBmIndex ? entry : bm))
        : [...existing, entry];
    try {
      await api.forwardsSetBookmarks(spec.id, updated);
      addBookmarkFor = null;
      editBmIndex = null;
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  async function removeBookmark(spec: PortForward, index: number) {
    const updated = (spec.bookmarks ?? []).filter((_, i) => i !== index);
    try {
      await api.forwardsSetBookmarks(spec.id, updated);
      await reload();
    } catch (e: any) {
      err = errMsg(e);
    }
  }

  function statusOf(specId: string): ForwardStatus | undefined {
    return active.find((s) => s.id === specId);
  }

  function fmtBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
    return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
  }

  async function copyAssignedPort(port: number) {
    try { await copyText(String(port), { label: "Port" }); } catch { /* ignore */ }
  }
</script>

<div class="forwards">
  <header>
    <strong>Port forwards</strong>
    <button onclick={() => { if (showAdd) { cancelForm(); } else { resetForm(); showAdd = true; } }}>{showAdd ? "Cancel" : "+ Add"}</button>
  </header>

  {#if err}<div class="err">{err}</div>{/if}

  {#if showAdd}
    <div class="add-form">
      <label>Kind
        <!-- Kind is immutable once created (it changes the whole listener
             semantics); disabled while editing. -->
        <select bind:value={nKind} disabled={editingForwardId !== null}>
          <option value="local">Local (L) - local listen → remote target</option>
          <option value="remote">Remote (R) - remote listen → local target</option>
          <option value="dynamic">Dynamic (D) - SOCKS5 proxy</option>
        </select>
        {#if editingForwardId !== null}
          <span class="field-note">Kind can't be changed - delete and recreate to switch.</span>
        {/if}
      </label>
      <!-- Line 1: the LISTEN/bind side.
           local/dynamic -> bind on THIS machine; remote -> bind on the SERVER. -->
      <div class="row">
        <label class="grow">{nKind === "remote" ? "Remote bind address" : "Local bind address"}
          <input
            bind:value={nLocalAddr}
            oninput={() => (nLocalAddrTouched = true)}
            placeholder="127.0.0.1"
            title={nKind === "remote"
              ? "Where the forward LISTENS on the server. 127.0.0.1 = reachable only from the server itself; 0.0.0.0 exposes it to the server's network."
              : "Where the forward LISTENS on this machine. 127.0.0.1 = this machine only; use your LAN IP or 0.0.0.0 to let other machines reach it."}
          />
        </label>
        <label class="port">{nKind === "remote" ? "Remote bind port" : "Local port"}
          <input type="number" bind:value={nLocalPort} placeholder="0 = auto" />
        </label>
      </div>
      {#if nKind !== "dynamic"}
        <!-- Line 2: the TARGET side that the listener dials.
             local -> target reached FROM the server; remote -> target on THIS machine. -->
        <div class="row">
          <label class="grow">{nKind === "remote" ? "Local target host" : "Target host"}
            <input
              bind:value={nRemoteHost}
              oninput={() => (nRemoteHostTouched = true)}
              placeholder={nKind === "remote" ? "127.0.0.1" : "127.0.0.1 or db.internal"}
              title={nKind === "remote"
                ? "Where the server's connections are delivered ON THIS machine. 127.0.0.1 = a service on this computer (e.g. a local squid/proxy)."
                : "What to connect to, as seen FROM the server. 127.0.0.1 = a service on the server itself; or a host the server can reach (e.g. db.internal)."}
            />
          </label>
          <label class="port">{nKind === "remote" ? "Local target port" : "Target port"}
            <input type="number" bind:value={nRemotePort} placeholder="5432" />
          </label>
        </div>
      {/if}
      <label>Description
        <input bind:value={nDesc} placeholder="Postgres tunnel" />
      </label>
      <label class="checkbox">
        <input type="checkbox" bind:checked={nAutoStart} />
        <span>Auto-start when this connection connects</span>
      </label>
      {#if editingForwardId !== null && statusOf(editingForwardId)?.state === "listening"}
        <p class="edit-note">This forward is running - stop and start it again to apply changes.</p>
      {/if}
      <div class="row" style="justify-content: flex-end; gap: 0.4rem;">
        <button onclick={cancelForm}>Cancel</button>
        <button class="primary" onclick={submitForward}>{editingForwardId !== null ? "Save" : "Create"}</button>
      </div>
    </div>
  {/if}

  {#if specs.length === 0}
    <div class="empty">
      No forwards yet. Click <strong>+ Add</strong> to create one.
    </div>
  {:else}
    <ul class="list">
      {#each specs as spec (spec.id)}
        {@const status = statusOf(spec.id)}
        {@const running = status?.state === "listening"}
        {@const liveAddr = (running && status) ? status.local_addr : (spec.local_addr ?? "127.0.0.1")}
        {@const liveSpecPort = spec.local_port ?? 0}
        {@const livePort = (running && status && status.local_port > 0) ? status.local_port : (liveSpecPort > 0 ? liveSpecPort : null)}
        {@const portLabel = livePort ?? (liveSpecPort === 0 ? "auto" : "?")}
        <li class="item">
          <div class="head">
            <span class="kind kind-{spec.kind}">{spec.kind}</span>
            <span class="desc">{spec.description || "(no description)"}</span>
            {#if spec.auto_start}<span class="badge">auto</span>{/if}
            <span class="dot" style="background: {running ? 'var(--green)' : 'var(--overlay0)'}"></span>
            <span class="status">{running ? "listening" : "stopped"}</span>
          </div>
          <div class="meta">
            {#if spec.kind === "dynamic"}
              <code>SOCKS5 @ {liveAddr}:{portLabel}</code>
            {:else if spec.kind === "local"}
              <code>{liveAddr}:{portLabel}</code>
              <span class="arrow">→</span>
              <code>{spec.remote_host}:{spec.remote_port}</code>
            {:else}
              <code>remote {liveAddr}:{portLabel}</code>
              <span class="arrow">→</span>
              <code>local {spec.remote_host}:{spec.remote_port}</code>
            {/if}
            {#if running && status && status.local_port > 0 && liveSpecPort === 0}
              <button
                class="copy-port"
                title="Copy {status.local_port} to clipboard"
                onclick={() => copyAssignedPort(status.local_port)}
              >📋 :{status.local_port}</button>
            {/if}
            {#if running && status}
              <span class="bytes">↓ {fmtBytes(status.bytes_in)} · ↑ {fmtBytes(status.bytes_out)}</span>
            {/if}
          </div>
          <div class="actions">
            {#if running}
              <button onclick={() => stopForward(spec)}>Stop</button>
              {#if spec.kind === "dynamic"}
                <button onclick={() => launchBrowser(spec)} title="Open a custom URL via this proxy" class="iconlbl"><IconGlobe size={12} /> Open URL…</button>
              {/if}
            {:else}
              <button class="primary" onclick={() => startForward(spec)}
                disabled={connecting}
                title={activeSession ? "Start this forward" : "Connect to the server and start this forward"}>
                {connecting ? "Connecting…" : activeSession ? "Start" : "Connect & start"}
              </button>
            {/if}
            <button onclick={() => openEditForward(spec)} title="Edit this forward">Edit</button>
            <button class="danger" onclick={() => removeForward(spec.id)}>Delete</button>
          </div>
          {#if spec.kind === "dynamic"}
            <div class="bookmarks">
              {#each spec.bookmarks ?? [] as bm, i}
                <span class="bm-chip">
                  <button
                    class="bm-launch bm-launch-active"
                    disabled={connecting}
                    title={running ? bm.url : `Connect, start the proxy, and open ${bm.url}`}
                    onclick={() => launchBrowser(spec, bm.url)}
                  >{bm.name}</button>
                  <button class="bm-edit" title="Edit bookmark" onclick={() => openEditBookmark(spec, i)}>✎</button>
                  <button class="bm-del" title="Remove bookmark" onclick={() => removeBookmark(spec, i)}>×</button>
                </span>
              {/each}
              {#if addBookmarkFor === spec.id}
                <span class="bm-add-form">
                  <input bind:value={newBmName} placeholder="Label" class="bm-input" />
                  <input bind:value={newBmUrl} placeholder="https://…" class="bm-input bm-url" />
                  <button class="bm-save" onclick={() => saveBookmark(spec)}>{editBmIndex !== null ? "Save" : "Add"}</button>
                  <button onclick={() => { addBookmarkFor = null; editBmIndex = null; }}>✕</button>
                </span>
              {:else}
                <button class="bm-new" onclick={() => openAddBookmark(spec.id)}>+ Bookmark</button>
              {/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if active.length > 0 && !activeSession}
    <div class="empty">
      Refresh to see live forwards (none from this connection are running).
    </div>
  {/if}
</div>

<style>
  .forwards {
    margin-top: 1.5rem;
    border-top: 1px solid var(--surface0);
    padding-top: 1rem;
  }
  header {
    /* `+ Add` used to live at the far right (justify-content:
       space-between). On a wide window the gap was huge - easier to
       keep label and action close. */
    display: flex; align-items: center; gap: 0.6rem;
    margin-bottom: 0.5rem;
  }
  header strong {
    font-size: 0.78rem; text-transform: uppercase;
    color: var(--subtext0); letter-spacing: 0.04em;
  }
  .err {
    color: var(--red); background: var(--crust);
    padding: 0.5rem 0.7rem; border-radius: 4px;
    border-left: 3px solid var(--red); font-size: 0.82rem;
    margin: 0.5rem 0;
  }
  .empty {
    color: var(--overlay0); padding: 0.6rem 0;
    font-size: 0.85rem;
  }
  .field-note {
    display: block; margin-top: 0.15rem;
    font-size: 0.72rem; color: var(--overlay1, var(--subtext0));
    line-height: 1.3;
  }
  .edit-note {
    margin: 0.2rem 0 0; font-size: 0.75rem; color: var(--peach);
  }
  .add-form {
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.7rem 0.85rem;
    margin-bottom: 0.6rem;
    display: flex; flex-direction: column; gap: 0.5rem;
  }
  .row { display: flex; gap: 0.5rem; }
  .grow { flex: 1; }
  .port { width: 7rem; }
  label {
    display: flex; flex-direction: column; gap: 0.2rem;
    font-size: 0.75rem; color: var(--subtext0);
  }
  label.checkbox {
    flex-direction: row; align-items: center; gap: 0.45rem;
  }
  label.checkbox input { width: auto; margin: 0; }
  input, select {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.3rem 0.45rem; font: inherit;
    font-size: 0.85rem;
  }
  input:focus, select:focus {
    outline: 1px solid var(--blue); border-color: var(--blue);
  }
  ul.list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.4rem; }
  .item {
    background: var(--crust); border-left: 3px solid var(--surface0);
    padding: 0.5rem 0.7rem;
    border-radius: 3px;
    display: flex; flex-direction: column; gap: 0.3rem;
  }
  .head { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; }
  .kind {
    font-size: 0.7rem; padding: 0.05rem 0.4rem;
    border-radius: 3px; text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .kind-local { background: var(--surface1); color: var(--blue); }
  .kind-remote { background: var(--surface1); color: var(--yellow); }
  .kind-dynamic { background: var(--surface1); color: var(--mauve); }
  .desc { flex: 1; font-weight: 500; }
  .badge {
    font-size: 0.7rem; background: var(--surface0);
    padding: 0.05rem 0.3rem; border-radius: 2px; color: var(--subtext0);
  }
  .dot { width: 7px; height: 7px; border-radius: 50%; }
  .status { font-size: 0.72rem; color: var(--subtext0); }
  .meta {
    display: flex; align-items: center; gap: 0.5rem;
    font-size: 0.78rem; color: var(--subtext0);
    flex-wrap: wrap;
  }
  code {
    background: var(--mantle); padding: 0.05rem 0.35rem;
    border-radius: 3px; font-size: 0.78rem;
  }
  .arrow { color: var(--overlay0); }
  .bytes { color: var(--overlay0); font-size: 0.72rem; margin-left: auto; }
  .copy-port {
    background: var(--surface0);
    color: var(--teal);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    padding: 0.1rem 0.45rem;
    font-size: 0.7rem;
    font-family: ui-monospace, monospace;
    cursor: pointer;
  }
  .copy-port:hover { background: var(--surface1); color: var(--text); }
  .actions { display: flex; gap: 0.4rem; margin-top: 0.2rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.3rem 0.7rem; border-radius: 3px;
    cursor: pointer; font: inherit; font-size: 0.8rem;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.4; cursor: not-allowed; }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover:not(:disabled) { background: var(--lavender); }
  button.danger { background: transparent; color: var(--red); }
  button.danger:hover { background: var(--red); color: var(--on-accent); }

  /* ---- proxy bookmarks ---- */
  .bookmarks {
    display: flex; flex-wrap: wrap; gap: 0.3rem;
    margin-top: 0.25rem;
  }
  .bm-chip {
    display: inline-flex; align-items: center;
    background: var(--mantle); border: 1px solid var(--surface0);
    border-radius: 3px; overflow: hidden;
  }
  .bm-launch {
    background: transparent; border: 0; color: var(--mauve);
    padding: 0.2rem 0.5rem; font: inherit; font-size: 0.78rem;
    cursor: pointer; white-space: nowrap;
  }
  .bm-launch:disabled { color: var(--surface1); cursor: not-allowed; }
  /* Theme-aware hover: a subtle raised surface that's light-grey in the
     light theme and dark-grey in dark, with the mauve label kept (was a
     hardcoded #d4b0ff that turned unreadable on the light theme's pale
     background). */
  .bm-launch-active:hover:not(:disabled) {
    background: var(--surface0);
    color: var(--mauve);
  }
  .bm-edit {
    background: transparent; border: 0; border-left: 1px solid var(--surface0);
    color: var(--overlay0); padding: 0.2rem 0.35rem;
    font: inherit; font-size: 0.75rem; cursor: pointer; line-height: 1;
  }
  .bm-edit:hover { color: var(--blue); }
  .bm-del {
    background: transparent; border: 0; border-left: 1px solid var(--surface0);
    color: var(--overlay0); padding: 0.2rem 0.35rem;
    font: inherit; font-size: 0.75rem; cursor: pointer; line-height: 1;
  }
  .bm-del:hover { color: var(--red); }
  .bm-add-form {
    display: inline-flex; align-items: center; gap: 0.25rem;
    background: var(--mantle); border: 1px solid var(--surface1);
    border-radius: 3px; padding: 0.15rem 0.35rem;
  }
  .bm-input {
    background: transparent; border: 0; border-bottom: 1px solid var(--surface0);
    color: var(--text); font: inherit; font-size: 0.78rem;
    padding: 0.1rem 0.2rem; width: 6rem; outline: none;
  }
  .bm-url { width: 12rem; }
  .bm-save {
    background: var(--surface0); color: var(--green); border: 0;
    padding: 0.15rem 0.45rem; border-radius: 2px;
    font: inherit; font-size: 0.78rem; cursor: pointer;
  }
  .bm-save:hover { background: var(--surface1); }
  .bm-new {
    background: transparent; border: 1px dashed var(--surface1);
    color: var(--overlay0); padding: 0.2rem 0.5rem; border-radius: 3px;
    font: inherit; font-size: 0.78rem; cursor: pointer;
  }
  .bm-new:hover { border-color: var(--mauve); color: var(--mauve); }
  .iconlbl { display: inline-flex; align-items: center; gap: 0.25rem; }
</style>
