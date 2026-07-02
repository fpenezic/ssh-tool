// Preferred in-app local-shell kind. Drives:
//   - which shell the bare "Local shell" button in the top bar
//     opens on plain click
//   - the radio in Settings -> Connection
//   - the visible label on the Local shell button
//
// Stored under the setting key `local_shell_kind`. Empty string ("")
// or any unknown value means "let the backend pick auto" - backend
// then tries WSL, PowerShell, cmd on Windows; $SHELL on Unix.
//
// Separate from `external_terminal_kind` because the in-app PTY pool
// and the external OS terminal are independent surfaces; the user may
// want WSL inside the app but Windows Terminal as the external one.

import { api } from "./api";

const KEY = "local_shell_kind";

export type LocalShellKind = "" | "wsl" | "powershell" | "cmd" | "bash" | "zsh" | "sh" | "fish";

class LocalShellPrefs {
  kind = $state<LocalShellKind>("");
  private loaded = false;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(KEY);
      if (raw) this.kind = raw as LocalShellKind;
    } catch {
      // missing key - keep auto default
    }
    this.loaded = true;
  }

  set(kind: LocalShellKind) {
    this.kind = kind;
    this.scheduleSave();
  }

  private scheduleSave() {
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      api.settingsSet(KEY, this.kind).catch(console.warn);
      this.saveTimer = null;
    }, 200);
  }
}

export const localShellPrefs = new LocalShellPrefs();
