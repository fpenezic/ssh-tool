// Singleton handlers for "operate on these connections / folders"
// actions triggered from any tree location (context menu, batch panel,
// keyboard shortcut later). Each `open*` function tells App.svelte which
// modal to render; the modals close themselves through these same flags.

import { api, type Connection } from "./api";
import { tree, sessions, paneTabs, view, selection, credentials } from "./stores.svelte";
import { vncSessions } from "./vncState.svelte.ts";
import { showPrompt } from "./promptModal.svelte.ts";
import { toast } from "./toast.svelte.ts";
import { unwrapRaw } from "./connectErrors";
import { presenceTakeover, isBusyElsewhere } from "./presenceTakeover.svelte.ts";

// isTransientConnectError flags failures worth a single auto-retry.
// Excludes anything credential / cryptographic so a wrong password
// doesn't burn the saved value twice or spam the audit log. Matches
// the same lower-cased substring style as connectErrors.ts.
function isTransientConnectError(e: unknown): boolean {
  const raw =
    typeof e === "object" && e && "message" in (e as any)
      ? String((e as any).message)
      : String(e);
  const lower = unwrapRaw(raw).toLowerCase();
  return (
    lower.includes("no such host") ||
    lower.includes("connection refused") ||
    lower.includes("i/o timeout") ||
    lower.includes("connection reset by peer") ||
    lower.includes("network is unreachable") ||
    lower.includes("temporary failure in name resolution") ||
    lower.includes("context deadline exceeded")
  );
}

// withTakeover wraps any connect/refresh attempt so a "profile is live
// on another machine" failure surfaces the take-over dialog instead of
// a raw error. On the user taking over (or connecting anyway) the
// attempt is retried once; on cancel the caller sees `cancelled` and
// should treat it as a quiet no-op (no error toast). Shared by every
// connect path (saved connections, bulk, dynamic entries, dynamic
// double-click) and the manual dynamic-folder refresh so they all offer
// the same hand-over instead of only connectOne.
type TakeoverOutcome<T> =
  | { ok: true; value: T }
  | { ok: false; cancelled: true }
  | { ok: false; cancelled: false; error: unknown };

export async function withTakeover<T>(attempt: () => Promise<T>): Promise<TakeoverOutcome<T>> {
  try {
    return { ok: true, value: await attempt() };
  } catch (firstErr) {
    const busy = isBusyElsewhere(firstErr);
    if (!busy) return { ok: false, cancelled: false, error: firstErr };
    const decision = await presenceTakeover.ask(busy.profileId, busy.owner);
    if (decision !== "retry") return { ok: false, cancelled: true };
    try {
      return { ok: true, value: await attempt() };
    } catch (retryErr) {
      return { ok: false, cancelled: false, error: retryErr };
    }
  }
}

// FolderPicker modal state
interface MoveTarget {
  connIds: string[];
  folderIds: string[];
  excludeIds: Set<string>;
  title: string;
}
class ConnectionActionsStore {
  // Move-to-folder modal
  movePending = $state<MoveTarget | null>(null);

  // Delete modal: items + a pending op fired on confirm. Shared by
  // the connections tree AND the credentials tree - DeleteConfirm
  // renders all four kinds.
  deleteItems = $state<Array<{ kind: "folder" | "connection" | "credentialFolder" | "credential"; name: string; detail?: string }>>([]);
  deletePending: (() => Promise<void>) | null = null;

  openMoveTo(connIds: string[], folderIds: string[]) {
    // Exclude every folder being moved and all its descendants.
    const exclude = new Set<string>(folderIds);
    for (const fid of folderIds) {
      collectDescendantFolderIds(fid, exclude);
    }
    this.movePending = {
      connIds,
      folderIds,
      excludeIds: exclude,
      title:
        `Move ${connIds.length + folderIds.length} item${
          connIds.length + folderIds.length === 1 ? "" : "s"
        } to…`,
    };
  }

  async commitMove(targetFolderId: string | null) {
    const m = this.movePending;
    this.movePending = null;
    if (!m) return;
    // Run sequentially: parallel updates against the same SQLite file
    // produce "database is locked" failures which the IPC layer swallows
    // - we'd then see ~50% of the rows actually move and no error.
    for (const id of m.connIds) {
      try {
        await api.connectionsUpdate({
          id,
          folderId: targetFolderId ?? undefined,
          clearFolder: targetFolderId === null,
        });
      } catch (e) { console.error("move conn failed", id, e); }
    }
    for (const id of m.folderIds) {
      try {
        await api.foldersUpdate({
          id,
          parentId: targetFolderId ?? undefined,
          clearParent: targetFolderId === null,
        });
      } catch (e) { console.error("move folder failed", id, e); }
    }
    await tree.load();
  }

  cancelMove() { this.movePending = null; }

  // Flip favorite on every passed connection. Used by the right-click
  // menu so the user can star without opening the editor.
  async toggleFavorites(ids: string[]) {
    const conns = ids
      .map((id) => tree.connectionById(id))
      .filter((c): c is Connection => !!c);
    if (conns.length === 0) return;
    // If any in the set isn't a favourite, the gesture promotes the
    // whole set. Otherwise, demote.
    const target = !conns.every((c) => c.favorite);
    for (const c of conns) {
      try { await api.connectionsUpdate({ id: c.id, favorite: target }); }
      catch (e) { console.error("toggle favorite failed", c.id, e); }
    }
    await tree.load();
  }

  // Last failed connect attempt: connectionID -> {message, debug lines}.
  // DetailPane reads this to render the same error + diagnostics it
  // would have shown if its own Connect button had been pressed. Any
  // path that calls api.sshConnect (TreeNode double-click, Enter,
  // BatchPanel Connect-all, etc.) should funnel through connectOne /
  // connectMany so the user sees the failure in the editor pane.
  lastConnectError = $state<Record<string, { message: string; debug: string[] }>>({});

  // Public alias - TreeNode's dynamic double-click path calls this
  // directly because it doesn't go through connectOne/connectMany.
  recordConnectError(connectionID: string, e: unknown) {
    this.recordFailure(connectionID, e);
  }

  private recordFailure(connectionID: string, e: unknown) {
    const message =
      typeof e === "object" && e && "message" in (e as any)
        ? String((e as any).message)
        : String(e);
    // Pull buffered debug; ignore fetch failures so we still surface
    // the original error.
    api.sshGetConnectDebug(connectionID).then(
      (lines) => {
        this.lastConnectError = {
          ...this.lastConnectError,
          [connectionID]: { message, debug: lines ?? [] },
        };
      },
      () => {
        this.lastConnectError = {
          ...this.lastConnectError,
          [connectionID]: { message, debug: [] },
        };
      },
    );
  }

  clearConnectError(connectionID: string) {
    if (!(connectionID in this.lastConnectError)) return;
    const next = { ...this.lastConnectError };
    delete next[connectionID];
    this.lastConnectError = next;
  }

  // Connect to a single connection. Adds the session + tab on success,
  // records the failure (message + buffered debug) on error so the
  // editor pane can show it.
  // The DEFAULT connect action for a double-click / Enter on a
  // connection. Normally SSH, but a connection with "VNC as default"
  // set (vnc_default + vnc_enabled resolved true) opens the VNC console
  // instead - for a host reached only over VNC (a Windows box, a
  // KVM-over-IP console). The explicit Connect button and batch connects
  // still go through connectOne (SSH); this only governs the default
  // gesture. Falls back to SSH if resolving fails.
  async connectDefault(id: string): Promise<boolean> {
    const c = tree.connectionById(id);
    if (!c) return false;
    if (c.protocol === "local") return this.connectLocal(c);
    try {
      const r = await api.connectionsResolve(id);
      if (r?.vnc_enabled && r?.vnc_default) {
        return this.openVncConnection(id);
      }
    } catch {
      // resolve failed - fall through to a normal SSH connect
    }
    return this.connectOne(id);
  }

  async connectOne(id: string, opts?: { overrideCredentialId?: string }): Promise<boolean> {
    const c = tree.connectionById(id);
    if (!c) return false;
    // Local-shell connection: spawn a local PTY running its initial
    // command (telnet / serial / "claude" / a REPL). No SSH dial, no
    // credential override, no WG take-over - none of that applies.
    if (c.protocol === "local") return this.connectLocal(c);
    const override = opts?.overrideCredentialId ?? "";
    const attempt = async () => override
      ? api.sshConnectWithOverride(c.id, override)
      : api.sshConnect(c.id);
    try {
      // A synced WG profile up on another machine offers a take-over
      // (withTakeover); a transient failure gets one silent retry.
      const res = await withTakeover(attempt);
      if (!res.ok && res.cancelled) return false; // user declined - quiet
      let r;
      if (res.ok) {
        r = res.value;
      } else if (isTransientConnectError(res.error)) {
        // Single auto-retry on transient classes - DNS hiccup, the
        // host wasn't up yet, brief network blip. Auth / handshake /
        // host-key failures are never retried (same outcome twice
        // burns the user's saved password + spams the audit log).
        await new Promise((resolve) => setTimeout(resolve, 800));
        r = await attempt();
      } else {
        throw res.error;
      }
      sessions.add({
        sessionId: r.session_id,
        connectionId: c.id,
        name: c.name,
        hostname: c.hostname,
        status: "connected",
      });
      paneTabs.addTab(r.session_id, c.name);
      view.setTab("terminal");
      this.clearConnectError(c.id);
      return true;
    } catch (e) {
      this.recordFailure(c.id, e);
      return false;
    }
  }

  // connectLocal opens a saved local-shell connection. The session is
  // tagged kind:"local" so the pane correctly disables SFTP / VNC /
  // reconnect (they don't apply to a local PTY).
  async connectLocal(c: Connection): Promise<boolean> {
    try {
      const r = await api.localConnect(c.id);
      sessions.add({
        sessionId: r.session_id,
        connectionId: c.id,
        name: c.name,
        hostname: r.display || r.kind,
        status: "connected",
        kind: "local",
      });
      paneTabs.addTab(r.session_id, c.name);
      view.setTab("terminal");
      this.clearConnectError(c.id);
      return true;
    } catch (e) {
      this.recordFailure(c.id, e);
      return false;
    }
  }

  // Connect to many connections. Pushes a tab per success; failures
  // land in lastConnectError keyed by connectionID.
  async connectMany(ids: string[]) {
    const conns = ids
      .map((id) => tree.connectionById(id))
      .filter((c): c is Connection => !!c);
    if (conns.length === 0) return;
    const results = await Promise.allSettled(
      conns.map(async (c) => {
        // Local-shell connections don't dial SSH; route them through the
        // local path (which adds its own session + tab).
        if (c.protocol === "local") {
          const ok = await this.connectLocal(c);
          if (!ok) throw new Error("local connect failed");
          return;
        }
        // Same take-over offer as connectOne (the singleton dialog
        // serialises if several share a busy profile).
        const res = await withTakeover(() => api.sshConnect(c.id));
        if (!res.ok && res.cancelled) return; // user declined - quiet skip
        if (!res.ok) {
          this.recordFailure(c.id, res.error);
          throw res.error;
        }
        const r = res.value;
        sessions.add({
          sessionId: r.session_id,
          connectionId: c.id,
          name: c.name,
          hostname: c.hostname,
          status: "connected",
        });
        paneTabs.addTab(r.session_id, c.name);
        this.clearConnectError(c.id);
      }),
    );
    if (results.some((r) => r.status === "fulfilled")) view.setTab("terminal");
  }

  // Bulk-connect dynamic entries (Ctrl-click + Enter, or
  // multi-select + Enter on a dynamic folder row). Same parallel
  // Promise.allSettled shape as connectMany so partial failures
  // don't block the rest.
  async connectDynamicMany(targets: Array<{ folderId: string; entryId: string }>) {
    if (targets.length === 0) return;
    const results = await Promise.allSettled(
      targets.map(async (t) => {
        // Reach into the cached entry to surface name + hostname
        // in the tab; the backend will resolve everything else.
        const entries = tree.dynamicEntries[t.folderId] ?? [];
        const meta = entries.find((e) => e.id === t.entryId);
        // Dynamic hosts route their first hop through the folder's
        // network profile too, so offer the same take-over. The dialog
        // is a singleton: if several targets share a busy profile the
        // first prompts and the rest await that one decision.
        const res = await withTakeover(() => api.sshConnectDynamic(t.folderId, t.entryId));
        if (!res.ok && res.cancelled) return; // user declined - quiet skip
        if (!res.ok) {
          this.recordConnectError("dyn:" + t.entryId, res.error);
          throw res.error;
        }
        const r = res.value;
        sessions.add({
          sessionId: r.session_id,
          connectionId: "dyn:" + t.entryId,
          name: meta?.name ?? t.entryId,
          hostname: meta?.hostname ?? "",
          status: "connected",
        });
        paneTabs.addTab(r.session_id, meta?.name ?? t.entryId);
      }),
    );
    if (results.some((r) => r.status === "fulfilled")) view.setTab("terminal");
  }

  // Open a VNC console tab for a saved connection. A connection pinned
  // from a Proxmox guest routes back through the Proxmox API (real
  // console); everything else uses generic VNC (direct or SSH-tunnelled).
  // We try the pinned-Proxmox path first and fall back on "not pinned".
  async openVncConnection(id: string): Promise<boolean> {
    const c = tree.connectionById(id);
    if (!c) return false;
    try {
      let vs: import("./api").VncSession;
      try {
        vs = await api.vncOpenPinnedProxmox(id);
      } catch {
        vs = await api.vncOpenConnection(id);
      }
      vncSessions.set(vs);
      sessions.add({
        sessionId: vs.session_id,
        connectionId: c.id,
        name: vs.title,
        hostname: c.hostname,
        kind: "vnc",
        status: "connecting",
      });
      paneTabs.addVncTab(vs.session_id, vs.title);
      view.setTab("terminal");
      return true;
    } catch (e) {
      toast.err(`VNC console failed: ${unwrapRaw(String((e as any)?.message ?? e))}`);
      return false;
    }
  }

  // Open a VNC console tab for a Proxmox VM/LXC dynamic entry.
  async openVncProxmox(folderId: string, entryId: string): Promise<boolean> {
    const entries = tree.dynamicEntries[folderId] ?? [];
    const meta = entries.find((e) => e.id === entryId);
    try {
      const vs = await api.vncOpenProxmox(folderId, entryId);
      vncSessions.set(vs);
      sessions.add({
        sessionId: vs.session_id,
        connectionId: "dyn:" + entryId,
        name: vs.title,
        hostname: meta?.hostname ?? "",
        kind: "vnc",
        status: "connecting",
      });
      paneTabs.addVncTab(vs.session_id, vs.title);
      view.setTab("terminal");
      return true;
    } catch (e) {
      toast.err(`Proxmox console failed: ${unwrapRaw(String((e as any)?.message ?? e))}`);
      return false;
    }
  }

  async cloneConnection(id: string) {
    const cloned = await api.connectionsClone(id);
    if (!cloned) return;
    await tree.load();
    selection.selectConnection(cloned.id);
  }

  async launchExternal(id: string) {
    let kind = "windowsterminal";
    try {
      const saved = await api.settingsGet("external_terminal_kind");
      if (saved === "powershell" || saved === "cmd" || saved === "windowsterminal") {
        kind = saved;
      }
    } catch { /* default */ }
    try {
      await api.launchExternalTerminal(id, kind);
    } catch (e: any) {
      toast.err(`Open in external terminal failed: ${e?.message ?? String(e)}`);
    }
  }

  openDeleteConnections(ids: string[]) {
    const items = ids
      .map((id) => tree.connectionById(id))
      .filter((c): c is Connection => !!c)
      .map((c) => ({ kind: "connection" as const, name: c.name, detail: c.hostname }));
    this.deleteItems = items;
    this.deletePending = async () => {
      for (const id of ids) {
        try { await api.connectionsDelete(id); } catch {}
      }
      selection.select({ kind: "none" });
      await tree.load();
    };
  }

  openDeleteFolders(ids: string[]) {
    const items: typeof this.deleteItems = [];
    for (const id of ids) collectFolderVictims(id, items);
    this.deleteItems = items;
    this.deletePending = async () => {
      for (const id of ids) {
        try { await api.foldersDelete(id); } catch {}
      }
      selection.select({ kind: "none" });
      await tree.load();
    };
  }

  openDeleteCredentials(ids: string[]) {
    const items = ids
      .map((id) => credentials.byId(id))
      .filter((c): c is NonNullable<typeof c> => !!c)
      .map((c) => ({ kind: "credential" as const, name: c.name, detail: c.kind }));
    this.deleteItems = items;
    this.deletePending = async () => {
      for (const id of ids) {
        try { await api.credentialsDelete(id); } catch (e) { toast.err(unwrapRaw(String((e as any)?.message ?? e))); }
      }
      selection.select({ kind: "none" });
      await credentials.load();
    };
  }

  openDeleteCredFolders(ids: string[]) {
    const items: typeof this.deleteItems = [];
    for (const id of ids) collectCredFolderVictims(id, items);
    this.deleteItems = items;
    this.deletePending = async () => {
      for (const id of ids) {
        // Backend cascades: credentials in the subtree go through the
        // full vault-cleanup path, subfolders via FK.
        try { await api.credentialFoldersDelete(id); } catch (e) { toast.err(unwrapRaw(String((e as any)?.message ?? e))); }
      }
      selection.select({ kind: "none" });
      await credentials.load();
    };
  }

  async commitDelete() {
    const fn = this.deletePending;
    this.deletePending = null;
    this.deleteItems = [];
    if (fn) await fn();
  }
  cancelDelete() {
    this.deletePending = null;
    this.deleteItems = [];
  }

  // ----- Folder lifecycle (so context menus + DetailPane share one impl) -----
  async addSubfolderUnder(parentId: string | null) {
    const name = await showPrompt("Folder name?");
    if (!name?.trim()) return;
    try {
      const created: any = await api.foldersCreate({
        name: name.trim(),
        parentId: parentId ?? undefined,
      });
      await tree.load();
      if (created?.id) selection.select({ kind: "folder", id: created.id });
    } catch (e) { console.error("create folder:", e); }
  }

  async addConnectionUnder(folderId: string | null) {
    const name = await showPrompt("Connection name?");
    if (!name?.trim()) return;
    const hostname = (await showPrompt("Hostname?")) ?? "";
    try {
      const conn: any = await api.connectionsCreate({
        folderId: folderId ?? undefined,
        name: name.trim(),
        hostname,
      });
      await tree.load();
      if (conn?.id) selection.select({ kind: "connection", id: conn.id });
    } catch (e) { console.error("create connection:", e); }
  }

  // Create a local-shell connection under a folder (telnet / serial /
  // "claude" / any command). Name-only prompt; shell + command are set in
  // the editor.
  async addLocalConnectionUnder(folderId: string | null) {
    const name = await showPrompt("Local shell connection name?");
    if (!name?.trim()) return;
    try {
      const conn: any = await api.connectionsCreate({
        folderId: folderId ?? undefined,
        name: name.trim(),
        hostname: "",
        protocol: "local",
      });
      await tree.load();
      if (conn?.id) selection.select({ kind: "connection", id: conn.id });
    } catch (e) { console.error("create local connection:", e); }
  }

  async renameFolder(folderId: string) {
    const f = tree.folderById(folderId);
    if (!f) return;
    const next = await showPrompt("Rename folder", f.name);
    if (!next?.trim() || next.trim() === f.name) return;
    try {
      await api.foldersUpdate({ id: folderId, name: next.trim() });
      await tree.load();
    } catch (e) { console.error("rename folder:", e); }
  }
}

function collectDescendantFolderIds(id: string, out: Set<string>) {
  for (const sub of tree.childrenOf(id)) {
    out.add(sub.id);
    collectDescendantFolderIds(sub.id, out);
  }
}

function collectFolderVictims(
  folderId: string,
  out: Array<{ kind: "folder" | "connection" | "credentialFolder" | "credential"; name: string; detail?: string }>,
) {
  const f = tree.folderById(folderId);
  if (!f) return;
  const childFolders = tree.childrenOf(folderId);
  const childConns = tree.connectionsIn(folderId);
  out.push({
    kind: "folder",
    name: f.name,
    detail: childFolders.length || childConns.length
      ? `${childFolders.length + childConns.length} item${
          childFolders.length + childConns.length === 1 ? "" : "s"
        } inside`
      : undefined,
  });
  for (const c of childConns) {
    out.push({ kind: "connection", name: c.name, detail: c.hostname });
  }
  for (const sub of childFolders) {
    collectFolderVictims(sub.id, out);
  }
}

// Mirrors collectFolderVictims for the credentials tree: the folder
// itself, every credential inside, then subfolders recursively - the
// exact list the backend cascade will delete.
function collectCredFolderVictims(
  folderId: string,
  out: Array<{ kind: "folder" | "connection" | "credentialFolder" | "credential"; name: string; detail?: string }>,
) {
  const f = credentials.folders.find((x) => x.id === folderId);
  if (!f) return;
  const childFolders = credentials.foldersIn(folderId);
  const childCreds = credentials.credsIn(folderId);
  const count = childFolders.length + childCreds.length;
  out.push({
    kind: "credentialFolder",
    name: f.name,
    detail: count ? `${count} item${count === 1 ? "" : "s"} inside` : undefined,
  });
  for (const c of childCreds) {
    out.push({ kind: "credential", name: c.name, detail: c.kind });
  }
  for (const sub of childFolders) {
    collectCredFolderVictims(sub.id, out);
  }
}

export const connectionActions = new ConnectionActionsStore();
