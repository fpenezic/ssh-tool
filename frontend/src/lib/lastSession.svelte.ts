// Reopen-last-session: continuous snapshot of the open tabs, restored
// once at startup after the vault unlocks. Chrome-style three-way
// behaviour: "ask" (default) prompts when the last session had tabs,
// "always" restores silently, "never" stays off.
//
// Snapshot scope: SSH tabs (connectionId), dynamic-inventory tabs
// (folderId + entryId, captured from the tree cache while it's warm)
// and local shells (shell kind). Pane splits collapse to the active
// leaf, same as the workspaces serializer.
//
// Save discipline matches window_state.go: saving is gated until the
// startup restore has run, otherwise the empty boot state would
// overwrite the tab set we're about to restore. Writes coalesce over
// a short window and flush() is wired to pagehide so a quit right
// after opening a tab doesn't lose it.

import { api } from "./api";
import { paneTabs, sessions, tree, view } from "./stores.svelte";
import { connectionActions } from "./connectionActions.svelte";
import { showConfirm } from "./confirmModal.svelte.ts";
import { toast } from "./toast.svelte.ts";

const MODE_KEY = "reopen_last_session_mode";
const TABS_KEY = "last_session_tabs_v1";

export type ReopenMode = "ask" | "always" | "never";

interface SavedTab {
  kind: "ssh" | "dyn" | "local";
  connectionId?: string; // ssh
  folderId?: string;     // dyn
  entryId?: string;      // dyn - last-known row id (regenerates per refresh!)
  externalId?: string;   // dyn - provider-stable id, the real restore key
  entryName?: string;    // dyn - tab label + name-match fallback
  hostname?: string;     // dyn - hostname-match fallback
  shellKind?: string;    // local
  title?: string;
  groupName?: string;
  groupColor?: string;
}

class LastSessionStore {
  mode = $state<ReopenMode>("ask");
  restoring = $state<boolean>(false);

  private loaded = false;
  private restoreDone = false;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const v = await api.settingsGet(MODE_KEY);
      if (v === "ask" || v === "always" || v === "never") this.mode = v;
    } catch { /* missing key fine */ }
    this.loaded = true;
  }

  setMode(v: ReopenMode) {
    if (this.mode === v) return;
    this.mode = v;
    api.settingsSet(MODE_KEY, v).catch(console.warn);
  }

  // Locate a dynamic entry across all cached folders. Entry row ids
  // regenerate on every provider refresh (backend assigns a fresh
  // uuid per fetch), so a session's id can be stale by snapshot time
  // - fall back to matching the session's hostname + name.
  private dynEntryFor(
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

  private serialize(): SavedTab[] {
    const out: SavedTab[] = [];
    for (const t of paneTabs.tabs) {
      const leaf = paneTabs.activePane(t.tabId);
      if (!leaf) continue;
      const sess = sessions.tabs.find((s) => s.sessionId === leaf.sessionId);
      if (!sess) continue;
      // VNC consoles aren't restorable: the bridge token dies with the
      // process and a console can't be silently re-established (Proxmox
      // would re-mint a ticket, generic VNC would re-tunnel). Skip them
      // so they don't get mis-saved as an ssh/dyn tab and reopened as a
      // terminal on next launch.
      if (sess.kind === "vnc") continue;
      const meta = {
        title: t.title,
        groupName: t.groupName,
        groupColor: t.groupColor,
      };
      if (sess.kind === "local") {
        // A saved local-shell connection (connectionId set, non-dyn) must
        // restore via LocalConnect so its InitialCommand re-runs (e.g. a
        // "claude" launcher). An ad-hoc local shell has no connectionId;
        // recovery and openLocalShell store its shell kind in `hostname`
        // (cmd / powershell / wsl / bash ...). Distinguishing the two here
        // is what stops a saved "claude on double-click" connection from
        // reopening as a bare WSL prompt on restore.
        if (sess.connectionId && !sess.connectionId.startsWith("dyn:")) {
          out.push({ kind: "local", connectionId: sess.connectionId, ...meta });
        } else {
          out.push({ kind: "local", shellKind: sess.hostname, ...meta });
        }
      } else if (sess.connectionId.startsWith("dyn:")) {
        const entryId = sess.connectionId.slice(4);
        const dyn = this.dynEntryFor(entryId, sess.hostname, sess.name);
        out.push({
          kind: "dyn",
          entryId,
          externalId: dyn?.entry.external_id ?? "",
          folderId: dyn?.folderId ?? "",
          entryName: dyn?.entry.name ?? sess.name,
          hostname: dyn?.entry.hostname ?? sess.hostname,
          ...meta,
        });
      } else if (sess.connectionId) {
        out.push({ kind: "ssh", connectionId: sess.connectionId, ...meta });
      }
    }
    return out;
  }

  // Coalesced snapshot - called from an $effect in App.svelte on every
  // tab/session change. Persists even in "never" mode so switching
  // away from it later restores the genuinely-last session, not the
  // last one from before the mode flip.
  schedule() {
    // No saves before the startup restore decision, and none during
    // the restore itself - a kill mid-restore must not overwrite the
    // saved set with a partially-rebuilt one.
    if (!this.restoreDone || this.restoring) return;
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      this.saveTimer = null;
      this.flush();
    }, 150);
  }

  // Write the snapshot now. Also wired to window pagehide from
  // App.svelte so a quit with a coalesce window still pending gets
  // its final state out (best effort - IPC is async).
  flush() {
    if (!this.restoreDone || this.restoring) return;
    if (this.saveTimer) {
      clearTimeout(this.saveTimer);
      this.saveTimer = null;
    }
    api.settingsSet(TABS_KEY, JSON.stringify(this.serialize())).catch(console.warn);
  }

  // Restore once after the first vault unlock. `recovered` = number of
  // tabs the backend session-recovery already brought back (UI reload
  // case) - if anything survived, this was not a cold start and
  // restoring on top would duplicate tabs.
  async restoreOnStartup(recovered: number) {
    if (this.restoreDone) return;
    this.restoreDone = true;
    if (recovered > 0 || this.mode === "never") return;

    let saved: SavedTab[] = [];
    try {
      const raw = await api.settingsGet(TABS_KEY);
      if (raw) saved = JSON.parse(raw);
    } catch { /* missing or corrupt - nothing to restore */ }
    if (!Array.isArray(saved) || saved.length === 0) return;

    if (this.mode === "ask") {
      const n = saved.length;
      const ok = await showConfirm({
        title: "Reopen last session?",
        message: `Reconnect ${n} tab${n === 1 ? "" : "s"} from the last session? You can change this behaviour in Settings - Window - Startup.`,
        okLabel: "Reopen",
      });
      // Declining keeps the snapshot intact: schedule() won't fire
      // until a tab/session mutation happens, and by then the user
      // has moved on deliberately.
      if (!ok) return;
    }

    this.restoring = true;
    // Connects run sequentially (preserves tab order) and each one
    // blocks until auth+PTY, so a slow host opens its tab visibly
    // late. The toasts attribute those stragglers - without them a
    // terminal popping up 30s after launch looks like a ghost.
    toast.ok(`Reopening ${saved.length} tab${saved.length === 1 ? "" : "s"} from the last session…`);
    let opened = 0;
    try {
      for (const spec of saved) {
        try {
          await this.restoreOne(spec);
          opened++;
        } catch (e: any) {
          toast.err(`Reopen failed: ${e?.message ?? String(e)}`);
        }
      }
    } finally {
      this.restoring = false;
      if (opened < saved.length) {
        toast.ok(`Reopened ${opened} of ${saved.length} tabs`);
      }
      // Persist the rebuilt state once - failed entries drop out so
      // they don't error again on every start.
      this.flush();
    }
  }

  private async restoreOne(spec: SavedTab) {
    if (!spec) return;
    const beforeIds = new Set(paneTabs.tabs.map((t) => t.tabId));

    // Legacy snapshot rows (pre-kind) carry only connectionId.
    const kind = spec.kind ?? (spec.connectionId ? "ssh" : undefined);

    if (kind === "ssh" && spec.connectionId) {
      const ok = await connectionActions.connectOne(spec.connectionId);
      if (!ok) return;
    } else if (kind === "dyn" && (spec.externalId || spec.entryId)) {
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
        await tree.loadDynamicEntries(fid);
        const hit = (tree.dynamicEntries[fid] ?? []).find(matches);
        if (hit) {
          folderId = fid;
          entry = hit;
          break;
        }
      }
      if (!entry || !folderId) {
        throw new Error(`${spec.entryName || "dynamic host"}: not in the inventory anymore`);
      }
      const res = await api.sshConnectDynamic(folderId, entry.id);
      sessions.add({
        sessionId: res.session_id,
        connectionId: "dyn:" + entry.id,
        name: entry.name,
        hostname: entry.hostname,
        status: "connected",
      });
      paneTabs.addTab(res.session_id, entry.name);
      view.setTab("terminal");
    } else if (kind === "local" && spec.connectionId) {
      // A saved local-shell connection: re-run it through LocalConnect so
      // its InitialCommand fires (a "claude" launcher, a REPL, ...). If the
      // connection was deleted since the snapshot, fall through to nothing
      // rather than spawning a bare shell that isn't what the user saved.
      const conn = tree.connectionById(spec.connectionId);
      if (!conn || conn.protocol !== "local") {
        throw new Error(`${spec.title || "local connection"}: no longer exists`);
      }
      const r = await api.localConnect(conn.id);
      sessions.add({
        sessionId: r.session_id,
        connectionId: conn.id,
        name: conn.name,
        hostname: r.display || r.kind,
        kind: "local",
        status: "connected",
      });
      paneTabs.addTab(r.session_id, conn.name);
      view.setTab("terminal");
    } else if (kind === "local") {
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
      const res = await api.localShellOpen(shellKind, "", 120, 32);
      sessions.add({
        sessionId: res.session_id,
        connectionId: "",
        name: res.display,
        hostname: res.kind,
        kind: "local",
        status: "connected",
      });
      paneTabs.addTab(res.session_id, res.display);
      view.setTab("terminal");
    } else {
      return;
    }

    const newTab = paneTabs.tabs.find((t) => !beforeIds.has(t.tabId));
    if (!newTab) return;
    if (spec.title) paneTabs.setTitle(newTab.tabId, spec.title);
    if (spec.groupName || spec.groupColor) {
      paneTabs.setGroup(newTab.tabId, spec.groupName, spec.groupColor);
    }
  }
}

export const lastSession = new LastSessionStore();
