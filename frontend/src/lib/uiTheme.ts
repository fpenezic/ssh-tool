// UI theme resolution, split out of appPrefs so the rules can be tested
// without a DOM or a settings round-trip.
//
// The distinction that matters here: UITheme is what the user picked,
// ResolvedTheme is what gets painted. "system" only exists in the first -
// it is a rule ("match the OS"), not a palette, and resolving it before it
// reaches any class toggle is what keeps the rest of the app from having
// to know it exists.

// What the user picked.
export type UITheme = "system" | "mocha" | "latte" | "hc";
// What actually gets painted.
export type ResolvedTheme = "mocha" | "latte" | "hc";

export const DARK_QUERY = "(prefers-color-scheme: dark)";

export function isUITheme(v: unknown): v is UITheme {
  return v === "system" || v === "mocha" || v === "latte" || v === "hc";
}

// Resolve a user choice to the palette to paint. Only "system" consults
// the OS; the explicit picks pass through untouched, so choosing Latte on
// a dark desktop stays Latte.
//
// prefersDark is injected rather than read here so the rule is testable
// and callers control when matchMedia is touched (applyCachedThemeEarly
// runs before Svelte mounts).
export function resolveTheme(theme: UITheme, prefersDark: boolean): ResolvedTheme {
  if (theme !== "system") return theme;
  return prefersDark ? "mocha" : "latte";
}

// Read the OS dark/light preference.
//
// All three desktop webviews forward the desktop setting to
// prefers-color-scheme, with caveats worth knowing: on Windows WebView2
// follows the "app mode" setting, which is separate from "Windows mode";
// on Linux WebKitGTK reads the GTK/GNOME color-scheme, which older desktops
// may leave unset.
//
// Unsupported or unset resolves to dark deliberately: the app's own default
// is dark, so a failed query keeps users where they were rather than
// flipping them to light.
export function osPrefersDark(): boolean {
  // The platform's own answer wins when we have one. WebKitGTK derives
  // prefers-color-scheme from the GTK theme, which on KDE does not track
  // the desktop's colour-scheme setting - so matchMedia there reports
  // the app's starting palette rather than what the user chose. The Go
  // side reads the xdg-desktop-portal, which every desktop implements.
  //
  // Windows and GNOME were never wrong, so this changes nothing there:
  // the reported value and matchMedia agree.
  if (platformPrefersDark !== null) return platformPrefersDark;
  try {
    return window.matchMedia(DARK_QUERY).matches;
  } catch {
    return true;
  }
}

// null means the platform has not told us anything, so matchMedia stays
// in charge. Set once at startup and updated by the os_theme_changed
// event; never written from the frontend's own guesses.
let platformPrefersDark: boolean | null = null;

export function setPlatformPrefersDark(dark: boolean | null) {
  platformPrefersDark = dark;
}
