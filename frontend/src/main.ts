import { mount } from "svelte";
import "./style.css";
import App from "./App.svelte";
import { applyCachedThemeEarly } from "./lib/appPrefs.svelte";
import { startMobileEventPump } from "./lib/mobileEvents";
import { startMobileDeepLink } from "./lib/mobileDeepLink";

// Apply the last-known UI theme synchronously, before Svelte mounts, so the
// first paint already matches the user's choice. Without this the default
// (dark) :root paints for a frame before appPrefs.load() reconciles the
// saved theme from the settings DB - a visible dark->latte flash on launch.
applyCachedThemeEarly();

// On Android, @wailsio/runtime (3.0.0+) routes IPC through the native
// invokeAsync bridge by itself when window.wails is present (the default
// fetch transport hangs there - the WebView can't deliver POST bodies to Go).

// On Android, Go->JS events can't be pushed into the WebView, so drain the
// Go-side event queue with a long-poll and re-dispatch locally. No-op on
// desktop.
startMobileEventPump();

// On Android, drain any ssh-tool:// deep link the Activity captured and
// re-dispatch it as the `deep_link_import` event App.svelte already handles.
// No-op on desktop.
startMobileDeepLink();

const app = mount(App, {
  target: document.getElementById("app")!,
});

export default app;
