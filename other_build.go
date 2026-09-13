//go:build !android && !ios

package main

import (
	"os"
	"path/filepath"
)

// OtherBuild describes a second copy of ssh-tool that was launched while
// this one is running.
//
// The single-instance guard hands the second launch's argv to us and
// exits it, which is right for a deep link but wrong for an upgrade: the
// user double-clicks the build they just downloaded, watches the old
// window come forward, and has no way to tell whether anything happened.
type OtherBuild struct {
	// Version is what the other process reported, e.g. "v0.95.0".
	Version string `json:"version"`
	// ExePath is where it was launched from.
	ExePath string `json:"exe_path"`
	// Newer is true when the other build's version sorts after ours.
	// Purely advisory - a build from a branch or a dev build is
	// "different" without being newer, and both are worth mentioning.
	Newer bool `json:"newer"`
}

// otherIsNewer ranks the two versions.
//
// Newer is advisory - it only decides the wording - so when the two
// cannot be compared this answers false rather than guessing. That
// covers a dev build (appVersion is "dev", or a git describe string for
// an untagged build), where nothing can be said about how a download
// ranks against it: "a different build was launched" is accurate,
// "a newer build" would not be.
func otherIsNewer(other, self string) bool {
	if _, ok := parseSemver(self); !ok {
		return false
	}
	if _, ok := parseSemver(other); !ok {
		return false
	}
	return semverGreater(other, self)
}

// describeOtherBuild returns a description when the handoff came from a
// build worth telling the user about, or nil when it was just another
// launch of the same one (the overwhelmingly common case: a deep link,
// or a second double-click).
func describeOtherBuild(msg instanceMsg) *OtherBuild {
	// Older builds do not send these fields. Nothing to say.
	if msg.Version == "" || msg.ExePath == "" {
		return nil
	}
	// Same version AND same path: an ordinary second launch.
	self, err := os.Executable()
	if err == nil {
		self, _ = filepath.EvalSymlinks(self)
	}
	other, _ := filepath.EvalSymlinks(msg.ExePath)
	if other == "" {
		other = msg.ExePath
	}
	if msg.Version == appVersion && other == self {
		return nil
	}
	// Same binary, reported differently (a dev build launched twice,
	// say). Still nothing useful to offer.
	if other == self {
		return nil
	}
	return &OtherBuild{
		Version: msg.Version,
		ExePath: other,
		Newer:   otherIsNewer(msg.Version, appVersion),
	}
}
