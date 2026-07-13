//go:build !android && !ios

// Desktop platform wiring, split out of main.go so the shared entrypoint
// compiles for mobile. This holds everything that only makes sense on a
// desktop OS: the relaunch/single-instance handshake, WSL GTK workarounds,
// the main window + system tray, the warn-before-quit / close-to-tray
// hooks, and ssh-tool:// deep-link dispatch.

package main

import (
	"embed"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

var _ embed.FS // keep the embed import for the //go:embed directive below

// appIcon is the application icon (window, taskbar, about box). PNG so
// GTK/WebKitGTK on Linux renders it; Windows/macOS accept it too.
//
//go:embed build/appicon.png
var appIcon []byte

// trayIcon is the system-tray icon. Must be PNG on Linux - the GTK
// StatusNotifier renders a "..." placeholder for a Windows .ico, which
// is what the user saw. Reusing the app PNG keeps one source of truth.
//
//go:embed build/appicon.png
var trayIcon []byte

// parseDeepLinkArg looks through CLI args for either a
// `--import-url=…` flag or an `ssh-tool://import?source=…` URI and
// returns the inner source URL. Returns "" if nothing matches.
//
// Supports the URI form because OS protocol-handler registrations
// pass the full URI as a single argv slot. The CLI form is the
// fallback for shells / scripts that don't have a handler
// registered.
func parseDeepLinkArg(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "--import-url=") {
			return strings.TrimPrefix(a, "--import-url=")
		}
		if strings.HasPrefix(a, "ssh-tool://") {
			u, err := url.Parse(a)
			if err != nil {
				continue
			}
			if u.Host == "import" || u.Path == "/import" {
				if src := u.Query().Get("source"); src != "" {
					return src
				}
			}
		}
	}
	return ""
}

// parseOpenDirArg looks through CLI args for `--open-dir=<path>` or
// the two-slot `--open-dir <path>` form (the file-manager
// registrations use the latter: `"exe" --open-dir "%V"`). Returns ""
// if nothing matches.
func parseOpenDirArg(args []string) string {
	for i, a := range args {
		if strings.HasPrefix(a, "--open-dir=") {
			return strings.TrimPrefix(a, "--open-dir=")
		}
		if a == "--open-dir" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// platformPreflight runs the desktop pre-window setup. Returns true if the
// process should exit immediately (it handed a deep link to an
// already-running instance via the single-instance socket).
func platformPreflight() bool {
	// Relaunch handshake: AppRelaunch spawns us with SSH_TOOL_WAIT_PID
	// set to the old instance's pid. Wait for it to die before touching
	// anything - it still holds store.db open (Windows file lock) and
	// its single-instance listener is still up.
	waitForParentExit()

	// Single-instance guard. Every launch hands its argv to the running
	// instance and exits, not just the ones carrying a deep link: launching
	// the app while it is already running now raises the existing window
	// instead of starting a second copy of the whole application.
	//
	// The guard used to be conditional on a deep-link / --open-dir arg, on the
	// grounds that Wails v3 spawns child processes for detached terminal
	// windows and an unconditional hand-off would kill them. That is not what
	// Wails does: WindowDetachTabAt calls app.Window.NewWithOptions, which is
	// a window in THIS process. Nothing re-executes the binary except
	// relaunchApp, exempted below. So a plain double-click on the icon quietly
	// started a second full instance.
	//
	// Two instances is not a cosmetic problem. Both open store.db, and both
	// hold their own in-memory tree: SQLite's locks keep the FILE consistent,
	// but they cannot stop the second instance from writing its stale snapshot
	// over an edit the first one just made. The edit is gone, with no error
	// anywhere. (They also fight over the MCP listener and each run their own
	// backup scheduler.)
	//
	// The relaunch child is the one exception: waitForParentExit() above has
	// already blocked until the old instance released store.db, so there is
	// nothing left to hand off to - and handing off to a dying listener is
	// exactly the race that env var exists to avoid.
	if os.Getenv("SSH_TOOL_WAIT_PID") == "" {
		if trySendToRunning(os.Args[1:]) {
			return true
		}
	}

	// WSL workarounds. Same as v2; the underlying GTK/WebKit stack still
	// needs them. v3 uses GTK4 + WebKitGTK 6 on Linux; the variables we
	// previously set for WebKit2 still apply for nested compositing and
	// DMA-BUF rendering.
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		os.Setenv("GDK_BACKEND", "x11")
		os.Setenv("NO_AT_BRIDGE", "1")
		os.Setenv("GTK_A11Y", "none")
		os.Setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
		os.Setenv("LIBGL_ALWAYS_SOFTWARE", "1")
	}
	return false
}

// buildApp constructs the desktop application with the full option set
// (quit gate, F12 DevTools accelerator, tray + window support).
func buildApp(appInst *App) *application.App {
	return application.New(application.Options{
		Name:        appName,
		Description: "SSH connection manager",
		Icon:        appIcon,
		Linux: application.LinuxOptions{
			// g_set_prgname: without it GTK reports the generic
			// "wails app" as the program name (taskbar hover, window
			// grouping). Must match the .desktop StartupWMClass so the
			// window binds to the launcher entry (and its icon).
			ProgramName: "ssh-tool",
		},
		Services: []application.Service{
			application.NewService(appInst),
			application.NewService(notifier),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// App-level quit gate. The WindowClosing hook below only
		// covers closing the window - macOS Cmd+Q (and any direct
		// app.Quit()) goes through applicationShouldTerminate
		// instead and would kill live SSH sessions with no warning.
		// Same contract as the hook: emit quit_request, let the
		// frontend confirm, ConfirmQuit() sets quitConfirmed and
		// re-quits, which passes straight through here.
		ShouldQuit: func() bool {
			if appInst.quitConfirmed.Load() {
				appInst.syncFlushOnQuit()
				appInst.stopTunnelsOnQuit()
				return true
			}
			if appInst.SshActiveSessionCount() == 0 {
				// Auto-sync: push a dirty profile on the way out,
				// capped at 10s so a dead network can't block quit.
				appInst.syncFlushOnQuit()
				appInst.stopTunnelsOnQuit()
				return true
			}
			// The confirm modal is useless behind a hidden window
			// (tray mode); surface it first.
			if w := appInst.mainWindow; w != nil {
				w.Show()
				w.Focus()
				appInst.windowHidden.Store(false)
			}
			EventsEmit("quit_request", appInst.SshActiveSessionCount())
			return false
		},
		// Global accelerator: F12 opens DevTools on the currently
		// focused window. xterm.js captures right-click for its own
		// selection menu, so the inspector via context menu is hard
		// to reach inside a live terminal pane - this keystroke
		// works regardless of where focus is.
		KeyBindings: map[string]func(application.Window){
			"F12": func(w application.Window) {
				if wv, ok := w.(*application.WebviewWindow); ok {
					wv.OpenDevTools()
				}
			},
		},
	})
}

// configurePlatform creates the main window, system tray, geometry
// persistence, quit/minimise hooks and deep-link dispatch. Returns a
// cleanup func (single-instance listener shutdown) to defer, or nil.
func configurePlatform(app *application.App, appInst *App) func() {
	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            appName,
		Width:            1200,
		Height:           800,
		MinWidth:         800,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(30, 30, 46),
		URL:              "/",
		// Enable native OS drag-and-drop into the WebView. The SftpPane
		// tags itself with data-file-drop-target so a drop there
		// surfaces real file system paths to the Go side (WebView's
		// JS DataTransfer.files would only give names + sizes).
		EnableFileDrop: true,
		// Enable DevTools so user can right-click → Inspect during
		// development; without this the WebView2 / WebKitGTK build
		// has no console output reachable from the UI.
		DevToolsEnabled: true,
	})
	registerFileDropForwarding(mainWindow)
	appInst.mainWindow = mainWindow

	// Persist + restore the window geometry (size, position, which
	// monitor, maximised) across runs. Restore runs from onDBReady (end
	// of initialise) because the store is opened during app.Run, after
	// this point; registering the geometry-event listeners is safe now.
	winSaver := newWindowStateSaver(appInst, mainWindow)
	winSaver.register()
	appInst.onDBReady = func() { winSaver.restore() }

	// System tray. Lets the user pick "close-to-tray" semantics
	// without losing background sessions. Icon click toggles the
	// main window; the right-click menu has Show / Quit.
	tray := app.SystemTray.New()
	tray.SetTooltip(appName)
	if len(trayIcon) > 0 {
		tray.SetIcon(trayIcon)
	}
	trayMenu := app.Menu.New()
	trayMenu.Add("Show window").OnClick(func(_ *application.Context) {
		mainWindow.Show()
		mainWindow.Focus()
	})
	trayMenu.AddSeparator()
	trayMenu.Add("Quit").OnClick(func(_ *application.Context) {
		appInst.quitConfirmed.Store(true)
		app.Quit()
	})
	tray.SetMenu(trayMenu)
	tray.OnClick(func() {
		// Toggle: hide if visible, show otherwise. Best-effort -
		// IsVisible isn't on every platform impl, so we re-show
		// unconditionally if hidden via our flag.
		if appInst.windowHidden.Load() {
			mainWindow.Show()
			mainWindow.Focus()
			appInst.windowHidden.Store(false)
		} else {
			mainWindow.Hide()
			appInst.windowHidden.Store(true)
		}
	})

	// Warn before quit if any SSH sessions are alive. The handler
	// cancels the close on the first attempt and emits "quit_request"
	// so the frontend can show a confirm modal; the user's "Yes,
	// disconnect" calls App.ConfirmQuit() which sets the flag and
	// re-quits, letting this branch fall through.
	//
	// If `close_to_tray` is set, the close is redirected to a tray
	// hide regardless of session state - the user explicitly asked
	// for the window to stay alive.
	// Hooks run synchronously before normal event listeners and their
	// Cancel() prevents the default close/minimise handler from
	// firing - exactly the gate we need to redirect to the tray or
	// raise the warn-before-quit modal.
	// Track focus so RequestAttention only flashes the taskbar when the app is
	// backgrounded, and auto-clear any flash the moment the user looks at it.
	mainWindow.RegisterHook(events.Common.WindowFocus, func(event *application.WindowEvent) {
		appInst.windowFocused.Store(true)
		mainWindow.Flash(false)
	})
	mainWindow.RegisterHook(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		appInst.windowFocused.Store(false)
	})

	mainWindow.RegisterHook(events.Common.WindowMinimise, func(event *application.WindowEvent) {
		if !appInst.shouldMinimiseToTray() {
			return
		}
		event.Cancel()
		mainWindow.Hide()
		appInst.windowHidden.Store(true)
	})

	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if appInst.quitConfirmed.Load() {
			return
		}
		if appInst.shouldCloseToTray() {
			event.Cancel()
			mainWindow.Hide()
			appInst.windowHidden.Store(true)
			return
		}
		if len(appInst.pool.IDs()) == 0 {
			return
		}
		event.Cancel()
		EventsEmit("quit_request", appInst.SshActiveSessionCount())
	})

	// Deep-link dispatch helper. Used both by the initial argv
	// scan (cold start) and by the single-instance listener
	// (when another ssh-tool was launched with a deep-link arg
	// while we were already running).
	dispatchDeepLink := func(argv []string, delay time.Duration) {
		importURL := parseDeepLinkArg(argv)
		if importURL == "" {
			return
		}
		log.Printf("deep link: import URL = %s", importURL)
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			EventsEmit("deep_link_import", importURL)
		}()
	}

	// "Open in ssh-tool" from a file manager: forward the directory to
	// the frontend, which opens the default local shell cd'd into it.
	// Same cold-start delay contract as the deep link - the webview
	// needs a beat before its event listeners are registered.
	dispatchOpenDir := func(argv []string, delay time.Duration) {
		dir := parseOpenDirArg(argv)
		if dir == "" {
			return
		}
		log.Printf("open-dir: %s", dir)
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			EventsEmit("open_dir_shell", dir)
		}()
	}

	// Cold-start path: did this invocation carry a deep link?
	dispatchDeepLink(os.Args[1:], 1200*time.Millisecond)
	dispatchOpenDir(os.Args[1:], 1200*time.Millisecond)

	// macOS path: Launch Services delivers ssh-tool:// opens as an
	// Apple Event, never as argv, so the scan above can't see them.
	// Wails surfaces it as ApplicationLaunchedWithUrl with the URI in
	// the event context. Fires for cold starts and for opens while
	// already running; the event never fires on Windows/Linux.
	app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(ev *application.ApplicationEvent) {
		if u := ev.Context().URL(); u != "" {
			if mainWindow != nil {
				mainWindow.Show()
				mainWindow.Focus()
				appInst.windowHidden.Store(false)
			}
			dispatchDeepLink([]string{u}, 500*time.Millisecond)
		}
	})

	// Single-instance listener: subsequent launches (e.g. browser
	// clicks "Open in ssh-tool" while this instance is already
	// running) connect here, hand us their argv, and exit. We
	// re-emit the deep link event and refocus the window.
	stopInstance, err := startInstanceServer(func(argv []string) {
		log.Printf("instance handoff: argv = %v", argv)
		// Bring the main window forward before the import flow
		// kicks in, otherwise the user wouldn't notice the action.
		if mainWindow != nil {
			mainWindow.Show()
			mainWindow.Focus()
			appInst.windowHidden.Store(false)
		}
		dispatchDeepLink(argv, 200*time.Millisecond)
		dispatchOpenDir(argv, 200*time.Millisecond)
	})
	if err != nil {
		log.Printf("single-instance: %v (continuing without)", err)
		return nil
	}
	return stopInstance
}
