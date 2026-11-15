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

// Short copy for a catalog's state on its root row. action names what
// one click does there.
export function catalogStateLabel(c: CatalogInfo): { text: string; tone: "ok" | "warn" | "err" | "busy"; action?: "signin" | "sync" | "unlock" | "slots" | "cancel" } {
  switch (c.state) {
    case "syncing":
      return { text: "syncing", tone: "busy" };
    case "signing_in":
      return { text: "signing in", tone: "busy", action: "cancel" };
    case "no_login":
      return isMobile
        ? { text: "sign in on desktop", tone: "warn" }
        : { text: "sign in to sync", tone: "warn", action: "signin" };
    case "signed_out":
      return isMobile
        ? { text: "signed out - sign in on desktop", tone: "err" }
        : { text: "signed out", tone: "err", action: "signin" };
    case "error":
      return { text: "sync failed", tone: "err", action: "sync" };
    case "locked":
      return { text: "vault locked", tone: "warn", action: "unlock" };
  }
  if (c.unbound > 0) {
    return { text: `${c.unbound} slot${c.unbound === 1 ? "" : "s"} to bind`, tone: "warn", action: "slots" };
  }
  return { text: "", tone: "ok" };
}

/** A catalog marker in a materialised setting (see store/catalog_refs.go). */
export interface CatalogMarker {
  unbound: boolean;
  catalogId: string;
  uuid: string;
}

export function parseMarker(v: string | null | undefined): CatalogMarker | null {
  if (!v) return null;
  let rest: string;
  let unbound: boolean;
  if (v.startsWith("cat-unbound:")) {
    unbound = true;
    rest = v.slice("cat-unbound:".length);
  } else if (v.startsWith("cat-missing:")) {
    unbound = false;
    rest = v.slice("cat-missing:".length);
  } else {
    return null;
  }
  const i = rest.indexOf("/");
  return { unbound, catalogId: i < 0 ? rest : rest.slice(0, i), uuid: i < 0 ? "" : rest.slice(i + 1) };
}

class CatalogStore {
  list = $state<CatalogInfo[]>([]);
  // Slots per catalog, loaded on demand (the editor's marker labels, the
  // slots panel).
  slots = $state<Record<string, CatalogSlotInfo[]>>({});
  // Which catalog the Settings section should open on (deep link from
  // the tree or a toast).
  focus = $state<{ id: string; panel: "slots" | "" } | null>(null);
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

  /** Open the Catalogs section in Settings, optionally on one catalog's slots. */
  open(id?: string, panel: "slots" | "" = "") {
    this.focus = id ? { id, panel } : null;
    view.setTabSettingsSection("catalogs");
  }

  // One toast per catalog and reason per session: unbound slots after the
  // first sync (the first thing a new user meets), a dead session, a
  // failing sync. Background work stays visible without a modal.
  private attention() {
    for (const c of this.list) {
      const label = catalogStateLabel(c);
      let key = "";
      let msg = "";
      if (c.state === "signed_out") {
        key = c.id + ":signed_out";
        msg = isMobile ? `${c.name}: signed out - sign in on a desktop` : `${c.name}: signed out - click to sign in again`;
      } else if (c.state === "error") {
        key = c.id + ":error";
        msg = `${c.name}: sync failed - ${c.error || c.last_error || "see Settings"}`;
      } else if (c.state === "ok" && c.unbound > 0 && c.last_sync_at) {
        key = c.id + ":unbound";
        msg = `${c.name}: ${label.text} before its connections work - click to bind`;
      }
      if (!key || this.notified.has(key)) continue;
      this.notified.add(key);
      const panel = key.endsWith(":unbound") ? "slots" : "";
      toast.push(c.state === "ok" ? "info" : "err", msg, 10000, () => this.open(c.id, panel));
    }
  }
}

export const catalogs = new CatalogStore();
