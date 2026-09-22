// Pane-tree persistence across a restart.
//
// Two features save "these tabs, this layout" and rebuild it later: named
// workspaces (workspaces.svelte.ts) and reopen-last-session
// (lastSession.svelte.ts). Both used to keep only the ACTIVE pane of a split
// tab, so a tab holding four terminals came back as one.
//
// The pane tree itself already survives a detach/redock round trip
// (SerializedPaneTab in panetypes.ts), but that shape is useless here: its
// leaves point at sessionIds, and sessions die with the process. This module
// is the missing translation - the same tree with each leaf's sessionId
// replaced by enough information to CONNECT it again.
//
// Shared sessions are the one structural subtlety. splitPaneShareSession puts
// a second view (SFTP) on the SAME ssh.Client, so two leaves can carry one
// sessionId. Connecting per leaf would open a second SSH session and quietly
// double the connection count, so leaves are grouped by a `ref` index and each
// ref connects exactly once.

import type { PaneNode, PaneLeaf } from "./panetypes";

export const PANE_SPEC_VERSION = 2;

/** How a single session gets re-established. Mirrors the flat SavedTab that
 *  lastSession has always written, so one restore path serves both. */
export interface SessionSpec {
  kind: "ssh" | "dyn" | "local";
  connectionId?: string; // ssh, and saved local connections
  folderId?: string;     // dyn
  entryId?: string;      // dyn - row id, regenerates on every provider refresh
  externalId?: string;   // dyn - provider-stable id, the real restore key
  entryName?: string;    // dyn - label + name-match fallback
  hostname?: string;     // dyn - hostname-match fallback
  shellKind?: string;    // local (ad-hoc shells only)
}

export type SpecNode = SpecLeaf | SpecSplit;

export interface SpecLeaf {
  kind: "pane";
  /** Index into the tab's `sessions` array. Two leaves sharing a session
   *  (terminal + SFTP on one client) carry the same ref. */
  ref: number;
  view?: "terminal" | "sftp";
}

export interface SpecSplit {
  kind: "split";
  direction: "horizontal" | "vertical";
  ratio: number;
  a: SpecNode;
  b: SpecNode;
}

/** A tab as persisted: its sessions, and the tree that arranges them. */
export interface TabSpec {
  title?: string;
  // Whether `title` is a name the user typed rather than one generated
  // from the tab's sessions. A generated title is rebuilt from the live
  // panes on restore; a chosen one has to survive, and the tab bar shows
  // it in place of the per-pane names.
  titleCustom?: boolean;
  groupName?: string;
  groupColor?: string;
  sessions: SessionSpec[];
  root: SpecNode;
}

/** What the caller must supply per leaf: the spec, or null to drop the leaf
 *  (a VNC console, or a session that has since vanished). */
export type SpecOfSession = (sessionId: string) => SessionSpec | null;

/**
 * Convert a live pane tree into its persistable form.
 *
 * Leaves whose session cannot be described are dropped and their splits
 * collapsed, so a tab mixing a terminal and a VNC console saves the terminal
 * rather than being skipped whole. Returns null when nothing is left.
 */
export function serializePaneSpec(root: PaneNode, specOf: SpecOfSession): {
  root: SpecNode;
  sessions: SessionSpec[];
} | null {
  const sessions: SessionSpec[] = [];
  // sessionId -> ref, so shared-session leaves resolve to one entry.
  const refs = new Map<string, number>();

  const walk = (n: PaneNode): SpecNode | null => {
    if (n.kind === "pane") {
      // "unavailable" is a guest-only placeholder and never has a session
      // worth reconnecting; vnc is refused by the caller's specOf.
      if (n.view === "vnc" || n.view === "unavailable") return null;
      let ref = refs.get(n.sessionId);
      if (ref === undefined) {
        const spec = specOf(n.sessionId);
        if (!spec) return null;
        ref = sessions.length;
        sessions.push(spec);
        refs.set(n.sessionId, ref);
      }
      const leaf: SpecLeaf = { kind: "pane", ref };
      if (n.view === "sftp") leaf.view = "sftp";
      return leaf;
    }
    const a = walk(n.a);
    const b = walk(n.b);
    // A split with one surviving child collapses into that child, exactly as
    // closePane does when a pane is closed by hand.
    if (!a) return b;
    if (!b) return a;
    return { kind: "split", direction: n.direction, ratio: n.ratio, a, b };
  };

  const out = walk(root);
  if (!out) return null;
  return { root: out, sessions };
}

/** Connects one spec and returns the live sessionId, or null if it could not
 *  be restored (the caller reports why). */
export type ConnectSpec = (spec: SessionSpec) => Promise<string | null>;

/**
 * Rebuild a live pane tree from a spec, connecting each session once.
 *
 * Connections run sequentially: they are the slow part, and the existing
 * restore paths already serialise them so a host that prompts (host key,
 * password) does not race three other prompts.
 *
 * A leaf whose session fails to connect is dropped and its split collapsed -
 * a tab with three of four panes is still the tab the user saved; losing all
 * four to one dead host is not. Returns null when every leaf failed.
 */
export async function restorePaneSpec(
  spec: TabSpec,
  connect: ConnectSpec,
): Promise<{ root: PaneNode; sessionIds: string[] } | null> {
  const ids: (string | null)[] = [];
  for (const s of spec.sessions ?? []) {
    let id: string | null = null;
    try {
      id = await connect(s);
    } catch {
      id = null; // the connect callback reports; here a failure is just a gap
    }
    ids.push(id);
  }

  let n = 0;
  const genId = (p: string) => `${p}-restore-${Date.now().toString(36)}-${n++}`;

  const walk = (node: SpecNode): PaneNode | null => {
    if (node.kind === "pane") {
      const sid = ids[node.ref];
      if (!sid) return null;
      const leaf: PaneLeaf = { kind: "pane", id: genId("pane"), sessionId: sid };
      if (node.view === "sftp") leaf.view = "sftp";
      return leaf;
    }
    const a = walk(node.a);
    const b = walk(node.b);
    if (!a) return b;
    if (!b) return a;
    return {
      kind: "split",
      id: genId("split"),
      direction: node.direction,
      ratio: node.ratio,
      a,
      b,
    };
  };

  const root = walk(spec.root);
  if (!root) return null;
  return { root, sessionIds: ids.filter((x): x is string => !!x) };
}

/** Whether a tab spec describes more than one pane - lets a caller take the
 *  cheap single-pane path and skip tree rebuilding entirely. */
export function isSplit(root: SpecNode): boolean {
  return root.kind === "split";
}

/** Upgrade a legacy flat entry (one session per tab, no tree) to a TabSpec.
 *  Version 1 workspaces and last_session_tabs_v1 rows are exactly this. */
export function specFromFlat(session: SessionSpec, meta: {
  title?: string;
  groupName?: string;
  groupColor?: string;
}): TabSpec {
  return {
    ...meta,
    sessions: [session],
    root: { kind: "pane", ref: 0 },
  };
}

/** Version 1 workspace tab: one connectionId, no tree. */
export interface LegacyTabSpec {
  connectionId?: string;
  title?: string;
  groupName?: string;
  groupColor?: string;
}

/**
 * Read a workspace layout of either version as v2 tab specs.
 *
 * Rows are classified by SHAPE rather than the layout's version field, so a
 * hand-edited or half-written layout still restores every row it can, and a
 * v2 row inside a v1-labelled layout is not thrown away.
 */
export function upgradeWorkspaceTabs(
  layout: { version?: number; tabs?: unknown } | null,
): TabSpec[] {
  const rows = layout?.tabs;
  if (!Array.isArray(rows) || rows.length === 0) return [];
  const out: TabSpec[] = [];
  for (const r of rows as (TabSpec & LegacyTabSpec)[]) {
    if (!r) continue;
    if (r.root && Array.isArray(r.sessions)) {
      out.push(r);
    } else if (r.connectionId) {
      out.push(specFromFlat(
        { kind: "ssh", connectionId: r.connectionId },
        { title: r.title, groupName: r.groupName, groupColor: r.groupColor },
      ));
    }
  }
  return out;
}
