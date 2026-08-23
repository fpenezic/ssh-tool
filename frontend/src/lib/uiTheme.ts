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
  try {
    return window.matchMedia(DARK_QUERY).matches;
  } catch {
    return true;
  }
}
