//go:build !android && !ios

package main

import "ssh-tool/internal/creds"

// localAutoUnlockPass returns the locally stored vault passphrase used for a
// silent same-passphrase sync pull / restore. On desktop this is the
// machine-bound auto-unlock sidecar (empty when the user never opted in).
// The android build reads the Keystore-backed secure store instead.
func (a *App) localAutoUnlockPass() string {
	pass, _ := creds.ReadSidecar(creds.DefaultPath())
	return pass
}
