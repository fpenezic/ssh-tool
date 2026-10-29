package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"

	"ssh-tool/internal/store"
)

// Window chrome preferences live in their own small file next to
// store.db, not in the store: the main window's frame is fixed at
// creation (macOS title bar style cannot change on a live window), and
// the window is created before app.Run opens the store.
type windowPrefs struct {
	// CompactTitleBar drops the native title bar: the app's tab bar
	// becomes the title bar (drag region, window controls) on Windows
	// and Linux, and macOS keeps its traffic lights over a full-size
	// content view. Experimental, off by default.
	CompactTitleBar bool `json:"compact_title_bar"`
}

// applyCompactChrome sets the window options for the compact title bar
// on a window whose frontend draws one (main window tab bar, detached
// window top bar). Windows and Linux go frameless and the frontend
// draws the controls (Windows keeps the DWM shadow, rounded corners and
// snap; the runtime arms edge resize on both). macOS keeps the native
// traffic lights over a full-size content view instead of going
// frameless, so nothing of the native window behaviour is redrawn.
func applyCompactChrome(opts *application.WebviewWindowOptions, on bool) {
	if !on {
		return
	}
	if runtime.GOOS == "darwin" {
		opts.Mac.TitleBar = application.MacTitleBarHidden
		return
	}
	opts.Frameless = true
}

func windowPrefsPath() string {
	return filepath.Join(store.DataDir(), "window.json")
}

// loadWindowPrefs never fails: a missing or broken file means defaults.
func loadWindowPrefs() windowPrefs {
	var p windowPrefs
	if b, err := os.ReadFile(windowPrefsPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func saveWindowPrefs(p windowPrefs) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(windowPrefsPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(windowPrefsPath(), b, 0o600)
}

// WindowChrome is what the frontend needs to draw the tab bar as a
// title bar: whether the pref is on (Settings checkbox), whether this
// window actually started with it (the pref only applies on restart),
// and the OS (traffic-light inset on macOS, own buttons elsewhere).
type WindowChrome struct {
	CompactTitleBar bool   `json:"compact_title_bar"`
	Active          bool   `json:"active"`
	OS              string `json:"os"`
}

func (a *App) WindowChromeGet() WindowChrome {
	return WindowChrome{
		CompactTitleBar: loadWindowPrefs().CompactTitleBar,
		Active:          a.compactTitleBar,
		OS:              runtime.GOOS,
	}
}

func (a *App) WindowChromeSetCompact(on bool) error {
	p := loadWindowPrefs()
	p.CompactTitleBar = on
	return saveWindowPrefs(p)
}
