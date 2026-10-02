// Quick connect: SSH to a typed-in [user@]host[:port] without saving a
// connection. The backend keeps the target in memory as "quick:<uuid>" (see
// app_quick.go), so these tabs reconnect and split like any other, but they
// are left out of reopen-last-session and workspaces (specForSession), and
// "Save as connection" turns one into a real connection later.
//
// The last targets typed are remembered on this machine only, as input
// suggestions: target, folder and credential id - never a secret.

import { api } from "./api";
import { paneTabs, sessions, view, tree, selection } from "./stores.svelte";
import { withTakeover } from "./connectionActions.svelte";
import { toast } from "./toast.svelte.ts";

export interface QuickTarget {
  target: string;
  folderId?: string;
  credentialId?: string;
}

const HISTORY_KEY = "quick_connect_history";
const HISTORY_MAX = 10;

function readHistory(): QuickTarget[] {
  try {
    const v = JSON.parse(localStorage.getItem(HISTORY_KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x) => x && typeof x.target === "string") : [];
  } catch {
    return [];
  }
}

export const isQuickId = (connectionId: string | undefined | null) =>
  !!connectionId && connectionId.startsWith("quick:");

class QuickConnectStore {
  open = $state(false);
  // Pre-filled target when opened from the palette with text already typed.
  initial = $state("");
  history = $state<QuickTarget[]>(readHistory());

  show(target = "") {
    this.initial = target;
    this.open = true;
  }

  close() {
    this.open = false;
  }

  private remember(t: QuickTarget) {
    const next = [t, ...this.history.filter((h) => h.target !== t.target)].slice(0, HISTORY_MAX);
    this.history = next;
    try { localStorage.setItem(HISTORY_KEY, JSON.stringify(next)); } catch { /* ignore */ }
  }

  forget(target: string) {
    this.history = this.history.filter((h) => h.target !== target);
    try { localStorage.setItem(HISTORY_KEY, JSON.stringify(this.history)); } catch { /* ignore */ }
  }

  // Throws the connect error so the dialog can show it and stay open.
  async connect(t: QuickTarget): Promise<boolean> {
    const res = await withTakeover(() =>
      api.sshQuickConnect({ target: t.target, folder_id: t.folderId, credential_id: t.credentialId }),
    );
    if (!res.ok && res.cancelled) return false;
    if (!res.ok) throw res.error;
    const r = res.value;
    this.remember(t);
    sessions.add({
      sessionId: r.session_id,
      connectionId: r.connection_id,
      name: r.name,
      hostname: r.hostname,
      status: "connected",
    });
    paneTabs.addTab(r.session_id, r.name);
    view.setTab("terminal");
    return true;
  }

  // Save the quick connection behind a session as a real connection, point
  // the session at it, and open it in the editor.
  async saveAsConnection(sessionId: string) {
    const s = sessions.tabs.find((x) => x.sessionId === sessionId);
    if (!s || !isQuickId(s.connectionId)) return;
    try {
      const conn = await api.quickConnectionSave(s.connectionId, s.name);
      sessions.tabs = sessions.tabs.map((x) =>
        x.connectionId === s.connectionId ? { ...x, connectionId: conn.id } : x,
      );
      await tree.load();
      selection.select({ kind: "connection", id: conn.id });
      view.setTab("connections");
      toast.ok(`Saved "${conn.name}" - rename or move it in the editor`);
    } catch (e: any) {
      toast.err(`Save failed: ${e?.message ?? e}`);
    }
  }
}

export const quickConnect = new QuickConnectStore();
