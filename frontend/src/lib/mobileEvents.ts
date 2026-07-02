// Mobile event delivery (Go -> JS) via long-poll.
//
// On desktop Wails pushes events into the WebView directly. On Android that
// push path is unavailable to app code (see mobile_events_android.go), so
// the Go side queues events and we drain them here with a long-poll IPC
// (App.MobilePollEvents), re-dispatching each into the normal Wails event
// system so existing EventsOn subscribers fire unchanged.
//
// Called by name (Call.ByName) rather than through the generated bindings
// facade: MobilePollEvents is an android-only IPC method, so it is absent
// from the committed desktop bindings. ByName resolves server-side by FQN
// and keeps this off the desktop type-check surface.

import { Call } from "@wailsio/runtime";
import { isMobile } from "./platform";

// FQN of the bound method: <package>.<type>.<method>. App lives in
// `package main`, so the reflected package path is "main".
const POLL_FQN = "main.App.MobilePollEvents";

interface MobileEvent {
  name: string;
  data: unknown;
}

let running = false;

// startMobileEventPump begins the long-poll loop. Idempotent and a no-op on
// desktop. Each polled event is handed to window._wails.dispatchWailsEvent -
// the same entry point the desktop runtime uses for an incoming Go event -
// so subscription via Events.On / our EventsOn shim works untouched.
export function startMobileEventPump(): void {
  if (!isMobile || running) return;
  running = true;
  void pump();
}

async function pump(): Promise<void> {
  // Small backoff so a transient IPC failure can't spin the loop hot.
  let backoffMs = 0;
  for (;;) {
    try {
      const events = (await Call.ByName(POLL_FQN)) as MobileEvent[] | null;
      backoffMs = 0;
      if (events && events.length) {
        const dispatch = (window as any)._wails?.dispatchWailsEvent;
        if (typeof dispatch === "function") {
          for (const ev of events) {
            dispatch({ name: ev.name, data: ev.data });
          }
        }
      }
    } catch (e) {
      console.warn("mobile event poll failed", e);
      backoffMs = Math.min(backoffMs ? backoffMs * 2 : 500, 5000);
      await new Promise((r) => setTimeout(r, backoffMs));
    }
  }
}
