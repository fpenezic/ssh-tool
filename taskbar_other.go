//go:build !windows

// No taskbar progress outside Windows yet. macOS would need a custom
// NSDockTile view drawn in ObjC, and Linux the DBus
// com.canonical.Unity.LauncherEntry signal (which KDE and Dash-to-Dock honour
// but vanilla GNOME ignores). Both are separate pieces of work; the tracker in
// app_taskbar.go runs regardless and simply has nowhere to paint.

package main

func (a *App) applyTaskbarProgress(pct int, state taskbarState) {}
