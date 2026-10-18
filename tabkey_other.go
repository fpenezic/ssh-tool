//go:build !linux || !cgo || gtk3 || android || server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// installTabKeyGuard: only GTK4 claims Ctrl+Tab for focus navigation; the
// other webviews deliver it to the page's keydown handler.
func installTabKeyGuard(_ *application.WebviewWindow) {}
