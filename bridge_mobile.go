//go:build android || ios

package main

// The MCP bridge subprocess is desktop-only. On mobile there is no second
// process model and no local socket, so this is never reached (main.go's flag
// check only fires on a CLI invocation), but the symbol must exist for the
// shared main.go to build.
func runMcpBridge() int { return 0 }
