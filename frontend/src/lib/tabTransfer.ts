// Tabs arriving in this window from another one: a tab dragged out of a
// detached window (redock), or one sent with "Send to window". The main
// window takes both from App.svelte, which stays mounted while the main
// window has no tabs (TerminalArea does not); a detached window takes
// "Send to window" in its TerminalArea.

import { api } from "./api";
import { sessions, paneTabs, view, decodePaneLayout } from "./stores.svelte";
import { kafkaTabs } from "./kafkaState.svelte";
import { dbTabs, dbTabTitle } from "./dbState.svelte";

// The custom type a tab drag carries (TerminalArea dragstart). Nothing
// else sets it; file drops carry "Files".
export const TAB_DRAG_TYPE = "application/x-ssh-tool-tab";

// Reconstruct a tab in THIS window from a pending-drag payload (used by the
// detached-drop redock AND the "Send to window" receive). Does not remove
// anything from the source - the caller decides that.
export async function reconstructTabFromPayload(p: { tab_id: string; sessions: string; layout: string }) {
  const sessionIds = p.sessions ? p.sessions.split(",") : [];
  const live = (await api.sshActiveSessions()) ?? [];
  // Ensure every session the tab references exists in this window's store.
  for (const s of live) {
    if (!sessionIds.includes(s.session_id)) continue;
    if (!sessions.tabs.find((t) => t.sessionId === s.session_id)) {
      sessions.add({
        sessionId: s.session_id,
        connectionId: s.connection_id,
        name: s.name,
        hostname: s.hostname,
        status: "connected",
      });
    }
  }
  // Kafka tabs travel too; their handle lives on the backend.
  try {
    for (const k of await kafkaTabs.refresh()) {
      if (!sessionIds.includes(k.id) || sessions.tabs.find((t) => t.sessionId === k.id)) continue;
      sessions.add({
        sessionId: k.id,
        connectionId: k.connection_id,
        name: `${k.name} (Kafka)`,
        hostname: k.bootstrap.join(","),
        kind: "kafka",
        status: "connected",
      });
    }
  } catch (e) {
    console.warn("kafka handles", e);
  }
  try {
    for (const d of await dbTabs.refresh()) {
      if (!sessionIds.includes(d.id) || sessions.tabs.find((t) => t.sessionId === d.id)) continue;
      sessions.add({
        sessionId: d.id,
        connectionId: d.connection_id,
        name: dbTabTitle(d),
        hostname: d.addr,
        kind: "db",
        status: "connected",
      });
    }
  } catch (e) {
    console.warn("db handles", e);
  }
  const layout = decodePaneLayout(p.layout ?? "");
  if (layout) {
    paneTabs.addTabFromLayout(layout);
  } else {
    for (const sid of sessionIds) {
      const s = live.find((x) => x.session_id === sid);
      if (!s) continue;
      paneTabs.addTab(s.session_id, s.name);
    }
  }
  view.setTab("terminal");
}

// Claim a pending drag from a detached window and rebuild its tab here.
// The source window removes the tab itself (tab_drag_accepted) and closes
// once it has none left, so its other tabs stay where they are.
export async function acceptTabDrag() {
  try {
    const p = await api.windowAcceptTabDrag();
    if (p) await reconstructTabFromPayload(p);
  } catch {
    // No pending drag - silently ignore
  }
}
