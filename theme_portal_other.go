//go:build !linux || android

package main

// watchDesktopTheme is Linux-only: macOS and Windows feed their webviews
// the system preference directly, so prefers-color-scheme is correct
// there and there is nothing to supplement.
func watchDesktopTheme(onChange func(dark bool)) func() { return func() {} }

// currentDesktopDarkMode reports not-known off Linux, which leaves
// prefers-color-scheme authoritative.
func currentDesktopDarkMode() (bool, bool) { return false, false }
