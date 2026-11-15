// Opens the publish dialog from a folder's context menu. Mounted once in
// App.svelte.
class PublishModalStore {
  open = $state(false);
  folderId = $state("");
  folderName = $state("");
  // A drop onto a catalog folder: preselect it as the target.
  targetUUID = $state("");
  catalogId = $state("");

  show(folderId: string, folderName: string, targetUUID = "", catalogId = "") {
    this.folderId = folderId;
    this.folderName = folderName;
    this.targetUUID = targetUUID;
    this.catalogId = catalogId;
    this.open = true;
  }

  close() {
    this.open = false;
  }
}

export const publishModal = new PublishModalStore();
