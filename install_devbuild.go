package main

import "strings"

// isDevBuild reports whether this process is a development build rather
// than a release.
//
// appVersion is injected at build time: "dev" for a plain `go run .`,
// a bare git describe string for an untagged build, and something with
// a "-dirty" suffix when the tree had uncommitted changes. A tagged
// release is exactly "vX.Y.Z".
//
// This matters for the install offer. A dev build run from ./bin is
// "loose" by every other measure, so without this check it offers to
// install itself over whatever the user actually uses - one stray click
// and their working copy is replaced by a half-finished build. Someone
// running from a build directory is doing so deliberately and does not
// need a menu entry for it.
func isDevBuild() bool {
	v := strings.TrimSpace(appVersion)
	if v == "" || v == "dev" || v == "unknown" {
		return true
	}
	// A release tag is bare: v0.94.0. Anything carrying a describe
	// suffix (-12-gabc1234) or -dirty is a build from between tags.
	return strings.Contains(v, "-")
}
