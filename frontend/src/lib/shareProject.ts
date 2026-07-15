// Projects the tabs a host picked to share into the guest manifest shape:
// pane trees with real session ids rewritten to opaque guest slots (s1, s2, …),
// plus the slot -> real-session list the backend needs.
//
// The rewrite is the security boundary on the frontend side: the guest never
// receives a real pool uuid, so a captured manifest reveals nothing and a
// crafted input frame can only ever name a slot that was shared.

import type { PaneNode, PaneLeaf, SerializedPaneTab } from "./panetypes";
import type { PaneTab } from "./stores.svelte";

export interface ProjectedShare {
  // {tabs:[...]} JSON to hand the backend as ShareStartInput.tabs_blob.
  tabsBlob: string;
  // slot -> real session. slot is authoritative: the backend honours it, so the
  // manifest tree and the hub routing agree.
  sessions: { slot: string; real_id: string; name: string }[];
  // The slot assignment, to carry forward on the next re-projection so a
  // session keeps its slot (its guest stream isn't disrupted).
  slotByReal: Map<string, string>;
}

// projectTabs walks the selected tabs and rewrites each leaf's real sessionId to
// a guest slot. A session already in `existing` KEEPS its slot; new sessions get
// the next free slot number. sftp/vnc leaves become an "unavailable" placeholder
// (a browser guest has neither).
export function projectTabs(
  tabs: PaneTab[],
  sessionName: (sessionId: string) => string,
  existing?: Map<string, string>,
): ProjectedShare {
  const slotByReal = new Map<string, string>(existing ?? []);
  const sessions: { slot: string; real_id: string; name: string }[] = [];
  const usedSlots = new Set(slotByReal.values());
  let nextN = 1;
  function freeSlot(): string {
    while (usedSlots.has("s" + nextN)) nextN++;
    const slot = "s" + nextN;
    usedSlots.add(slot);
    return slot;
  }

  function slotFor(realId: string): string {
    let slot = slotByReal.get(realId);
    if (!slot) {
      slot = freeSlot();
      slotByReal.set(realId, slot);
    }
    if (!sessions.some((s) => s.real_id === realId)) {
      sessions.push({ slot, real_id: realId, name: sessionName(realId) });
    }
    return slot;
  }

  function rewrite(node: PaneNode): PaneNode {
    if (node.kind === "pane") {
      const leaf = node as PaneLeaf;
      if (leaf.view === "sftp" || leaf.view === "vnc") {
        return { ...leaf, view: "unavailable", sessionId: "" };
      }
      return { ...leaf, sessionId: slotFor(leaf.sessionId) };
    }
    return { ...node, a: rewrite(node.a), b: rewrite(node.b) };
  }

  const projected: SerializedPaneTab[] = tabs.map((t) => ({
    title: t.title,
    root: rewrite(t.root),
    groupName: t.groupName,
    groupColor: t.groupColor,
    locked: t.locked,
  }));

  return {
    tabsBlob: JSON.stringify({ tabs: projected }),
    sessions,
    slotByReal,
  };
}

// realSessionIds returns the distinct real session ids referenced by a set of
// tabs (used to tell shareShared which sessions a share covers).
export function realSessionIds(tabs: PaneTab[]): string[] {
  const out = new Set<string>();
  function walk(node: PaneNode) {
    if (node.kind === "pane") {
      const leaf = node as PaneLeaf;
      if (leaf.view !== "sftp" && leaf.view !== "vnc" && leaf.sessionId) {
        out.add(leaf.sessionId);
      }
    } else {
      walk(node.a);
      walk(node.b);
    }
  }
  for (const t of tabs) walk(t.root);
  return [...out];
}
