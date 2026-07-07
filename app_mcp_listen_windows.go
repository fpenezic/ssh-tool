//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// listenLocal on Windows binds a loopback TCP listener (127.0.0.1:0, so no
// network exposure) and records the chosen address in a 0600 file at addrPath,
// which the bridge subprocess reads. Windows lacks unix sockets without an
// extra dependency; loopback + a per-user 0600 rendezvous file is the
// equivalent local-only channel.
func listenLocal(addrPath string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(addrPath, []byte(ln.Addr().String()), 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("write mcp addr file: %w", err)
	}
	return ln, nil
}

// dialLocal reads the loopback address from the rendezvous file and dials it.
func dialLocal(addrPath string) (net.Conn, error) {
	b, err := os.ReadFile(addrPath)
	if err != nil {
		return nil, fmt.Errorf("read mcp addr file: %w", err)
	}
	return net.Dial("tcp", strings.TrimSpace(string(b)))
}
