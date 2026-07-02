// In-app confirm dialog. Replaces window.confirm() so the WebView
// modal doesn't block the event loop (which trips Wails IPC) and so
// the prompt picks up the app's theme.
interface ConfirmState {
  title: string;
  message: string;
  okLabel: string;
  cancelLabel: string;
  danger: boolean;
  resolve: (ok: boolean) => void;
}

class ConfirmModalStore {
  pending = $state<ConfirmState | null>(null);

  show(opts: {
    title: string;
    message: string;
    okLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
  }): Promise<boolean> {
    return new Promise((resolve) => {
      this.pending = {
        title: opts.title,
        message: opts.message,
        okLabel: opts.okLabel ?? "OK",
        cancelLabel: opts.cancelLabel ?? "Cancel",
        danger: !!opts.danger,
        resolve,
      };
    });
  }

  confirm() {
    const p = this.pending;
    this.pending = null;
    p?.resolve(true);
  }
  cancel() {
    const p = this.pending;
    this.pending = null;
    p?.resolve(false);
  }
}

export const confirmModal = new ConfirmModalStore();
export const showConfirm = (opts: {
  title: string;
  message: string;
  okLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
}) => confirmModal.show(opts);
