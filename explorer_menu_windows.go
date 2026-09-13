//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// "Open in ssh-tool" in the Explorer right-click menu. Two per-user
// registrations (HKCU, no admin needed):
//
//	HKCU\Software\Classes\Directory\shell\ssh-tool             right-click ON a folder (%1)
//	HKCU\Software\Classes\Directory\Background\shell\ssh-tool  right-click INSIDE a folder (%V)
//
// Both launch `"exe" --open-dir "<path>"`; the single-instance
// handoff forwards it to a running ssh-tool, which opens the default
// local shell cd'd into the directory.

const explorerMenuLabel = "Open in ssh-tool"

func explorerMenuKeys() []struct{ base, pathVar string } {
	return []struct{ base, pathVar string }{
		{`Software\Classes\Directory\shell\ssh-tool`, `%1`},
		{`Software\Classes\Directory\Background\shell\ssh-tool`, `%V`},
	}
}

func registerExplorerMenu() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate exe: %w", err)
	}
	return registerExplorerMenuAt(exe)
}

// registerExplorerMenuAt registers a specific binary, for use after an
// install moves the executable.
func registerExplorerMenuAt(exe string) error {
	for _, e := range explorerMenuKeys() {
		root, _, err := registry.CreateKey(registry.CURRENT_USER, e.base, registry.ALL_ACCESS)
		if err != nil {
			return fmt.Errorf("create %s: %w", e.base, err)
		}
		if err := root.SetStringValue("", explorerMenuLabel); err != nil {
			root.Close()
			return fmt.Errorf("set label: %w", err)
		}
		// Explorer renders the exe's first icon group next to the entry.
		if err := root.SetStringValue("Icon", exe+",0"); err != nil {
			root.Close()
			return fmt.Errorf("set icon: %w", err)
		}
		root.Close()

		cmd, _, err := registry.CreateKey(registry.CURRENT_USER, e.base+`\command`, registry.ALL_ACCESS)
		if err != nil {
			return fmt.Errorf("create %s\\command: %w", e.base, err)
		}
		launch := fmt.Sprintf(`"%s" --open-dir "%s"`, exe, e.pathVar)
		if err := cmd.SetStringValue("", launch); err != nil {
			cmd.Close()
			return fmt.Errorf("set command: %w", err)
		}
		cmd.Close()
	}
	return nil
}

func unregisterExplorerMenu() error {
	for _, e := range explorerMenuKeys() {
		// Delete leaf-first; ignore "not exist" so unregister is
		// idempotent.
		_ = registry.DeleteKey(registry.CURRENT_USER, e.base+`\command`)
		_ = registry.DeleteKey(registry.CURRENT_USER, e.base)
	}
	return nil
}

// explorerMenuStatus returns the registered launch command, or ""
// when the menu entry is absent. The UI shows it verbatim so the
// user can spot a stale path after moving the exe.
func explorerMenuStatus() string {
	k, err := registry.OpenKey(
		registry.CURRENT_USER, `Software\Classes\Directory\shell\ssh-tool\command`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	return v
}

// explorerMenuTarget pulls the executable out of the registered command
// line, which is written as `"<exe>" --open-dir "%1"`.
//
// Returns "" when it cannot be parsed, which the caller reports as
// "cannot tell" rather than as broken.
func explorerMenuTarget() string {
	return commandLineProgram(explorerMenuStatus())
}

// commandLineProgram takes the program out of a Windows command line.
// The path is quoted (it routinely contains spaces - Program Files,
// a username with a space), so this is not a split on whitespace.
func commandLineProgram(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if cmd[0] == '"' {
		if i := strings.IndexByte(cmd[1:], '"'); i >= 0 {
			return cmd[1 : 1+i]
		}
		return ""
	}
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i]
	}
	return cmd
}
