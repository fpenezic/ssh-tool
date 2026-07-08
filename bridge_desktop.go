//go:build !android && !ios

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
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
	conn, err := dialMcp()
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

// dialMcp connects to the app. It prefers the loopback TCP leg when the app has
// enabled it (mcp-bridge.tcp file present) - this is the path a WSL client uses
// to reach the Windows app, since WSL2 forwards localhost to the host but can't
// see the app's unix socket. Falls back to the local socket / pipe otherwise.
func dialMcp() (net.Conn, error) {
	if b, err := os.ReadFile(mcpTCPPath()); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
		if len(parts) == 2 {
			addr, token := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if c, derr := net.Dial("tcp", addr); derr == nil {
				// Authenticate: the token line must precede any MCP traffic.
				if _, werr := c.Write([]byte(token + "\n")); werr == nil {
					return c, nil
				}
				_ = c.Close()
			}
		}
	}
	conn, err := dialLocal(mcpSocketPath())
	if err != nil {
		return nil, err
	}
	// Present the local token as the first line before any MCP traffic. The app
	// writes it 0600 next to the socket; a same-user bridge can read it, an
	// unrelated local process guessing the socket path cannot.
	token, terr := os.ReadFile(mcpLocalTokenPath())
	if terr != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read mcp token: %w", terr)
	}
	if _, werr := conn.Write([]byte(strings.TrimSpace(string(token)) + "\n")); werr != nil {
		_ = conn.Close()
		return nil, werr
	}
	return conn, nil
}
