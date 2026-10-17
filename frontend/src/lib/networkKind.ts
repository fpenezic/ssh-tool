import { isMobile } from "./platform";

// Display names for network profile kinds and the plugins behind them,
// shared by every picker and card so a new kind is named in one place.

export const ocFlavorTitle: Record<string, string> = {
  anyconnect: "Cisco AnyConnect / ocserv", fortinet: "Fortinet SSL VPN", f5: "F5 BIG-IP",
  gp: "Palo Alto GlobalProtect", pulse: "Pulse / Ivanti", nc: "Juniper Network Connect",
};

// Short VPN type for an OpenConnect profile ("AnyConnect", not the
// gateway-family label the edit form shows).
const ocFlavorShort: Record<string, string> = {
  anyconnect: "AnyConnect", fortinet: "Fortinet", f5: "F5",
  gp: "GlobalProtect", pulse: "Pulse", nc: "Network Connect",
};

export function pluginTitle(name: string): string {
  switch (name) {
    case "netbird": return "NetBird";
    case "tailscale": return "Tailscale";
    case "openconnect": return "OpenConnect";
    case "openvpn": return "OpenVPN";
    case "checkpoint": return "Check Point";
    case "wireguard": return "WireGuard";
    default: return name;
  }
}

// profileKindTitle names a profile for pickers: the VPN type for an
// OpenConnect profile, else the kind.
export function profileKindTitle(np: { kind: string; openconnect?: { flavor?: string } }): string {
  if (np.kind === "openconnect") return ocFlavorShort[np.openconnect?.flavor ?? "anyconnect"] ?? "OpenConnect";
  return pluginTitle(np.kind || "wireguard");
}

// Plugin-backed kinds run as a separate process, which android / iOS
// cannot spawn: a profile of such a kind (synced from a desktop) never
// brings its tunnel up there. "auto" still dials directly, through the
// system VPN when the official app is connected.
const PLUGIN_KINDS = new Set(["netbird", "tailscale", "openconnect", "openvpn", "checkpoint"]);
export function profileRunsHere(np: { kind: string }): boolean {
  return !(isMobile && PLUGIN_KINDS.has(np.kind));
}
export const NOT_ON_MOBILE_TITLE =
  "This VPN runs as a plugin, which mobile cannot start. With the profile on auto the connection goes direct, so connect the official VPN app as the system VPN; on always it cannot connect here.";
