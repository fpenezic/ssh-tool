//go:build !windows

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A binary installed by a distro package must refuse the in-app update
// even when the process could write the file - running as root is the
// case where an in-place swap silently desynchronises the package
// database.
func TestCheckInstallWritableRejectsPackagePaths(t *testing.T) {
	for _, dir := range []string{
		"/usr/bin",
		"/usr/local/bin",
		"/usr/bin/",
		"/opt/ssh-tool",
		"/snap/ssh-tool/current/bin",
		"/app/bin", // flatpak sandbox
	} {
		err := checkInstallWritable(dir)
		if err == nil {
			t.Errorf("%s: expected a refusal, got nil", dir)
			continue
		}
		if !errors.Is(err, ErrPackageManaged) {
			t.Errorf("%s: error should wrap ErrPackageManaged, got %v", dir, err)
		}
	}
}

// The standalone binary is the common case: downloaded, chmod +x, run
// from wherever the user keeps it. That must keep updating in place.
func TestCheckInstallWritableAllowsUserDirs(t *testing.T) {
	dir := t.TempDir()
	if err := checkInstallWritable(dir); err != nil {
		t.Fatalf("a user-owned directory should be updatable: %v", err)
	}
	// The probe must not survive; a stray dotfile next to the binary
	// would be reported as a bug by someone eventually.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe file left behind: %v", entries)
	}
}

// A path that merely starts with the same letters as a package prefix is
// not a package path. /usr/binaries is not /usr/bin.
func TestCheckInstallWritableDoesNotMatchPrefixByString(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "usr", "binaries")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkInstallWritable(dir); err != nil {
		t.Errorf("%s should not be treated as a package path: %v", dir, err)
	}
}

func TestCheckInstallWritableRejectsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if err := checkInstallWritable(dir); err == nil {
		t.Error("a read-only directory should fail the probe")
	}
}
