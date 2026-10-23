<script lang="ts">
  // Layout for a window opened via WindowDetachTab. The backend has
  // already populated the shared session pool - we only need to
  // recover the session(s) that should live on this window's tab and
  // render TerminalArea against them.
  //
  // The detached window observes the same Wails event bus as the main
  // window. pty_output / session_state events arrive for every
  // session, but xterm only writes data for sessions mapped into this
  // window's pane tree (via paneTabs.addTab below). Sessions that
  // belong to the main window pass through silently.

  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api } from "./api";
  import { sessions, paneTabs, view, tree, decodePaneLayoutsMulti, encodePaneLayouts } from "./stores.svelte";
  import { serializeOpenTabs } from "./lastSession.svelte";
  import { workspaces } from "./workspaces.svelte";
  import StatusBar from "./StatusBar.svelte";
  import { vncSessions } from "./vncState.svelte.ts";
  import { kafkaTabs } from "./kafkaState.svelte";
  import { dbTabs, dbTabTitle } from "./dbState.svelte";
  import TerminalArea from "./TerminalArea.svelte";
  import { broadcast } from "./broadcast.svelte";
  import { recording } from "./recording.svelte";

  interface Props {
    detachedTabKey: string;
    windowName: string;
  }
  let { detachedTabKey, windowName }: Props = $props();

  let recovering = $state(true);
  let recoveryError = $state<string | null>(null);

  // The "detachedTabKey" is the tabId from the original main-window
  // pane tree. Backend doesn't know about pane trees - it only owns
  // sessions. The detach IPC passes session ids via `?sessions=` and
  // the full pane tree via `?layout=` (URL-safe base64 JSON); both
  // are parsed below. Legacy detaches without those params fall
  // back to recovering all active sessions as a flat list.

  onMount(async () => {
    view.setTab("terminal");
    // Hook into the backend-owned broadcast group so this window sees
    // the same membership the main window does.
    broadcast.init();
    // Same deal for recording state - a session recorded in the main
    // window keeps its indicator after a detach.
    recording.init();
    // The tab bar resolves a tab's icon through tree.connectionById, and
    // only the main window loaded the tree, so every tab torn off into
    // this window showed the default glyph. Not awaited: an icon arriving
    // a moment late beats holding back the terminals.
    void tree.load().catch(console.warn);
    // Workspace frames need the list to name them (a tab carries only the
    // id); without it a workspace moved here lost its frame, and with it
    // save and close.
    void workspaces.load().catch(console.warn);
    try {
      // Parse the comma-separated session IDs passed by the main window.
      // Fall back to all active sessions if none were specified (legacy/manual open).
      const params = new URLSearchParams(window.location.search);
      const sessionParam = params.get("sessions");
      const layoutParam = params.get("layout") ?? "";
      const allowedIds = sessionParam ? new Set(sessionParam.split(",")) : null;
      // One tab from a drag-out, a whole window's worth from a restore.
      const layouts = decodePaneLayoutsMulti(layoutParam);

      // SSH sessions. Hydrate SessionStore for every session this
      // window owns; layout restore (below) maps leaves onto these
      // entries by sessionId.
      const live = (await api.sshActiveSessions()) ?? [];
      for (const s of live) {
        if (allowedIds && !allowedIds.has(s.session_id)) continue;
        if (sessions.tabs.find((t) => t.sessionId === s.session_id)) continue;
        sessions.add({
          sessionId: s.session_id,
          connectionId: s.connection_id,
          name: s.name,
          hostname: s.hostname,
          status: "connected",
        });
      }

      // Local PTY sessions. The detached window used to skip these
      // entirely (only ssh recovered), so a tear-off of a local
      // terminal tab landed in a window with the tab strip rendered
      // (sessionId visible) but no terminal - Terminal.svelte saw
      // no entry in sessions.tabs, isLocal() defaulted to false,
      // SSH IPCs went nowhere.
      const locals = (await api.localShellList()) ?? [];
      for (const l of locals) {
        if (allowedIds && !allowedIds.has(l.session_id)) continue;
        if (sessions.tabs.find((t) => t.sessionId === l.session_id)) continue;
        sessions.add({
          sessionId: l.session_id,
          connectionId: l.connection_id || "local:" + l.session_id,
          name: l.name || l.display || l.kind,
          hostname: l.kind,
          status: "connected",
          kind: "local",
        });
      }

      // VNC console sessions. Like local PTYs, they live on the backend
      // and are re-fetched (with a fresh ws token) so a detached console
      // tab can reconnect noVNC. vncSessions store gets populated too.
      const vncs = await vncSessions.refresh();
      for (const v of vncs) {
        if (allowedIds && !allowedIds.has(v.session_id)) continue;
        if (sessions.tabs.find((t) => t.sessionId === v.session_id)) continue;
        sessions.add({
          sessionId: v.session_id,
          connectionId: "",
          name: v.title,
          hostname: "",
          status: "connecting",
          kind: "vnc",
        });
      }

      // Kafka tabs: the cluster handle (and its consumers) live on the
      // backend, so the tab carries on where the other window left it.
      const kafkas = await kafkaTabs.refresh();
      for (const k of kafkas) {
        if (allowedIds && !allowedIds.has(k.id)) continue;
        if (sessions.tabs.find((t) => t.sessionId === k.id)) continue;
        sessions.add({
          sessionId: k.id,
          connectionId: k.connection_id,
          name: `${k.name} (Kafka)`,
          hostname: k.bootstrap.join(","),
          status: "connected",
          kind: "kafka",
        });
      }

      // Database tabs: the handle and its query sessions live on the
      // backend; DbPane adopts the tab list.
      const dbs = await dbTabs.refresh();
      for (const d of dbs) {
        if (allowedIds && !allowedIds.has(d.id)) continue;
        if (sessions.tabs.find((t) => t.sessionId === d.id)) continue;
        sessions.add({
          sessionId: d.id,
          connectionId: d.connection_id,
          name: dbTabTitle(d),
          hostname: d.addr,
          status: "connected",
          kind: "db",
        });
      }

      // Restore the pane tree from the serialized layout so splits and
      // group metadata survive the detach. If the main window didn't
      // ship a layout (legacy / direct open), fall back to one tab
      // per recovered session.
      if (layouts.length > 0) {
        for (const layout of layouts) paneTabs.addTabFromLayout(layout);
        // A whole workspace moved here is this window's active one, so the
        // status bar offers saving it.
        const ws = layouts.find((l) => l.workspaceId)?.workspaceId;
        if (ws) workspaces.activeId = ws;
      } else {
        for (const s of live) {
          if (allowedIds && !allowedIds.has(s.session_id)) continue;
          paneTabs.addTab(s.session_id, s.name);
        }
        for (const l of locals) {
          if (allowedIds && !allowedIds.has(l.session_id)) continue;
          paneTabs.addTab(l.session_id, l.name || l.display || l.kind);
        }
        for (const v of vncs) {
          if (allowedIds && !allowedIds.has(v.session_id)) continue;
          paneTabs.addVncTab(v.session_id, v.title);
        }
        for (const k of kafkas) {
          if (allowedIds && !allowedIds.has(k.id)) continue;
          paneTabs.addKafkaTab(k.id, `${k.name} (Kafka)`);
        }
        for (const d of dbs) {
          if (allowedIds && !allowedIds.has(d.id)) continue;
          paneTabs.addDbTab(d.id, dbTabTitle(d));
        }
      }
    } catch (e: any) {
      recoveryError = errMsg(e);
    } finally {
      recovering = false;
    }
  });

  // Session IDs this window owns - parsed from ?sessions= at mount time.
  const ownedSessions = new URLSearchParams(window.location.search).get("sessions") ?? "";

  // Auto-close the detached window once its last tab is gone. This
  // covers the Ctrl+D case (closeOnCleanExit drops the leaf, then the
  // tab - see SessionStore.autoCloseSession): the main window bounces
  // to the Connections view at that point, but here there's no view
  // to bounce to, so we close the OS window instead.
  //
  // Wait until at least one tab has appeared before arming the
  // auto-close; otherwise a recovery that finds zero matching
  // sessions (e.g. backend pool already cleared) would slam the
  // window shut a frame after open. The user's complaint about
  // "instant window close" pointed straight at this.
  let hadTabs = $state(false);
  $effect(() => {
    if (paneTabs.tabs.length > 0) hadTabs = true;
  });
  $effect(() => {
    if (recovering) return;
    if (!hadTabs) return;
    if (paneTabs.tabs.length === 0) {
      api.windowCloseSelf(windowName).catch(console.warn);
    }
  });

  // This window's share of the last session: its open tabs, handed to the
  // backend on every change. The backend keeps them only if the app quits
  // with this window open (a close by hand or a redock forgets them).
  // Sent at once, not debounced: once the quit has begun the backend takes
  // no more saves (the tabs are being torn down then), so a save held back
  // for a few hundred ms would be lost and an older snapshot restored. The
  // JSON comparison keeps unchanged reruns off the wire.
  let lastSaved = "";
  $effect(() => {
    if (recovering) return;
    // Read the snapshot here so every tab, pane and session it covers is
    // a dependency of this effect.
    const json = JSON.stringify(serializeOpenTabs());
    if (json === lastSaved) return;
    lastSaved = json;
    api.detachedSessionSave(windowName, json).catch(console.warn);
  });

  async function redock() {
    // Ship the layout of EVERY tab in this window back so the main window
    // rebuilds them all (splits / group meta intact) instead of losing all
    // but the first. addTabFromLayout regenerates tab/pane ids so collisions
    // with the main window are impossible.
    const layout = paneTabs.tabs.length > 0 ? encodePaneLayouts(paneTabs.tabs) : "";
    try {
      await api.windowRedockTab(detachedTabKey, ownedSessions, layout);
    } catch (e) {
      console.warn("redock signal failed", e);
    }
    try {
      await api.windowCloseSelf(windowName);
    } catch (e) {
      console.warn("close-self failed", e);
    }
  }
</script>

<div class="detached">
  <div class="topbar">
    <span class="title">Detached window - {detachedTabKey}</span>
    <button class="redock" onclick={redock} title="Send this tab back to the main window">
      ⤴ Re-dock to main window
    </button>
  </div>

  {#if recovering}
    <div class="hint">Restoring sessions…</div>
  {:else if recoveryError}
    <div class="err">Recovery failed: {recoveryError}</div>
  {:else}
    <TerminalArea />
  {/if}
  <StatusBar />
</div>

<style>
  .detached {
    display: grid;
    grid-template-rows: 32px 1fr auto;
    height: 100vh;
    background: var(--base);
    color: var(--text);
  }
  .topbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.3rem 0.8rem;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.8rem;
  }
  .title { color: var(--subtext0); }
  .redock {
    background: var(--blue);
    color: var(--on-accent);
    border: 0;
    padding: 0.25rem 0.7rem;
    border-radius: 3px;
    cursor: pointer;
    font: inherit;
    font-size: 0.8rem;
    font-weight: 600;
  }
  .redock:hover { background: var(--lavender); }
  .hint, .err { padding: 1rem; color: var(--overlay0); }
  .err { color: var(--red); }
</style>
