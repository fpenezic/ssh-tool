//go:build windows

package main

import "os/exec"

// silenceSpawn hides the console window for a short-lived probe. Without
// it, asking another build for its version flashes a black conhost box
// over the GUI.
func silenceSpawn(cmd *exec.Cmd) { hideConsole(cmd) }
