<script lang="ts">
  // What the detail pane shows when nothing is selected: recent
  // connections, workspaces, network profiles and a line of state (vault,
  // sync, backup, update, issues). Everything here is data the app already
  // keeps; nothing is fetched from a remote. The recents get the liveness
  // dot by registering with probeState like an expanded folder does, so a
  // host is probed only while it is on screen and only when probing is on.
  import { onMount } from "svelte";
  import { tree, sessions, selection, view } from "./stores.svelte";
  import { api, type Connection, type NetworkProfileInfo } from "./api";
  import { connectionActions } from "./connectionActions.svelte";
  import { probeState } from "./probeState.svelte";
  import { workspaces } from "./workspaces.svelte";
  import { networkProfiles } from "./networkProfiles.svelte";
  import { profileKindTitle, profileRunsHere, NOT_ON_MOBILE_TITLE } from "./networkKind";
  import { issues } from "./issues.svelte";
  import { updateCheck } from "./updateCheck.svelte";
  import { vaultState } from "./vaultState.svelte";
  import { syncState } from "./syncState.svelte";
  import { quickConnect } from "./quickConnect.svelte";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { isMobile } from "./platform";
  import UpdateModal from "./UpdateModal.svelte";
  import { fleet, folderHostIds, fleetHostName, TLS_WARN_DAYS, type FactsSnapshot, type ReportSettings } from "./fleetStore.svelte";
  import { hostIssues } from "./factsIssues";
  import { failedIgnore } from "./failedIgnore.svelte";
  import { EventsOn } from "./wailsRuntime";
  import Icon from "./Icon.svelte";
  import {
    IconHistory, IconHost, IconWorkspace, IconVpn, IconLock, IconRefresh,
    IconSave, IconDownload, IconAlertTriangle, IconGlobe, IconTerminal, IconActivity,
  } from "./iconMap";

  const RECENT_COUNT = 9;

  const recent = $derived(tree.recent(RECENT_COUNT));
  const liveIds = $derived(new Set(
    sessions.tabs.filter((t) => t.status === "connected").map((t) => t.connectionId),
  ));

  probeState.loadEnabled();
  $effect(() => {
    const ids = recent.map((c) => c.id);
    for (const id of ids) probeState.register(id);
    return () => { for (const id of ids) probeState.unregister(id); };
  });

  function ago(sec: number | null): string {
    if (!sec) return "";
    const d = Math.max(0, Date.now() / 1000 - sec);
    if (d < 60) return "now";
    if (d < 3600) return `${Math.floor(d / 60)} min`;
    if (d < 86400) return `${Math.floor(d / 3600)} h`;
    if (d < 2 * 86400) return "yesterday";
    return `${Math.floor(d / 86400)} days`;
  }

  function where(c: Connection): string {
    const parts: string[] = [];
    let f = tree.folderById(c.folder_id ?? null);
    for (let guard = 0; f && guard < 64; guard++) {
      parts.unshift(f.name);
      f = tree.folderById(f.parent_id ?? null);
    }
    return [parts.join(" / "), c.hostname].filter(Boolean).join(" - ");
  }

  function connect(c: Connection) {
    void connectionActions.connectDefault(c.id);
  }

  // ---- workspaces ----
  // The sync passphrase lives in the vault: read before an auto-unlock
  // finishes, sync looks unconfigured. Read the state once the vault status
  // is known and unlocked, and again each time the Connections view comes
  // back (the pane stays mounted while Settings is open, where sync and
  // backup are changed) and after a push or pull.
  $effect(() => {
    if (!vaultState.loading && vaultState.status === "unlocked" && view.tab === "connections") void loadState();
  });
  onMount(() => {
    const offs = ["sync_auto_pushed", "profile_reloaded"].map((ev) => EventsOn(ev, () => { void loadState(); }));
    return () => offs.forEach((off) => off());
  });
  onMount(() => {
    if (!workspaces.list.length) void workspaces.load();
    void networkProfiles.load();
  });
  const wsList = $derived(
    [...workspaces.list].sort((a, b) => (b.last_opened_at ?? 0) - (a.last_opened_at ?? 0)).slice(0, 6),
  );
  function tabCount(layoutJSON: string): number {
    try { return (JSON.parse(layoutJSON)?.tabs ?? []).length; } catch { return 0; }
  }
  function openWorkspace(id: string) {
    workspaces.open(id).catch((e) => toast.err(errMsg(e)));
  }

  // ---- network profiles ----
  let npBusy = $state<Record<string, boolean>>({});
  function npLine(np: NetworkProfileInfo): string {
    const state = np.paused ? "paused" : np.status.running ? "up" : "down";
    return [profileKindTitle(np), np.mode || "manual", state].join(" - ");
  }
  async function npToggle(np: NetworkProfileInfo) {
    npBusy = { ...npBusy, [np.id]: true };
    try {
      if (np.status.running) await api.networkProfileStop(np.id);
      else {
        const st = await api.networkProfileTest(np.id);
        if (!st.running) toast.err(`Tunnel ${np.name} did not start`);
      }
      await networkProfiles.load(true);
    } catch (e) {
      toast.err(errMsg(e));
    } finally {
      const n = { ...npBusy };
      delete n[np.id];
      npBusy = n;
    }
  }

  // ---- state line ----
  let syncOn = $state(false);
  let syncAuto = $state(false);
  let lastSync = $state(0);
  let backupOn = $state(false);
  let lastBackup = $state(0);
  const backupStale = $derived(backupOn && Date.now() / 1000 - lastBackup > 3 * 86400);
  async function loadState() {
    try {
      const cfg = await api.syncConfigGet();
      syncOn = !!cfg && cfg.has_passphrase && !!(cfg.url || cfg.sftp_host || cfg.has_dropbox_token
        || cfg.has_onedrive_token || cfg.has_gdrive_token);
      syncAuto = !!cfg?.auto;
      lastSync = cfg?.last_sync_at ?? 0;
    } catch { syncOn = false; }
    try {
      backupOn = !!(await api.autoBackupPrefsGet())?.enabled;
      const list = (await api.backupsList()) ?? [];
      lastBackup = list.reduce((m, b) => Math.max(m, Date.parse(b.created_at) / 1000 || 0), 0);
    } catch { /* no backups yet */ }
  }
  function when(sec: number): string {
    if (!sec) return "never";
    const d = new Date(sec * 1000);
    const today = new Date().toDateString() === d.toDateString();
    return today
      ? `today ${d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`
      : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  }
  let updateOpen = $state(false);

  // ---- fleet ----
  // The latest stored facts run per folder, judged by that folder's report
  // settings: the same rules as the health report. Nothing runs on the
  // hosts; the age of each run is shown.
  let factsRuns = $state<Record<string, FactsSnapshot>>({});
  let reportSettings = $state<Record<string, ReportSettings>>({});
  let factsLoaded = $state(false);
  async function loadFacts() {
    try {
      const runs = ((await api.factsLatest()) ?? {}) as Record<string, FactsSnapshot>;
      factsRuns = runs;
      for (const id of Object.keys(runs)) {
        // Not filtered by folder here: the tree may still be loading.
        if (reportSettings[id]) continue;
        fleet.loadReportSettings(id)
          .then((rs) => (reportSettings = { ...reportSettings, [id]: rs }))
          .catch(() => {});
      }
    } catch { /* no facts gathered yet */ }
    factsLoaded = true;
  }
  onMount(() => {
    void loadFacts();
    void fleet.init();
    return EventsOn("fleet_facts_saved", () => { void loadFacts(); });
  });
  interface FleetRow { folderId: string; label: string; at: number; hosts: number; bad: number; noAnswer: number; red: boolean; lines: string[] }
  const fleetRows = $derived.by(() => {
    const out: FleetRow[] = [];
    for (const [folderId, snap] of Object.entries(factsRuns)) {
      const folder = tree.folderById(folderId);
      const rs = reportSettings[folderId];
      if (!folder || !rs) continue;
      const ok = snap.results.filter((r) => r.state === "ok");
      const lines: string[] = [];
      let bad = 0, red = false;
      for (const r of ok) {
        const is = hostIssues(r, snap, rs, (h, u) => (rs.expectedUnits ?? []).includes(u) || failedIgnore.isIgnored(h.connection_id, u));
        if (!is.length) continue;
        bad++;
        if (is.some((i) => i.level === "red")) red = true;
        lines.push(`${r.name}: ${is.map((i) => i.text).join(", ")}`);
      }
      const noAnswer = snap.results.length - ok.length;
      out.push({ folderId, label: where2(folderId), at: snap.at, hosts: snap.results.length, bad, noAnswer, red: red || noAnswer > 0, lines });
    }
    return out.sort((a, b) => Number(b.red) - Number(a.red) || b.bad - a.bad || a.label.localeCompare(b.label));
  });
  const certRows = $derived.by(() => {
    void fleet.tls;
    return Object.keys(fleet.tls)
      .map((id) => ({ id, d: fleet.daysLeft(id) }))
      .filter((x): x is { id: string; d: number } => x.d !== null && x.d < TLS_WARN_DAYS)
      .sort((a, b) => a.d - b.d);
  });
  function where2(folderId: string): string {
    const parts: string[] = [];
    let f = tree.folderById(folderId);
    for (let guard = 0; f && guard < 64; guard++) {
      parts.unshift(f.name);
      f = tree.folderById(f.parent_id ?? null);
    }
    return parts.join(" / ");
  }
  function openReport(row: FleetRow) {
    fleet.show({ tool: "facts", ids: folderHostIds(row.folderId), label: tree.folderById(row.folderId)?.name ?? row.label, folderId: row.folderId, showSnapshot: true });
  }
  const FLEET_COUNT = 8;
  let fleetAll = $state(false);
  const fleetShown = $derived(fleetAll ? fleetRows : fleetRows.slice(0, FLEET_COUNT));
  function factsAge(ms: number): string {
    const a = ago(Math.floor(ms / 1000));
    return a === "now" ? "just now" : a === "yesterday" ? "yesterday" : `${a} ago`;
  }

  let qcText = $state("");
  function quick(e: SubmitEvent) {
    e.preventDefault();
    quickConnect.show(qcText.trim());
    qcText = "";
  }
</script>

<div class="home">
  <div class="state">
    {#if vaultState.locked}
      <button class="chip warn" onclick={() => window.dispatchEvent(new CustomEvent("vault-unlock-now"))}
        title="Stored passwords, keys and tokens cannot be read until the vault is unlocked">
        <IconLock size={13} /> Vault locked - unlock
      </button>
    {:else if !vaultState.loading}
      <span class="chip"><IconLock size={13} class="ok" /> Vault unlocked</span>
    {/if}
    {#if syncState.remoteAhead}
      <button class="chip warn" onclick={() => syncState.quickPull()} title="Pull the newer profile">
        <IconRefresh size={13} /> Sync: newer profile{syncState.remoteAhead.device ? ` from ${syncState.remoteAhead.device}` : ""} - pull
      </button>
    {:else if syncOn}
      <button class="chip" onclick={() => view.setTabSettingsSection("sync")}>
        <IconRefresh size={13} class="ok" /> Sync{syncAuto ? " (auto)" : ""} <span class="dim">- {when(lastSync)}</span>
      </button>
    {/if}
    {#if backupOn}
      <button class="chip" class:warn={backupStale} onclick={() => view.setTabSettingsSection("backup")}
        title={backupStale ? "Auto backup is on, but the newest backup is older than 3 days" : ""}>
        <IconSave size={13} class={backupStale ? "" : "ok"} /> Backup <span class="dim">- {when(lastBackup)}</span>
      </button>
    {:else}
      <button class="chip" onclick={() => view.setTabSettingsSection("backup")} title="Turn on a daily encrypted backup in Settings">
        <IconSave size={13} /> Auto backup off{#if lastBackup}<span class="dim">&nbsp;- last {when(lastBackup)}</span>{/if}
      </button>
    {/if}
    {#if updateCheck.available}
      <button class="chip link" onclick={() => (updateOpen = true)}>
        <IconDownload size={13} /> Update {updateCheck.latest} available
      </button>
    {/if}
  </div>

  {#if issues.items.length}
    <ul class="issues">
      {#each issues.items as it (it.id)}
        <li class:error={it.severity === "error"}>
          <IconAlertTriangle size={14} />
          <span class="it-title" title={it.detail}>{it.title}</span>
          <button class="btn small" onclick={it.action}>{it.actionLabel}</button>
        </li>
      {/each}
    </ul>
  {/if}

  <form class="qc" onsubmit={quick}>
    <label for="home-qc">Quick connect</label>
    <input id="home-qc" bind:value={qcText} placeholder="user@host:port" autocomplete="off" spellcheck="false" />
    <button type="submit" class="btn primary">Connect</button>
  </form>

  <div class="grid">
    <section class="card">
      <header>
        <IconHistory size={14} />
        <h2>Recent</h2>
      </header>
      {#if recent.length === 0}
        <p class="none">Connections you open show up here.</p>
      {/if}
      {#each recent as c (c.id)}
        {@const live = liveIds.has(c.id)}
        {@const probe = live ? null : probeState.stateOf(c.id)}
        {@const stage = connectionActions.connectStage[c.id]}
        <div class="row">
          <span class="ico" title={live ? "Connected" : probe === "up" ? "Reachable" : probe === "down" ? "Unreachable" : ""}>
            <Icon imageId={c.icon_image_id} iconName={c.icon_name} iconColor={c.icon_color} size={16}>
              {#if c.protocol === "web"}<IconGlobe size={16} />{:else if c.protocol === "local"}<IconTerminal size={16} />{:else}<IconHost size={16} />{/if}
            </Icon>
            {#if live || probe === "up" || probe === "down"}
              <span class="badge" class:live class:up={probe === "up"} class:down={probe === "down"}></span>
            {/if}
          </span>
          <button class="name" onclick={() => selection.selectConnection(c.id)} ondblclick={() => connect(c)} title="Click to edit, double-click to connect">
            <span class="nm">{c.name}</span>
            <span class="sub">{where(c)}</span>
          </button>
          <span class="ago">{ago(tree.lastUsed(c))}</span>
          <button class="btn small" onclick={() => connect(c)} disabled={stage !== undefined} title={stage || ""}>
            {stage === undefined ? "Connect" : stage === "Queued" ? "Queued" : "Connecting..."}
          </button>
        </div>
      {/each}
    </section>

    <div class="side">
      <section class="card">
        <header>
          <IconWorkspace size={14} />
          <h2>Workspaces</h2>
        </header>
        {#if wsList.length === 0}
          <p class="none">Save open tabs as a workspace from the Workspaces menu in the status bar.</p>
        {:else}
          <div class="ws">
            {#each wsList as w (w.id)}
              {@const n = tabCount(w.layout_json)}
              <button class="ws-card" onclick={() => openWorkspace(w.id)} title="Open the workspace's tabs">
                <span class="nm">{w.name}</span>
                <span class="sub">{n === 1 ? "1 tab" : `${n} tabs`}{workspaces.isOpen(w.id) ? " - open" : ""}</span>
              </button>
            {/each}
          </div>
        {/if}
      </section>

      <section class="card">
          <header>
            <IconVpn size={14} />
            <h2>Network profiles</h2>
          </header>
          {#if !networkProfiles.list.length && !networkProfiles.loading}
            <p class="none">No network profiles yet. <button class="link" onclick={() => view.setTabSettingsSection("network")}>Add one in Settings</button></p>
          {/if}
          {#each networkProfiles.list as np (np.id)}
            {@const here = !isMobile || profileRunsHere(np)}
            <div class="row">
              <span class="pill" class:up={np.status.running && !np.paused}></span>
              <button class="name" onclick={() => view.openNetworkProfile(np.id)} title="Open in Settings">
                <span class="nm">{np.name}</span>
                <span class="sub">{npLine(np)}</span>
              </button>
              <button class="btn small" onclick={() => npToggle(np)}
                disabled={npBusy[np.id] || !here || (np.paused && !np.status.running)}
                title={!here ? NOT_ON_MOBILE_TITLE : np.paused ? "Paused: resume the profile in Settings" : ""}>
                {npBusy[np.id] ? "..." : np.status.running ? "Disconnect" : "Connect"}
              </button>
            </div>
          {/each}
        </section>
    </div>
  </div>

  <section class="card">
      <header>
        <IconActivity size={14} />
        <h2>Fleet</h2>
        <span class="hint">from the last facts run per folder, no new checks</span>
      </header>
      {#if factsLoaded && !fleetRows.length && !certRows.length}
        <p class="none">No facts gathered yet. Select a folder and click Gather facts; its hosts show up here.</p>
      {/if}
      {#each fleetShown as row (row.folderId)}
        <div class="frow">
          <span class="nm f-folder" title={row.label}>{row.label}</span>
          {#if row.bad || row.noAnswer}
            <span class="problems f-state" class:red={row.red}>
              {[row.bad && `${row.bad} of ${row.hosts} need attention`, row.noAnswer && `${row.noAnswer} did not answer`].filter(Boolean).join(", ")}
            </span>
            <span class="sub f-issue" title={row.lines.join("\n")}>{row.lines[0] ?? ""}{row.lines.length > 1 ? ` (+${row.lines.length - 1} more)` : ""}</span>
          {:else}
            <span class="sub ok-text f-state">all {row.hosts} OK</span>
            <span class="f-issue"></span>
          {/if}
          <span class="sub f-age">{factsAge(row.at)}</span>
          <button class="btn small" onclick={() => openReport(row)}>Report</button>
        </div>
      {/each}
      {#if fleetRows.length > FLEET_COUNT}
        <button class="more" onclick={() => (fleetAll = !fleetAll)}>
          {fleetAll ? "Show fewer" : `Show all ${fleetRows.length} folders`}
        </button>
      {/if}
      {#if certRows.length}
        <div class="frow">
          <span class="nm f-folder">TLS certificates</span>
          <span class="problems f-certs">{certRows.map((c) => `${fleetHostName(c.id, fleet.tls[c.id]?.name)} ${c.d < 0 ? "expired" : `${c.d} days`}`).join(", ")}</span>
        </div>
      {/if}
    </section>

  <p class="keys">Ctrl+K search - Ctrl+Shift+T reopen closed tab - Ctrl+Shift+P snippets - Ctrl+1..9 switch tab</p>
</div>

{#if updateOpen}
  <UpdateModal onClose={() => (updateOpen = false)} />
{/if}

<style>
  .home { display: flex; flex-direction: column; gap: 1rem; max-width: 1800px; padding-top: 1rem; }
  .state { display: flex; flex-wrap: wrap; gap: 0.4rem; }
  .chip {
    display: inline-flex; align-items: center; gap: 0.35rem;
    padding: 0.25rem 0.6rem; border-radius: 6px;
    background: var(--mantle); border: 1px solid var(--surface0);
    color: var(--text); font: inherit; font-size: 0.85rem;
  }
  button.chip { cursor: pointer; }
  button.chip:hover { border-color: var(--surface2); }
  .chip.link { color: var(--blue); }
  .chip.warn { color: var(--peach); border-color: var(--peach); }
  .chip :global(.ok) { color: var(--green); }
  .dim { color: var(--subtext0); }
  .issues { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.3rem; }
  .issues li {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.35rem 0.6rem; border-radius: 6px;
    border: 1px solid var(--yellow); color: var(--text);
  }
  .issues li :global(svg) { color: var(--yellow); flex-shrink: 0; }
  .issues li.error { border-color: var(--red); }
  .issues li.error :global(svg) { color: var(--red); }
  .it-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .qc { display: flex; align-items: center; gap: 0.5rem; }
  .qc label, .card h2 {
    font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--subtext0); font-weight: 700; margin: 0;
  }
  .qc label { width: 7.5rem; flex-shrink: 0; }
  .qc input {
    flex: 1; min-width: 0;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  .qc input:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .btn {
    background: var(--surface0); color: var(--text);
    border: 1px solid transparent; border-radius: 3px;
    padding: 0.4rem 0.85rem; font: inherit; line-height: 1.2; cursor: pointer;
  }
  .btn:hover:not(:disabled) { background: var(--surface1); }
  .btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .btn.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  .btn.primary:hover:not(:disabled) { background: var(--lavender); }
  .hint { margin-left: auto; font-size: 0.8rem; color: var(--overlay1); }
  .problems { color: var(--peach); font-size: 0.85rem; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .problems.red { color: var(--red); }
  .ok-text { color: var(--green); }
  .grid { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 1rem; align-items: start; }
  @media (max-width: 1000px) { .grid { grid-template-columns: minmax(0, 1fr); } }
  /* Fleet: one line per folder, columns lined up across rows. */
  .frow {
    display: grid; grid-template-columns: minmax(10rem, 1.2fr) minmax(12rem, 1fr) minmax(0, 2.5fr) 6rem auto;
    align-items: center; gap: 0.8rem;
    padding: 0.35rem 0.8rem; border-bottom: 1px solid var(--base);
  }
  .frow:last-child { border-bottom: 0; }
  .f-folder, .f-state, .f-issue { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .f-age { text-align: right; }
  .f-certs { grid-column: 2 / -1; }
  @media (max-width: 1000px) {
    .frow { grid-template-columns: minmax(0, 1fr) auto; }
    .f-state, .f-issue, .f-age { grid-column: 1; }
    .frow .btn { grid-column: 2; grid-row: 1; }
  }
  .more {
    width: 100%; background: none; border: 0; padding: 0.45rem; font: inherit; font-size: 0.85rem;
    color: var(--blue); cursor: pointer; border-top: 1px solid var(--base);
  }
  .more:hover { text-decoration: underline; }
  .side { display: flex; flex-direction: column; gap: 1rem; min-width: 0; }
  .card { border: 1px solid var(--surface0); border-radius: 8px; background: var(--mantle); min-width: 0; }
  .card header {
    display: flex; align-items: center; gap: 0.45rem;
    padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--surface0);
    color: var(--subtext0);
  }
  .none { color: var(--overlay1); margin: 0; padding: 0.7rem 0.8rem; font-size: 0.9rem; }
  .row {
    display: flex; align-items: center; gap: 0.55rem;
    padding: 0.35rem 0.8rem; border-bottom: 1px solid var(--base);
    color: var(--overlay1);
  }
  .row:last-child { border-bottom: 0; }
  .ico { position: relative; display: inline-flex; flex-shrink: 0; color: var(--overlay1); }
  /* Liveness on the icon's corner: a row with no probe (a local shell, a
     folder that opts out, probing off) just shows its icon. */
  .badge {
    position: absolute; right: -3px; bottom: -2px;
    width: 7px; height: 7px; border-radius: 50%;
    border: 1.5px solid var(--mantle); background: var(--overlay0);
  }
  .badge.live { background: var(--blue); }
  .badge.up { background: var(--green); }
  .badge.down { background: var(--red); }
  .pill { width: 8px; height: 8px; border-radius: 50%; background: var(--overlay0); flex-shrink: 0; }
  .pill.up { background: var(--green); }
  .name {
    flex: 1; min-width: 0; display: flex; flex-direction: column; align-items: flex-start;
    background: none; border: 0; padding: 0; font: inherit; text-align: left; cursor: pointer; color: var(--text);
  }
  .nm { font-weight: 600; }
  .sub { color: var(--subtext0); font-size: 0.85rem; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .name:hover .nm { color: var(--blue); }
  .ago { color: var(--subtext0); font-size: 0.85rem; width: 5rem; text-align: right; flex-shrink: 0; }
  .btn.small { font-size: 0.85rem; padding: 0.2rem 0.65rem; flex-shrink: 0; }
  .ws { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.5rem; padding: 0.7rem 0.8rem; }
  .ws-card {
    display: flex; flex-direction: column; align-items: flex-start; gap: 0.1rem;
    padding: 0.5rem 0.65rem; border-radius: 6px; border: 1px solid var(--surface0);
    background: var(--base); color: var(--text); font: inherit; text-align: left; cursor: pointer; min-width: 0;
  }
  .ws-card:hover { border-color: var(--blue); }
  .ws-card .sub { font-size: 0.8rem; }
  .link { background: none; border: 0; padding: 0; font: inherit; color: var(--blue); cursor: pointer; }
  .link:hover { text-decoration: underline; }
  .keys { color: var(--overlay1); font-size: 0.85rem; margin: 0; }
</style>
