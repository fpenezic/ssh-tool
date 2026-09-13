//go:build !android && !ios

package main

import "log"

// reRegisterMovedIntegrations points existing desktop registrations at
// the newly installed binary.
//
// Every registration stores an absolute path. Installing moves the
// executable, so a handler registered from ~/Downloads keeps launching
// that copy - which still works right up until the user deletes the
// download, and then fails with nothing to explain why. Someone who
// registered these before installing expects them to follow.
//
// Only registrations that ALREADY EXIST are rewritten: this is a
// migration, not an opportunity to enable things the user did not ask
// for. Failures are logged and otherwise ignored, because the install
// itself succeeded and re-registering by hand is one button away.
//
// The MCP bridge is deliberately not touched - it writes into another
// application's config file, and rewriting that without being asked is
// a bigger liberty than adjusting our own registrations. The UI warns
// about it instead.
func reRegisterMovedIntegrations(target string) {
	if urlSchemeStatus() != "" {
		if err := registerURLSchemeAt(target); err != nil {
			log.Printf("install: could not re-point the ssh-tool:// handler: %v", err)
		}
	}
	if explorerMenuStatus() != "" {
		if err := registerExplorerMenuAt(target); err != nil {
			log.Printf("install: could not re-point the file-manager menu: %v", err)
		}
	}
}
