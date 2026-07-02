//go:build !windows

package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// validateAgentSocket guards against pointing the user's SSH agent at
// a hostile UNIX socket on the same machine: any process can drop a
// socket on the filesystem, and dialling it as an "agent" would
// happily ask it to sign challenges with whatever key the malicious
// peer claims to expose.
//
// We require:
//   - the path resolves to a UNIX socket (Mode&ModeSocket != 0);
//   - the socket is owned by the current user (uid match);
//   - the parent directory is owned by the current user and has
//     permissions no looser than 0700 - otherwise another local user
//     could plant a replacement under the right name.
//
// SSH_AUTH_SOCK populated by the OS itself (ssh-agent, gnome-keyring,
// 1Password agent…) lives in $XDG_RUNTIME_DIR which already meets
// these constraints; the checks only fire on misuse or attack.
//
// Returns a descriptive error; callers refuse to dial when this
// returns non-nil. No-op on Windows (named-pipe ACLs handle the
// equivalent there; see agent_validate_windows.go).
func validateAgentSocket(path string) error {
	if path == "" {
		return fmt.Errorf("empty socket path")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat agent socket %s: %w", path, err)
	}
	// Reject symlinks - a symlink to a hostile socket would pass the
	// downstream socket check after one extra hop. Real agent sockets
	// are bind()ed regular socket inodes, not links.
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("agent socket %s is a symlink; refusing", path)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("agent path %s is not a socket (mode=%s)", path, fi.Mode())
	}

	uid := os.Getuid()
	if sysStat, ok := fi.Sys().(*syscall.Stat_t); ok {
		if int(sysStat.Uid) != uid {
			return fmt.Errorf("agent socket %s owned by uid %d, expected %d", path, sysStat.Uid, uid)
		}
	}

	parent := filepath.Dir(path)
	pfi, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("stat agent socket parent %s: %w", parent, err)
	}
	if !pfi.IsDir() {
		return fmt.Errorf("agent socket parent %s is not a directory", parent)
	}
	if pSysStat, ok := pfi.Sys().(*syscall.Stat_t); ok {
		if int(pSysStat.Uid) != uid {
			return fmt.Errorf("agent socket parent %s owned by uid %d, expected %d", parent, pSysStat.Uid, uid)
		}
	}
	// Parent perms must not allow group/other write or even
	// traversal - anyone with +x on the parent could replace the
	// socket between our stat and dial. 0700 / 0500 are fine; 0755
	// is not.
	if mode := pfi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("agent socket parent %s has loose perms %o (need 0700 or stricter)", parent, mode)
	}
	return nil
}
