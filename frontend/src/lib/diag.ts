// Diagnostic logging that reaches the in-app Log viewer.
//
// console.log only lands in the browser devtools, which a user running the
// desktop app (especially on a remote machine) can't easily open. diag()
// mirrors the line into the Go log via the FrontendLog IPC so it shows up in
// Settings -> Logs alongside backend output. Fire-and-forget; never throws.
//
// This is temporary instrumentation for the VNC "stuck connecting" and
// terminal "garbled ll" investigation - remove once those are pinned down.

import { api } from "./api";

export function diag(tag: string, msg: string) {
  const line = `${tag}: ${msg}`;
  try {
    console.log("[diag]", line);
  } catch { /* ignore */ }
  try {
    api.frontendLog(line);
  } catch { /* ignore */ }
}
