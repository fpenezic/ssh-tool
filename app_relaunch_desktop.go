//go:build !android && !ios

package main

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

// relaunchApp spawns a fresh instance of this binary and quits the current
// one - the "restart to apply" step after a sync pull or backup restore,
// without making the user find the icon again. The child gets
// SSH_TOOL_WAIT_PID so its startup waits for this process to release
// store.db (and so it doesn't hand itself off to us via the single-instance
// socket and exit). The android build can't re-exec a process, so it has its
// own relaunchApp (reload the WebView; pending restore applies in-process).
func (a *App) relaunchApp() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "SSH_TOOL_WAIT_PID="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return err
	}
	a.quitConfirmed.Store(true)
	if a.app != nil {
		// Give the IPC response a moment to reach the webview before
		// the process dies.
		go func() {
			time.Sleep(300 * time.Millisecond)
			a.app.Quit()
		}()
	}
	return nil
}
