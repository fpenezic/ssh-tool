package main

import (
	"os"
	"path/filepath"
	"testing"
)

// syncFingerprintBody must strip the format tag so two fingerprints
// with different tags but the same content compare equal - the
// property that lets an upgrade re-baseline instead of pushing an
// unchanged profile.
func TestSyncFingerprintBody(t *testing.T) {
	cases := map[string]string{
		"fp1|folders=2;vault=99": "folders=2;vault=99", // tagged
		"folders=2;vault=99":     "folders=2;vault=99", // legacy (no tag)
		"fp2|x":                  "x",                  // future tag
	}
	for in, want := range cases {
		if got := syncFingerprintBody(in); got != want {
			t.Errorf("syncFingerprintBody(%q) = %q, want %q", in, got, want)
		}
	}
	// Same content, different format tag -> equal bodies.
	if syncFingerprintBody("fp1|A") != syncFingerprintBody("fp2|A") {
		t.Fatalf("same content with different tags should have equal bodies")
	}
	// Different content -> different bodies, even with same tag.
	if syncFingerprintBody("fp1|A") == syncFingerprintBody("fp1|B") {
		t.Fatalf("different content should have different bodies")
	}
}

// A stamp from an older format (no tag) with identical content must
// be re-baselined silently, not pushed.
func TestReconcileFormatChangeReBaselines(t *testing.T) {
	dir := t.TempDir()
	stamp := filepath.Join(dir, "sync-pushed.stamp")
	// Simulate the helpers' file IO against a temp stamp by pointing
	// writeSyncStamp/syncPushedFingerprint at it is awkward (path is
	// derived from store), so test the decision logic directly.
	oldStamp := "folders=2;vault=99"  // legacy, no tag
	newFP := "fp1|folders=2;vault=99" // same content, new tag
	if syncFingerprintBody(oldStamp) != syncFingerprintBody(newFP) {
		t.Fatalf("upgrade with same content should reconcile")
	}
	_ = os.WriteFile(stamp, []byte(oldStamp), 0o600) // touch to prove temp usable
	editedFP := "fp1|folders=3;vault=99"             // a real edit
	if syncFingerprintBody(oldStamp) == syncFingerprintBody(editedFP) {
		t.Fatalf("a real edit must NOT reconcile - it should push")
	}
}
