//go:build linux && !android

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// registerURLScheme writes a per-user .desktop file under
// ~/.local/share/applications and runs `xdg-mime default` to
// associate the ssh-tool scheme with it.
//
// freedesktop spec:
//
//	https://wiki.archlinux.org/title/Default_applications#Registering_a_new_URL_scheme
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
	return registerURLSchemeAt(exe)
}

// registerURLSchemeAt registers a specific binary, rather than whichever
// one is running. Used after an install moves the executable: the
// handler has to point at the new location, not at the copy that
// happens to be executing the install.
func registerURLSchemeAt(exe string) error {
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

// urlSchemeTarget reads back the executable the handler actually
// launches, by parsing Exec= out of the .desktop file xdg-mime names.
//
// Returns "" when it cannot be determined - another application owns
// the scheme, the file is gone, the line is shaped unexpectedly. The
// caller reports that as "cannot tell" rather than as broken.
func urlSchemeTarget() string {
	name := strings.TrimSpace(urlSchemeStatus())
	if name == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Search the user directory first, then the system ones, in the
	// order XDG resolves them.
	dirs := []string{filepath.Join(home, ".local", "share", "applications")}
	if xdg := os.Getenv("XDG_DATA_DIRS"); xdg != "" {
		for _, d := range strings.Split(xdg, ":") {
			if d != "" {
				dirs = append(dirs, filepath.Join(d, "applications"))
			}
		}
	} else {
		dirs = append(dirs,
			"/usr/local/share/applications",
			"/usr/share/applications")
	}

	for _, d := range dirs {
		body, err := os.ReadFile(filepath.Join(d, name))
		if err != nil {
			continue
		}
		return execTargetFromDesktopEntry(string(body))
	}
	return ""
}

// execTargetFromDesktopEntry pulls the program out of an Exec= line.
//
// The value may be quoted (we write it with %q so a path with spaces
// survives) and carries field codes like %u after it, so this is not a
// plain split on whitespace.
func execTargetFromDesktopEntry(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Exec=") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "Exec="))
		if v == "" {
			return ""
		}
		if v[0] == '"' {
			// Quoted: take up to the closing quote, honouring \" escapes.
			var out []rune
			escaped := false
			for _, r := range v[1:] {
				switch {
				case escaped:
					out = append(out, r)
					escaped = false
				case r == '\\':
					escaped = true
				case r == '"':
					return string(out)
				default:
					out = append(out, r)
				}
			}
			return string(out)
		}
		// Unquoted: up to the first space.
		if i := strings.IndexByte(v, ' '); i >= 0 {
			return v[:i]
		}
		return v
	}
	return ""
}
