<script lang="ts" module>
  // An app-level command surfaced in the palette. Supplied by
  // App.svelte (the actions close over App state - settings view,
  // local shell opener, vault lock flow). Workspace-open actions are
  // built inside the palette from the workspaces store.
  export interface PaletteAction {
    id: string;
    title: string;
    hint?: string; // verb shown on the ↵ hint, default "run"
    keywords?: string[];
    run: () => void | Promise<void>;
  }
</script>

<script lang="ts">
  import { tree, selection, sessions, paneTabs, view } from "./stores.svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import { api } from "./api";
  import { fuzzyMatch, highlightSegments, type FuzzyMatch } from "./fuzzy";
  import { clickOutside } from "./clickOutside";
  import type { Folder, Connection, PortForward, ForwardStatus, ProxyBookmark } from "./api";
  import { IconFolder, IconHost, IconTunnel, IconExternalLink, IconAction, IconWorkspace, dynamicEntryIcon } from "./iconMap";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { workspaces } from "./workspaces.svelte";
  import type { Component } from "svelte";

  interface Props {
    onClose: () => void;
    actions?: PaletteAction[];
  }
  let { onClose, actions = [] }: Props = $props();

  // Refresh the workspace list once per palette open so "Open
  // workspace: X" rows reflect reality.
  $effect(() => {
    workspaces.load().catch(() => {});
  });

  // Pre-fetch dynamic-inventory entries when the palette opens so a
  // typed query can match VMs in dynamic folders that haven't been
  // expanded yet in the tree. Idempotent - only fetches folders
  // whose entries aren't already cached.
  $effect(() => {
    for (const id of Object.keys(tree.dynamicFolders)) {
      if (!tree.dynamicEntries[id]) {
        tree.loadDynamicEntries(id);
      }
    }
  });

  // Pre-fetch all port-forwards + currently running ones so the
  // palette can offer tunnel toggles and bookmark launchers across
  // every connection in a single IPC pair. Refreshed once at open
  // time only - we don't poll inside the modal; the user closes and
  // reopens for fresh state, same as for the rest of the index.
  let allForwards = $state<PortForward[]>([]);
  let runningForwardIds = $state<Set<string>>(new Set());
  $effect(() => {
    (async () => {
      try {
        allForwards = (await api.forwardsListAll()) ?? [];
      } catch { allForwards = []; }
      try {
        const live = (await api.forwardsActive("")) ?? [];
        runningForwardIds = new Set(
          live.filter((f: ForwardStatus) => f.state === "listening").map((f) => f.id),
        );
      } catch { runningForwardIds = new Set(); }
    })();
  });

  let query = $state("");
  let activeIdx = $state(0);
  let inputEl: HTMLInputElement | undefined = $state();
  let listEl: HTMLDivElement | undefined = $state();

  // Build the searchable index: every folder + connection with a
  // human-readable label that includes its folder path. Recompute when the
  // tree changes (rarely during the modal's lifetime, but cheap regardless).
  type Entry =
    | { kind: "folder"; folder: Folder; label: string; haystacks: string[] }
    | { kind: "connection"; conn: Connection; label: string; haystacks: string[] }
    | {
        kind: "dynamic_entry";
        folderId: string;
        entryId: string;
        name: string;
        hostname: string;
        status: string;
        // Provider entry kind ("host" / "server" / "guest_vm" /
        // "guest_lxc"). Drives the row icon so VM / LXC / host
        // each get the right visual.
        entryKind: string;
        tags: string[];
        label: string;
        haystacks: string[];
      }
    | {
        // Port-forward toggle. Carries the spec + the cached running
        // status (refreshed when the palette opens) so the row can
        // render the correct verb and ↵-hint without an extra IPC.
        kind: "forward";
        spec: PortForward;
        running: boolean;
        label: string;
        haystacks: string[];
      }
    | {
        // Bookmark open. Belongs to a specific dynamic forward; we
        // need the forward id so SshLaunchBrowser routes through the
        // right SOCKS5 listener. `running` mirrors the parent
        // forward's state - if false, the action will start it
        // first (same UX as TunnelPopover).
        kind: "bookmark";
        spec: PortForward;
        bookmark: ProxyBookmark;
        running: boolean;
        label: string;
        haystacks: string[];
      }
    | {
        // App command (open settings, lock vault, ...) or a
        // workspace-open. `workspace` only switches the row icon.
        kind: "action";
        action: PaletteAction;
        workspace: boolean;
        label: string;
        haystacks: string[];
      };

  const entries = $derived<Entry[]>(buildIndex());

  function buildIndex(): Entry[] {
    const out: Entry[] = [];
    for (const f of tree.folders) {
      const path = folderPath(f.id);
      out.push({
        kind: "folder",
        folder: f,
        label: path || f.name,
        haystacks: [path || f.name, f.name],
      });
    }
    for (const c of tree.connections) {
      const folderPathStr = c.folder_id ? folderPath(c.folder_id) : "";
      const label = folderPathStr ? `${folderPathStr} / ${c.name}` : c.name;
      const haystacks = [c.name, c.hostname ?? "", folderPathStr, ...(c.tags ?? [])];
      out.push({ kind: "connection", conn: c, label, haystacks });
    }
    // Dynamic-inventory entries: every cached child of every dynamic
    // folder. Indexed by `folder path / entry name` so they sort
    // alongside regular connections in the same namespace.
    for (const [folderId, entries] of Object.entries(tree.dynamicEntries)) {
      const folderPathStr = folderPath(folderId);
      for (const e of entries) {
        const label = folderPathStr ? `${folderPathStr} / ${e.name}` : e.name;
        const tags = e.tags ?? [];
        out.push({
          kind: "dynamic_entry",
          folderId,
          entryId: e.id,
          name: e.name,
          hostname: e.hostname,
          status: e.status,
          entryKind: e.kind,
          tags,
          label,
          haystacks: [e.name, e.hostname, folderPathStr, ...tags],
        });
      }
    }
    // Port-forward toggles + their bookmarks. We render the parent
    // connection name in the label so the user can disambiguate
    // "Postgres tunnel" rows that exist on multiple bastions.
    for (const f of allForwards) {
      const conn = tree.connections.find((c) => c.id === f.connection_id);
      const connName = conn?.name ?? f.connection_id;
      const desc = f.description || forwardSpecSummary(f);
      const label = `${connName} · ${desc}`;
      const haystacks = [desc, connName, conn?.hostname ?? "", "tunnel", "forward"];
      out.push({
        kind: "forward",
        spec: f,
        running: runningForwardIds.has(f.id),
        label,
        haystacks,
      });
      if (f.kind === "dynamic" && f.bookmarks?.length) {
        for (const bm of f.bookmarks) {
          out.push({
            kind: "bookmark",
            spec: f,
            bookmark: bm,
            running: runningForwardIds.has(f.id),
            label: `${connName} · ${bm.name || bm.url}`,
            haystacks: [bm.name ?? "", bm.url, connName, "bookmark", "browser"],
          });
        }
      }
    }
    // App commands (from App.svelte) + workspace-open rows. Hidden on
    // an empty query like forwards/bookmarks; a leading ">" narrows
    // the palette to just these.
    for (const a of actions) {
      out.push({
        kind: "action",
        action: a,
        workspace: false,
        label: a.title,
        haystacks: [a.title, ...(a.keywords ?? [])],
      });
    }
    for (const w of workspaces.list) {
      out.push({
        kind: "action",
        workspace: true,
        action: {
          id: "workspace:" + w.id,
          title: `Open workspace: ${w.name}`,
          hint: "open",
          run: async () => {
            try {
              await workspaces.open(w.id);
              view.setTab("terminal");
            } catch (e: any) {
              toast.err(`Open workspace failed: ${e?.message ?? String(e)}`);
            }
          },
        },
        label: `Open workspace: ${w.name}`,
        haystacks: [w.name, "workspace"],
      });
    }
    return out;
  }

  function forwardSpecSummary(f: PortForward): string {
    if (f.kind === "dynamic") return `SOCKS5 :${f.local_port ?? "?"}`;
    const dir = f.kind === "local" ? "L" : "R";
    return `${dir} ${f.local_port ?? "?"} → ${f.remote_host ?? "?"}:${f.remote_port ?? "?"}`;
  }

  function folderPath(folderId: string | null): string {
    if (!folderId) return "";
    const segs: string[] = [];
    let cur: Folder | null = tree.folderById(folderId);
    let guard = 0;
    while (cur && guard++ < 100) {
      segs.unshift(cur.name);
      cur = cur.parent_id ? tree.folderById(cur.parent_id) : null;
    }
    return segs.join(" / ");
  }

  // Match each entry. Score is the BEST score across its haystacks; we keep
  // the positions from that best haystack for highlighting the label.
  interface Result {
    entry: Entry;
    score: number;
    matchAgainst: string;
    positions: number[];
  }

  const results = $derived<Result[]>(rank(query));

  function rank(q: string): Result[] {
    // ">" prefix narrows the palette to commands only (VS Code
    // convention). The rest of the query fuzzy-matches within them.
    let pool = entries;
    let actionsOnly = false;
    if (q.trimStart().startsWith(">")) {
      actionsOnly = true;
      pool = entries.filter((e) => e.kind === "action");
      q = q.trimStart().slice(1).trim();
    }
    if (!q) {
      // Empty query: just show first ~50 entries in their natural order, with
      // connections first because they're the usual target. Forwards /
      // bookmarks / commands are excluded from the empty view - they'd
      // swamp the list, and the user will type (or ">") to find them.
      const list: Entry[] = actionsOnly
        ? pool.slice(0, 50)
        : [
            ...pool.filter((e) => e.kind === "connection"),
            ...pool.filter((e) => e.kind === "dynamic_entry"),
            ...pool.filter((e) => e.kind === "folder"),
          ].slice(0, 50);
      return list.map((e) => ({
        entry: e,
        score: 0,
        matchAgainst: e.label,
        positions: [],
      }));
    }
    const out: Result[] = [];
    for (const e of pool) {
      let best: { score: number; matchAgainst: string; positions: number[] } | null = null;
      // Also try the merged label as a haystack so "production/web" matches.
      const tryHaystacks = [e.label, ...e.haystacks];
      for (const h of tryHaystacks) {
        if (!h) continue;
        const m: FuzzyMatch | null = fuzzyMatch(q, h);
        if (!m) continue;
        if (best === null || m.score < best.score) {
          best = { score: m.score, matchAgainst: h, positions: m.positions };
        }
      }
      if (best !== null) {
        // Connections + dynamic entries get a small score bonus so
        // they outrank folders on similar scores - the user usually
        // wants to act on a host, not navigate to a folder. Forwards
        // and bookmarks get a small penalty so a query that matches
        // both a host and a tunnel description doesn't surface the
        // tunnel first.
        let bonus = 0;
        if (e.kind === "connection" || e.kind === "dynamic_entry") bonus = -0.5;
        else if (e.kind === "forward" || e.kind === "bookmark" || e.kind === "action") bonus = 0.5;
        out.push({
          entry: e,
          score: best.score + bonus,
          matchAgainst: best.matchAgainst,
          positions: best.positions,
        });
      }
    }
    out.sort((a, b) => a.score - b.score);
    return out.slice(0, 50);
  }

  // Reset active index whenever results change.
  $effect(() => {
    void results.length;
    activeIdx = 0;
  });

  function chooseResult(r: Result) {
    if (r.entry.kind === "action") {
      const a = r.entry.action;
      onClose();
      queueMicrotask(() => {
        Promise.resolve(a.run()).catch((e: any) => {
          toast.err(`${a.title} failed: ${e?.message ?? String(e)}`);
        });
      });
      return;
    }
    if (r.entry.kind === "folder") {
      selection.select({ kind: "folder", id: r.entry.folder.id });
      view.setTab("connections");
      onClose();
      return;
    }
    if (r.entry.kind === "dynamic_entry") {
      const e = r.entry;
      onClose();
      queueMicrotask(async () => {
        if (e.status === "stopped") {
          const ok = await showConfirm({
            title: "Host is stopped",
            message: `${e.name} is stopped in the provider.\n\nConnect anyway?`,
            okLabel: "Connect",
          });
          if (!ok) return;
        }
        try {
          const res = await api.sshConnectDynamic(e.folderId, e.entryId);
          sessions.add({
            sessionId: res.session_id,
            connectionId: "dyn:" + e.entryId,
            name: e.name,
            hostname: e.hostname,
            status: "connected",
          });
          paneTabs.addTab(res.session_id, e.name);
          view.setTab("terminal");
        } catch (err: any) {
          toast.err(`Connect failed: ${err?.message ?? String(err)}`);
        }
      });
      return;
    }
    if (r.entry.kind === "forward") {
      const spec = r.entry.spec;
      const running = r.entry.running;
      onClose();
      queueMicrotask(async () => {
        try {
          if (running) {
            await api.forwardsStop(spec.id);
          } else {
            const sid = await ensureSessionForConnection(spec.connection_id);
            if (!sid) return;
            await api.forwardsStart(spec.id, sid);
          }
        } catch (e) {
          toast.err(`Tunnel ${running ? "stop" : "start"} failed: ${(e as any)?.message ?? String(e)}`);
        }
      });
      return;
    }
    if (r.entry.kind === "bookmark") {
      const spec = r.entry.spec;
      const bm = r.entry.bookmark;
      const running = r.entry.running;
      onClose();
      queueMicrotask(async () => {
        try {
          if (!running) {
            const sid = await ensureSessionForConnection(spec.connection_id);
            if (!sid) return;
            await api.forwardsStart(spec.id, sid);
          }
          await api.sshLaunchBrowser(spec.id, bm.url);
        } catch (e) {
          toast.err(`Open bookmark failed: ${(e as any)?.message ?? String(e)}`);
        }
      });
      return;
    }
    // Connection: connect immediately (the whole point of the palette).
    // Route through connectDefault so local-shell and VNC-default
    // connections do the right thing (not a blind SSH dial).
    const c = r.entry.conn;
    onClose();
    queueMicrotask(async () => {
      const ok = await connectionActions.connectDefault(c.id);
      if (!ok) {
        const last = connectionActions.lastConnectError[c.id];
        toast.err(`Connect failed: ${last?.message ?? "connect failed"}`);
      }
    });
  }

  // Pick the most recently opened connected session matching the given
  // connection, mirroring PortForwards.svelte's behaviour. Returns
  // null when no live session exists - the caller surfaces the error.
  function activeSessionForConnection(connectionId: string): string | null {
    const match = sessions.tabs
      .filter((t) => t.connectionId === connectionId && t.status === "connected")
      .at(-1);
    return match?.sessionId ?? null;
  }

  // ensureSessionForConnection returns a live session id for the given
  // connection, opening one on demand if nothing is connected yet.
  // The auto-opened session gets a tab so it's visible (and closeable)
  // to the user - silently held sessions are confusing later. Returns
  // null on failure and surfaces an alert.
  async function ensureSessionForConnection(connectionId: string): Promise<string | null> {
    const existing = activeSessionForConnection(connectionId);
    if (existing) return existing;
    const c = tree.connectionById(connectionId);
    if (!c) {
      toast.err(`Connection not found.`);
      return null;
    }
    try {
      const res = await api.sshConnect(connectionId);
      sessions.add({
        sessionId: res.session_id,
        connectionId,
        name: c.name,
        hostname: c.hostname,
        status: "connected",
      });
      paneTabs.addTab(res.session_id, c.name);
      return res.session_id;
    } catch (e) {
      toast.err(`Connect failed: ${(e as any)?.message ?? String(e)}`);
      return null;
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = Math.min(results.length - 1, activeIdx + 1);
      scrollActiveIntoView();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = Math.max(0, activeIdx - 1);
      scrollActiveIntoView();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const r = results[activeIdx];
      if (r) chooseResult(r);
    }
  }

  function scrollActiveIntoView() {
    if (!listEl) return;
    queueMicrotask(() => {
      const el = listEl?.querySelector<HTMLElement>(`[data-idx="${activeIdx}"]`);
      el?.scrollIntoView({ block: "nearest" });
    });
  }

  $effect(() => {
    // Focus the input on mount.
    if (inputEl) {
      inputEl.focus();
    }
  });

  function iconFor(e: Entry): Component {
    if (e.kind === "folder") return IconFolder;
    if (e.kind === "forward") return IconTunnel;
    if (e.kind === "bookmark") return IconExternalLink;
    if (e.kind === "dynamic_entry") return dynamicEntryIcon(e.entryKind);
    if (e.kind === "action") return e.workspace ? IconWorkspace : IconAction;
    return IconHost;
  }

  function rowKey(e: Entry): string {
    switch (e.kind) {
      case "folder": return "folder:" + e.folder.id;
      case "connection": return "conn:" + e.conn.id;
      case "dynamic_entry": return "dyn:" + e.entryId;
      case "forward": return "fwd:" + e.spec.id;
      case "bookmark": return "bm:" + e.spec.id + ":" + e.bookmark.url;
      case "action": return "act:" + e.action.id;
    }
  }

  // True when the entry already has a live connected session - used
  // to render the label in green so the palette doubles as a quick
  // status indicator. Folders, forwards and bookmarks don't have a
  // direct "is connected" notion and stay default-coloured.
  function isEntryConnected(e: Entry): boolean {
    return entrySessionCount(e) > 0;
  }

  function entrySessionCount(e: Entry): number {
    if (e.kind === "connection") {
      return sessions.tabs.filter(
        (t) => t.connectionId === e.conn.id && t.status === "connected",
      ).length;
    }
    if (e.kind === "dynamic_entry") {
      const dynId = "dyn:" + e.entryId;
      return sessions.tabs.filter(
        (t) => t.connectionId === dynId && t.status === "connected",
      ).length;
    }
    return 0;
  }
</script>

<div
  class="overlay"
  role="dialog"
  aria-modal="true"
  onkeydown={(e) => { if (e.key === "Escape") onClose(); }}
  tabindex="-1"
>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="modal"
    role="document"
    use:clickOutside={{ onOutside: onClose }}
    onkeydown={(e) => {
      // Handle nav here so the input's own keystrokes don't bubble up
      // to the window listener (which would re-trigger the global
      // tab-cycle / Ctrl+K shortcuts in App.svelte). The window
      // listener is intentionally bypassed for this palette since
      // we have to swallow propagation to keep typed input from
      // reaching App-level shortcuts; that swallow also blocks the
      // <svelte:window onkeydown={onKey}> we used to rely on for
      // arrow / enter / escape.
      if (
        e.key === "ArrowDown" ||
        e.key === "ArrowUp" ||
        e.key === "Enter" ||
        e.key === "Escape"
      ) {
        onKey(e);
        e.stopPropagation();
      } else {
        // Plain text input / Ctrl+K toggle - still need to stop
        // propagation so the parent shortcuts don't fire while the
        // user is typing into the search box.
        e.stopPropagation();
      }
    }}
  >
    <input
      bind:this={inputEl}
      bind:value={query}
      placeholder="Search connections, folders, tunnels, bookmarks… (&gt; for commands)"
      spellcheck="false"
      autocomplete="off"
    />
    <div class="list" bind:this={listEl}>
      {#if results.length === 0}
        <div class="empty">No matches.</div>
      {/if}
      {#each results as r, i (rowKey(r.entry))}
        {@const labelMatched = r.matchAgainst === r.entry.label}
        {@const segs = labelMatched
          ? highlightSegments(r.entry.label, r.positions)
          : [{ text: r.entry.label, match: false }]}
        {@const isConn = r.entry.kind === "connection"}
        {@const Ic = iconFor(r.entry)}
        {@const sessCount = entrySessionCount(r.entry)}
        {@const connected = sessCount > 0}
        <div
          class="row"
          class:active={i === activeIdx}
          data-idx={i}
          role="button"
          tabindex="0"
          onclick={() => chooseResult(r)}
          onmousemove={() => (activeIdx = i)}
          onkeydown={(e) => {
            if (e.key === "Enter") { e.preventDefault(); chooseResult(r); }
          }}
        >
          <span
            class="icon"
            class:tunnel-on={r.entry.kind === "forward" && r.entry.running}
            class:connected
          ><Ic size={14} /></span>
          <div class="meta">
            <div class="label" class:connected>
              {#each segs as s}
                {#if s.match}<mark>{s.text}</mark>{:else}<span>{s.text}</span>{/if}
              {/each}
              {#if r.entry.kind === "forward" && r.entry.running}
                <span class="pill pill-on">running</span>
              {:else if connected}
                <span class="pill pill-on">connected{sessCount > 1 ? ` (${sessCount})` : ""}</span>
              {/if}
            </div>
            {#if isConn && r.entry.kind === "connection"}
              {@const host = r.entry.conn.hostname}
              {@const ctags = r.entry.conn.tags ?? []}
              {#if host || ctags.length > 0}
                <div class="sub">
                  {#if host}<span class="sub-host">{host}</span>{/if}
                  {#each ctags as t}
                    <span
                      class="sub-tag"
                      class:hit={query.trim() && t.toLowerCase().includes(query.trim().toLowerCase())}
                    >{t}</span>
                  {/each}
                </div>
              {/if}
            {:else if r.entry.kind === "dynamic_entry"}
              <div class="sub">
                <span class="sub-host">{r.entry.hostname}</span>
                {#if r.entry.status === "stopped"}<span class="dyn-stopped">stopped</span>{/if}
                {#each r.entry.tags as t}
                  <span
                    class="sub-tag"
                    class:hit={query.trim() && t.toLowerCase().includes(query.trim().toLowerCase())}
                  >{t}</span>
                {/each}
              </div>
            {:else if r.entry.kind === "bookmark"}
              {@const bmConn = tree.connections.find((c) => c.id === (r.entry as any).spec.connection_id)}
              {@const bmConnected = !!bmConn && sessions.tabs.some((t) => t.connectionId === bmConn.id && t.status === "connected")}
              <div class="sub">
                {#if bmConn}<span class="bm-conn" class:on={bmConnected}>{bmConn.name}</span> · {/if}{r.entry.bookmark.url}
              </div>
            {:else if r.entry.kind === "forward"}
              {@const fwdConn = tree.connections.find((c) => c.id === (r.entry as any).spec.connection_id)}
              {@const fwdConnected = !!fwdConn && sessions.tabs.some((t) => t.connectionId === fwdConn.id && t.status === "connected")}
              {#if fwdConn}
                <div class="sub"><span class="bm-conn" class:on={fwdConnected}>{fwdConn.name}</span></div>
              {/if}
            {/if}
          </div>
          {#if isConn || r.entry.kind === "dynamic_entry"}
            <span class="hint">↵ connect</span>
          {:else if r.entry.kind === "forward"}
            <span class="hint">↵ {r.entry.running ? "stop" : "start"}</span>
          {:else if r.entry.kind === "bookmark"}
            <span class="hint">↵ open</span>
          {:else if r.entry.kind === "action"}
            <span class="hint">↵ {r.entry.action.hint ?? "run"}</span>
          {:else}
            <span class="hint">↵ open</span>
          {/if}
        </div>
      {/each}
    </div>
    <footer>
      <span><kbd>↑↓</kbd> navigate</span>
      <span><kbd>↵</kbd> select</span>
      <span><kbd>&gt;</kbd> commands</span>
      <span><kbd>Esc</kbd> close</span>
    </footer>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: flex-start; justify-content: center;
    z-index: 300;
    padding-top: 12vh;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 8px;
    width: min(640px, 92vw);
    max-height: 70vh;
    display: flex; flex-direction: column;
    overflow: hidden;
    box-shadow: 0 20px 60px rgba(0,0,0,0.6);
  }
  input {
    background: var(--mantle); color: var(--text);
    border: 0;
    border-bottom: 1px solid var(--surface0);
    padding: 0.7rem 0.9rem;
    font: inherit;
    font-size: 0.95rem;
    outline: none;
  }
  .list {
    flex: 1;
    overflow: auto;
    padding: 0.3rem 0;
  }
  .empty {
    color: var(--overlay0);
    padding: 0.8rem 0.9rem;
    font-size: 0.85rem;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0.8rem;
    cursor: pointer;
  }
  .row.active {
    background: var(--surface0);
  }
  .icon {
    width: 1.4rem;
    text-align: center;
    font-size: 0.95rem;
  }
  .meta {
    flex: 1;
    min-width: 0;
  }
  .label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: 0.9rem;
  }
  .label mark {
    background: transparent;
    color: var(--yellow);
    font-weight: 600;
  }
  .sub {
    color: var(--overlay0);
    font-size: 0.72rem;
    margin-top: 0.05rem;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.3rem;
  }
  .sub-host { color: var(--overlay0); }
  .sub-tag {
    display: inline-block;
    padding: 0.02rem 0.35rem;
    border-radius: 2px;
    background: var(--surface0);
    color: var(--overlay1);
    font-size: 0.65rem;
    font-family: ui-sans-serif, system-ui, sans-serif;
  }
  .sub-tag.hit { background: var(--yellow); color: var(--base); font-weight: 600; }
  .dyn-stopped {
    color: var(--yellow);
    background: #292318;
    padding: 0.02rem 0.3rem;
    border-radius: 2px;
    margin-left: 0.4rem;
    font-size: 0.65rem;
  }
  .icon.tunnel-on { color: var(--green); }
  .icon.connected { color: var(--green); }
  .label.connected { color: var(--green); }
  .bm-conn { color: var(--blue); }
  .bm-conn.on { color: var(--green); }
  .pill {
    display: inline-block;
    margin-left: 0.4rem;
    padding: 0.02rem 0.3rem;
    border-radius: 999px;
    font-size: 0.6rem;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    vertical-align: middle;
    position: relative;
    top: -1px;
  }
  .pill-on { color: var(--on-accent); background: var(--green); }
  .hint {
    color: var(--overlay0);
    font-size: 0.7rem;
  }
  footer {
    display: flex;
    gap: 1rem;
    padding: 0.4rem 0.9rem;
    border-top: 1px solid var(--surface0);
    color: var(--overlay0);
    font-size: 0.72rem;
  }
  kbd {
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 2px;
    padding: 0.05rem 0.3rem;
    font-size: 0.7rem;
    color: var(--subtext0);
    font-family: ui-monospace, monospace;
    margin-right: 0.2rem;
  }
</style>
