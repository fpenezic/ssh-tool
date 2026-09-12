//go:build !windows && !android && !ios

package main

import "os/exec"

// silenceSpawn is a no-op off Windows: there is no console window to
// hide when spawning a short-lived probe.
func silenceSpawn(cmd *exec.Cmd) {}
