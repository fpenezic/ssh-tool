// Unsaved edits a navigation would throw away. An editor registers a
// source while it is mounted; whatever moves the view away from it (a tree
// selection, an editor tab, a profile switch) asks confirmLeave() first and
// goes ahead only on true. The prompt offers Save, Discard and Stay.
import { confirmModal } from "./confirmModal.svelte.ts";

export interface UnsavedSource {
  // Shown in the prompt: "db-01", "the forward on db-01".
  label: () => string;
  dirty: () => boolean;
  // false when the save failed (the editor shows why): the view stays.
  save: () => Promise<boolean>;
  // Throws the edits away, back to the stored values.
  discard: () => void;
  // Which navigation drops it. "editor" (default): the connection / folder
  // editor, lost on a selection change. "tab": a sub-form inside one editor
  // tab (the forward form), lost on a tab switch and a selection change.
  // "settings": a Settings page form; Settings stays mounted, so only its
  // own navigation (a profile switch) asks.
  scope?: Scope;
}

export type Scope = "editor" | "tab" | "settings";
// What a selection change in the tree drops.
export const SELECTION: Scope[] = ["editor", "tab"];

const sources = new Set<UnsavedSource>();
let asking = false;

export function registerUnsaved(src: UnsavedSource): () => void {
  sources.add(src);
  return () => { sources.delete(src); };
}

function dirtySources(scopes: Scope[]): UnsavedSource[] {
  return [...sources].filter((s) => scopes.includes(s.scope ?? "editor") && s.dirty());
}

export function hasUnsaved(scopes: Scope[] = SELECTION): boolean {
  return dirtySources(scopes).length > 0;
}

// Resolves true when the caller may navigate: nothing was dirty, the edits
// were saved, or the user chose Discard.
export async function confirmLeave(scopes: Scope[] = SELECTION): Promise<boolean> {
  const dirty = dirtySources(scopes);
  if (dirty.length === 0) return true;
  // A second navigation while the prompt is open is dropped; the first
  // one decides.
  if (asking) return false;
  asking = true;
  try {
    const names = dirty.map((s) => s.label()).filter(Boolean);
    const list = names.length ? names.join(", ") : "this editor";
    const what = list.charAt(0).toUpperCase() + list.slice(1);
    const r = await confirmModal.show({
      title: "Unsaved changes",
      message: `${what} has changes that are not saved.`,
      okLabel: "Save",
      altLabel: "Discard",
      cancelLabel: "Stay",
    });
    if (r.alt) {
      for (const s of dirty) s.discard();
      return true;
    }
    if (!r.ok) return false;
    for (const s of dirty) {
      if (!(await s.save())) return false;
    }
    // Something may have been typed while the saves ran: leave only when
    // every source in scope is clean now.
    return dirtySources(scopes).length === 0;
  } finally {
    asking = false;
  }
}
