//go:build android || ios

// Mobile secure storage + biometric IPC, backing vault auto-unlock.
//
// The vault passphrase is stored in Keystore-backed EncryptedSharedPreferences
// (Android) via the native bridge, and retrieval is gated behind a system
// biometric prompt. This mirrors the desktop machine-bound sidecar but with
// the platform's hardware-backed keystore + biometrics instead of a file.
//
// The biometric prompt is asynchronous: AndroidBiometricAuthenticate fires it
// and the result returns as the "common:biometric" event. That event is
// emitted via app.Event.Emit directly by the Wails native layer, bypassing
// our EventsEmit shim, so it would not reach the frontend poll queue on its
// own. registerMobileBiometricBridge wires a Go listener that forwards it
// into the queue (see mobile_events_android.go), so the frontend receives it
// through the same MobilePollEvents long-poll as every other event.

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// vaultSecureKey is the EncryptedSharedPreferences key under which the vault
// passphrase is stored.
const vaultSecureKey = "vault_passphrase"

// MobileSecureSetVaultPass stores the vault passphrase in the platform secure
// store (Keystore-backed). Called by the frontend after a successful unlock
// when the user opts into auto-unlock.
//
// The store is fail-closed: a write that did not land has to surface, or the
// user is told auto-unlock is on and then finds it silently absent at the
// next launch.
func (a *App) MobileSecureSetVaultPass(passphrase string) error {
	return application.Android.SecureSet(vaultSecureKey, passphrase)
}

// MobileSecureHasVaultPass reports whether a passphrase is stored (so the
// frontend can offer biometric auto-unlock on launch).
//
// The error is deliberately not folded into the bool. "Nothing stored" and
// "the Keystore threw" both mean no biometric unlock right now, but only the
// second is worth telling the user about - otherwise a broken keystore looks
// exactly like never having enabled auto-unlock.
func (a *App) MobileSecureHasVaultPass() (bool, error) {
	_, found, err := application.Android.SecureGet(vaultSecureKey)
	if err != nil {
		return false, err
	}
	return found, nil
}

// MobileSecureClearVaultPass removes the stored passphrase (used when the
// user turns auto-unlock off or locks-and-forgets).
func (a *App) MobileSecureClearVaultPass() error {
	return application.Android.SecureDelete(vaultSecureKey)
}

// MobileBiometricUnlock fires the system biometric prompt. On the
// "common:biometric" {ok} result (delivered to the frontend via the poll
// queue), the frontend calls MobileUnlockWithStoredPass to finish.
func (a *App) MobileBiometricUnlock() error {
	application.Android.BiometricAuthenticate("Unlock your credential vault")
	return nil
}

// MobileUnlockWithStoredPass reads the stored passphrase and unlocks the
// vault. Called by the frontend only after a successful biometric result, so
// the secret never crosses into JS. Returns true on success.
func (a *App) MobileUnlockWithStoredPass() (bool, error) {
	pass, found, err := application.Android.SecureGet(vaultSecureKey)
	if err != nil {
		return false, err
	}
	if !found || pass == "" {
		return false, nil
	}
	if err := a.vault.Unlock(pass, false); err != nil {
		return false, err
	}
	a.recordAudit("vault.unlock", "", map[string]string{"biometric": "true"})
	return true, nil
}

// localAutoUnlockPass returns the vault passphrase from the Keystore-backed
// secure store, if the user opted into biometric auto-unlock. There is no
// machine-bound sidecar on android. Used by SyncPullLive to merge a pulled
// vault in place (same-passphrase case) so a pull applies without a restart -
// which matters doubly on android, where AppRelaunch (process re-exec) is a
// no-op. Empty when auto-unlock was never enabled.
func (a *App) localAutoUnlockPass() string {
	// Errors collapse to "" on purpose: the caller (SyncPullLive) treats an
	// empty passphrase as "cannot merge in place", which is the right
	// outcome whether the secret is absent or the Keystore is unreachable.
	pass, found, err := application.Android.SecureGet(vaultSecureKey)
	if err != nil || !found {
		return ""
	}
	return pass
}

// registerMobileBiometricBridge forwards the native biometric result event
// into the mobile poll queue so the frontend can react to it. Called once at
// startup (after rt is set).
func registerMobileBiometricBridge() {
	if rt == nil {
		return
	}
	rt.Event.On("common:biometric", func(e *application.CustomEvent) {
		mobileEnqueueEvent("common:biometric", e.Data)
	})
}
