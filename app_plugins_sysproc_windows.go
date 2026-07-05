//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole applies CREATE_NO_WINDOW so a short-lived helper spawn
// (e.g. `<helper> --version` during PluginsStatus) doesn't flash a
// black conhost box over the GUI. Same treatment tunnelhelper gives
// the long-lived tunnel process.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
