//go:build linux && !android

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// registerURLScheme writes a per-user .desktop file under
// ~/.local/share/applications and runs `xdg-mime default` to
// associate the ssh-tool scheme with it.
//
// freedesktop spec:
//   https://wiki.archlinux.org/title/Default_applications#Registering_a_new_URL_scheme
//
// The .desktop file's Exec line uses %u so xdg-open passes the
// full URI to the binary; our argv parser already handles it.
//
// Idempotent: overwrites the existing file.
func registerURLScheme() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate exe: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir applications dir: %w", err)
	}
	desktopPath := filepath.Join(dir, "ssh-tool-url.desktop")

	body := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=ssh-tool (URL handler)
Comment=Handle ssh-tool:// deep links
Exec=%q %%u
Terminal=false
NoDisplay=true
MimeType=x-scheme-handler/ssh-tool;
Categories=Network;
`, exe)
	if err := os.WriteFile(desktopPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write .desktop: %w", err)
	}

	// Register MIME → desktop file. update-desktop-database refreshes
	// the cache (best-effort; xdg-mime works without it on most distros).
	_ = exec.Command("update-desktop-database", dir).Run()
	if err := exec.Command("xdg-mime", "default",
		"ssh-tool-url.desktop", "x-scheme-handler/ssh-tool").Run(); err != nil {
		return fmt.Errorf("xdg-mime default: %w (is xdg-utils installed?)", err)
	}
	return nil
}

func urlSchemeStatus() string {
	out, err := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/ssh-tool").Output()
	if err != nil {
		return ""
	}
	return string(out)
}
