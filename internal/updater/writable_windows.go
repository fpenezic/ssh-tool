//go:build windows

package updater

import (
	"errors"
	"fmt"
	"os"
)

// ErrPackageManaged exists for symmetry with the Unix build; Windows has
// no distro packages, so nothing ever returns it.
var ErrPackageManaged = errors.New("installed from a system package")

// checkInstallWritable probes the install directory. Windows ships no
// package manager we install through, so the only failure that matters
// is an unwritable location - typically Program Files without elevation.
func checkInstallWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".ssh-tool-update-probe-*")
	if err != nil {
		return fmt.Errorf("updater: %s is not writable, so the update "+
			"cannot be installed. Move ssh-tool.exe somewhere your user "+
			"owns, or run it as administrator: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return nil
}
