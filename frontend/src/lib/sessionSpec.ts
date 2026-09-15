// Describing a live session well enough to dial it again, and doing the dial.
//
// Shared by reopen-last-session and named workspaces: both persist "these
// panes" and rebuild them later, and both need exactly this translation
// between a running session and something that survives a restart. It lived
// in lastSession until workspaces grew pane-tree support too.
//
// Pairs with paneSpec.ts, which handles the TREE; this module handles a single
// leaf's session.

import { api } from "./api";
import { sessions, tree } from "./stores.svelte";
import { connectionActions, isTransientConnectError } from "./connectionActions.svelte";
import { toast } from "./toast.svelte.ts";
import type { SessionSpec } from "./paneSpec";

// Dynamic folders already pulled during the CURRENT restore, so restoring 25
// dynamic hosts hits each provider once rather than once per host. Reset by
// beginRestore() at the start of each run.
let inventoryPulled = new Set<string>();

/** Call once before a batch of connectSpec() calls. */
export function beginRestore() {
  inventoryPulled = new Set<string>();
}

// Locate a dynamic entry across all cached folders. Entry row ids
// regenerate on every provider refresh (backend assigns a fresh
// uuid per fetch), so a session's id can be stale by snapshot time
// - fall back to matching the session's hostname + name.
export function dynEntryFor(
  entryId: string,
  hostname: string,
  name: string,
): { folderId: string; entry: (typeof tree.dynamicEntries)[string][number] } | null {
  for (const [fid, list] of Object.entries(tree.dynamicEntries)) {
    const hit = list.find((e) => e.id === entryId);
    if (hit) return { folderId: fid, entry: hit };
  }
  for (const [fid, list] of Object.entries(tree.dynamicEntries)) {
    const hit = list.find(
      (e) => (hostname && e.hostname === hostname) || (name && e.name === name),
    );
    if (hit) return { folderId: fid, entry: hit };
  }
  return null;
}

// Describe one live session well enough to dial it again. Returns null
// for a session that cannot be restored, which drops its pane.

export function specForSession(sessionId: string): SessionSpec | null {
  const sess = sessions.tabs.find((s) => s.sessionId === sessionId);
  if (!sess) return null;
  // VNC consoles aren't restorable: the bridge token dies with the
  // process and a console can't be silently re-established (Proxmox
  // would re-mint a ticket, generic VNC would re-tunnel). Skip them
  // so they don't get mis-saved as an ssh/dyn tab and reopened as a
  // terminal on next launch.
  if (sess.kind === "vnc") return null;

  if (sess.kind === "local") {
    // A saved local-shell connection (connectionId set, non-dyn) must
    // restore via LocalConnect so its InitialCommand re-runs (e.g. a
    // "claude" launcher). An ad-hoc local shell has no connectionId;
    // recovery and openLocalShell store its shell kind in `hostname`
    // (cmd / powershell / wsl / bash ...). Distinguishing the two here
    // is what stops a saved "claude on double-click" connection from
    // reopening as a bare WSL prompt on restore.
    if (sess.connectionId && !sess.connectionId.startsWith("dyn:")) {
      return { kind: "local", connectionId: sess.connectionId };
    }
    return { kind: "local", shellKind: sess.hostname };
  }
  if (sess.connectionId.startsWith("dyn:")) {
    const entryId = sess.connectionId.slice(4);
    const dyn = dynEntryFor(entryId, sess.hostname, sess.name);
    return {
      kind: "dyn",
      entryId,
      externalId: dyn?.entry.external_id ?? "",
      folderId: dyn?.folderId ?? "",
      entryName: dyn?.entry.name ?? sess.name,
      hostname: dyn?.entry.hostname ?? sess.hostname,
    };
  }
  if (sess.connectionId) {
    return { kind: "ssh", connectionId: sess.connectionId };
  }
  return null;
}


// Dial one pane's session. Returns the live sessionId, or null when the
// pane has to be dropped. Throwing here would fail the whole tab, so
// everything recoverable is reported and swallowed.
export async function connectSpec(spec: SessionSpec): Promise<string | null> {
  // Legacy rows (pre-kind) carry only connectionId.
  const kind = spec.kind ?? (spec.connectionId ? "ssh" : undefined);

  if (kind === "ssh" && spec.connectionId) {
    return connectionActions.connectSessionOnly(spec.connectionId);
  }

  if (kind === "dyn" && (spec.externalId || spec.entryId)) {
    // Entry row ids regenerate on every provider refresh, so the
    // saved id may be dead. Resolve through the provider-stable
    // external_id (name/hostname as a last resort) against freshly
    // loaded entries, then connect with the CURRENT row id.
    const matches = (e: { id: string; external_id: string; name: string; hostname: string }) =>
      (spec.externalId && e.external_id === spec.externalId) ||
      (spec.entryId && e.id === spec.entryId) ||
      (spec.hostname && e.hostname === spec.hostname) ||
      (spec.entryName && e.name === spec.entryName);

    let folderId = "";
    let entry: { id: string; name: string; hostname: string } | null = null;
    const candidates = spec.folderId
      ? [spec.folderId, ...Object.keys(tree.dynamicFolders).filter((f) => f !== spec.folderId)]
      : Object.keys(tree.dynamicFolders);
    for (const fid of candidates) {
      // Only pull a folder's entries once per restore. Restoring 25 dynamic
      // hosts used to re-fetch the inventory 25 times; a provider that rate
      // limits (or just answers slowly) then returns an empty list, and the
      // host is reported as "not in the inventory anymore" even though it is
      // there. The tree is loaded before restore runs, so a folder already
      // populated needs no round trip at all.
      if (!inventoryPulled.has(fid)) {
        inventoryPulled.add(fid);
        if ((tree.dynamicEntries[fid] ?? []).length === 0) {
          await tree.loadDynamicEntries(fid);
        }
      }
      const hit = (tree.dynamicEntries[fid] ?? []).find(matches);
      if (hit) {
        folderId = fid;
        entry = hit;
        break;
      }
    }
    if (!entry || !folderId) {
      toast.err(`${spec.entryName || "dynamic host"}: not in the inventory anymore`);
      return null;
    }
    // Same single retry the saved-connection path gets: restoring 25 hosts
    // back to back reliably turns up one transient DNS/refused/timeout, and
    // without a retry that host is simply missing after a restart. Auth and
    // host-key failures are NOT retried - see isTransientConnectError.
    try {
      let res;
      try {
        res = await api.sshConnectDynamic(folderId, entry.id);
      } catch (e) {
        if (!isTransientConnectError(e)) throw e;
        await new Promise((r) => setTimeout(r, 800));
        res = await api.sshConnectDynamic(folderId, entry.id);
      }
      sessions.add({
        sessionId: res.session_id,
        connectionId: "dyn:" + entry.id,
        name: entry.name,
        hostname: entry.hostname,
        status: "connected",
      });
      return res.session_id;
    } catch (e: any) {
      toast.err(`${entry.name}: ${e?.message ?? String(e)}`);
      return null;
    }
  }

  if (kind === "local" && spec.connectionId) {
    // A saved local-shell connection: re-run it through LocalConnect so
    // its InitialCommand fires (a "claude" launcher, a REPL, ...). If the
    // connection was deleted since the snapshot, open nothing rather than
    // spawning a bare shell that isn't what the user saved.
    const conn = tree.connectionById(spec.connectionId);
    if (!conn || conn.protocol !== "local") {
      toast.err("a saved local connection no longer exists");
      return null;
    }
    return connectionActions.connectSessionOnly(conn.id);
  }

  if (kind === "local") {
    // Ad-hoc local shell (no connectionId). hostname carries the resolved
    // shell kind, but auto-resolve stores a canonical label the spawner
    // won't accept back as input: on Linux/mac the auto shell is "shell"
    // (from $SHELL), which is not a valid kind, so local.Spawn("shell")
    // errors with "unsupported shell kind" and the restore toasts a
    // failure. Only feed back kinds the spawner takes; anything else
    // (incl. "shell") falls to "" = auto and gets the same default shell.
    // Mirrors duplicateTab in TerminalArea.svelte.
    const spawnKinds = ["wsl", "powershell", "cmd", "bash", "zsh", "sh", "fish"];
    const shellKind = spawnKinds.includes(spec.shellKind ?? "") ? spec.shellKind! : "";
    try {
      const res = await api.localShellOpen(shellKind, "", 120, 32);
      sessions.add({
        sessionId: res.session_id,
        connectionId: "",
        name: res.display,
        hostname: res.kind,
        kind: "local",
        status: "connected",
      });
      return res.session_id;
    } catch (e: any) {
      toast.err(`local shell: ${e?.message ?? String(e)}`);
      return null;
    }
  }

  return null;
}
