import { mount } from "svelte";
import "./style.css";
import App from "./App.svelte";
import { applyCachedThemeEarly } from "./lib/appPrefs.svelte";
import { installAndroidTransport } from "./lib/androidTransport";
import { startMobileEventPump } from "./lib/mobileEvents";
import { startMobileDeepLink } from "./lib/mobileDeepLink";

// Apply the last-known UI theme synchronously, before Svelte mounts, so the
// first paint already matches the user's choice. Without this the default
// (dark) :root paints for a frame before appPrefs.load() reconciles the
// saved theme from the settings DB - a visible dark->latte flash on launch.
applyCachedThemeEarly();

// On Android, route @wailsio/runtime IPC through the native invokeAsync
// bridge before anything calls a bound method (otherwise the default fetch
// transport hangs - the WebView can't deliver POST bodies to Go). No-op on
// desktop.
installAndroidTransport();

// On Android, Go->JS events can't be pushed into the WebView, so drain the
// Go-side event queue with a long-poll and re-dispatch locally. No-op on
// desktop. Started after the transport so its own IPC calls have a route.
startMobileEventPump();

// On Android, drain any ssh-tool:// deep link the Activity captured and
// re-dispatch it as the `deep_link_import` event App.svelte already handles.
// No-op on desktop.
startMobileDeepLink();

const app = mount(App, {
  target: document.getElementById("app")!,
});

export default app;
