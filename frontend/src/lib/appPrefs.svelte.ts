// App-wide UI preferences distinct from terminal prefs: density,
// base font size, color tag rendering, active-row affordance, live
// timers. Persisted via the settings DB; applied by toggling CSS
// custom properties on the <html> root so every consumer can opt
// in via `var(--…)` without prop-drilling.

import { api } from "./api";

const DENSITY_KEY = "ui_density";
const FONT_SIZE_KEY = "ui_font_size";
const TAG_BG_KEY = "ui_tag_bg";
const ACTIVE_ROW_KEY = "ui_active_row_emphasis";
const TAB_TIMER_KEY = "ui_tab_timer";
const THEME_KEY = "ui_theme";

export type Density = "compact" | "comfortable" | "cozy";
export type UITheme = "mocha" | "latte" | "hc";

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
  // applied via the `theme-hc` class on <html>.
  uiTheme = $state<UITheme>("mocha");

  private loaded = false;

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
      if (v === "mocha" || v === "latte" || v === "hc") this.uiTheme = v;
    } catch { /* missing key fine */ }
    this.loaded = true;
    this.apply();
  }

  setUITheme(t: UITheme) {
    if (this.uiTheme === t) return;
    this.uiTheme = t;
    api.settingsSet(THEME_KEY, t).catch(console.warn);
    this.apply();
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
    // Theme is selected by class on <html>; CSS in style.css
    // reads it. Only one of theme-light / theme-hc is set at a
    // time; "mocha" = no class (the default :root values apply).
    root.classList.toggle("theme-light", this.uiTheme === "latte");
    root.classList.toggle("theme-hc", this.uiTheme === "hc");
  }
}

export const appPrefs = new AppPrefs();
