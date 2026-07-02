// Tiny pub/sub for the dynamic-folder editor modal so any tree
// context-menu can open it without prop-drilling.
class DynEditorStore {
  open = $state(false);
  parentId = $state<string | null>(null);
  existingFolderId = $state<string | null>(null);

  showNew(parentId: string | null) {
    this.parentId = parentId;
    this.existingFolderId = null;
    this.open = true;
  }

  showEdit(folderId: string) {
    this.parentId = null;
    this.existingFolderId = folderId;
    this.open = true;
  }

  close() {
    this.open = false;
    this.parentId = null;
    this.existingFolderId = null;
  }
}

export const dynEditor = new DynEditorStore();
