// Platform detection for the frontend. The same Svelte UI runs in the
// desktop WebView (Windows/macOS/Linux) and, on the android-spike branch,
// in the Android WebView. A handful of features are desktop-only - detached
// windows, the system tray prefs, local-shell tabs, the isolated-browser
// launcher, the OS-terminal launcher, the self-updater - because their
// backend IPC is excluded on mobile (see the //go:build !android && !ios
// split). Components gate those with `if (!isMobile)` / `{#if !isMobile}`.
//
// Detection is by WebView userAgent. Android's WebView always carries
// "Android"; iOS WKWebView carries "iPhone"/"iPad". This needs no backend
// round-trip, so it's available synchronously at module load.

const ua = typeof navigator !== "undefined" ? navigator.userAgent.toLowerCase() : "";

export const isAndroid = ua.includes("android");
export const isIOS = /iphone|ipad|ipod/.test(ua);

// isMobile is the single flag components check. True on Android/iOS where
// the desktop-only surfaces must be hidden.
export const isMobile = isAndroid || isIOS;
