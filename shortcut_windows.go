//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// createStartMenuShortcut writes a per-user Start Menu .lnk pointing at
// the installed exe.
//
// A .lnk is a COM-serialised structure, not a text file, so it cannot be
// written by hand. The shell's own IShellLink does it - see
// shortcut_com_windows.go for the binding and for why this no longer
// shells out to PowerShell (short version: the PowerShell spawn made
// Defender classify the release binary as Trojan:Win32/Sabsik.FL.A!ml).
//
// Per-user (%APPDATA%) rather than all-users (%PROGRAMDATA%) on purpose:
// the all-users Start Menu needs admin, and the whole point of this
// install path is that it does not.
func createStartMenuShortcut(target string) error {
	lnk := startMenuShortcutPath()
	if lnk == "" {
		return fmt.Errorf("cannot locate %%APPDATA%%")
	}
	if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
		return fmt.Errorf("create Start Menu directory: %w", err)
	}

	// COM apartments are per thread, and the interface pointers are only
	// valid on the thread that created them. Pin this goroutine for the
	// duration so the initialise, the calls and the uninitialise all land
	// on the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := createShortcutCOM(lnk, target, filepath.Dir(target), "SSH connection manager"); err != nil {
		return fmt.Errorf("create shortcut: %w", err)
	}

	// Confirm the file is actually on disk. The COM path reports failure
	// properly, unlike the PowerShell one it replaced, but the check is
	// cheap and it is what the caller's contract promises.
	fi, err := os.Stat(lnk)
	if err != nil {
		return fmt.Errorf("shortcut was not created at %s", lnk)
	}
	if fi.Size() == 0 {
		os.Remove(lnk)
		return fmt.Errorf("shortcut at %s was created empty", lnk)
	}
	return nil
}

// removeStartMenuShortcut deletes the entry again. Unused today - kept
// beside its twin so an uninstall path has something to call.
func removeStartMenuShortcut() error {
	lnk := startMenuShortcutPath()
	if lnk == "" {
		return nil
	}
	if err := os.Remove(lnk); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
