// Tiny pub/sub for the connection-export modal so any tree-node
// right-click can open it without prop-drilling through Sidebar.
// Mounted once in App.svelte, opens whenever something calls
// exportModal.open(ids, suggestedName).

class ExportModalStore {
  open = $state(false);
  connectionIds = $state<string[]>([]);
  folderIds = $state<string[]>([]);
  suggestedName = $state("ssh-tool-export");

  show(ids: string[], suggestedName?: string) {
    this.connectionIds = ids;
    this.folderIds = [];
    this.suggestedName = suggestedName ?? "ssh-tool-export";
    this.open = true;
  }

  showFolders(folderIds: string[], suggestedName?: string) {
    this.connectionIds = [];
    this.folderIds = folderIds;
    this.suggestedName = suggestedName ?? "ssh-tool-export";
    this.open = true;
  }

  close() {
    this.open = false;
    this.connectionIds = [];
    this.folderIds = [];
  }
}

export const exportModal = new ExportModalStore();
