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
  focus = $state<{ id: string; panel: "slots" | "" } | null>(null);
  // An enrollment waiting for the user's confirmation (a link, or the
  // "Add with a code" form). Nothing is redeemed until they confirm.
  enroll = $state<{ url: string; code: string } | null>(null);
  private wired = false;
  private notified = new Set<string>();

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
    return slot ? `slot "${slot.name}" not bound` : "catalog slot not bound";
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
    this.load();
  }

  /** Show the enrollment confirmation (code may be empty: typed there). */
  startEnroll(url: string, code: string) {
    this.enroll = { url, code };
  }

  /** Open the Catalogs section in Settings, optionally on one catalog's slots. */
  open(id?: string, panel: "slots" | "" = "") {
    this.focus = id ? { id, panel } : null;
    view.setTabSettingsSection("catalogs");
  }

  // One toast per catalog and reason per session: a dead session, a
  // failing sync. Background work stays visible without a modal. Unbound
  // slots get no toast: the connect dialog asks for them when a connection
  // needs them (catalogBindGate), and the root's chip is the standing hint.
  private attention() {
    for (const c of this.list) {
      let key = "";
      let msg = "";
      if (c.state === "signed_out") {
        key = c.id + ":signed_out";
        msg = isMobile ? `${c.name}: signed out - sign in again with a code from the catalog` : `${c.name}: signed out - click to sign in again`;
      } else if (c.state === "error") {
        key = c.id + ":error";
        msg = `${c.name}: sync failed - ${c.error || c.last_error || "see Settings"}`;
      }
      if (!key || this.notified.has(key)) continue;
      this.notified.add(key);
      toast.push("err", msg, 10000, () => this.open(c.id, ""));
    }
  }
}

export const catalogs = new CatalogStore();
