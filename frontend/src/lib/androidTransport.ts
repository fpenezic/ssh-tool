// Android IPC transport for @wailsio/runtime.
//
// Why this exists: the @wailsio/runtime version we ship (alpha.79) predates
// Wails' Android support and only knows the default HTTP fetch() transport.
// Android's WebView cannot deliver fetch() POST bodies to
// shouldInterceptRequest, so every bound-method call (vault unlock, store
// reads, SSH connect) would hang - the request leaves the page but the
// args + call-id never reach Go. That is the "Checking vault…" freeze.
//
// The native bridge (WailsJSBridge) exposes window.wails.invokeAsync, and
// the Go side (alpha.101) has nativeHandleRuntimeCall returning an
// {"ok":...,"data"|"text"|"error":...} envelope. This module registers a
// custom transport (the same shape alpha.89's runtime ships natively) that
// routes runtime calls through invokeAsync and resolves them from
// window._wailsAndroidCallback. Desktop is untouched: installAndroidTransport
// no-ops unless window.wails.invokeAsync is present.

import { setTransport, clientId } from "@wailsio/runtime";

interface AndroidJSBridge {
  invokeAsync(callbackID: string, payload: string): void;
}

// Small unique-id generator (avoids pulling in nanoid, which alpha.79 keeps
// internal). Collision-free enough for in-flight call correlation.
function genID(): string {
  return (
    Date.now().toString(36) +
    "-" +
    Math.random().toString(36).slice(2, 10)
  );
}

export function installAndroidTransport(): boolean {
  const w = window as any;
  const bridge: AndroidJSBridge | null =
    typeof w.wails?.invokeAsync === "function" ? w.wails : null;
  if (!bridge) return false;

  const pending = new Map<
    string,
    { resolve: (v: any) => void; reject: (e: any) => void }
  >();

  // The Java side calls this with the JSON envelope STRING (or an error
  // string). Parse the envelope and settle the matching call.
  w._wailsAndroidCallback = (
    id: string,
    response: string | null,
    error: string | null,
  ) => {
    const p = pending.get(id);
    if (!p) return;
    pending.delete(id);
    if (error) {
      p.reject(new Error(error));
      return;
    }
    try {
      const envelope = JSON.parse(response ?? "{}");
      if (!envelope.ok) {
        p.reject(new Error(envelope.error ?? "unknown runtime call error"));
        return;
      }
      p.resolve("text" in envelope ? envelope.text : envelope.data);
    } catch (e) {
      p.reject(e);
    }
  };

  setTransport({
    call(objectID: number, method: number, windowName: string, args: any) {
      return new Promise((resolve, reject) => {
        const id = genID();
        pending.set(id, { resolve, reject });
        try {
          bridge.invokeAsync(
            id,
            JSON.stringify({
              object: objectID,
              method,
              windowName,
              args: args ?? null,
              clientId,
            }),
          );
        } catch (e) {
          pending.delete(id);
          reject(e);
        }
      });
    },
  });
  return true;
}
