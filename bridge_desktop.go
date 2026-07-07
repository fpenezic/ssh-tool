//go:build !android && !ios

package main

import (
	"fmt"
	"io"
	"os"
)

// runMcpBridge is the `--mcp-bridge` entrypoint. It is a dumb bidirectional
// pipe: the LLM client speaks MCP (newline-delimited JSON-RPC) over this
// process's stdin/stdout, and we forward it verbatim to the running ssh-tool
// desktop app over its local socket, where the actual MCP server lives (see
// app_mcp_desktop.go). No MCP logic here - MCP-over-socket IS the protocol.
//
// Returns a process exit code. A non-zero code + a stderr line is what the LLM
// client surfaces when the app isn't running or the bridge is disabled.
func runMcpBridge() int {
	addr := mcpSocketPath()
	conn, err := dialLocal(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ssh-tool mcp bridge: cannot reach the app (%v).\n"+
				"Make sure ssh-tool is running and 'Allow LLM (MCP) access' is enabled in Settings.\n",
			err)
		return 1
	}
	defer conn.Close()

	// Pump both directions; exit when either side closes.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(conn, os.Stdin); done <- struct{}{} }()
	go func() { _, _ = io.Copy(os.Stdout, conn); done <- struct{}{} }()
	<-done
	return 0
}
