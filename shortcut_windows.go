//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// createStartMenuShortcut writes a per-user Start Menu .lnk pointing at
// the installed exe.
//
// A .lnk is a COM-serialised structure, not a text file, so it cannot be
// written directly without pulling in an OLE binding. PowerShell's
// WScript.Shell wrapper is on every supported Windows, needs no admin,
// and is what installers use for exactly this. The cost is spawning a
// process once per install, which is fine for something the user
// triggers by hand.
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

	// Paths go through the argument list rather than into the script
	// text: a directory name containing a quote or a backtick would
	// otherwise break the script, and %LOCALAPPDATA% is user-controlled.
	script := `
param([string]$LnkPath, [string]$TargetPath, [string]$WorkDir)
$shell = New-Object -ComObject WScript.Shell
$sc = $shell.CreateShortcut($LnkPath)
$sc.TargetPath = $TargetPath
$sc.WorkingDirectory = $WorkDir
$sc.Description = "SSH connection manager"
$sc.Save()
`
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script,
		"-LnkPath", lnk,
		"-TargetPath", target,
		"-WorkDir", filepath.Dir(target),
	)
	// Without this the shortcut creation flashes a conhost window over
	// the GUI for the moment PowerShell runs.
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("create shortcut: %s", msg)
	}
	if _, err := os.Stat(lnk); err != nil {
		return fmt.Errorf("shortcut was not created at %s", lnk)
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
