//go:build !android && !ios

package main

import (
	"os"
	"testing"
)

// The common case by far: a deep link, or the user double-clicking the
// icon of the app they already have open. Saying anything there would
// be noise.
func TestDescribeOtherBuildIgnoresSameBuild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot resolve own path")
	}
	got := describeOtherBuild(instanceMsg{
		Argv: []string{"ssh-tool://open"}, Version: appVersion, ExePath: self,
	})
	if got != nil {
		t.Errorf("same version + same path should be silent, got %+v", got)
	}
}

// Builds before this field existed send neither, and we have nothing to
// report about them.
func TestDescribeOtherBuildIgnoresOldSenders(t *testing.T) {
	for _, msg := range []instanceMsg{
		{Argv: []string{"x"}},
		{Argv: []string{"x"}, Version: "v0.95.0"},
		{Argv: []string{"x"}, ExePath: "/tmp/ssh-tool"},
	} {
		if got := describeOtherBuild(msg); got != nil {
			t.Errorf("incomplete message should be silent, got %+v", got)
		}
	}
}

// The case this exists for: a downloaded upgrade run while the installed
// copy is open. Without this the user sees the old window surface and
// assumes the download was broken.
func TestDescribeOtherBuildReportsNewerDownload(t *testing.T) {
	got := describeOtherBuild(instanceMsg{
		Version: "v99.0.0",
		ExePath: "/home/user/Downloads/ssh-tool-linux-amd64",
	})
	if got == nil {
		t.Fatal("a different build at a different path should be reported")
	}
	if got.Version != "v99.0.0" {
		t.Errorf("Version = %q", got.Version)
	}
	// Newer is only claimed when both sides parse. Under `go test`
	// appVersion is "dev", so the honest answer here is false - the
	// launch is still reported, just without the "newer" wording.
	if _, ok := parseSemver(appVersion); ok && !got.Newer {
		t.Error("v99.0.0 should rank newer than a real release version")
	}
}

// Downgrades and branch builds are still worth mentioning - the user
// launched something and it appeared to do nothing - but must not claim
// to be newer.
func TestDescribeOtherBuildReportsOlderWithoutClaimingNewer(t *testing.T) {
	got := describeOtherBuild(instanceMsg{
		Version: "v0.0.1",
		ExePath: "/tmp/some-old-ssh-tool",
	})
	if got == nil {
		t.Fatal("an older build at another path should still be reported")
	}
	if got.Newer {
		t.Error("v0.0.1 must not be reported as newer")
	}
}

// Same file reached by a different path (a symlink, or a relative
// launch) is the same build: resolve before comparing.
func TestDescribeOtherBuildResolvesSymlinks(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot resolve own path")
	}
	link := t.TempDir() + "/ssh-tool-link"
	if err := os.Symlink(self, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got := describeOtherBuild(instanceMsg{Version: appVersion, ExePath: link}); got != nil {
		t.Errorf("a symlink to ourselves is not another build, got %+v", got)
	}
}

// A dev build has no parseable version, and semverGreater answers false
// for any comparison involving it. Left alone that would report a real
// tagged release as "not newer" than a dev build, which is backwards
// for the one workflow that hits it most: testing an upgrade.
func TestOtherIsNewerRefusesToGuess(t *testing.T) {
	// Nothing can be said about how a release ranks against a dev build,
	// so nothing is: the launch is reported without the "newer" claim.
	// Note "v0.94.0-4-g271ae09-dirty" is NOT in this list - parseSemver
	// drops everything from the first "-", so a git describe string
	// carries a usable version and is compared normally (below).
	for _, self := range []string{"dev", "unknown"} {
		if otherIsNewer("v0.95.0", self) {
			t.Errorf("must not rank against unparseable local version %q", self)
		}
		if otherIsNewer("also-not-semver", self) {
			t.Errorf("two unparseable versions should not rank, self=%q", self)
		}
	}
	// Two real versions compare normally in both directions.
	if !otherIsNewer("v0.95.0", "v0.94.0") {
		t.Error("v0.95.0 > v0.94.0")
	}
	// An untagged local build still carries its base version, which is
	// the common case when testing an upgrade against a local build.
	if !otherIsNewer("v0.95.0", "v0.94.0-4-g271ae09-dirty") {
		t.Error("a release should outrank the describe string of an older base")
	}
	if otherIsNewer("v0.94.0", "v0.95.0") {
		t.Error("v0.94.0 is not newer than v0.95.0")
	}
}
