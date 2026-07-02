// Persisted layout dimensions. Currently just the sidebar width for
// the Connections + Credentials views. Stored in the settings table
// so dragging the divider once survives restarts.

import { api } from "./api";

const SIDEBAR_KEY = "layout_sidebar_width";
const MIN = 180;
const MAX = 640;
const DEFAULT = 320;

class LayoutPrefs {
  sidebarWidth = $state(DEFAULT);
  private loaded = false;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(SIDEBAR_KEY);
      const n = parseInt(raw, 10);
      if (!isNaN(n) && n >= MIN && n <= MAX) this.sidebarWidth = n;
    } catch { /* missing key - default */ }
    this.loaded = true;
  }

  setSidebarWidth(px: number) {
    const clamped = Math.max(MIN, Math.min(MAX, Math.round(px)));
    if (clamped === this.sidebarWidth) return;
    this.sidebarWidth = clamped;
    // Debounce the persistence - a single drag fires many events.
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      api.settingsSet(SIDEBAR_KEY, String(this.sidebarWidth)).catch(console.warn);
      this.saveTimer = null;
    }, 200);
  }
}

export const layoutPrefs = new LayoutPrefs();
