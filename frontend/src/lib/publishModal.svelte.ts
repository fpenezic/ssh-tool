// Opens the publish dialog from a folder's context menu. Mounted once in
// App.svelte.
class PublishModalStore {
  open = $state(false);
  folderId = $state("");
  folderName = $state("");

  show(folderId: string, folderName: string) {
    this.folderId = folderId;
    this.folderName = folderName;
    this.open = true;
  }

  close() {
    this.open = false;
  }
}

export const publishModal = new PublishModalStore();
