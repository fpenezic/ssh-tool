//go:build android

package creds

// Machine-bound auto-unlock is disabled on Android. There is no stable
// machine-id (and the Linux /etc/machine-id derivation in machine.go is
// absent on Android), so the sidecar would fall back to a weak
// hostname-derived key that defeats the lock. On Android the user types
// the passphrase each launch instead. A future build will back this with
// EncryptedSharedPreferences + BiometricPrompt (see plan P3).
//
// These stubs satisfy the same exported surface as machine.go (which is
// excluded on Android) so callers in vault.go / app.go / backup compile
// and become no-ops.

const sidecarSuffix = ".local.key"

// SidecarPath is kept for API parity; nothing is written here on Android.
func SidecarPath(vaultPath string) string { return vaultPath + sidecarSuffix }

// SidecarExists always reports false: auto-unlock is off on Android.
func SidecarExists(string) bool { return false }

// WriteSidecar is a no-op on Android.
func WriteSidecar(string, string) error { return nil }

// ReadSidecar reports "no sidecar" so AutoUnlock cleanly falls through to
// the passphrase prompt.
func ReadSidecar(string) (string, error) { return "", nil }

// DeleteSidecar is a no-op on Android.
func DeleteSidecar(string) error { return nil }

// SealForMachine / OpenForMachine back the desktop backup restore-pending
// flow, which is not used on Android. Provide identity-ish stubs so the
// backup package still compiles; the data never round-trips through a
// real Android backup path in this build.
func SealForMachine(plaintext []byte) ([]byte, error) { return plaintext, nil }

func OpenForMachine(blob []byte) ([]byte, error) { return blob, nil }
