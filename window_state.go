//go:build !android && !ios

package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// winstateDebug toggles verbose window-geometry logging to the app log
// (Settings -> Logs). Off by default; flip on to diagnose restore.
const winstateDebug = false

func winlog(format string, args ...any) {
	if winstateDebug {
		log.Printf("[winstate] "+format, args...)
	}
}

// windowStateKey is the app_settings key holding the persisted window
// geometry. Stored as JSON so adding fields later doesn't need a schema
// change.
const windowStateKey = "window_state_v1"

// windowState is the persisted geometry of the main window. Bounds are
// absolute desktop coordinates (Wails Bounds()), so they naturally
// encode which monitor the window was on in a multi-display setup.
type windowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

// windowStateSaver debounces geometry writes. WindowDidMove/DidResize
// fire continuously during a drag; we only persist the latest state a
// short while after movement settles, plus once synchronously on close.
type windowStateSaver struct {
	app *App
	win application.Window

	mu        sync.Mutex
	timer     *time.Timer
	maximised bool // last-known maximised flag (events don't always carry it)

	// pending holds the geometry to apply once the window is shown. The
	// initial Width/Height from WebviewWindowOptions are applied during
	// the show, so a SetBounds done too early (before show) gets clobbered
	// - we (re)apply on the first WindowShow to win that race.
	applied bool
	pending *windowState
}

func newWindowStateSaver(a *App, win application.Window) *windowStateSaver {
	return &windowStateSaver{app: a, win: win}
}

// rectOnScreen reports whether enough of the rect (a grabbable strip
// of the title bar) is inside some display. Guards restore against
// persisted garbage: Windows reports -32000,-32000 for minimised
// windows, and a maximise/unmaximise glitch can leave the restore
// rect entirely outside every display - faithfully restoring that
// strands the window where the user can't reach it (real incident:
// titlebar double-click warped the window off-screen, we saved it,
// and every relaunch restored it right back off-screen).
func rectOnScreen(ws windowState) bool {
	app := application.Get()
	if app == nil || app.Screen == nil {
		return true // can't verify - accept rather than fight
	}
	screens := app.Screen.GetAll()
	if len(screens) == 0 {
		return true
	}
	const grab = 50 // px of overlap required in both axes
	for _, sc := range screens {
		b := sc.Bounds
		overlapX := min(ws.X+ws.Width, b.X+b.Width) - max(ws.X, b.X)
		overlapY := min(ws.Y+ws.Height, b.Y+b.Height) - max(ws.Y, b.Y)
		// The title bar sits at the top of the rect, so require the
		// top edge itself to be reachable, not just any slice of the
		// window body.
		if overlapX >= grab && overlapY >= grab && ws.Y >= b.Y-grab && ws.Y < b.Y+b.Height-grab {
			return true
		}
	}
	return false
}

// restore reads the saved geometry and applies it before the window is
// shown. Best-effort: a missing or malformed record leaves the window
// at its default size/position.
func (s *windowStateSaver) restore() {
	if s.app == nil || s.app.db == nil || s.win == nil {
		return
	}
	raw, ok := s.app.localSettingGet(windowStateKey)
	if !ok || raw == "" {
		winlog("restore: no saved state (ok=%v)", ok)
		return
	}
	var ws windowState
	if err := json.Unmarshal([]byte(raw), &ws); err != nil {
		winlog("restore: bad JSON: %v (raw=%q)", err, raw)
		return
	}
	winlog("restore: loaded %+v", ws)
	s.mu.Lock()
	s.pending = &ws
	s.maximised = ws.Maximised
	s.mu.Unlock()
	// Try now (covers the case where the window is already shown), and
	// again on the first WindowShow (covers the common case where the
	// default size from options would otherwise overwrite us).
	s.applyPending()
}

// applyPending writes the saved geometry to the window. It is retried
// from several lifecycle events because an early call (before the window
// is realized) does nothing - SetBounds is a no-op and Bounds() reads
// back 0x0. We only mark it `applied` once the bounds actually stick, so
// later events (WindowShow / RuntimeReady) get another go. Once applied,
// subsequent calls are no-ops so a real user resize isn't fought.
func (s *windowStateSaver) applyPending() {
	s.mu.Lock()
	if s.applied || s.pending == nil || s.win == nil {
		s.mu.Unlock()
		return
	}
	ws := *s.pending
	s.mu.Unlock()

	if ws.Width > 0 && ws.Height > 0 {
		// Off-screen rescue: keep the saved size but let the window
		// open centred on the primary display instead of restoring a
		// position the user can't reach.
		if !rectOnScreen(ws) {
			winlog("applyPending: saved rect %+v is off-screen, recentring", ws)
			if app := application.Get(); app != nil && app.Screen != nil {
				if prim := primaryScreen(app.Screen.GetAll()); prim != nil {
					wa := prim.WorkArea
					if ws.Width > wa.Width {
						ws.Width = wa.Width
					}
					if ws.Height > wa.Height {
						ws.Height = wa.Height
					}
					ws.X = wa.X + (wa.Width-ws.Width)/2
					ws.Y = wa.Y + (wa.Height-ws.Height)/2
				}
			}
		}
		winlog("applyPending: SetBounds %+v", ws)
		s.win.SetBounds(application.Rect{X: ws.X, Y: ws.Y, Width: ws.Width, Height: ws.Height})
		after := s.win.Bounds()
		winlog("applyPending: bounds after SetBounds = %+v", after)
		// The window wasn't ready - bounds didn't take. Leave `applied`
		// false so the next lifecycle event retries.
		if after.Width == 0 && after.Height == 0 {
			winlog("applyPending: not realized yet, will retry")
			return
		}
	}
	if ws.Maximised {
		s.win.Maximise()
	}
	s.mu.Lock()
	s.applied = true
	s.mu.Unlock()
	winlog("applyPending: done")
}

// primaryScreen picks the primary display, falling back to the first.
func primaryScreen(screens []*application.Screen) *application.Screen {
	for _, sc := range screens {
		if sc.IsPrimary {
			return sc
		}
	}
	if len(screens) > 0 {
		return screens[0]
	}
	return nil
}

// schedule debounces a save after a move/resize. Coalesces a burst of
// events into one write ~500ms after the last one.
func (s *windowStateSaver) schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(500*time.Millisecond, s.save)
}

// save captures the current geometry and persists it. When maximised we
// keep the stored restore-bounds (the pre-maximise rect) so unmaximising
// after a restart returns to a sensible size, and only flip the flag.
func (s *windowStateSaver) save() {
	if s.app == nil || s.app.db == nil || s.win == nil {
		return
	}
	// Don't persist geometry until the restore has been applied. The
	// window emits resize/move events while it's being shown at the
	// default size; saving those would overwrite the user's saved bounds
	// with 1200x800 before we ever get to restore them.
	s.mu.Lock()
	ready := s.applied || s.pending == nil
	s.mu.Unlock()
	if !ready {
		winlog("save: skipped (restore not applied yet)")
		return
	}

	// Never persist while minimised - Windows parks minimised windows
	// at -32000,-32000 and saving that strands the next launch
	// off-screen.
	if s.win.IsMinimised() {
		winlog("save: skipped (minimised)")
		return
	}

	maximised := s.win.IsMaximised()

	ws := windowState{Maximised: maximised}
	if maximised {
		// Bounds while maximised are the full screen; don't overwrite the
		// saved restore rect with that. Reuse the last persisted rect.
		if raw, ok := s.app.localSettingGet(windowStateKey); ok && raw != "" {
			var prev windowState
			if json.Unmarshal([]byte(raw), &prev) == nil {
				ws.X, ws.Y, ws.Width, ws.Height = prev.X, prev.Y, prev.Width, prev.Height
			}
		}
	} else {
		b := s.win.Bounds()
		ws.X, ws.Y, ws.Width, ws.Height = b.X, b.Y, b.Width, b.Height
		// Reject obviously-bogus rects (minimise placeholder coords, a
		// transient 0x0 during a mode change) instead of persisting them.
		if ws.Width <= 0 || ws.Height <= 0 || ws.X <= -30000 || ws.Y <= -30000 {
			winlog("save: skipped (bogus rect %+v)", ws)
			return
		}
	}
	s.mu.Lock()
	s.maximised = maximised
	s.mu.Unlock()

	if data, err := json.Marshal(ws); err == nil {
		winlog("save: persisting %+v", ws)
		_ = s.app.localSettingSet(windowStateKey, string(data))
	}
}

// register wires the saver to the window's geometry events. Move/resize
// debounce; maximise saves immediately (it's a discrete user action);
// closing flushes synchronously so the final state is never lost.
func (s *windowStateSaver) register() {
	if s.win == nil {
		return
	}
	// Apply the saved geometry once the window is actually shown - by
	// then the default options size has been applied, so our SetBounds
	// sticks instead of being overwritten.
	s.win.OnWindowEvent(events.Common.WindowShow, func(_ *application.WindowEvent) { winlog("event: WindowShow"); s.applyPending() })
	s.win.OnWindowEvent(events.Common.WindowRuntimeReady, func(_ *application.WindowEvent) { winlog("event: WindowRuntimeReady"); s.applyPending() })
	s.win.OnWindowEvent(events.Common.WindowDidMove, func(_ *application.WindowEvent) { winlog("event: WindowDidMove"); s.schedule() })
	s.win.OnWindowEvent(events.Common.WindowDidResize, func(_ *application.WindowEvent) { winlog("event: WindowDidResize"); s.schedule() })
	s.win.OnWindowEvent(events.Common.WindowMaximise, func(_ *application.WindowEvent) { winlog("event: WindowMaximise"); s.save() })
	s.win.OnWindowEvent(events.Common.WindowUnMaximise, func(_ *application.WindowEvent) { winlog("event: WindowUnMaximise"); s.save() })
	// Flush on close so the last position is persisted even if a debounce
	// timer was still pending. RegisterHook runs synchronously before the
	// window tears down.
	s.win.RegisterHook(events.Common.WindowClosing, func(_ *application.WindowEvent) { s.save() })
}
