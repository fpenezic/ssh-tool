// Lightweight toast notification store. One-line confirmations for
// actions whose feedback would otherwise live next to the trigger
// (e.g. the "Saved" pill next to a Save button that may be scrolled
// off-screen). Rendered by App.svelte's <ToastHost> in the bottom-
// right corner.
//
// Not a full notification system - no rich content, no stacking
// history. A toast may carry ONE optional click action (navigate
// somewhere); anything richer, replace with a real library.

export type ToastKind = "ok" | "err" | "info";

export interface Toast {
  id: number;
  kind: ToastKind;
  msg: string;
  // Absolute ms after which the toast auto-dismisses. 0 = sticky.
  expiresAt: number;
  // Optional click action - runs before the toast dismisses. Click
  // without one just dismisses, as before.
  onClick?: () => void;
}

class ToastStore {
  toasts = $state<Toast[]>([]);
  private nextId = 1;

  push(kind: ToastKind, msg: string, ttlMs = 2500, onClick?: () => void): number {
    const id = this.nextId++;
    const expiresAt = ttlMs > 0 ? Date.now() + ttlMs : 0;
    this.toasts = [...this.toasts, { id, kind, msg, expiresAt, onClick }];
    if (ttlMs > 0) {
      setTimeout(() => this.dismiss(id), ttlMs);
    }
    return id;
  }

  ok(msg: string, ttlMs = 2500) { return this.push("ok", msg, ttlMs); }
  err(msg: string, ttlMs = 4000) { return this.push("err", msg, ttlMs); }
  info(msg: string, ttlMs = 2500, onClick?: () => void) { return this.push("info", msg, ttlMs, onClick); }

  dismiss(id: number) {
    this.toasts = this.toasts.filter((t) => t.id !== id);
  }
}

export const toast = new ToastStore();
