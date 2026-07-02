// App-wide terminal preferences. Persisted via the settings table so the
// zoom level survives restarts. Single source of truth so every open
// xterm reacts to the same $state - Terminal.svelte runs an $effect that
// reads fontSize and applies it to its xterm instance.

import { api } from "./api";
import { DEFAULT_THEME_ID, findTheme } from "./themes";

const FONT_SIZE_KEY = "terminal_font_size";
const THEME_KEY = "terminal_theme";
const AUTOCLOSE_KEY = "terminal_close_on_clean_exit";
const FONT_FAMILY_KEY = "terminal_font_family";
const SCROLLBACK_KEY = "terminal_scrollback";
const DISABLE_WEBGL_KEY = "terminal_disable_webgl";
const SERVER_STATS_KEY = "server_stats_enabled";
const MIN_FONT = 6;
const MAX_FONT = 40;
const DEFAULT_FONT = 13;
export const DEFAULT_FONT_FAMILY =
  "ui-monospace, 'JetBrains Mono', Menlo, monospace";
const MIN_SCROLLBACK = 500;
const MAX_SCROLLBACK = 100000;
export const DEFAULT_SCROLLBACK = 5000;

class TerminalPrefs {
  fontSize = $state(DEFAULT_FONT);
  themeId = $state(DEFAULT_THEME_ID);
  fontFamily = $state(DEFAULT_FONT_FAMILY);
  scrollback = $state(DEFAULT_SCROLLBACK);
  // When true, a tab whose session ends with a clean shell exit (Ctrl+D
  // / `exit 0`) is closed automatically. Non-zero exits and network
  // drops always leave the tab open so the user can see what happened.
  closeOnCleanExit = $state(false);
  // When true, Terminal.svelte skips loading the WebGL renderer addon
  // and falls back to xterm's DOM/canvas renderer. Workaround for
  // sluggish keystroke echo on Linux WebKit builds where the WebGL
  // pipeline runs on software GL (LIBGL_ALWAYS_SOFTWARE=1) - canvas is
  // often faster in that case. Default off; effect requires reload of
  // each affected tab.
  disableWebgl = $state(false);
  // When true, the status bar shows a load/memory/disk/users readout for
  // the focused SSH session, probed every 10s. Off by default: it runs a
  // command on the remote host, which not every box (network gear) should
  // get, and it's only worth the round-trip if the user wants it.
  serverStatsEnabled = $state(false);
  private loaded = false;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;
  private saveThemeTimer: ReturnType<typeof setTimeout> | null = null;
  private saveAutoCloseTimer: ReturnType<typeof setTimeout> | null = null;
  private saveFontFamilyTimer: ReturnType<typeof setTimeout> | null = null;
  private saveScrollbackTimer: ReturnType<typeof setTimeout> | null = null;
  private saveDisableWebglTimer: ReturnType<typeof setTimeout> | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(FONT_SIZE_KEY);
      const n = parseInt(raw, 10);
      if (!isNaN(n) && n >= MIN_FONT && n <= MAX_FONT) {
        this.fontSize = n;
      }
    } catch {
      // missing key is fine - keep default
    }
    try {
      const t = await api.settingsGet(THEME_KEY);
      if (t) this.themeId = t;
    } catch {
      // missing key is fine
    }
    try {
      const v = await api.settingsGet(AUTOCLOSE_KEY);
      if (v === "1") this.closeOnCleanExit = true;
    } catch {
      // missing key is fine
    }
    try {
      const ff = await api.settingsGet(FONT_FAMILY_KEY);
      if (ff && ff.trim()) this.fontFamily = ff;
    } catch {
      // missing key is fine
    }
    try {
      const raw = await api.settingsGet(SCROLLBACK_KEY);
      const n = parseInt(raw, 10);
      if (!isNaN(n) && n >= MIN_SCROLLBACK && n <= MAX_SCROLLBACK) {
        this.scrollback = n;
      }
    } catch {
      // missing key is fine
    }
    try {
      const v = await api.settingsGet(DISABLE_WEBGL_KEY);
      if (v === "1") this.disableWebgl = true;
    } catch {
      // missing key is fine
    }
    try {
      const v = await api.settingsGet(SERVER_STATS_KEY);
      if (v === "1") this.serverStatsEnabled = true;
    } catch {
      // missing key is fine
    }
    this.loaded = true;
  }

  setServerStatsEnabled(v: boolean) {
    if (this.serverStatsEnabled === v) return;
    this.serverStatsEnabled = v;
    api.settingsSet(SERVER_STATS_KEY, v ? "1" : "0").catch(console.warn);
  }

  setDisableWebgl(v: boolean) {
    if (this.disableWebgl === v) return;
    this.disableWebgl = v;
    if (this.saveDisableWebglTimer) clearTimeout(this.saveDisableWebglTimer);
    this.saveDisableWebglTimer = setTimeout(() => {
      api.settingsSet(DISABLE_WEBGL_KEY, v ? "1" : "0").catch(console.warn);
      this.saveDisableWebglTimer = null;
    }, 200);
  }

  setFontFamily(s: string) {
    const v = s.trim() || DEFAULT_FONT_FAMILY;
    if (v === this.fontFamily) return;
    this.fontFamily = v;
    if (this.saveFontFamilyTimer) clearTimeout(this.saveFontFamilyTimer);
    this.saveFontFamilyTimer = setTimeout(() => {
      api.settingsSet(FONT_FAMILY_KEY, v).catch(console.warn);
      this.saveFontFamilyTimer = null;
    }, 300);
  }

  setScrollback(n: number) {
    const clamped = Math.max(MIN_SCROLLBACK, Math.min(MAX_SCROLLBACK, n));
    if (clamped === this.scrollback) return;
    this.scrollback = clamped;
    if (this.saveScrollbackTimer) clearTimeout(this.saveScrollbackTimer);
    this.saveScrollbackTimer = setTimeout(() => {
      api.settingsSet(SCROLLBACK_KEY, String(clamped)).catch(console.warn);
      this.saveScrollbackTimer = null;
    }, 300);
  }

  setCloseOnCleanExit(v: boolean) {
    if (this.closeOnCleanExit === v) return;
    this.closeOnCleanExit = v;
    if (this.saveAutoCloseTimer) clearTimeout(this.saveAutoCloseTimer);
    this.saveAutoCloseTimer = setTimeout(() => {
      api.settingsSet(AUTOCLOSE_KEY, v ? "1" : "0").catch(console.warn);
      this.saveAutoCloseTimer = null;
    }, 200);
  }

  setTheme(id: string) {
    if (this.themeId === id) return;
    this.themeId = id;
    if (this.saveThemeTimer) clearTimeout(this.saveThemeTimer);
    this.saveThemeTimer = setTimeout(() => {
      api.settingsSet(THEME_KEY, this.themeId).catch(console.warn);
      this.saveThemeTimer = null;
    }, 200);
  }

  // Convenience for consumers that just want the actual theme object.
  get theme() {
    return findTheme(this.themeId);
  }

  setFontSize(n: number) {
    const clamped = Math.max(MIN_FONT, Math.min(MAX_FONT, n));
    if (clamped === this.fontSize) return;
    this.fontSize = clamped;
    this.scheduleSave();
  }

  bumpFontSize(delta: number) {
    this.setFontSize(this.fontSize + delta);
  }

  resetFontSize() {
    this.setFontSize(DEFAULT_FONT);
  }

  // Debounce writes so wheel storms don't hammer the DB.
  private scheduleSave() {
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      api.settingsSet(FONT_SIZE_KEY, String(this.fontSize)).catch(console.warn);
      this.saveTimer = null;
    }, 400);
  }
}

export const terminalPrefs = new TerminalPrefs();
