// Terminal copy/paste behavior preference. Three modes mirroring the
// dominant convention on each major platform:
//
//   windows:
//     Ctrl+Shift+C copies the current selection.
//     Ctrl+Shift+V pastes.
//     Ctrl+C is always SIGINT - never copies, no exceptions.
//     Right-click is smart: if there's a selection, copy + clear; else
//     paste from clipboard.
//     No auto-copy when you finish a left-drag selection.
//
//   linux:
//     Same key bindings (Ctrl+Shift+C/V).
//     Selecting text auto-copies it into the clipboard (closest we get
//     to the X primary selection - we don't have a real primary buffer
//     in Wails).
//     Middle-click pastes.
//     Right-click pastes (xterm tradition) - no smart toggle.
//
//   mac:
//     Cmd+C copies the selection (Cmd here means metaKey).
//     Cmd+V pastes.
//     Ctrl+C is still SIGINT.
//     Right-click pastes. No auto-copy.
//
// All modes share: paste guard pops modal on multi-line clipboard
// (handled in Terminal.svelte's existing onHostPaste).

import { api } from "./api";

const KEY = "terminal_copy_paste_mode";

export type CopyPasteMode = "windows" | "linux" | "mac";

function detect(): CopyPasteMode {
  const plat = (typeof navigator !== "undefined" && navigator.platform) || "";
  if (/Mac|iPhone|iPad/i.test(plat)) return "mac";
  if (/Win/i.test(plat)) return "windows";
  return "linux";
}

class CopyPastePrefs {
  mode = $state<CopyPasteMode>(detect());
  private loaded = false;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(KEY);
      if (raw === "windows" || raw === "linux" || raw === "mac") {
        this.mode = raw;
      }
    } catch {
      // missing key - keep auto-detected default
    }
    this.loaded = true;
  }

  set(mode: CopyPasteMode) {
    this.mode = mode;
    this.scheduleSave();
  }

  private scheduleSave() {
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      api.settingsSet(KEY, this.mode).catch(console.warn);
      this.saveTimer = null;
    }, 200);
  }
}

export const copyPastePrefs = new CopyPastePrefs();
