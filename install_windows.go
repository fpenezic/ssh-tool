//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallState describes how the running .exe is installed, so the UI
// can offer to finish the job. The Linux file of the same name carries
// the full commentary; this is the Windows half.
//
// Windows has no ~/.local/bin convention and no package manager to
// defer to, so the states are simpler:
//
//   - "user": the exe lives in the per-user program directory
//     (%LOCALAPPDATA%\Programs\ssh-tool) and has a Start Menu shortcut.
//   - "loose": it is running from Downloads, the Desktop, a USB stick.
//     Works, but it is not in the Start Menu, so there is nothing to
//     pin and nothing to search for.
//
// There is deliberately no "package" state: nothing here is owned by a
// package manager, so the in-app updater stays in charge throughout.
type InstallState struct {
	Kind             string `json:"kind"`
	ExePath          string `json:"exe_path"`
	TargetPath       string `json:"target_path"`
	CanOffer         bool   `json:"can_offer"`
	DesktopEntry     bool   `json:"desktop_entry"`
	InstalledVersion string `json:"installed_version"`
	Replaces         bool   `json:"replaces"`
}

// userProgramDir is where a per-user install belongs on Windows.
// %LOCALAPPDATA%\Programs is what modern installers use when they do not
// ask for admin, and the user owns it - which keeps the in-app updater
// working, unlike Program Files.
func userProgramDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "Programs", "ssh-tool")
}

// startMenuShortcutPath is the per-user Start Menu entry.
func startMenuShortcutPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu",
		"Programs", "ssh-tool.lnk")
}

func (a *App) GetInstallState() InstallState {
	exePath, err := os.Executable()
	if err != nil {
		return InstallState{Kind: "loose"}
	}
	return installStateFor(exePath)
}

// installStateFor is GetInstallState with the executable path injected,
// so tests can describe a layout instead of being run from one.
func installStateFor(exePath string) InstallState {
	st := InstallState{Kind: "loose"}

	// A development build never offers to install itself. It would
	// otherwise offer to replace the user's working copy with a build
	// from their own tree, which is one mis-click away from losing the
	// version they actually use.
	if isDevBuild() {
		st.Kind = "dev"
		if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = resolved
		}
		st.ExePath = exePath
		return st
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	st.ExePath = exePath

	if lnk := startMenuShortcutPath(); lnk != "" {
		if _, err := os.Stat(lnk); err == nil {
			st.DesktopEntry = true
		}
	}

	progDir := userProgramDir()
	if progDir == "" {
		return st
	}
	target := filepath.Join(progDir, "ssh-tool.exe")

	// Already installed: the only thing that could still be missing is
	// the Start Menu shortcut.
	if strings.EqualFold(filepath.Clean(exePath), filepath.Clean(target)) {
		st.Kind = "user"
		st.TargetPath = exePath
		st.CanOffer = !st.DesktopEntry
		return st
	}

	st.TargetPath = target
	st.CanOffer = true
	if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
		st.Replaces = true
		st.InstalledVersion = binaryVersion(target)
	}
	return st
}

// binaryVersion asks another ssh-tool.exe what version it is. See the
// Linux twin for why this is a flag rather than a scan of the file.
func binaryVersion(path string) string {
	return runPrintVersion(path)
}

// InstallToUserPrefix copies the running exe into the per-user program
// directory and creates a Start Menu shortcut. No admin, and the exe
// stays user-owned so in-app updates keep working.
func (a *App) InstallToUserPrefix() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running binary: %w", err)
	}
	return installFrom(exePath)
}

func installFrom(exePath string) (string, error) {
	// See the Linux twin: a development build must not replace the
	// user's working copy, and the UI check alone is not enough.
	if isDevBuild() {
		return "", fmt.Errorf("this is a development build (%s); install a release instead", appVersion)
	}
	progDir := userProgramDir()
	if progDir == "" {
		return "", fmt.Errorf("cannot locate %%LOCALAPPDATA%%")
	}
	if err := os.MkdirAll(progDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", progDir, err)
	}
	target := filepath.Join(progDir, "ssh-tool.exe")

	if !strings.EqualFold(filepath.Clean(exePath), filepath.Clean(target)) {
		if err := copyExecutable(exePath, target); err != nil {
			return "", err
		}
	}
	if err := createStartMenuShortcut(target); err != nil {
		// The copy succeeded; report the shortcut failure without
		// pretending the whole thing failed.
		return target, fmt.Errorf("installed to %s, but the Start Menu shortcut failed: %w", target, err)
	}
	return target, nil
}

// copyExecutable writes src to dst via a temp file in the destination
// directory, so a failure part-way through cannot leave a truncated exe
// where a working one used to be.
//
// Unlike Unix, Windows will not let us replace a RUNNING exe - but this
// only ever writes to the install target, and the running process is the
// one being copied FROM, so the case does not arise. A stale target from
// a previous run is replaced by renaming it aside first.
func copyExecutable(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// Rename over an existing file fails on Windows, so move the old one
	// out of the way first. It is deleted on the next install if the
	// file is no longer locked.
	if _, err := os.Stat(dst); err == nil {
		old := dst + ".old"
		_ = os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("replace %s (is it running?): %w", dst, err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install to %s: %w", dst, err)
	}
	return nil
}

// RelaunchFromInstall restarts the app from the freshly installed copy.
func (a *App) RelaunchFromInstall(path string) error {
	if path == "" {
		return fmt.Errorf("no installed path to restart from")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("installed binary is missing: %w", err)
	}
	return a.relaunchAs(path)
}
