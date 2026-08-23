package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// PendingStaged is the gate that decides whether a launch swaps a binary
// in, so the cases that must NOT pass it matter more than the happy path:
// a manifest pointing at a file whose contents changed after it was hashed
// is exactly the corrupt-download scenario, and swapping it over a working
// exe would leave the user with nothing that runs.
func TestPendingStagedRejectsDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, stagedManifestName)
	stagedPath := filepath.Join(dir, "ssh-tool.exe.new")
	scriptPath := filepath.Join(dir, "ssh-tool-apply-update.cmd")
	if err := os.WriteFile(stagedPath, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("@echo off"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A digest that cannot match the bytes above.
	if err := writeStagedManifestAt(manifest, StagedUpdate{
		Version:     "v9.9.9",
		StagedPath:  stagedPath,
		ApplyScript: scriptPath,
		SHA256:      "00",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := pendingStagedAt(manifest); ok {
		t.Fatal("PendingStaged accepted a staged file whose digest does not match the manifest")
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Error("a rejected staged binary should be deleted, not left for the next launch to re-check")
	}
}

// A manifest that survived but lost its apply script (partial cleanup, a
// disk-space failure mid-write) must not be treated as pending: Apply would
// fail on the missing path and the user would see an error on every launch.
func TestPendingStagedRejectsMissingScript(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, stagedManifestName)
	stagedPath := filepath.Join(dir, "ssh-tool.exe.new")
	if err := os.WriteFile(stagedPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStagedManifestAt(manifest, StagedUpdate{
		Version:     "v9.9.9",
		StagedPath:  stagedPath,
		ApplyScript: filepath.Join(dir, "does-not-exist.cmd"),
		SHA256:      sum,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := pendingStagedAt(manifest); ok {
		t.Fatal("PendingStaged accepted a manifest whose apply script is gone")
	}
}

// The happy path: a manifest whose file still hashes to the recorded digest
// is what a clean "downloaded but never restarted" run leaves behind.
func TestPendingStagedAcceptsIntactDownload(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, stagedManifestName)
	stagedPath := filepath.Join(dir, "ssh-tool.exe.new")
	scriptPath := filepath.Join(dir, "ssh-tool-apply-update.cmd")
	if err := os.WriteFile(stagedPath, []byte("a new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("@echo off"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	want := StagedUpdate{
		Version:     "v9.9.9",
		StagedPath:  stagedPath,
		ApplyScript: scriptPath,
		SHA256:      sum,
		Size:        12,
	}
	if err := writeStagedManifestAt(manifest, want); err != nil {
		t.Fatal(err)
	}
	got, ok := pendingStagedAt(manifest)
	if !ok {
		t.Fatal("PendingStaged rejected an intact staged download")
	}
	if got.Version != want.Version || got.ApplyScript != want.ApplyScript {
		t.Errorf("round-trip lost fields: got %+v want %+v", got, want)
	}
	clearStagedAt(manifest)
	if _, ok := pendingStagedAt(manifest); ok {
		t.Error("ClearStaged left the manifest behind")
	}
}
