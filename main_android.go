//go:build android || ios

// Mobile platform wiring. The native host (Android MainActivity / iOS view
// controller) owns the WebView, so there is no Go-side window or tray to
// create. buildApp returns a minimal application; configurePlatform is a
// no-op. Secure storage / biometric unlock and a foreground service are
// deferred to later phases (see the android spike plan).

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	sshlayer "ssh-tool/internal/ssh"
)

// In c-shared build mode main() is not called automatically by the Go
// runtime - the native host loads libwails.so and there is no exe
// entrypoint. RegisterAndroidMain hands our main() to the Wails runtime so
// nativeInit (called from the Activity) runs it. Must live in the root
// package that gets compiled into libwails.so.
func init() {
	application.RegisterAndroidMain(main)
}

// platformPreflight has nothing to do on mobile: no second process, no
// deep-link argv, no GTK. Never asks the process to exit early.
func platformPreflight() bool { return false }

// buildApp constructs a minimal application for mobile. No quit gate, no
// key bindings, no tray - those are desktop concepts. The Wails mobile
// runtime drives the lifecycle from the native side.
func buildApp(appInst *App) *application.App {
	return application.New(application.Options{
		Name:        appName,
		Description: "SSH connection manager",
		Services: []application.Service{
			application.NewService(appInst),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})
}

// configurePlatform on mobile creates no window (the native Activity owns
// the WebView and the asset server; a Go-side WebviewWindow hijacks asset
// serving). It only wires the native biometric result event into the
// frontend poll queue. Returns nil (nothing to defer).
func configurePlatform(_ *application.App, appInst *App) func() {
	// HOME is set by the Activity before this point, so a writable TMPDIR
	// can now be derived (sync pull / backup staging need os.CreateTemp).
	ensureMobileTempDir()
	registerMobileBiometricBridge()

	// opkssh OIDC login can't shell out to xdg-open/open on android. Route
	// the login URL to the system browser via an Intent.ACTION_VIEW through
	// the JNI bridge (WailsBridge.openURL). The Auth callback server still
	// runs in-process on loopback, so the redirect lands back in Go.
	sshlayer.BrowserOpenHook = func(url string) error {
		application.Android.OpenURL(url)
		return nil
	}

	// Hold the foreground service up while an opkssh OIDC login is in
	// flight so the backgrounded process keeps network for the token
	// exchange (see setLoginKeepAlive).
	sshlayer.LoginKeepAliveHook = appInst.setLoginKeepAlive
	return nil
}
