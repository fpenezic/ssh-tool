//go:build !android && !ios

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// runPrintVersion asks another ssh-tool binary what version it is, by
// running it with --print-version (handled at the top of main, before
// the single-instance guard, so it prints and exits without launching).
//
// The version is injected with -ldflags -X main.appVersion, so it sits
// in the file as a plain string with no marker around it; asking the
// binary is both simpler than scanning and correct across builds.
//
// Returns "" when the binary cannot be run or does not answer - an old
// build predating the flag, a different architecture, a corrupt
// download. Callers treat that as "something is installed, version
// unknown", which is still worth reporting.
//
// Scanning the file for the injected string was tried as a fallback for
// the Windows GUI build and removed: run against a real ssh-tool.exe it
// returned "v1.0.0" from an unrelated string in some dependency, not the
// build version. A confident wrong answer here would label an upgrade
// with the wrong version, which is worse than no version at all - the
// callers already handle "" as "installed, version unknown".
func runPrintVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--print-version")
	silenceSpawn(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	// Guard against a binary that ignores the flag and prints something
	// else entirely (or opens a window and returns nothing).
	if len(v) > 64 || strings.ContainsAny(v, "\n\r") {
		return ""
	}
	return v
}
