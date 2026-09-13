//go:build linux && !android

package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// The bug this guards against: Wails filters SettingChanged on the
// "org.gnome.desktop.interface" namespace only, so KDE's signal (sent
// under the cross-desktop "org.freedesktop.appearance") is discarded and
// the app never follows a theme change after startup.
func TestIsColorSchemeSignalAcceptsBothNamespaces(t *testing.T) {
	cases := []struct {
		name string
		ns   string
		key  string
		want bool
	}{
		{"KDE / portal spec", "org.freedesktop.appearance", "color-scheme", true},
		{"GNOME", "org.gnome.desktop.interface", "color-scheme", true},
		{"wrong key, right namespace", "org.freedesktop.appearance", "accent-color", false},
		{"unrelated namespace", "org.example.other", "color-scheme", false},
	}
	for _, tc := range cases {
		sig := &dbus.Signal{Body: []interface{}{tc.ns, tc.key, dbus.MakeVariant(uint32(1))}}
		if got := isColorSchemeSignal(sig); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Signals arrive from outside the process, so malformed ones must not
// panic the watcher goroutine.
func TestIsColorSchemeSignalRejectsMalformed(t *testing.T) {
	cases := []*dbus.Signal{
		nil,
		{Body: nil},
		{Body: []interface{}{}},
		{Body: []interface{}{"org.freedesktop.appearance"}}, // truncated
		{Body: []interface{}{42, "color-scheme"}},           // namespace not a string
		{Body: []interface{}{"org.freedesktop.appearance", 7}},
	}
	for i, sig := range cases {
		if isColorSchemeSignal(sig) {
			t.Errorf("case %d: malformed signal accepted", i)
		}
	}
}
