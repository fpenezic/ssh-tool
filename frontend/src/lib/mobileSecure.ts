// Frontend wrapper for the mobile vault auto-unlock IPC.
//
// These App methods exist only on android/ios builds (absent from the
// committed desktop bindings), so - like the event poll - they are called by
// FQN via Call.ByName to stay off the desktop type-check surface. No-ops
// resolve to safe defaults on desktop where the methods don't exist.

import { Call } from "@wailsio/runtime";
import { isMobile } from "./platform";

const P = "main.App.";

async function call<T>(method: string, ...args: unknown[]): Promise<T | null> {
  if (!isMobile) return null;
  try {
    return (await Call.ByName(P + method, ...args)) as T;
  } catch (e) {
    console.warn("mobile secure IPC failed:", method, e);
    return null;
  }
}

// Whether a vault passphrase is stored for biometric auto-unlock.
export function mobileSecureHasVaultPass(): Promise<boolean | null> {
  return call<boolean>("MobileSecureHasVaultPass");
}

// Persist the passphrase to Keystore-backed storage (after a successful
// unlock when the user opted into auto-unlock).
export function mobileSecureSetVaultPass(passphrase: string): Promise<unknown> {
  return call("MobileSecureSetVaultPass", passphrase);
}

// Forget the stored passphrase (auto-unlock off / lock-and-forget).
export function mobileSecureClearVaultPass(): Promise<unknown> {
  return call("MobileSecureClearVaultPass");
}

// Fire the system biometric prompt. Resolves immediately; the outcome
// arrives as the "common:biometric" event {ok, error}.
export function mobileBiometricUnlock(): Promise<unknown> {
  return call("MobileBiometricUnlock");
}

// After a successful biometric result, read the stored passphrase and unlock
// the vault server-side (the secret never crosses into JS). Returns true on
// success.
export function mobileUnlockWithStoredPass(): Promise<boolean | null> {
  return call<boolean>("MobileUnlockWithStoredPass");
}
