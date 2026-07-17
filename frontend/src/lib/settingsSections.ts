// Single source of truth for the Settings side-nav sections. Both
// Settings.svelte (renders the nav) and App.svelte (builds a
// "Settings: X" command-palette action per section, so Ctrl-K can
// jump straight to e.g. Bitwarden) consume this list. Keeping the
// list here means a new section shows up in the palette automatically.

export type SectionId =
  | "appearance"
  | "connection"
  | "network"
  | "terminal"
  | "recording"
  | "browser"
  | "snippets"
  | "workspaces"
  | "vault"
  | "external"
  | "backup"
  | "sync"
  | "audit"
  | "import"
  | "export"
  | "llm"
  | "sharing"
  | "logs"
  | "updates"
  | "about";

// The three external secret managers share one "External secrets"
// side-nav section with an in-section tab picker (so the sidebar
// doesn't grow a row per backend). These are the tab ids. They are
// ALSO accepted as deep-link targets (palette "bitwarden", the
// From-KeePass credential flow, a restored settings_active_section)
// and mapped to section "external" + the matching tab.
export type ExternalTabId = "keepass" | "bitwarden" | "infisical";

export const EXTERNAL_TABS: { id: ExternalTabId; title: string; keywords: string[] }[] = [
  { id: "keepass",   title: "KeePass",   keywords: ["kdbx", "secret", "password manager", "external"] },
  { id: "bitwarden", title: "Bitwarden", keywords: ["vaultwarden", "secret", "password manager", "external", "2fa"] },
  { id: "infisical", title: "Infisical", keywords: ["secret", "password manager", "external"] },
];

export function isExternalTab(id: string): id is ExternalTabId {
  return id === "keepass" || id === "bitwarden" || id === "infisical";
}

export type SectionGroup =
  | "Appearance"
  | "Network"
  | "Security"
  | "Import / Export"
  | "Integrations"
  | "Diagnostics";

export type SectionDef = {
  id: SectionId;
  title: string;
  group: SectionGroup;
  // Extra search terms for the command palette so a section is
  // findable by words that don't appear in its title (e.g. "2fa"
  // -> Bitwarden, "theme" -> Appearance). The title + group are
  // always searchable; these are additions.
  keywords?: string[];
};

export const SETTINGS_SECTIONS: SectionDef[] = [
  { id: "appearance",  title: "Appearance",        group: "Appearance",     keywords: ["theme", "colors", "font", "look"] },
  { id: "connection",  title: "Connection",        group: "Appearance",     keywords: ["defaults", "keepalive", "timeout"] },
  { id: "terminal",    title: "Terminal",          group: "Appearance",     keywords: ["xterm", "font", "webgl", "scrollback", "cursor"] },
  { id: "browser",     title: "Browser launcher",  group: "Appearance",     keywords: ["socks", "proxy", "chrome", "profile"] },
  { id: "snippets",    title: "Snippets",          group: "Appearance",     keywords: ["commands", "macros"] },
  { id: "workspaces",  title: "Workspaces",        group: "Appearance",     keywords: ["layout", "tabs", "session set"] },
  { id: "network",     title: "Network profiles",  group: "Network",        keywords: ["wireguard", "netbird", "tailscale", "vpn", "wg", "tunnel"] },
  { id: "recording",   title: "Session recording", group: "Security",       keywords: ["asciicast", "asciinema", "capture", "replay"] },
  { id: "vault",       title: "Vault",             group: "Security",       keywords: ["passphrase", "lock", "encryption", "master", "auto-unlock"] },
  { id: "external",    title: "External secrets",  group: "Security",       keywords: ["keepass", "kdbx", "bitwarden", "vaultwarden", "infisical", "secret", "password manager", "2fa"] },
  { id: "backup",      title: "Backup & restore",  group: "Security",       keywords: ["snapshot", "export", "restore", "auto-backup"] },
  { id: "sync",        title: "Sync",              group: "Security",       keywords: ["webdav", "push", "pull", "profile sync"] },
  { id: "audit",       title: "Audit log",         group: "Security",       keywords: ["history", "log", "activity"] },
  { id: "import",      title: "Import",            group: "Import / Export", keywords: ["rdm", "devolutions", "putty", "mobaxterm", "ssh config", "superputty", "kitty"] },
  { id: "export",      title: "Export connections", group: "Import / Export", keywords: ["backup", "csv", "dump"] },
  { id: "llm",         title: "LLM (MCP) access",  group: "Integrations",   keywords: ["mcp", "claude", "ai", "bridge", "yolo", "agent"] },
  { id: "sharing",     title: "Sharing",           group: "Integrations",   keywords: ["broadcast", "share", "collaborate"] },
  { id: "updates",     title: "Updates",           group: "Diagnostics",    keywords: ["version", "upgrade", "release", "changelog"] },
  { id: "logs",        title: "Logs",              group: "Diagnostics",    keywords: ["debug", "diagnostics", "troubleshoot"] },
  { id: "about",       title: "About",             group: "Diagnostics",    keywords: ["version", "credits", "license", "profile stats"] },
];
