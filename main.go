// ssh-tool entrypoint - ported from Wails v2 to v3 alpha on the
// wails3-experiment branch.
//
// Key shape changes from v2:
//   - wails.Run(opts) -> application.New(opts).Run()
//   - Bind: []any -> Services: []application.Service
//   - OnStartup hook -> service implements ServiceStartup
//   - wruntime.* -> app.Event / app.Browser / application.OpenFileDialog
//     (see wails3_runtime.go for our shims)
//
// Multi-window is the prize on this branch; opening a second window is
// app.Window.NewWithOptions(...) and every window observes the same
// event bus.
package main

import (
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"ssh-tool/internal/store"
)

//go:embed all:frontend/dist
var assets embed.FS

// Injected at build time via -ldflags="-X main.appName=ssh-tool-dev" in dev builds.
var appName = "ssh-tool"

// Injected at build time via -ldflags="-X main.appVersion=v0.1.0".
// Default "dev" makes go-run builds clearly distinguishable from
// tagged releases in the about panel + log file.
var appVersion = "dev"

// Injected at build time via -ldflags="-X main.appCommit=<sha>".
// Short commit hash; "unknown" when missing.
var appCommit = "unknown"

func main() {
	// Desktop pre-flight: relaunch handshake (wait for the old instance
	// to release store.db + its single-instance listener), the deep-link
	// single-instance hand-off, and the WSL GTK/WebKit env workarounds.
	// All no-ops on mobile, where there is no second process, no deep
	// link argv, and no GTK. Returns true if this process should exit
	// immediately (it handed a deep link to an already-running instance).
	if platformPreflight() {
		os.Exit(0)
	}

	// Tee log output through a ring buffer (for the in-app log viewer)
	// AND a rolling file in the user-data dir so we have a persistent
	// log trail across restarts. File path:
	//   %APPDATA%\ssh-tool\logs\app.log  (Windows)
	//   ~/.local/share/ssh-tool/logs/app.log  (Linux)
	//
	// One file per app launch, kept compact (rotated when it crosses
	// 5 MiB, last 3 retained). This is intentionally tiny - observability
	// for a desktop app, not a server.
	var fileWriter io.Writer = io.Discard
	if logDir := filepath.Join(store.DataDir(), "logs"); logDir != "" {
		if err := os.MkdirAll(logDir, 0o755); err == nil {
			fileWriter = openLogFile(filepath.Join(logDir, "app.log"))
		}
	}
	stamp := log.New(io.MultiWriter(os.Stderr, fileWriter), "", log.LstdFlags|log.Lmicroseconds)
	_ = stamp // currently we just route the standard `log` to the same sink
	logBuf := newLogBuffer(io.MultiWriter(os.Stderr, fileWriter))
	log.SetOutput(logBuf)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("=== %s %s (%s) start %s ===",
		appName, appVersion, appCommit,
		time.Now().Format(time.RFC3339))

	appInst := NewApp()
	appInst.logBuf = logBuf

	// Construct the Wails application with the platform-appropriate
	// options + window/tray/hook wiring. Desktop builds create the main
	// window, system tray, quit gate and deep-link plumbing here; the
	// android/ios build returns a minimal app (the native Activity hosts
	// the webview, so there is no Go-side window to create).
	app := buildApp(appInst)

	// Expose to the runtime shim so EventsEmit/dialogs find the same App.
	initRuntime(app)
	// And hand the app reference to App so it can open new windows.
	appInst.app = app

	// Platform-specific post-construction wiring (window geometry, tray,
	// single-instance listener, deep-link dispatch). No-op on mobile.
	stop := configurePlatform(app, appInst)
	if stop != nil {
		defer stop()
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
