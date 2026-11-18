// ssh-tool://catalog/open from the catalog's web page ("Open in ssh-tool",
// catalog PROTOCOL 8.3). Any page can open that URL, so a link only ever
// selects and reveals the record: it never connects, starts a forward or
// runs anything. A server that is not attached gets one offer, to open its
// own /connect page in the browser.
import { api } from "./api";
import { catalogs } from "./catalog.svelte";
import { catalogVisibility } from "./catalogVisibility.svelte";
import { tree, view } from "./stores.svelte";
import { toast } from "./toast.svelte.ts";
import { errMsg } from "./connectErrors";

export interface CatalogOpenLinkArgs { url: string; kind: string; uuid: string }

/** True when the row sits under a catalog folder hidden from the tree. */
function hiddenInTree(kind: "folder" | "connection", id: string): boolean {
  let fid: string | null | undefined = kind === "connection" ? tree.connectionById(id)?.folder_id : id;
  for (let i = 0; fid && i < 64; i++) {
    const f = tree.folderById(fid);
    if (!f) return false;
    if (f.catalog_id && f.remote_uuid && catalogVisibility.isHidden(f.catalog_id, f.remote_uuid)) return true;
    fid = f.parent_id;
  }
  return false;
}

export async function openCatalogLink(l: CatalogOpenLinkArgs): Promise<void> {
  let r;
  try {
    r = await api.catalogOpenLink(l.url, l.kind, l.uuid);
  } catch (e) {
    toast.err("Catalog link: " + errMsg(e), 6000);
    return;
  }
  if (r.status === "not_connected") {
    const page = r.connect_url ?? "";
    toast.info("That catalog is not connected to this app. Click to open its connect page in your browser.", 10000,
      () => { if (page) void api.openURL(page); });
    return;
  }
  const name = r.catalog_name || "Catalog";
  if (r.status === "not_found") {
    const why = r.sync_error ? ` (the sync failed: ${r.sync_error})` : "";
    toast.err(`${name}: not shared with you or no longer exists${why}`, 8000);
    return;
  }
  if (r.kind === "slot") {
    catalogs.open(r.catalog_id, "slots", r.id);
    return;
  }
  const kind = r.kind === "folder" ? "folder" : "connection";
  const id = r.id ?? "";
  const known = () => (kind === "folder" ? !!tree.folderById(id) : !!tree.connectionById(id));
  if (!known()) await tree.load();
  if (!known()) {
    toast.err(`${name}: not shared with you or no longer exists`, 8000);
    return;
  }
  if (hiddenInTree(kind, id)) {
    toast.info(`${name}: it is in a catalog folder you hid from your tree. Click to choose the folders you see.`, 8000,
      () => catalogs.open(r.catalog_id));
    return;
  }
  await view.reveal(kind, id);
}
