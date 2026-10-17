// Drag & drop for the credentials tree, following the connections tree:
// every row is a drop target, the empty area below the rows is the root,
// and a multi-selection moves as one. Credentials and credential folders
// sort by name, so a drop picks the folder an item lands in, never a
// slot: over a folder row the middle half drops into that folder and the
// top / bottom quarter beside it (its parent folder); over a credential
// row it drops into that credential's folder. The highlight is always on
// the folder that receives the drop (the tree itself for the root).

import { api } from "./api";
import { credentials, drag } from "./stores.svelte";
import { computeIntent } from "./treeDnd";

export interface CredDragSet {
  creds: string[];
  folders: string[];
}

export type CredDropRow = { kind: "credential" | "credentialFolder"; id: string };

// The items the current gesture moves: the grabbed row plus the rest of
// the selection when the grabbed row is part of it. null = not a
// credentials-tree drag (a tab, a connection, a file).
export function credDragSet(): CredDragSet | null {
  if (drag.credentialId) {
    return { creds: uniq([drag.credentialId, ...drag.multiCredIds]), folders: [] };
  }
  if (drag.credentialFolderId) {
    return { creds: [], folders: uniq([drag.credentialFolderId, ...drag.multiCredFolderIds]) };
  }
  return null;
}

function uniq(ids: string[]): string[] {
  return Array.from(new Set(ids));
}

// The folder a drop over `row` lands in (null = root).
export function credDropTarget(e: DragEvent, rowEl: HTMLElement, row: CredDropRow): string | null {
  if (row.kind === "credential") return credentials.byId(row.id)?.folder_id ?? null;
  if (computeIntent(e, rowEl, true) === "inside") return row.id;
  return credentials.folderById(row.id)?.parent_id ?? null;
}

// A drop is invalid when it would put a folder inside itself or one of
// its descendants, or when nothing would actually move.
export function isInvalidCredDrop(set: CredDragSet, target: string | null): boolean {
  for (const id of set.folders) {
    if (target && isDescendantOf(target, id)) return true;
  }
  const credMoves = set.creds.some((id) => {
    const c = credentials.byId(id);
    return c && (c.folder_id ?? null) !== target;
  });
  const folderMoves = set.folders.some((id) => {
    const f = credentials.folderById(id);
    return f && (f.parent_id ?? null) !== target;
  });
  return !credMoves && !folderMoves;
}

function isDescendantOf(candidate: string, ancestor: string): boolean {
  let cur: string | null = candidate;
  for (let i = 0; i < 10000; i++) {
    if (!cur) return false;
    if (cur === ancestor) return true;
    const f = credentials.folderById(cur);
    if (!f) return false;
    cur = f.parent_id ?? null;
  }
  return false;
}

export async function applyCredDrop(set: CredDragSet, target: string | null): Promise<void> {
  for (const id of set.creds) {
    const c = credentials.byId(id);
    if (!c || (c.folder_id ?? null) === target) continue;
    await api.credentialsUpdate({
      id,
      folder_id: target ?? undefined,
      set_folder_to_null: target === null,
    });
  }
  for (const id of set.folders) {
    const f = credentials.folderById(id);
    if (!f || (f.parent_id ?? null) === target) continue;
    await api.credentialFoldersUpdate(id, undefined, target ?? undefined, target === null);
  }
  await credentials.load();
}

// Row handlers. They stop propagation either way so the tree's root
// handler never sees a drag that is over a row: an invalid target shows
// the no-drop cursor instead of falling through to "move to root".
export function credRowDragOver(e: DragEvent, row: CredDropRow) {
  const set = credDragSet();
  if (!set) return;
  e.stopPropagation();
  const target = credDropTarget(e, e.currentTarget as HTMLElement, row);
  if (isInvalidCredDrop(set, target)) {
    drag.hoverCredFolder(null);
    return;
  }
  e.preventDefault();
  e.dataTransfer!.dropEffect = "move";
  drag.hoverCredFolder(target ?? "ROOT");
}

export async function credRowDrop(e: DragEvent, row: CredDropRow) {
  const set = credDragSet();
  if (!set) return;
  e.preventDefault();
  e.stopPropagation();
  const target = credDropTarget(e, e.currentTarget as HTMLElement, row);
  drag.end();
  if (isInvalidCredDrop(set, target)) return;
  try { await applyCredDrop(set, target); }
  catch (err) { console.error("cred drop failed", err); }
}

// Empty area below the rows: move to the root.
export function credAreaDragOver(e: DragEvent) {
  const set = credDragSet();
  if (!set) return;
  if (isInvalidCredDrop(set, null)) {
    drag.hoverCredFolder(null);
    return;
  }
  e.preventDefault();
  e.dataTransfer!.dropEffect = "move";
  drag.hoverCredFolder("ROOT");
}

export function credAreaDragLeave(e: DragEvent) {
  if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
  drag.hoverCredFolder(null);
}

export async function credAreaDrop(e: DragEvent) {
  const set = credDragSet();
  if (!set) return;
  e.preventDefault();
  drag.end();
  if (isInvalidCredDrop(set, null)) return;
  try { await applyCredDrop(set, null); }
  catch (err) { console.error("cred drop failed", err); }
}
