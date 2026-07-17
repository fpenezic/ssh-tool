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
  | "keepass"
  | "bitwarden"
  | "infisical"
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

export type SectionGroup =
  | "Appearance"
  | "Security"
  | "Import / Export"
  | "Integrations"
  | "App"
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
  { id: "network",     title: "Network profiles",  group: "Appearance",     keywords: ["wireguard", "netbird", "tailscale", "vpn", "wg"] },
  { id: "terminal",    title: "Terminal",          group: "Appearance",     keywords: ["xterm", "font", "webgl", "scrollback", "cursor"] },
  { id: "browser",     title: "Browser launcher",  group: "Appearance",     keywords: ["socks", "proxy", "chrome", "profile"] },
  { id: "snippets",    title: "Snippets",          group: "Appearance",     keywords: ["commands", "macros"] },
  { id: "workspaces",  title: "Workspaces",        group: "Appearance",     keywords: ["layout", "tabs", "session set"] },
  { id: "recording",   title: "Session recording", group: "Security",       keywords: ["asciicast", "asciinema", "capture", "replay"] },
  { id: "vault",       title: "Vault",             group: "Security",       keywords: ["passphrase", "lock", "encryption", "master", "auto-unlock"] },
  { id: "keepass",     title: "KeePass",           group: "Security",       keywords: ["kdbx", "secret", "password manager", "external"] },
  { id: "bitwarden",   title: "Bitwarden",         group: "Security",       keywords: ["vaultwarden", "secret", "password manager", "external"] },
  { id: "infisical",   title: "Infisical",         group: "Security",       keywords: ["secret", "password manager", "external"] },
  { id: "backup",      title: "Backup & restore",  group: "Security",       keywords: ["snapshot", "export", "restore", "auto-backup"] },
  { id: "sync",        title: "Sync",              group: "Security",       keywords: ["webdav", "push", "pull", "profile sync"] },
  { id: "audit",       title: "Audit log",         group: "Security",       keywords: ["history", "log", "activity"] },
  { id: "import",      title: "Import",            group: "Import / Export", keywords: ["rdm", "devolutions", "putty", "mobaxterm", "ssh config", "superputty", "kitty"] },
  { id: "export",      title: "Export connections", group: "Import / Export", keywords: ["backup", "csv", "dump"] },
  { id: "llm",         title: "LLM (MCP) access",  group: "Integrations",   keywords: ["mcp", "claude", "ai", "bridge", "yolo", "agent"] },
  { id: "sharing",     title: "Sharing",           group: "Integrations",   keywords: ["broadcast", "share", "collaborate"] },
  { id: "updates",     title: "Updates",           group: "App",            keywords: ["version", "upgrade", "release", "changelog"] },
  { id: "logs",        title: "Logs",              group: "Diagnostics",    keywords: ["debug", "diagnostics", "troubleshoot"] },
  { id: "about",       title: "About",             group: "Diagnostics",    keywords: ["version", "credits", "license", "profile stats"] },
];
