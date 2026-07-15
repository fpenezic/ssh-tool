//go:build android || ios

package main

// Mobile has no share server: it cannot bind a listener a colleague could
// reach, and the whole feature is desktop-facing. These stubs let the shared
// app.go / app_share.go compile; a.share simply stays nil, so every ShareStart
// returns "sharing is disabled" and every teardown hook is a no-op.

func (a *App) startShareServer() {}
func (a *App) stopShareServer()  {}
