// Pure helpers for tree drag & drop. Stateless; the actual drag state
// lives in `drag` in stores.svelte.ts (shared with split-view DnD).

import { api, type Folder, type Connection } from "./api";
import { tree } from "./stores.svelte";

export type DragKind = "folder" | "connection";
export type DropIntent = "before" | "after" | "inside";

/**
 * Classify drop intent from the pointer Y position within the row.
 * For folders: top quarter = before, bottom quarter = after, middle =
 * inside (child). For connections: just top-half = before, bottom-half =
 * after; they're leaves so 'inside' makes no sense.
 */
export function computeIntent(
  e: DragEvent,
  rowEl: HTMLElement,
  isFolder: boolean
): DropIntent {
  const rect = rowEl.getBoundingClientRect();
  const y = e.clientY - rect.top;
  const h = rect.height;
  if (!isFolder) {
    return y < h / 2 ? "before" : "after";
  }
  if (y < h * 0.25) return "before";
  if (y > h * 0.75) return "after";
  return "inside";
}

/**
 * Reject obviously-bogus drops so we don't draw a false indicator that
 * the backend will then refuse (cycle detection). Backend still
 * validates as the source of truth.
 */
export function isInvalidDrop(
  sourceKind: DragKind,
  sourceId: string,
  targetKind: DragKind,
  targetId: string,
  intent: DropIntent
): boolean {
  if (sourceKind === targetKind && sourceId === targetId) return true;

  if (sourceKind === "folder") {
    if (intent === "inside" && targetKind === "folder") {
      if (isDescendantOf(targetId, sourceId)) return true;
    }
    if ((intent === "before" || intent === "after") && targetKind === "folder") {
      const t = tree.folderById(targetId);
      const tParent = t?.parent_id ?? null;
      if (tParent !== null && isDescendantOf(tParent, sourceId)) return true;
    }
  }
  return false;
}

function isDescendantOf(candidateID: string, ancestorID: string): boolean {
  if (candidateID === ancestorID) return true;
  let cur = tree.folderById(candidateID);
  while (cur && cur.parent_id) {
    if (cur.parent_id === ancestorID) return true;
    cur = tree.folderById(cur.parent_id);
  }
  return false;
}

/**
 * Resolve a drop intent to (parentId, sortOrder). Used by applyDrop to
 * persist the new placement.
 */
function resolveDropTarget(
  targetKind: DragKind,
  target: Folder | Connection,
  intent: DropIntent
): { parentId: string | null; sortOrder: number } {
  if (intent === "inside") {
    const f = target as Folder;
    const childrenLen = tree.childrenOf(f.id).length + tree.connectionsIn(f.id).length;
    return { parentId: f.id, sortOrder: childrenLen };
  }
  const parentId =
    targetKind === "folder"
      ? (target as Folder).parent_id ?? null
      : (target as Connection).folder_id ?? null;
  const siblings = mergedSiblings(parentId);
  const targetIdx = siblings.findIndex(
    (s) => s.kind === targetKind && s.id === target.id
  );
  if (targetIdx < 0) return { parentId, sortOrder: siblings.length };
  if (intent === "before") return { parentId, sortOrder: siblings[targetIdx].sort };
  return { parentId, sortOrder: siblings[targetIdx].sort + 1 };
}

interface Sibling { kind: DragKind; id: string; sort: number; }
function mergedSiblings(parentId: string | null): Sibling[] {
  return [
    ...tree.childrenOf(parentId).map((f): Sibling => ({ kind: "folder", id: f.id, sort: f.sort_order })),
    ...tree.connectionsIn(parentId).map((c): Sibling => ({ kind: "connection", id: c.id, sort: c.sort_order })),
  ].sort((a, b) => a.sort - b.sort);
}

/**
 * Renumber siblings around the drop. Simpler than fractional indexing -
 * with hundreds of items it's still cheap to rewrite a whole sibling
 * list when reordering.
 */
function renumber(
  parentId: string | null,
  draggedKind: DragKind,
  draggedId: string,
  draggedSort: number
): Sibling[] {
  const sibs = mergedSiblings(parentId).filter(
    (s) => !(s.kind === draggedKind && s.id === draggedId)
  );
  const insertAt = sibs.findIndex((s) => s.sort >= draggedSort);
  const dragged: Sibling = { kind: draggedKind, id: draggedId, sort: 0 };
  if (insertAt < 0) sibs.push(dragged);
  else sibs.splice(insertAt, 0, dragged);
  return sibs.map((s, i) => ({ ...s, sort: i }));
}

/**
 * Reparent the dragged node and renumber its new siblings. Reload tree
 * at the end so all consumers see the new layout.
 */
export async function applyDrop(
  sourceKind: DragKind,
  sourceId: string,
  targetKind: DragKind,
  targetId: string,
  intent: DropIntent
): Promise<void> {
  const target =
    targetKind === "folder"
      ? tree.folderById(targetId)
      : tree.connectionById(targetId);
  if (!target) return;

  const { parentId, sortOrder } = resolveDropTarget(targetKind, target, intent);
  const siblingUpdates = renumber(parentId, sourceKind, sourceId, sortOrder);

  if (sourceKind === "folder") {
    await api.foldersUpdate({
      id: sourceId,
      parentId: parentId ?? undefined,
      clearParent: parentId === null,
    });
  } else {
    await api.connectionsUpdate({
      id: sourceId,
      folderId: parentId ?? undefined,
      clearFolder: parentId === null,
    });
  }

  for (const s of siblingUpdates) {
    try {
      if (s.kind === "folder") {
        await api.foldersUpdate({ id: s.id, sortOrder: s.sort });
      } else {
        await api.connectionsUpdate({ id: s.id, sortOrder: s.sort });
      }
    } catch (e) { console.error("renumber failed", s, e); }
  }

  await tree.load();
}

/**
 * Multi-drop: reparent N items into a single target folder (or root).
 * Skips the per-item reorder/renumber dance - moved items get appended
 * to the destination, leaving the source-side ordering unchanged. The
 * intent argument is collapsed: dropping multi on a folder always means
 * "into this folder"; dropping on a non-folder row means "into that
 * row's parent folder". For reorder you single-drag.
 *
 * Folder items skip themselves and any descendants of moved folders
 * (would create a cycle).
 */
export async function applyMultiDrop(
  connIds: string[],
  folderIds: string[],
  targetKind: DragKind,
  targetId: string,
): Promise<void> {
  const target =
    targetKind === "folder"
      ? tree.folderById(targetId)
      : tree.connectionById(targetId);
  if (!target) return;

  const destParentId =
    targetKind === "folder"
      ? (target as Folder).id
      : (target as Connection).folder_id ?? null;

  await applyMultiToParent(connIds, folderIds, destParentId);
}

export async function applyMultiDropToRoot(
  connIds: string[],
  folderIds: string[],
): Promise<void> {
  await applyMultiToParent(connIds, folderIds, null);
}

async function applyMultiToParent(
  connIds: string[],
  folderIds: string[],
  destParentId: string | null,
): Promise<void> {
  // Filter folder moves that would create a cycle: dropping a folder
  // into itself or any of its own descendants.
  const safeFolders = folderIds.filter((id) => {
    if (destParentId === null) return true;
    if (destParentId === id) return false;
    return !isDescendantOf(destParentId, id);
  });

  // SQLite (modernc, no WAL) serialises writes; firing N updates in
  // parallel produced "database is locked" failures and ~50% of rows
  // silently stayed put. Sequential is plenty fast at any realistic
  // multi-select size.
  for (const id of connIds) {
    try {
      await api.connectionsUpdate({
        id,
        folderId: destParentId ?? undefined,
        clearFolder: destParentId === null,
      });
    } catch (e) { console.error("multi-drop conn failed", id, e); }
  }
  for (const id of safeFolders) {
    try {
      await api.foldersUpdate({
        id,
        parentId: destParentId ?? undefined,
        clearParent: destParentId === null,
      });
    } catch (e) { console.error("multi-drop folder failed", id, e); }
  }
  await tree.load();
}

/**
 * Drop directly into the root (no specific target row). Used by an
 * empty area below the tree to reorganise back to top level.
 */
export async function applyDropToRoot(
  sourceKind: DragKind,
  sourceId: string
): Promise<void> {
  const siblings = mergedSiblings(null);
  const targetSort = siblings.length; // append at end
  const siblingUpdates = renumber(null, sourceKind, sourceId, targetSort);

  if (sourceKind === "folder") {
    await api.foldersUpdate({
      id: sourceId,
      clearParent: true,
    });
  } else {
    await api.connectionsUpdate({
      id: sourceId,
      clearFolder: true,
    });
  }
  for (const s of siblingUpdates) {
    try {
      if (s.kind === "folder") {
        await api.foldersUpdate({ id: s.id, sortOrder: s.sort });
      } else {
        await api.connectionsUpdate({ id: s.id, sortOrder: s.sort });
      }
    } catch (e) { console.error("renumber failed", s, e); }
  }
  await tree.load();
}
