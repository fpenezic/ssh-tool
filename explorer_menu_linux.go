//go:build linux && !android

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// "Open in ssh-tool" for Linux file managers. Two per-user
// registrations, no root needed:
//
//   - KDE Dolphin: a servicemenu .desktop under
//     ~/.local/share/kio/servicemenus/ (KF5.85+ location).
//   - GNOME Nautilus: an executable script under
//     ~/.local/share/nautilus/scripts/ (shows up in the Scripts
//     submenu; Nautilus has no plain user-level context-menu API).
//
// Both end up running `ssh-tool --open-dir <path>`, same entry point
// as the Windows Explorer registration.

func explorerMenuPaths() (dolphin, nautilus string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("home dir: %w", err)
	}
	dolphin = filepath.Join(home, ".local/share/kio/servicemenus/ssh-tool-open-dir.desktop")
	nautilus = filepath.Join(home, ".local/share/nautilus/scripts/Open in ssh-tool")
	return dolphin, nautilus, nil
}

func registerExplorerMenu() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate exe: %w", err)
	}
	dolphin, nautilus, err := explorerMenuPaths()
	if err != nil {
		return err
	}

	desktop := fmt.Sprintf(`[Desktop Entry]
Type=Service
MimeType=inode/directory;
Actions=openInSshTool

[Desktop Action openInSshTool]
Name=Open in ssh-tool
Icon=ssh-tool
Exec="%s" --open-dir %%f
`, exe)
	if err := os.MkdirAll(filepath.Dir(dolphin), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dolphin, []byte(desktop), 0o644); err != nil {
		return fmt.Errorf("dolphin servicemenu: %w", err)
	}

	// Nautilus hands the selection via env; fall back to the script's
	// cwd (right-click on the folder background).
	script := fmt.Sprintf(`#!/bin/sh
# Installed by ssh-tool (Settings -> Integration). Opens the selected
# directory as a local shell tab in ssh-tool.
dir="$(printf '%%s' "$NAUTILUS_SCRIPT_SELECTED_FILE_PATHS" | head -n1)"
[ -z "$dir" ] && dir="$PWD"
exec "%s" --open-dir "$dir"
`, exe)
	if err := os.MkdirAll(filepath.Dir(nautilus), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(nautilus, []byte(script), 0o755); err != nil {
		return fmt.Errorf("nautilus script: %w", err)
	}
	return nil
}

func unregisterExplorerMenu() error {
	dolphin, nautilus, err := explorerMenuPaths()
	if err != nil {
		return err
	}
	_ = os.Remove(dolphin)
	_ = os.Remove(nautilus)
	return nil
}

// explorerMenuStatus reports which integrations are installed, or ""
// when neither file exists.
func explorerMenuStatus() string {
	dolphin, nautilus, err := explorerMenuPaths()
	if err != nil {
		return ""
	}
	out := ""
	if _, err := os.Stat(dolphin); err == nil {
		out = "Dolphin servicemenu"
	}
	if _, err := os.Stat(nautilus); err == nil {
		if out != "" {
			out += " + "
		}
		out += "Nautilus script"
	}
	return out
}
