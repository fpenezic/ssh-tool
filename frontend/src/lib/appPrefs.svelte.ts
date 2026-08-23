// App-wide UI preferences distinct from terminal prefs: density,
// base font size, color tag rendering, active-row affordance, live
// timers. Persisted via the settings DB; applied by toggling CSS
// custom properties on the <html> root so every consumer can opt
// in via `var(--…)` without prop-drilling.

import { api } from "./api";
import { DARK_QUERY, isUITheme, osPrefersDark, resolveTheme } from "./uiTheme";
import type { ResolvedTheme, UITheme } from "./uiTheme";

export type { UITheme } from "./uiTheme";

const DENSITY_KEY = "ui_density";
const FONT_SIZE_KEY = "ui_font_size";
const TAG_BG_KEY = "ui_tag_bg";
const ACTIVE_ROW_KEY = "ui_active_row_emphasis";
const TAB_TIMER_KEY = "ui_tab_timer";
const THEME_KEY = "ui_theme";
// localStorage mirror of the theme, read synchronously at boot before the
// WebView paints. The authoritative value lives in the settings DB, but
// settingsGet is an async IPC round-trip - reconciling it only after the
// first paint flashes the default (dark) theme for a frame before flipping
// to latte. localStorage is synchronous and lets main.ts apply the class
// before mount. See applyCachedThemeEarly.
const THEME_CACHE_KEY = "ui_theme_cache";

export type Density = "compact" | "comfortable" | "cozy";

// Toggle the theme classes on <html>. Shared by the early boot path and
// the reactive apply() so both stay in sync. "mocha" = no class (the
// default :root values apply).
function applyThemeClasses(theme: ResolvedTheme) {
  const root = document.documentElement;
  root.classList.toggle("theme-light", theme === "latte");
  root.classList.toggle("theme-hc", theme === "hc");
}

// applyCachedThemeEarly runs from main.ts before Svelte mounts. It reads the
// last-known theme from localStorage and applies the class so the first
// paint already matches, killing the dark->latte flash. The async load()
// later reconciles against the settings DB (normally a no-op).
//
// "system" resolves here too: matchMedia is synchronous, so following the
// OS costs nothing at boot and a light-desktop user gets a light first
// frame instead of a dark flash.
export function applyCachedThemeEarly() {
  try {
    const v = localStorage.getItem(THEME_CACHE_KEY);
    if (isUITheme(v)) applyThemeClasses(resolveTheme(v, osPrefersDark()));
  } catch { /* localStorage unavailable (rare) - fall through to async load */ }
}

const MIN_FONT = 11;
const MAX_FONT = 18;
const DEFAULT_FONT = 13;

class AppPrefs {
  density = $state<Density>("compact");
  baseFontSize = $state<number>(DEFAULT_FONT);
  // When true, color-tagged rows get a soft background tint in
  // addition to the left strip. Power-user view.
  tagBackground = $state<boolean>(false);
  // When true, the tree row that owns the currently focused terminal
  // tab gets a brighter left-border highlight so it stands out from
  // mere "live but not focused" rows.
  activeRowEmphasis = $state<boolean>(false);
  // When true, the tab bar shows a small "Nm" / "Nh" timer next to
  // each connected session indicating uptime since connect.
  tabTimer = $state<boolean>(false);
  // UI theme variant. "mocha" = default Catppuccin Mocha with a
  // slightly lifted muted-text floor. "hc" = high contrast,
  // applied via the `theme-hc` class on <html>. "system" follows the
  // OS dark/light setting, resolving to mocha or latte.
  //
  // This is the app chrome only. Terminal colours are a separate
  // preference (terminalPrefs) and deliberately do not follow it - a
  // terminal scheme is picked for contrast against shell output, not to
  // match window decorations.
  uiTheme = $state<UITheme>("mocha");

  private loaded = false;
  // Live handle on the OS preference so a desktop that flips dark/light
  // while the app runs updates immediately, without a restart.
  private darkQuery: MediaQueryList | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const d = await api.settingsGet(DENSITY_KEY);
      if (d === "compact" || d === "comfortable" || d === "cozy") {
        this.density = d;
      }
    } catch { /* missing key fine */ }
    try {
      const raw = await api.settingsGet(FONT_SIZE_KEY);
      const n = parseInt(raw, 10);
      if (!isNaN(n) && n >= MIN_FONT && n <= MAX_FONT) {
        this.baseFontSize = n;
      }
    } catch { /* missing key fine */ }
    try {
      const v = await api.settingsGet(TAG_BG_KEY);
      this.tagBackground = v === "1";
    } catch { /* missing key fine */ }
    try {
      const v = await api.settingsGet(ACTIVE_ROW_KEY);
      this.activeRowEmphasis = v === "1";
    } catch { /* missing key fine */ }
    try {
      const v = await api.settingsGet(TAB_TIMER_KEY);
      this.tabTimer = v === "1";
    } catch { /* missing key fine */ }
    try {
      const v = await api.settingsGet(THEME_KEY);
      if (isUITheme(v)) this.uiTheme = v;
    } catch { /* missing key fine */ }
    this.watchOSTheme();
    // Refresh the synchronous boot cache so the next launch paints this
    // theme from the first frame (no dark->latte flash).
    try { localStorage.setItem(THEME_CACHE_KEY, this.uiTheme); } catch { /* ignore */ }
    this.loaded = true;
    this.apply();
  }

  setUITheme(t: UITheme) {
    if (this.uiTheme === t) return;
    this.uiTheme = t;
    api.settingsSet(THEME_KEY, t).catch(console.warn);
    try { localStorage.setItem(THEME_CACHE_KEY, t); } catch { /* ignore */ }
    this.apply();
  }

  // Subscribe once to the OS dark/light preference. The listener stays
  // attached for every theme choice and apply() decides whether it
  // matters - re-subscribing on each change would be more moving parts
  // for no gain, and apply() is idempotent.
  private watchOSTheme() {
    if (this.darkQuery) return;
    try {
      this.darkQuery = window.matchMedia(DARK_QUERY);
      this.darkQuery.addEventListener("change", () => {
        if (this.uiTheme === "system") this.apply();
      });
    } catch { /* matchMedia unsupported - "system" falls back to dark */ }
  }

  setDensity(d: Density) {
    if (this.density === d) return;
    this.density = d;
    api.settingsSet(DENSITY_KEY, d).catch(console.warn);
    this.apply();
  }

  setBaseFontSize(n: number) {
    if (!Number.isFinite(n)) return;
    const clamped = Math.max(MIN_FONT, Math.min(MAX_FONT, Math.round(n)));
    if (clamped === this.baseFontSize) {
      // Re-apply anyway so a no-op save still pushes the var
      // onto :root (covers reload edge cases).
      this.apply();
      return;
    }
    this.baseFontSize = clamped;
    api.settingsSet(FONT_SIZE_KEY, String(clamped)).catch(console.warn);
    this.apply();
  }

  setTagBackground(v: boolean) {
    if (this.tagBackground === v) return;
    this.tagBackground = v;
    api.settingsSet(TAG_BG_KEY, v ? "1" : "0").catch(console.warn);
    this.apply();
  }

  setActiveRowEmphasis(v: boolean) {
    if (this.activeRowEmphasis === v) return;
    this.activeRowEmphasis = v;
    api.settingsSet(ACTIVE_ROW_KEY, v ? "1" : "0").catch(console.warn);
  }

  setTabTimer(v: boolean) {
    if (this.tabTimer === v) return;
    this.tabTimer = v;
    api.settingsSet(TAB_TIMER_KEY, v ? "1" : "0").catch(console.warn);
  }

  // Push the current values onto <html> so CSS can pick them up via
  // var(--…) without component re-renders. Tag/active/timer flags
  // are reactive through Svelte; only density + font need root
  // variables.
  private apply() {
    const root = document.documentElement;
    // Row paddings scale with density. Keep the strip thickness and
    // border-radius constant so the visual rhythm stays consistent.
    const rowY = this.density === "cozy" ? "0.45rem"
      : this.density === "comfortable" ? "0.32rem"
      : "0.2rem";
    const subGap = this.density === "cozy" ? "0.2rem"
      : this.density === "comfortable" ? "0.12rem"
      : "0.05rem";
    root.style.setProperty("--row-pad-y", rowY);
    root.style.setProperty("--row-sub-gap", subGap);
    root.style.setProperty("--ui-font-size", `${this.baseFontSize}px`);
    // Theme is selected by class on <html>; CSS in style.css reads it.
    // Resolve first so "system" never reaches the class toggles.
    const resolved = resolveTheme(this.uiTheme, osPrefersDark());
    applyThemeClasses(resolved);
  }
}

export const appPrefs = new AppPrefs();
