//go:build windows

package ssh

import (
	"net"
	"os"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

// defaultAgentPipe is the Win32-OpenSSH agent, which needs no
// SSH_AUTH_SOCK (usually unset on Windows).
const defaultAgentPipe = `\\.\pipe\openssh-ssh-agent`

// agentPath is the agent: the credential's path, else SSH_AUTH_SOCK, else
// the Windows OpenSSH agent's pipe.
func agentPath(configured string) string {
	if configured != "" {
		return configured
	}
	if env := os.Getenv("SSH_AUTH_SOCK"); env != "" {
		return env
	}
	return defaultAgentPipe
}

// dialAgent opens a named pipe (the OpenSSH agent, Pageant's pipe), or an
// AF_UNIX socket for any other path (a socket some agents create).
func dialAgent(path string) (net.Conn, error) {
	if strings.HasPrefix(path, `\\.\pipe\`) {
		return namedpipe.DialTimeout(path, 5*time.Second)
	}
	return net.Dial("unix", path)
}
