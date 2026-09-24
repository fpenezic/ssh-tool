// SFTP pane view toggles (row colouring). Persisted in the settings DB,
// which stays on this machine - profile sync does not carry it.

import { api } from "./api";

const KEY = "ui_sftp_view";

export interface SftpViewPrefs {
  dirs: boolean;
  hidden: boolean;
  temp: boolean;
  types: boolean;
  owner: boolean;
}

const DEFAULTS: SftpViewPrefs = { dirs: true, hidden: true, temp: true, types: true, owner: true };

class SftpView {
  prefs = $state<SftpViewPrefs>({ ...DEFAULTS });
  private loaded = false;

  async load() {
    if (this.loaded) return;
    this.loaded = true;
    try {
      const raw = await api.settingsGet(KEY);
      if (raw) this.prefs = { ...DEFAULTS, ...JSON.parse(raw) };
    } catch { /* missing or unreadable key: defaults */ }
  }

  set(k: keyof SftpViewPrefs, v: boolean) {
    this.prefs = { ...this.prefs, [k]: v };
    api.settingsSet(KEY, JSON.stringify(this.prefs)).catch(console.warn);
  }
}

export const sftpView = new SftpView();
