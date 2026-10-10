// Which status bar items the user wants to see (right-click the bar).
// Stored as the list of HIDDEN items, so anything added later shows by
// default. Indicators that flag a problem or a live exposure (vault
// locked, issues, update, shared / MCP sessions, VPN, sync, captures, log
// tails) and
// the version (the way to About) are not in the list and always show.

import { api } from "./api";

const KEY = "statusbar_hidden";

export type StatusBarItem =
  | "workspaces" | "sessions" | "forwards" | "broadcast" | "focus"
  | "shell" | "cpu" | "mem" | "disk" | "users";

class StatusBarPrefs {
  hidden = $state<Set<StatusBarItem>>(new Set());
  private loaded = false;

  async load() {
    if (this.loaded) return;
    this.loaded = true;
    try {
      const raw = await api.settingsGet(KEY);
      const v = raw ? JSON.parse(raw) : [];
      if (Array.isArray(v)) this.hidden = new Set(v);
    } catch {
      // missing key is fine
    }
  }

  shows(item: StatusBarItem): boolean {
    return !this.hidden.has(item);
  }

  toggle(item: StatusBarItem) {
    const next = new Set(this.hidden);
    if (next.has(item)) next.delete(item);
    else next.add(item);
    this.hidden = next;
    api.settingsSet(KEY, JSON.stringify([...next])).catch(console.warn);
  }
}

export const statusBarPrefs = new StatusBarPrefs();
