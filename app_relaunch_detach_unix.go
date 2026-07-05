//go:build !android && !ios && !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachRelaunchChild moves the re-launched instance into its OWN
// session (setsid) so it survives the death of the current process.
//
// On Linux desktops the app runs inside a per-app systemd scope
// (app-gnome-*.scope). When the old instance calls app.Quit() the
// scope tears down and systemd sends SIGTERM to EVERY process still
// in it - including a child we just exec'd with a plain cmd.Start(),
// which inherits the parent's process group and scope. The result the
// user sees: click Restart, the app closes and never comes back;
// launching it by hand works (fresh scope). Setsid detaches the child
// into a new session/process-group so the dying scope no longer owns
// it.
func detachRelaunchChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
