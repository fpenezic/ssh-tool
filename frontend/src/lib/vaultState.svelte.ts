import { api } from "./api";
import { isLockedState, normalizeVaultState, type VaultStatusState } from "./vaultLock";

/**
 * Shared vault lock state.
 *
 * This exists because "the vault is locked" and "the app is showing the
 * unlock gate" are NOT the same thing. VaultGate offers "Skip (memory only)",
 * which lets the user into the app with the vault still locked on the
 * backend - and in that state nothing on screen said so. The first sign was a
 * credential reveal or an SSH connect failing.
 *
 * The backend emits no vault-lock event (the `vault_locked` subscription that
 * used to sit in StatusBar was listening for something nobody sends), so this
 * store polls VaultStatus on a slow timer and refreshes on demand at the
 * moments that matter - after a lock, an unlock, or opening the credentials
 * view. Polling is cheap: VaultStatus reads an in-process flag.
 */
class VaultState {
  /** True until the first status read completes, so the UI can stay quiet. */
  loading = $state(true);
  /** "not_initialized" | "locked" | "unlocked" */
  status = $state<VaultStatusState>("unlocked");
  autoUnlockAvailable = $state(false);

  #timer: ReturnType<typeof setInterval> | null = null;
  #refreshing = false;

  get locked(): boolean {
    return isLockedState(this.status);
  }

  /**
   * Read the backend's view. Safe to call concurrently - overlapping calls
   * collapse, so a burst of refresh triggers costs one round trip.
   */
  async refresh(): Promise<void> {
    if (this.#refreshing) return;
    this.#refreshing = true;
    try {
      const st = await api.vaultStatus();
      this.status = normalizeVaultState(st?.state);
      this.autoUnlockAvailable = !!st?.auto_unlock_available;
    } catch {
      // A failed status read must not silently claim "unlocked" - that is
      // the direction that hides the problem from the user.
      this.status = "locked";
    } finally {
      this.loading = false;
      this.#refreshing = false;
    }
  }

  /**
   * Start the background poll. Idempotent, so a second mount does not stack
   * timers. The interval is deliberately slow: this is a safety net for
   * state changes with no event, not the primary update path.
   */
  start(intervalMs = 15_000): void {
    void this.refresh();
    if (this.#timer) return;
    this.#timer = setInterval(() => void this.refresh(), intervalMs);
  }

  stop(): void {
    if (this.#timer) {
      clearInterval(this.#timer);
      this.#timer = null;
    }
  }

  /** Optimistic local update after an action whose result we already know. */
  markLocked(): void {
    this.status = "locked";
    this.loading = false;
  }

  markUnlocked(): void {
    this.status = "unlocked";
    this.loading = false;
  }
}

export const vaultState = new VaultState();
