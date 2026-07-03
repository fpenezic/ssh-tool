//go:build darwin

package main

import "errors"

// Finder has no user-level context-menu registry an app can write
// into directly - the supported path is a Quick Action (Automator
// workflow) or a Finder Sync extension, both of which need bundle
// packaging work. Tracked in TODO; the IPC surface stays uniform so
// the Settings UI can show "not supported on macOS yet".

var errExplorerMenuUnsupported = errors.New("file-manager integration is not supported on macOS yet")

func registerExplorerMenu() error   { return errExplorerMenuUnsupported }
func unregisterExplorerMenu() error { return errExplorerMenuUnsupported }
func explorerMenuStatus() string    { return "" }
