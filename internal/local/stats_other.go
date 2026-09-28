//go:build !windows

package local

import (
	"errors"
	"os/exec"

	sshlayer "ssh-tool/internal/ssh"
)

func windowsStats() (*sshlayer.ServerStats, error) {
	return nil, errors.New("windows stats on a non-windows build")
}

func hideConsole(*exec.Cmd) {}
