//go:build windows

package tunnelhelper

import (
	"os/exec"
	"syscall"
)

// configureSysProcAttr hides the helper's console window - without
// CREATE_NO_WINDOW every helper spawn flashes a black conhost box on
// top of the GUI app.
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
