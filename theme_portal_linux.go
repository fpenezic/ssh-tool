//go:build linux && !android

package main

import (
	"log"

	"github.com/godbus/dbus/v5"
)

// watchDesktopTheme follows the desktop's light/dark preference through
// the xdg-desktop-portal and calls onChange whenever it flips.
//
// Wails has its own theme monitor, but it filters SettingChanged on the
// "org.gnome.desktop.interface" namespace. KDE's portal announces
// changes under "org.freedesktop.appearance" - the cross-desktop
// namespace, and the one Wails itself reads when it queries the current
// value - so on KDE the signal arrives and is discarded. The result is
// an app that reads the theme correctly at startup and then never
// notices the user changing it.
//
// Both namespaces are accepted here: GNOME emits the gnome one (as a
// string, "prefer-dark"), the portal spec uses appearance (as a uint32,
// 1 = dark). Whichever arrives, the value is re-read from the portal
// rather than parsed out of the signal, so there is one interpretation
// of it instead of two.
//
// Returns a stop function. A desktop with no portal (or no session bus)
// logs once and does nothing further - prefers-color-scheme stays in
// charge there, which is the pre-existing behaviour.
func watchDesktopTheme(onChange func(dark bool)) func() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Printf("theme: no session bus, desktop theme changes will not be followed: %v", err)
		return func() {}
	}

	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Settings"),
		dbus.WithMatchMember("SettingChanged"),
		dbus.WithMatchObjectPath("/org/freedesktop/portal/desktop"),
	); err != nil {
		log.Printf("theme: cannot subscribe to portal SettingChanged: %v", err)
		conn.Close()
		return func() {}
	}

	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig, ok := <-ch:
				if !ok {
					return
				}
				if !isColorSchemeSignal(sig) {
					continue
				}
				dark, known := readPortalDarkMode(conn)
				if !known {
					continue
				}
				onChange(dark)
			}
		}
	}()

	return func() {
		close(done)
		conn.RemoveSignal(ch)
		conn.Close()
	}
}

// isColorSchemeSignal reports whether a SettingChanged signal concerns
// the colour scheme, under either namespace that carries it.
func isColorSchemeSignal(sig *dbus.Signal) bool {
	if sig == nil || len(sig.Body) < 2 {
		return false
	}
	ns, ok := sig.Body[0].(string)
	if !ok {
		return false
	}
	key, ok := sig.Body[1].(string)
	if !ok {
		return false
	}
	if key != "color-scheme" {
		return false
	}
	return ns == "org.freedesktop.appearance" || ns == "org.gnome.desktop.interface"
}

// readPortalDarkMode asks the portal for the current colour scheme.
//
// The reply is a variant wrapping a variant wrapping a uint32, where
// 0 = no preference, 1 = dark, 2 = light. Anything unexpected reports
// not-known rather than guessing, which leaves prefers-color-scheme in
// charge instead of forcing a value we invented.
func readPortalDarkMode(conn *dbus.Conn) (dark bool, known bool) {
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	call := obj.Call("org.freedesktop.portal.Settings.Read", 0,
		"org.freedesktop.appearance", "color-scheme")
	if call.Err != nil {
		return false, false
	}
	var outer dbus.Variant
	if err := call.Store(&outer); err != nil {
		return false, false
	}
	inner, ok := outer.Value().(dbus.Variant)
	if !ok {
		return false, false
	}
	scheme, ok := inner.Value().(uint32)
	if !ok {
		return false, false
	}
	// 0 is "no preference", which is not the same as light: leave the
	// webview's own answer alone in that case.
	if scheme == 0 {
		return false, false
	}
	return scheme == 1, true
}

// currentDesktopDarkMode reads the preference once, for the initial
// paint. Separate connection because it runs before the watcher and
// must not depend on it having started.
func currentDesktopDarkMode() (dark bool, known bool) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, false
	}
	defer conn.Close()
	return readPortalDarkMode(conn)
}
