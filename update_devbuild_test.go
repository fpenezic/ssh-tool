package main

import "testing"

// A development build must be able to move ONTO a release: that is how
// someone stops testing their own build and goes back to a shipped
// version. Three earlier guards in this codebase blocked more than they
// were protecting (see the install paths), so pin the ranking here.
//
// The confirmation itself lives in DownloadUpdate, which needs a resolved
// release and a network call; what is testable without either is the
// comparison it makes, and that is where the dev-build case is subtle:
// parseSemver drops the describe suffix, so a dev build ranks against the
// tag it was built FROM.
func TestSemverGreaterAcrossDevBuilds(t *testing.T) {
	cases := []struct {
		name    string
		release string
		running string
		want    bool
	}{
		{"dev build behind the release", "v0.95.0", "v0.94.0-27-gbc8f0bd", true},
		{"dirty dev build behind the release", "v0.95.0", "v0.94.0-27-gbc8f0bd-dirty", true},
		// Built from the release tag itself, plus commits: AHEAD of it.
		{"dev build ahead of the release", "v0.95.0", "v0.95.0-3-gabc1234", false},
		{"release equal to itself", "v0.95.0", "v0.95.0", false},
		// No number to rank at all: never claim an update is available.
		{"plain go run", "v0.95.0", "dev", false},
		{"unknown version", "v0.95.0", "unknown", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := semverGreater(c.release, c.running); got != c.want {
				t.Errorf("semverGreater(%q, %q) = %v, want %v", c.release, c.running, got, c.want)
			}
		})
	}
}

// The UI matches this prefix to turn the refusal into a prompt rather than
// an error banner (DEV_CONFIRM_PREFIX in UpdateModal.svelte). Changing it
// on one side only would silently put the old dead-end message back.
func TestDevReplaceErrPrefixMatchesFrontend(t *testing.T) {
	if devReplaceErrPrefix != "dev-build-confirm: " {
		t.Errorf("devReplaceErrPrefix = %q; UpdateModal.svelte matches on %q",
			devReplaceErrPrefix, "dev-build-confirm: ")
	}
}
