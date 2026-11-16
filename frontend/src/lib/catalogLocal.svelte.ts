// "Remove catalog" keeping it as a local folder, and "Copy to my
// connections": the copy dialog's state and the report of what a keep or
// a copy left empty (CatalogLocalModal shows both).
import { api, type LocaliseResult } from "./api";
import { toast } from "./toast.svelte";
import { tree, selection } from "./stores.svelte";
import { errMsg } from "./connectErrors";

class CatalogLocalStore {
  copy = $state<{ kind: "folders" | "connections"; id: string; name: string } | null>(null);
  report = $state<{ title: string; res: LocaliseResult } | null>(null);

  startCopy(kind: "folders" | "connections", id: string, name: string) {
    this.copy = { kind, id, name };
  }

  /** The summary toast; a click opens the list of empty fields. */
  announce(title: string, res: LocaliseResult) {
    const kept = `${res.connections} connection${res.connections === 1 ? "" : "s"}` + (res.folders ? `, ${res.folders} folder${res.folders === 1 ? "" : "s"}` : "");
    const empty = res.emptied.length;
    if (empty === 0 && !(res.outside_jumps?.length)) {
      toast.ok(`${title}: ${kept}`, 6000);
      return;
    }
    const tail = empty ? ` - ${empty} field${empty === 1 ? "" : "s"} left empty, click for the list` : " - click for details";
    toast.info(`${title}: ${kept}${tail}`, 15000, () => (this.report = { title, res }));
  }

  async runCopy(targetId: string): Promise<string | null> {
    const c = this.copy;
    if (!c) return null;
    try {
      const res = await api.catalogCopyToLocal(c.kind, c.id, targetId);
      this.copy = null;
      await tree.load();
      this.announce(`Copied ${c.name}`, res);
      if (res.root_id) {
        if (c.kind === "folders") void selection.selectFolderById(res.root_id);
        else void selection.selectConnection(res.root_id);
      }
      return null;
    } catch (e) {
      return errMsg(e);
    }
  }
}

export const catalogLocal = new CatalogLocalStore();
