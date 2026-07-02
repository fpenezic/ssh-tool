//go:build !android && !ios

package main

// mobileEnqueueEvent is a no-op on desktop: events are pushed straight
// into the WebView by Wails (window._wails.dispatchWailsEvent), so there
// is no queue to feed. The android/ios build provides the real
// implementation plus the MobilePollEvents IPC method. Kept as a stub so
// EventsEmit can call it unconditionally without a runtime.GOOS branch.
func mobileEnqueueEvent(_ string, _ any) {}
