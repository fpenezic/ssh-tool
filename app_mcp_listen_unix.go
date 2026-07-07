//go:build (!android && !ios) && !windows

package main

import (
	"net"
	"os"
)

// listenLocal opens a unix-domain socket at addr with 0600 perms so only the
// current user can connect. The `--mcp-bridge` subprocess (same user) dials it.
func listenLocal(addr string) (net.Listener, error) {
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	// Tighten perms: only the owner may connect.
	_ = os.Chmod(addr, 0o600)
	return ln, nil
}

// dialLocal connects to the unix socket (used by the bridge subprocess).
func dialLocal(addr string) (net.Conn, error) {
	return net.Dial("unix", addr)
}
