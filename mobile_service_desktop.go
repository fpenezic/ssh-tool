//go:build !android && !ios

package main

// syncForegroundService is a no-op on desktop: there is no OS process
// suspension to guard against, so SSH sessions survive backgrounding
// without a keep-alive service. The android/ios build provides the real
// implementation. Kept as a method stub so the connect/disconnect paths can
// call it unconditionally.
func (a *App) syncForegroundService() {}
