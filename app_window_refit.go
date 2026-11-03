//go:build !android && !ios

package main

import (
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// A maximised frameless window (compact title bar, Windows) sizes its
// client area from Wails' cached screen list on WM_NCCALCSIZE. When a
// display goes away and comes back (a KVM switch, a dock), Windows moves
// the window to the remaining screen and back again, and the frame can be
// computed before the cache is refreshed: the window rect covers the
// screen but the client area (and the WebView in it) keeps the other
// screen's size, with the window background showing beside it. The window
// size alone does not show it, so once the display messages settle every
// maximised window is maximised again, which recomputes the frame against
// the fresh screen list.
const (
	wmDisplayChange = 0x007E
	wmSettingChange = 0x001A
	spiSetWorkArea  = 0x002F
	displaySettle   = 1500 * time.Millisecond
)

var (
	refitMu    sync.Mutex
	refitTimer *time.Timer
)

// displayChangeInterceptor is the app's Windows WndProcInterceptor. It only
// watches; every message goes on to Wails.
func displayChangeInterceptor(a *App) func(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	return func(_ uintptr, msg uint32, wParam, _ uintptr) (uintptr, bool) {
		if !a.compactTitleBar {
			return 0, false
		}
		if msg == wmDisplayChange || (msg == wmSettingChange && wParam == spiSetWorkArea) {
			refitMu.Lock()
			if refitTimer != nil {
				refitTimer.Stop()
			}
			refitTimer = time.AfterFunc(displaySettle, refitMaximisedWindows)
			refitMu.Unlock()
		}
		return 0, false
	}
}

func refitMaximisedWindows() {
	app := application.Get()
	if app == nil {
		return
	}
	log.Printf("display change: settled, checking maximised windows")
	// Unmaximise / maximise show and activate the window: a window hidden
	// to the tray stays out, and the one that had focus gets it back.
	var refit []*application.WebviewWindow
	var focused *application.WebviewWindow
	for _, w := range app.Window.GetAll() {
		ww, ok := w.(*application.WebviewWindow)
		if !ok || !ww.IsVisible() {
			continue
		}
		if ww.IsFocused() {
			focused = ww
		}
		if ww.IsMaximised() {
			refit = append(refit, ww)
		}
	}
	for _, ww := range refit {
		width, height := ww.Size()
		if scr, err := ww.GetScreen(); err == nil && scr != nil {
			log.Printf("display change: refit %s (window %dx%d, work area %dx%d)", ww.Name(), width, height, scr.WorkArea.Width, scr.WorkArea.Height)
		}
		ww.UnMaximise()
		ww.Maximise()
	}
	if focused != nil && len(refit) > 0 {
		focused.Focus()
	}
}
