// Vault preferences distinct from app-wide UI prefs. Today this is
// just the auto-lock-after-N-minutes-idle setting; will grow when
// we add idle-warning toasts, change-passphrase wizard, etc.
//
// Persisted via the settings DB. 0 = disabled.

import { api } from "./api";

const AUTOLOCK_KEY = "vault_autolock_minutes";

const MIN_LOCK = 0;
const MAX_LOCK = 240;
export const DEFAULT_AUTOLOCK = 0;

class VaultPrefs {
  autoLockMinutes = $state<number>(DEFAULT_AUTOLOCK);

  private loaded = false;

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(AUTOLOCK_KEY);
      const n = parseInt(raw, 10);
      if (!isNaN(n) && n >= MIN_LOCK && n <= MAX_LOCK) {
        this.autoLockMinutes = n;
      }
    } catch { /* missing fine */ }
    this.loaded = true;
  }

  setAutoLockMinutes(n: number) {
    const clamped = Math.max(MIN_LOCK, Math.min(MAX_LOCK, Math.floor(n || 0)));
    if (clamped === this.autoLockMinutes) return;
    this.autoLockMinutes = clamped;
    api.settingsSet(AUTOLOCK_KEY, String(clamped)).catch(console.warn);
  }
}

export const vaultPrefs = new VaultPrefs();
