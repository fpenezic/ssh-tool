//go:build android || ios

package main

import "fmt"

// relaunchApp on android cannot re-exec the process (there is no exe
// entrypoint; the native host loaded libwails.so). In practice a sync pull
// applies live - the store is mirrored into the running DB and, when the
// vault passphrase is available from the Keystore secure store, the vault is
// merged in place too (see SyncPullLive + localAutoUnlockPass), so no restart
// is needed and the "Restart now" button is never shown.
//
// The button only appears in the rare case where auto-unlock is off, so the
// pulled vault was staged to pending-restore for a restart-time swap. That
// swap runs in initialise() (backup.ApplyPending, before store.Open), which
// on android only happens on a cold start. We can't safely tear down and
// reopen the live store/vault in-process, so signal the frontend to refresh
// its now-live store and tell the user to fully close and reopen the app.
func (a *App) relaunchApp() error {
	// Connections/settings are already live; refresh the frontend stores.
	EventsEmit("profile_reloaded", int64(0))
	return fmt.Errorf("close and reopen the app to apply the new passwords and keys (swipe it away from Recents, then launch again)")
}
