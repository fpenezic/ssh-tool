//go:build !windows

package ssh

import (
	"net"
	"os"
)

// agentPath is the agent socket: the credential's, else SSH_AUTH_SOCK.
func agentPath(configured string) string {
	if configured != "" {
		return configured
	}
	return os.Getenv("SSH_AUTH_SOCK")
}

func dialAgent(path string) (net.Conn, error) { return net.Dial("unix", path) }
