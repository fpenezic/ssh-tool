// Single source of truth for the Settings side-nav sections. Both
// Settings.svelte (renders the nav) and App.svelte (builds a
// "Settings: X" command-palette action per section, so Ctrl-K can
// jump straight to e.g. Bitwarden) consume this list. Keeping the
// list here means a new section shows up in the palette automatically.

export type SectionId =
  | "appearance"
  | "window"
  | "shells"
  | "connection"
  | "network"
  | "liveness"
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
  | "desktop"
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
  | "General"
  | "Terminal"
  | "Connections"
  | "Security"
  | "Data"
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
  { id: "appearance",  title: "Appearance",        group: "General",        keywords: ["theme", "colors", "font", "look", "density", "status bar", "server status"] },
  { id: "window",      title: "Window & startup",  group: "General",        keywords: ["tray", "minimise", "minimize", "close", "startup", "restore", "tabs"] },
  { id: "updates",     title: "Updates",           group: "General",        keywords: ["version", "upgrade", "release", "changelog"] },
  { id: "terminal",    title: "Terminal",          group: "Terminal",       keywords: ["xterm", "font", "webgl", "scrollback", "cursor", "copy", "paste", "timestamp", "color scheme"] },
  { id: "shells",      title: "Shells",            group: "Terminal",       keywords: ["local shell", "wsl", "powershell", "cmd", "external terminal", "windows terminal"] },
  { id: "snippets",    title: "Snippets",          group: "Terminal",       keywords: ["commands", "macros"] },
  { id: "workspaces",  title: "Workspaces",        group: "Terminal",       keywords: ["layout", "tabs", "session set"] },
  { id: "recording",   title: "Session recording", group: "Terminal",       keywords: ["asciicast", "asciinema", "capture", "replay"] },
  { id: "connection",  title: "Connection",        group: "Connections",    keywords: ["defaults", "keepalive", "timeout"] },
  { id: "network",     title: "Network profiles",  group: "Connections",    keywords: ["wireguard", "netbird", "tailscale", "vpn", "wg", "tunnel"] },
  { id: "liveness",    title: "Liveness probe",    group: "Connections",    keywords: ["ping", "reachability", "up", "down", "probe", "status", "alive"] },
  { id: "browser",     title: "Browser launcher",  group: "Connections",    keywords: ["socks", "proxy", "chrome", "profile"] },
  { id: "vault",       title: "Vault",             group: "Security",       keywords: ["passphrase", "lock", "encryption", "master", "auto-unlock"] },
  { id: "external",    title: "External secrets",  group: "Security",       keywords: ["keepass", "kdbx", "bitwarden", "vaultwarden", "infisical", "secret", "password manager", "2fa"] },
  { id: "audit",       title: "Audit log",         group: "Security",       keywords: ["history", "log", "activity"] },
  { id: "backup",      title: "Backup & restore",  group: "Data",           keywords: ["snapshot", "export", "restore", "auto-backup"] },
  { id: "sync",        title: "Sync",              group: "Data",           keywords: ["webdav", "push", "pull", "profile sync", "dropbox", "onedrive", "google drive"] },
  { id: "import",      title: "Import",            group: "Data",           keywords: ["rdm", "devolutions", "putty", "mobaxterm", "ssh config", "superputty", "kitty"] },
  { id: "export",      title: "Export connections", group: "Data",          keywords: ["backup", "csv", "dump"] },
  { id: "llm",         title: "LLM (MCP) access",  group: "Integrations",   keywords: ["mcp", "claude", "ai", "bridge", "yolo", "agent"] },
  { id: "sharing",     title: "Session sharing",   group: "Integrations",   keywords: ["share", "browser", "guest", "collaborate"] },
  { id: "desktop",     title: "Desktop integration", group: "Integrations", keywords: ["install", "uninstall", "start menu", "applications menu", "launcher", "icon", "shortcut", "context menu", "explorer", "nautilus", "dolphin", "url scheme", "ssh-tool://", "protocol handler", "right-click"] },
  { id: "logs",        title: "Logs",              group: "Diagnostics",    keywords: ["debug", "diagnostics", "troubleshoot"] },
  { id: "about",       title: "About",             group: "Diagnostics",    keywords: ["version", "credits", "license", "profile stats"] },
];
