/**
 * Pure helpers for interpreting the backend's vault status.
 *
 * Split out of vaultState.svelte.ts so the decisions can be tested: that
 * module imports api.ts, which pulls in the Wails runtime and needs a real
 * `window`, so it cannot be loaded under vitest.
 */

export type VaultStatusState = "not_initialized" | "locked" | "unlocked";

/**
 * Whether the UI should warn that secrets are unreadable.
 *
 * Only "locked" counts. "not_initialized" means there is no vault yet - the
 * user has not created one, so there is nothing being withheld and a "vault
 * locked" warning would be nonsense on a fresh install.
 */
export function isLockedState(state: VaultStatusState): boolean {
  return state === "locked";
}

/**
 * Normalise whatever VaultStatus returned into a known state.
 *
 * Anything unrecognised - including a missing field - resolves to "locked".
 * That direction is deliberate: claiming "unlocked" when we do not actually
 * know hides the problem from the user, which is exactly the failure this
 * indicator exists to prevent.
 */
export function normalizeVaultState(raw: unknown): VaultStatusState {
  if (raw === "unlocked" || raw === "not_initialized" || raw === "locked") {
    return raw;
  }
  return "locked";
}
