//go:build android || ios

// Mobile stubs for desktop-only package-level helpers that App methods
// still reference. The methods themselves stay in app.go (one file, shared
// across platforms); on mobile they compile against these stubs and the
// frontend hides the buttons that would invoke them (see the isMobile
// guards). Splitting the methods out would mean threading the desktop tag
// through a 5000-line file - these five stubs are the smaller, lower-risk
// cut for the spike.

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// launchInSystemTerminal / registerFileDropForwarding back desktop methods
// (SshLaunchInSystemTerminal, WindowDetachTab) that stay in app.go but are
// never invoked on mobile (the frontend hides their entry points). Stubs so
// the methods compile.
func launchInSystemTerminal([]string) error                   { return nil }
func registerFileDropForwarding(_ *application.WebviewWindow) {}

// File/directory dialogs: Wails' Dialog API is desktop-only. On mobile the
// flows that use these (SFTP folder transfer, session-recording dir picker,
// backup save/open) are themselves out of scope, so the stubs return empty
// (treated as "cancelled" by callers).
func OpenFileDialog(OpenFileDialogOptions) (string, error)      { return "", nil }
func OpenDirectoryDialog(OpenFileDialogOptions) (string, error) { return "", nil }
func SaveFileDialog(SaveFileDialogOptions) (string, error)      { return "", nil }

// URL-scheme registration is an OS desktop-shell concept (registry / XDG /
// LaunchServices). Not applicable on mobile.
func registerURLScheme() error { return ErrURLSchemeNotSupported }
func urlSchemeStatus() string  { return "" }
