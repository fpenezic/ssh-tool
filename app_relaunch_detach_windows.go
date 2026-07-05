//go:build windows

package main

import "os/exec"

// detachRelaunchChild is a no-op on Windows: the re-launched instance
// is already independent (no systemd scope teardown to escape), and
// the existing SSH_TOOL_WAIT_PID handshake covers the store.db lock.
func detachRelaunchChild(_ *exec.Cmd) {}
