// Drag & drop for the credentials tree. Simpler than the connections tree:
// credentials don't have sort_order (sorted by name), so we only support
// drop-inside (move-into-folder, or move-to-root when dropped on the empty
// area below the tree). No before/after intent, no reorder.

import { api } from "./api";
import { credentials } from "./stores.svelte";

export type CredDragKind = "credential" | "credentialFolder";

// Is moving `srcKind:srcId` into folder `targetFolderId` (null = root)
// a no-op or a cycle?
export function isInvalidCredDrop(
  srcKind: CredDragKind,
  srcId: string,
  targetFolderId: string | null,
  alsoMoving: string[] = [],
): boolean {
  if (srcKind === "credential") {
    // Multi-drag: the gesture is valid as long as at least one of the
    // moving credentials would actually change folder. A pure no-op
    // (every selected cred already lives in the target) stays invalid
    // so the drop indicator doesn't light up.
    const ids = Array.from(new Set([srcId, ...alsoMoving]));
    const anyMoves = ids.some((id) => {
      const c = credentials.byId(id);
      return c && (c.folder_id ?? null) !== targetFolderId;
    });
    return !anyMoves;
  }
  // Folder: can't drop on self, on current parent (no-op), or any descendant.
  if (srcId === targetFolderId) return true;
  const f = credentials.folderById(srcId);
  if (!f) return true;
  if ((f.parent_id ?? null) === targetFolderId) return true;
  if (targetFolderId && isDescendantOf(targetFolderId, srcId)) return true;
  return false;
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

export async function applyCredDrop(
  srcKind: CredDragKind,
  srcId: string,
  targetFolderId: string | null,
  alsoMoving: string[] = [],
): Promise<void> {
  if (srcKind === "credential") {
    // Multi-select drag moves every selected credential, not just the
    // grabbed row. Build the unique id set (grabbed + the rest), drop
    // any that already live in the target folder (no-op), and move the
    // remainder. Folders never carry a multi set.
    const ids = Array.from(new Set([srcId, ...alsoMoving]));
    for (const id of ids) {
      const c = credentials.byId(id);
      if (!c) continue;
      if ((c.folder_id ?? null) === targetFolderId) continue; // already there
      await api.credentialsUpdate({
        id,
        folder_id: targetFolderId ?? undefined,
        set_folder_to_null: targetFolderId === null,
      });
    }
  } else {
    await api.credentialFoldersUpdate(
      srcId,
      undefined,
      targetFolderId ?? undefined,
      targetFolderId === null,
    );
  }
  await credentials.load();
}
