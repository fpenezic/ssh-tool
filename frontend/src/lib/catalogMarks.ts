// Pure helpers for shared catalogs (no stores), so they can be tested.
import type { CatalogInfo } from "./api";
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
    case "waiting":
      return { text: "waits for profile sync", tone: "busy" };
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

