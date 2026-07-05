//go:build !windows

package main

import "os/exec"

// hideConsole is a no-op off Windows.
func hideConsole(_ *exec.Cmd) {}
