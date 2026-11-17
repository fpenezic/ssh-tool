// Opens the publish dialog from a folder's or connection's context menu,
// or a drop onto a catalog folder. Mounted once in App.svelte.
class PublishModalStore {
  open = $state(false);
  folderId = $state("");
  folderName = $state("");
  // A single connection instead of a folder (folderId is "" then).
  connId = $state("");
  // A drop onto a catalog folder: preselect it as the target.
  targetUUID = $state("");
  catalogId = $state("");

  show(folderId: string, folderName: string, targetUUID = "", catalogId = "") {
    this.folderId = folderId;
    this.connId = "";
    this.folderName = folderName;
    this.targetUUID = targetUUID;
    this.catalogId = catalogId;
    this.open = true;
  }

  showConnection(connId: string, name: string, targetUUID = "", catalogId = "") {
    this.show("", name, targetUUID, catalogId);
    this.connId = connId;
  }

  close() {
    this.open = false;
  }
}

export const publishModal = new PublishModalStore();
