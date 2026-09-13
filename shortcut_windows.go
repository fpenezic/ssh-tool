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
	//
	// The script has to be a FILE, invoked with -File. A param() block
	// is only honoured for a script; with -Command the named arguments
	// are parsed as further commands, so -LnkPath became "the term
	// '-LnkPath' is not recognized" and every $LnkPath below it was
	// empty - CreateShortcut("") then failed with "the shortcut pathname
	// must end with .lnk or .url".
	script := `param([string]$LnkPath, [string]$TargetPath, [string]$WorkDir)
$ErrorActionPreference = "Stop"
$shell = New-Object -ComObject WScript.Shell
$sc = $shell.CreateShortcut($LnkPath)
$sc.TargetPath = $TargetPath
$sc.WorkingDirectory = $WorkDir
$sc.Description = "SSH connection manager"
$sc.Save()
`
	scriptFile, err := os.CreateTemp("", "ssh-tool-shortcut-*.ps1")
	if err != nil {
		return fmt.Errorf("write shortcut script: %w", err)
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(script); err != nil {
		scriptFile.Close()
		return fmt.Errorf("write shortcut script: %w", err)
	}
	if err := scriptFile.Close(); err != nil {
		return fmt.Errorf("write shortcut script: %w", err)
	}

	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
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
	// PowerShell exits 0 for a script that printed errors unless
	// $ErrorActionPreference stops it, and an empty .lnk path used to
	// fail this way - so confirm the file is actually there rather than
	// trusting the exit status.
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
