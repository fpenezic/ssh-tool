//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// registerURLScheme writes the per-user registry entries that bind
// `ssh-tool://...` URIs to this executable. Format documented at
// https://learn.microsoft.com/en-us/previous-versions/windows/internet-explorer/ie-developer/platform-apis/aa767914(v=vs.85)
//
// Layout (HKCU = HKEY_CURRENT_USER):
//
//   HKCU\Software\Classes\ssh-tool
//     (Default)         = "URL:SSH-Tool Catalog Import"
//     "URL Protocol"    = ""    (sentinel - its presence flags this
//                                key as a URL-handler root)
//     \DefaultIcon
//       (Default)       = "<exe path>,1"
//     \shell\open\command
//       (Default)       = "\"<exe path>\" \"%1\""
//
// Idempotent: overwrites existing values.
func registerURLScheme() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate exe: %w", err)
	}

	// Root key.
	root, _, err := registry.CreateKey(
		registry.CURRENT_USER, `Software\Classes\ssh-tool`, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("create root key: %w", err)
	}
	defer root.Close()
	if err := root.SetStringValue("", "URL:SSH-Tool Catalog Import"); err != nil {
		return fmt.Errorf("set root default: %w", err)
	}
	if err := root.SetStringValue("URL Protocol", ""); err != nil {
		return fmt.Errorf("set URL Protocol: %w", err)
	}

	// DefaultIcon.
	iconKey, _, err := registry.CreateKey(
		registry.CURRENT_USER, `Software\Classes\ssh-tool\DefaultIcon`, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("create DefaultIcon: %w", err)
	}
	defer iconKey.Close()
	if err := iconKey.SetStringValue("", exe+",1"); err != nil {
		return fmt.Errorf("set DefaultIcon: %w", err)
	}

	// shell\open\command - the actual launch line. %1 is replaced by
	// the full URI Windows received from the browser.
	cmdKey, _, err := registry.CreateKey(
		registry.CURRENT_USER, `Software\Classes\ssh-tool\shell\open\command`, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("create shell command key: %w", err)
	}
	defer cmdKey.Close()
	launch := fmt.Sprintf(`"%s" "%%1"`, exe)
	if err := cmdKey.SetStringValue("", launch); err != nil {
		return fmt.Errorf("set launch command: %w", err)
	}

	return nil
}

func urlSchemeStatus() string {
	k, err := registry.OpenKey(
		registry.CURRENT_USER, `Software\Classes\ssh-tool\shell\open\command`, registry.QUERY_VALUE)
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
