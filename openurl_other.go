//go:build !windows

// Non-Windows URL opening stays on the Wails runtime, which uses the
// platform's own opener (`open` on macOS, xdg-open and friends on
// Linux). Only Windows needed replacing - see openurl_windows.go.

package main

import "errors"

// openURLPlatform reports that there is no platform-specific override, so
// BrowserOpenURL falls through to the Wails runtime.
func openURLPlatform(string) error {
	return errNoPlatformOpener
}

var errNoPlatformOpener = errors.New("no platform-specific URL opener")
