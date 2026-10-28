// In-app confirm dialog. Replaces window.confirm() so the WebView
// modal doesn't block the event loop (which trips Wails IPC) and so
// the prompt picks up the app's theme.
interface ConfirmState {
  title: string;
  message: string;
  okLabel: string;
  cancelLabel: string;
  danger: boolean;
  // When set, the dialog shows a checkbox with this label and reports
  // its state back. For prompts the user may not want to see again,
  // where a separate settings toggle is the long way round.
  checkboxLabel?: string;
  checked: boolean;
  // A third button between Cancel and OK (Discard in the unsaved-changes
  // prompt). Its click resolves with alt: true and ok: false.
  altLabel?: string;
  // Only the buttons (or Esc) answer: a click outside is ignored. The
  // unsaved-changes prompt opens on a click, and the second click of a
  // double-click would otherwise land outside it and answer Stay.
  modal?: boolean;
  resolve: (r: ConfirmResult) => void;
}

// A plain boolean still reads as "did they accept", so `if (ok)` keeps
// working for callers that ignore the checkbox.
export interface ConfirmResult {
  ok: boolean;
  checked: boolean;
  alt?: boolean;
}

class ConfirmModalStore {
  pending = $state<ConfirmState | null>(null);

  show(opts: {
    title: string;
    message: string;
    okLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    checkboxLabel?: string;
    // Initial state of the checkbox (default unchecked).
    checked?: boolean;
    altLabel?: string;
    modal?: boolean;
  }): Promise<ConfirmResult> {
    // A dialog already open is answered "cancel" first: its caller (the
    // unsaved-changes prompt, say) would otherwise wait forever.
    const prev = this.pending;
    if (prev) {
      this.pending = null;
      prev.resolve({ ok: false, checked: !!prev.checked });
    }
    return new Promise((resolve) => {
      this.pending = {
        title: opts.title,
        message: opts.message,
        okLabel: opts.okLabel ?? "OK",
        cancelLabel: opts.cancelLabel ?? "Cancel",
        danger: !!opts.danger,
        checkboxLabel: opts.checkboxLabel,
        checked: !!opts.checked,
        altLabel: opts.altLabel,
        modal: opts.modal,
        resolve,
      };
    });
  }

  toggleChecked(v: boolean) {
    if (this.pending) this.pending.checked = v;
  }

  confirm() {
    const p = this.pending;
    this.pending = null;
    p?.resolve({ ok: true, checked: !!p?.checked });
  }
  alt() {
    const p = this.pending;
    this.pending = null;
    p?.resolve({ ok: false, checked: !!p?.checked, alt: true });
  }
  cancel() {
    const p = this.pending;
    this.pending = null;
    // The checkbox is reported on cancel too: "don't ask again" is a
    // thing people tick on their way to saying no.
    p?.resolve({ ok: false, checked: !!p?.checked });
  }
}

export const confirmModal = new ConfirmModalStore();

// showConfirm keeps the boolean contract its callers were written
// against: `if (await showConfirm(...))`. Returning the result object
// here would silently always be truthy.
export const showConfirm = async (opts: {
  title: string;
  message: string;
  okLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
}): Promise<boolean> => (await confirmModal.show(opts)).ok;

// showConfirmWithCheckbox is the same dialog plus an opt-out tickbox,
// for prompts the user may not want to see again. Returns both answers.
export const showConfirmWithCheckbox = (opts: {
  title: string;
  message: string;
  okLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  checkboxLabel: string;
}): Promise<ConfirmResult> => confirmModal.show(opts);
