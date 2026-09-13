//go:build !windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPackageManaged reports that the running binary was installed by a
// distro package (deb/rpm/pacman) and must be updated through the system
// package manager. Callers show it to the user as-is.
var ErrPackageManaged = errors.New("installed from a system package")

// packageDirs are the prefixes a distro package installs into. A binary
// living under one of them is owned by dpkg/rpm/pacman even if the
// filesystem would let us write there (a user running as root, say),
// which is exactly when replacing it does the most damage: the swap
// succeeds, the package database still describes the old file, and the
// next upgrade reverts it with no warning.
var packageDirs = []string{
	"/usr/bin",
	"/usr/local/bin",
	"/usr/sbin",
	"/bin",
	"/sbin",
	"/opt",
	"/snap",
	"/var/lib/flatpak",
	"/app", // inside a flatpak sandbox
}

// checkInstallWritable refuses the in-app update when the binary is
// package-managed or its directory is not writable.
//
// The self-contained binary from the releases page lives wherever the
// user put it - ~/Downloads, ~/.local/bin, the desktop - and updates in
// place like the Windows build does. Anything under a package prefix
// belongs to the package manager instead.
func checkInstallWritable(dir string) error {
	clean := filepath.Clean(dir)
	for _, p := range packageDirs {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return fmt.Errorf("%w: update it with your package manager "+
				"(pacman -Syu, apt upgrade, dnf upgrade), or run the "+
				"standalone binary from the releases page to get in-app "+
				"updates", ErrPackageManaged)
		}
	}

	// Not a package path, so the only question left is whether we can
	// write. Probe with a real file: checking the mode bits misses
	// read-only mounts, full filesystems and ACLs.
	probe, err := os.CreateTemp(dir, ".ssh-tool-update-probe-*")
	if err != nil {
		return fmt.Errorf("updater: %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return nil
}
