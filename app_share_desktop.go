//go:build !android && !ios

package main

// Desktop lifecycle for the browser session-share server. Mirrors the MCP
// listener lifecycle (app_mcp_desktop.go): idempotent start/stop guarded by a
// setting, started from initialise() at boot and live-toggled from SettingsSet.

import "log"

// startShareServer builds and holds the share server when share_enabled is on.
// Idempotent and self-gating. The server does not BIND until the first
// ShareStart (each share picks its own interface); this just makes the IPC
// surface live so the frontend can start shares.
func (a *App) startShareServer() {
	a.shareLifecycleMu.Lock()
	defer a.shareLifecycleMu.Unlock()
	if a.share != nil {
		return
	}
	if !a.boolSetting("share_enabled") {
		return
	}
	srv, err := a.buildShareServer()
	if err != nil {
		log.Printf("share: build server: %v", err)
		return
	}
	a.share = srv
}

// stopShareServer tears every share down and drops the server.
func (a *App) stopShareServer() {
	a.shareLifecycleMu.Lock()
	srv := a.share
	a.share = nil
	a.shareLifecycleMu.Unlock()
	if srv != nil {
		srv.StopAll()
	}
}
