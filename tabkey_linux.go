//go:build linux && cgo && !gtk3 && !android && !server

package main

/*
#cgo pkg-config: gtk4

#include <gtk/gtk.h>
#include <stdint.h>

extern void sshtoolTabCycle(uintptr_t win, int dir);

// GTK binds Ctrl+Tab / Ctrl+Shift+Tab on every window to keyboard focus
// navigation. Left to it, the keystroke reaches the page (which switches
// the terminal tab) AND moves GTK focus off the WebView, so the next
// keystrokes go nowhere until a second Ctrl+Tab moves it back. Taking the
// keys in the capture phase keeps them away from both.
static gboolean sshtool_tab_key(GtkEventControllerKey *ctrl, guint keyval,
                                guint keycode, GdkModifierType state,
                                gpointer data) {
	GdkModifierType mods = state & (GDK_CONTROL_MASK | GDK_ALT_MASK | GDK_SUPER_MASK);
	if (mods != GDK_CONTROL_MASK) return FALSE;
	if (keyval == GDK_KEY_Tab) {
		sshtoolTabCycle((uintptr_t)data, (state & GDK_SHIFT_MASK) ? -1 : 1);
		return TRUE;
	}
	if (keyval == GDK_KEY_ISO_Left_Tab) {
		sshtoolTabCycle((uintptr_t)data, -1);
		return TRUE;
	}
	return FALSE;
}

static void sshtool_install_tab_key(void *window, uintptr_t id) {
	GtkEventController *c = gtk_event_controller_key_new();
	gtk_event_controller_set_propagation_phase(c, GTK_PHASE_CAPTURE);
	g_signal_connect(c, "key-pressed", G_CALLBACK(sshtool_tab_key), (gpointer)id);
	gtk_widget_add_controller(GTK_WIDGET(window), c);
}
*/
import "C"

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

var (
	tabKeyMu      sync.Mutex
	tabKeyWindows = map[uintptr]*application.WebviewWindow{}
	tabKeyNext    uintptr
)

// installTabKeyGuard routes Ctrl+Tab / Ctrl+Shift+Tab in w to the frontend
// as a "tab_cycle_key" event (+1 / -1) instead of letting GTK use them for
// focus navigation. The frontend runs the same tab cycling it does for the
// keydown on other platforms.
func installTabKeyGuard(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	tabKeyMu.Lock()
	tabKeyNext++
	id := tabKeyNext
	tabKeyWindows[id] = w
	tabKeyMu.Unlock()
	// The GtkWindow does not exist yet when NewWithOptions returns, and
	// RuntimeReady fires again on every page reload: install once, on the
	// main thread.
	var once sync.Once
	w.OnWindowEvent(events.Common.WindowRuntimeReady, func(_ *application.WindowEvent) {
		once.Do(func() {
			application.InvokeAsync(func() {
				if native := w.NativeWindow(); native != nil {
					C.sshtool_install_tab_key(native, C.uintptr_t(id))
				}
			})
		})
	})
}

//export sshtoolTabCycle
func sshtoolTabCycle(id C.uintptr_t, dir C.int) {
	tabKeyMu.Lock()
	w := tabKeyWindows[uintptr(id)]
	tabKeyMu.Unlock()
	if w == nil {
		return
	}
	w.DispatchWailsEvent(&application.CustomEvent{Name: "tab_cycle_key", Data: int(dir)})
}
