// Pure helpers for shared catalogs (no stores), so they can be tested.
import type { CatalogInfo } from "./api";
import { isMobile } from "./platform";

const backendNames: Record<string, string> = { bitwarden: "Bitwarden", infisical: "Infisical" };

// The name of an external placeholder's secret backend, as a button says it.
export function externalBackendLabel(backend: string | undefined): string {
  const b = (backend ?? "").trim();
  return backendNames[b.toLowerCase()] ?? (b || "the vault");
}

// Short copy for a catalog's state on its root row. action names what
// one click does there.
export function catalogStateLabel(c: CatalogInfo): { text: string; tone: "ok" | "warn" | "err" | "busy"; action?: "signin" | "code" | "sync" | "unlock" | "slots" | "cancel" } {
  switch (c.state) {
    case "syncing":
      return { text: "syncing", tone: "busy" };
    case "signing_in":
      return { text: "signing in", tone: "busy", action: "cancel" };
    case "no_login":
      return isMobile
        ? { text: "sign in with a code", tone: "warn", action: "code" }
        : { text: "sign in to sync", tone: "warn", action: "signin" };
    case "signed_out":
      return isMobile
        ? { text: "signed out - sign in with a code", tone: "err", action: "code" }
        : { text: "signed out", tone: "err", action: "signin" };
    case "error":
      return { text: "sync failed", tone: "err", action: "sync" };
    case "locked":
      return { text: "vault locked", tone: "warn", action: "unlock" };
    case "waiting":
      return { text: "waits for profile sync", tone: "busy" };
    case "waiting_vpn":
      return { text: "waits for VPN", tone: "busy", action: "sync" };
  }
  if (c.unbound > 0) {
    return { text: `${c.unbound} to bind`, tone: "warn", action: "slots" };
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


/** What an ssh-tool:// link asks for (Android hands the whole URL over). */
export type SshToolLink =
  | { kind: "catalog"; url: string; code: string }
  | { kind: "catalog_open"; url: string; recordKind: string; uuid: string }
  | { kind: "import"; source: string }
  | null;

export function parseSshToolLink(raw: string): SshToolLink {
  if (!/^ssh-tool:\/\//i.test(raw)) return null;
  let u: URL;
  try {
    // URL() does not know the scheme's authority rules everywhere; parse
    // it as http with the same host and path.
    u = new URL("http://" + raw.slice("ssh-tool://".length));
  } catch {
    return null;
  }
  const host = u.hostname.toLowerCase();
  const path = u.pathname.replace(/\/+$/, "");
  if (host === "catalog" && path === "/add") {
    const url = u.searchParams.get("url") ?? "";
    const code = u.searchParams.get("code") ?? "";
    return url && code ? { kind: "catalog", url, code } : null;
  }
  if (host === "catalog" && path === "/open") {
    // Select and reveal only; an unknown kind or a malformed uuid is no
    // link at all (catalog PROTOCOL 8.3).
    const url = u.searchParams.get("url") ?? "";
    const recordKind = u.searchParams.get("kind") ?? "";
    const uuid = (u.searchParams.get("uuid") ?? "").toLowerCase();
    const okKind = recordKind === "connections" || recordKind === "folders" || recordKind === "slots";
    const okUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(uuid);
    return url && okKind && okUUID ? { kind: "catalog_open", url, recordKind, uuid } : null;
  }
  if (host === "import" || path === "/import") {
    const source = u.searchParams.get("source") ?? "";
    return source ? { kind: "import", source } : null;
  }
  return null;
}
