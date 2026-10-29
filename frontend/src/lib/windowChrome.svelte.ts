// Main window chrome: the experimental compact title bar. The pref lives
// in window.json on the Go side (read before the window exists, see
// app_window_prefs.go); `active` is what this window started with, since
// the frame cannot change on a live window. While active, the tab bar is
// the title bar: a drag region, plus our own min / max / close buttons on
// Windows and Linux (macOS keeps its native traffic lights).

import { api } from "./api";
import { isMobile } from "./platform";

class WindowChrome {
  // The Settings checkbox (applies on restart).
  compact = $state(false);
  // This window started without the native title bar.
  active = $state(false);
  os = $state("");
  private loaded = false;

  get mac() { return this.active && this.os === "darwin"; }
  // Windows / Linux draw their own window buttons.
  get ownControls() { return this.active && this.os !== "darwin"; }

  async load() {
    if (this.loaded || isMobile) return;
    this.loaded = true;
    try {
      const c = await api.windowChromeGet();
      this.compact = c.compact_title_bar;
      this.active = c.active;
      this.os = c.os;
    } catch {
      // server mode / older backend: native frame, nothing to draw
    }
  }

  async setCompact(on: boolean) {
    await api.windowChromeSetCompact(on);
    this.compact = on;
  }
}

export const windowChrome = new WindowChrome();
