//go:build android || ios

package main

// The MCP bridge is desktop-only: it needs a local socket + a subprocess the
// LLM launches, neither of which applies on mobile. These stubs keep the
// shared initialise() path building under the mobile tags.

func (a *App) startMcpListener() {}
func (a *App) stopMcpListener()  {}
