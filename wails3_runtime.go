// Thin shim that wraps the Wails v3 runtime calls we used to make via the
// v2 wruntime package. Keeps app.go readable during the port: instead of
// teaching every callsite the new API, we map (oldName) -> (v3 equivalent)
// here once. Long-term these wrappers can be inlined.
package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// rt holds the global *application.App reference. v2 used a per-call
// `ctx` parameter; v3 prefers the singleton via application.Get(). We
// stash the constructed App at startup so we don't repeatedly call Get
// (which would also work but reads less obviously).
var rt *application.App

// initRuntime is called from main() right after the App is constructed.
// We pass the app reference so all subsequent EventsEmit calls route
// through the same instance - needed once we open extra windows because
// every window observes the same event bus.
func initRuntime(app *application.App) {
	rt = app
}

// EventsEmit fires an event on the global event bus. Mirrors the v2
// signature so app.go can be ported with a search/replace.
func EventsEmit(name string, data any) {
	if rt == nil {
		return
	}
	// Desktop: Wails pushes the event into the WebView. Mobile: that push
	// path is unavailable, so also enqueue for the frontend long-poll
	// (MobilePollEvents). mobileEnqueueEvent is a no-op on desktop.
	mobileEnqueueEvent(name, data)
	rt.Event.Emit(name, data)
}

// BrowserOpenURL routes a URL to the OS default browser.
func BrowserOpenURL(url string) {
	if rt == nil {
		return
	}
	_ = rt.Browser.OpenURL(url)
}

// OpenFileDialogOptions / SaveFileDialogOptions mirror the v2 shape but
// only carry the fields we actually used. v3 uses a builder pattern; we
// wrap that into a function that returns the chosen path (empty on
// cancel) to match the v2 surface.
type OpenFileDialogOptions struct {
	Title string
}

type SaveFileDialogOptions struct {
	Title           string
	DefaultFilename string
}
