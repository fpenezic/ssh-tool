// Clipboard helpers. The password-copy path auto-clears the clipboard
// after 30 seconds (matches 1Password / Bitwarden defaults). We don't
// try to detect whether the user has copied something else in between
// - if they did, our clear is a no-op (we read the clipboard first and
// only clear if our exact value is still there).
//
// Both helpers fire a confirmation toast so the user gets feedback even
// when the copy trigger (a button, a right-click action) sits far from
// where they're looking. Pass `label` to name what was copied
// ("Hostname copied"); omit it for a generic "Copied". The sensitive
// path never echoes the value into the toast.

import { toast } from "./toast.svelte.ts";

const PASSWORD_CLEAR_MS = 30_000;

// Most call sites want a confirmation toast (a button or menu action far
// from where the user is looking). A few render their own inline hint
// (PaneNode / DetailPane copy buttons) - those pass {toast:false} so the
// feedback isn't doubled. `label` names what was copied ("Hostname
// copied"); omit for a generic "Copied".
export interface CopyOpts {
  label?: string;
  toast?: boolean;
}

export async function copyText(text: string, opts: CopyOpts = {}): Promise<void> {
  await navigator.clipboard.writeText(text);
  if (opts.toast !== false) toast.ok(opts.label ? `${opts.label} copied` : "Copied");
}

// Copy a sensitive value (typically a password) and schedule a clear
// 30s later. If the user copies something else in between, the value
// in the clipboard will no longer match and we leave it alone.
export async function copySensitive(text: string, opts: CopyOpts = {}): Promise<void> {
  await navigator.clipboard.writeText(text);
  if (opts.toast !== false) toast.ok(`${opts.label ?? "Password"} copied - clears in 30s`);
  setTimeout(async () => {
    try {
      const cur = await navigator.clipboard.readText();
      if (cur === text) {
        await navigator.clipboard.writeText("");
      }
    } catch {
      // Clipboard read can fail without user-gesture context in some
      // browsers; the best-effort clear is a nice-to-have, not a
      // correctness guarantee.
    }
  }, PASSWORD_CLEAR_MS);
}
