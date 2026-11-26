// Shared connection catalogs on the frontend: the list with each catalog's
// state (kept live from the backend's catalog_status / catalog_changed
// events), the attention signals, and helpers for the catalog markers a
// materialised setting can hold.
//
// A catalog's rows are ordinary folders / connections / forwards carrying
// catalog_id; its root folder carries catalog_id without remote_uuid. The
// tree, the editor and the forwards list ask this store what to show.

import { EventsOn } from "./wailsRuntime";
import { api, type CatalogInfo, type CatalogState, type CatalogSlotInfo } from "./api";
import { toast } from "./toast.svelte";
import { tree, view } from "./stores.svelte";
import { isMobile } from "./platform";

export { catalogStateLabel, parseMarker, type CatalogMarker } from "./catalogMarks";
import { catalogStateLabel, parseMarker } from "./catalogMarks";

class CatalogStore {
  list = $state<CatalogInfo[]>([]);
  // Slots per catalog, loaded on demand (the editor's marker labels, the
  // slots panel).
  slots = $state<Record<string, CatalogSlotInfo[]>>({});
  // Which catalog the Settings section should open on (deep link from
  // the tree or a toast).
  // slot: one slot to show (the editor's "bind the slot" link).
  focus = $state<{ id: string; panel: "slots" | ""; slot?: string } | null>(null);
  // An enrollment waiting for the user's confirmation (a link, or the
  // "Add with a code" form). Nothing is redeemed until they confirm.
  enroll = $state<{ url: string; code: string; network?: string } | null>(null);
  // Icon images a publish held back for the catalog's upload limit, being
  // sent in the background (per catalog). Shown in the status bar.
  iconUploads = $state<Record<string, { name: string; left: number; total: number; waitingUntil?: number; message?: string }>>({});
  private wired = false;
  private notified = new Set<string>();

  // The local top-level "Matched to <catalog>" folders Match my connections
  // parks the replaced copies in (app_catalog_match.go matchParkName). Their
  // connections duplicate catalog ones, so tree search and Ctrl-K leave them
  // out; the folder itself stays in the tree.
  matchParkFolders = $derived.by(() => {
    void tree.version;
    const names = new Set(this.list.map((c) => "Matched to " + c.name));
    const out = new Set<string>();
    if (names.size === 0) return out;
    for (const f of tree.folders) {
      if (f.parent_id === null && !f.catalog_id && names.has(f.name)) out.add(f.id);
    }
    return out;
  });

  // inMatchPark: the folder is a "Matched to" folder or sits inside one.
  inMatchPark(folderId: string | null | undefined): boolean {
    const parks = this.matchParkFolders;
    if (!folderId || parks.size === 0) return false;
    let id: string | null = folderId;
    for (let guard = 0; id && guard < 64; guard++) {
      if (parks.has(id)) return true;
      id = tree.folderById(id)?.parent_id ?? null;
    }
    return false;
  }

  get byRoot(): Map<string, CatalogInfo> {
    return new Map(this.list.map((c) => [c.root_folder_id, c]));
  }
  byId(id: string | undefined | null): CatalogInfo | undefined {
    return id ? this.list.find((c) => c.id === id) : undefined;
  }

  async load() {
    try {
      this.list = (await api.catalogList()) ?? [];
    } catch {
      this.list = [];
    }
    this.attention();
  }

  async loadSlots(id: string): Promise<CatalogSlotInfo[]> {
    try {
      const s = (await api.catalogSlots(id)) ?? [];
      this.slots = { ...this.slots, [id]: s };
      return s;
    } catch {
      return this.slots[id] ?? [];
    }
  }

  /** Text for a marker: the slot's name when known. */
  markerLabel(v: string | null | undefined): string | null {
    const m = parseMarker(v);
    if (!m) return null;
    if (!this.byId(m.catalogId)) return "not set - choose one";
    const slot = this.slots[m.catalogId]?.find((s) => s.uuid === m.uuid);
    if (!m.unbound) return "not available to you - set your own";
    return slot ? `placeholder "${slot.name}" not bound` : "catalog placeholder not bound";
  }

  init() {
    if (this.wired) return;
    this.wired = true;
    EventsOn("catalog_status", (e: any) => {
      const id = String(e?.catalog_id ?? "");
      const state = String(e?.state ?? "") as CatalogState;
      this.list = this.list.map((c) => (c.id === id ? { ...c, state, error: e?.error ?? "" } : c));
      if (state === "signed_out" || state === "error") this.attention();
    });
    EventsOn("catalog_changed", async () => {
      await Promise.all([this.load(), tree.load()]);
      for (const id of Object.keys(this.slots)) this.loadSlots(id);
    });
    EventsOn("profile_reloaded", () => this.load());
    EventsOn("catalog_icon_uploads", (e: any) => {
      const id = String(e?.catalog_id ?? "");
      const left = Number(e?.left ?? 0);
      const next = { ...this.iconUploads };
      const was = next[id];
      if (left <= 0) {
        delete next[id];
        if (was && Number(e?.total ?? 0) > 0) toast.info(`${was.name}: icon images sent to the catalog`);
      } else {
        next[id] = { name: String(e?.catalog_name ?? ""), left, total: Number(e?.total ?? left),
          waitingUntil: e?.waiting_until ? Number(e.waiting_until) : undefined, message: e?.message || undefined };
      }
      this.iconUploads = next;
    });
    this.load();
  }

  /** Show the enrollment confirmation (code may be empty: typed there). */
  startEnroll(url: string, code: string, network = "") {
    this.enroll = { url, code, network };
  }

  /** Open the Catalogs section in Settings, optionally on one catalog's slots. */
  open(id?: string, panel: "slots" | "" = "", slot = "") {
    this.focus = id ? { id, panel, slot: slot || undefined } : null;
    view.setTabSettingsSection("catalogs");
  }

  // A failing sync (server out of reach, VPN down) is no toast: it shows in
  // the status bar's issues badge (issues.svelte.ts) for as long as it
  // lasts and goes away with the next good pass, however often the sync
  // runs. A dead session needs the user, so it also gets one toast per
  // session. Unbound slots get none: the connect dialog asks for them when
  // a connection needs them (catalogBindGate), and the root's chip is the
  // standing hint.
  private attention() {
    for (const c of this.list) {
      if (c.state !== "signed_out") continue;
      const key = c.id + ":signed_out";
      if (this.notified.has(key)) continue;
      this.notified.add(key);
      const msg = isMobile ? `${c.name}: signed out - sign in again with a code from the catalog` : `${c.name}: signed out - click to sign in again`;
      toast.push("err", msg, 10000, () => this.open(c.id, ""));
    }
  }

  /** Catalogs whose last pass failed or whose session ended, for the issues badge. */
  get troubled(): CatalogInfo[] {
    return this.list.filter((c) => c.state === "error" || c.state === "signed_out");
  }
}

export const catalogs = new CatalogStore();
