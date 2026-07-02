package creds

import "github.com/zalando/go-keyring"

// PurgeLegacyKeyringEntries removes per-secret entries that earlier
// versions of the app mirrored into the OS keychain (Windows Credential
// Manager, macOS Keychain, Linux secret service). Those entries silently
// bypassed Lock() because the OS keeps the keychain unlocked for the
// life of the user's login session, so a locked vault still leaked
// secrets to anything Get()-ing through the keyring fallback.
//
// Callers pass the list of credential IDs known to the database; the
// function deletes both the "cred:<id>" and "cred:<id>:passphrase"
// accounts under the legacy service name. Errors are swallowed: any
// account that was never written returns ErrNotFound, and the keyring
// may simply be unavailable on this platform - both are fine.
func PurgeLegacyKeyringEntries(credentialIDs []string) {
	const legacyService = "dev.bipe.ssh-tool"
	for _, id := range credentialIDs {
		_ = keyring.Delete(legacyService, "cred:"+id)
		_ = keyring.Delete(legacyService, "cred:"+id+":passphrase")
	}
}
