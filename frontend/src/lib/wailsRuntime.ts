// Frontend compat shim for the Wails v2 -> v3 runtime move.
//
// v2 exposed `EventsOn(name, (payload) => ...)` where the callback got the
// raw data. v3 (`@wailsio/runtime`) gives a wrapped `WailsEvent` object
// instead, with `.data` carrying the payload. To avoid rewriting every
// subscription site, we expose an EventsOn that unwraps for us.
//
// Kept tiny on purpose - when the rest of the codebase has been audited
// and we're sure no callsite needs the WailsEvent envelope (sender id,
// for multi-window routing), we can inline this.

import { Events } from "@wailsio/runtime";

export function EventsOn<T = unknown>(
  name: string,
  cb: (payload: T) => void,
): () => void {
  return Events.On(name, (ev) => {
    cb(ev.data as T);
  });
}
