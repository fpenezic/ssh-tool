interface PromptState {
  message: string;
  defaultValue: string;
  password: boolean;
  resolve: (value: string | null) => void;
}

class PromptModalStore {
  pending = $state<PromptState | null>(null);

  show(message: string, opts?: { defaultValue?: string; password?: boolean }): Promise<string | null> {
    return new Promise((resolve) => {
      this.pending = {
        message,
        defaultValue: opts?.defaultValue ?? "",
        password: opts?.password ?? false,
        resolve,
      };
    });
  }

  confirm(value: string) {
    const p = this.pending;
    this.pending = null;
    p?.resolve(value.trim() || null);
  }

  cancel() {
    const p = this.pending;
    this.pending = null;
    p?.resolve(null);
  }
}

export const promptModal = new PromptModalStore();
export const showPrompt = (
  message: string,
  defaultValueOrOpts?: string | { defaultValue?: string; password?: boolean },
): Promise<string | null> => {
  if (typeof defaultValueOrOpts === "string" || defaultValueOrOpts === undefined) {
    return promptModal.show(message, { defaultValue: defaultValueOrOpts ?? "" });
  }
  return promptModal.show(message, defaultValueOrOpts);
};
