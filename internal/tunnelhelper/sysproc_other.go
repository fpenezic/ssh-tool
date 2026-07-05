//go:build !windows

package tunnelhelper

import "os/exec"

// configureSysProcAttr is a no-op off Windows; the helper is a plain
// child process.
func configureSysProcAttr(_ *exec.Cmd) {}
