//go:build linux && !android

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstallState describes how the running binary is installed, so the UI
// can offer to finish the job.
//
// The three states a Linux user can be in:
//
//   - a distro package: /usr/bin, owned by pacman/apt/dnf. Nothing to
//     offer; updates come from the package manager.
//   - installed in the user's own prefix (~/.local/bin) with a desktop
//     entry: the good state for the standalone binary.
//   - run from wherever it was downloaded: works, but there is no menu
//     entry and no icon, which is what makes it look unfinished.
//
// Only the third state produces an offer.
type InstallState struct {
	// Kind is "package", "user", or "loose".
	Kind string `json:"kind"`
	// ExePath is where the running binary actually lives.
	ExePath string `json:"exe_path"`
	// TargetPath is where an install would put it (empty unless loose).
	TargetPath string `json:"target_path"`
	// CanOffer is true when installing is possible and worth suggesting.
	CanOffer bool `json:"can_offer"`
	// DesktopEntry is true when a launcher entry already exists.
	DesktopEntry bool `json:"desktop_entry"`
}

// appIconSVG is installed alongside the PNG. Icon themes prefer the
// scalable entry, which keeps the launcher sharp at whatever size the
// desktop asks for - a 128px PNG upscaled to a 256px dock looks soft.
//
//go:embed build/appicon.svg
var appIconSVG []byte

// userBinDir is fixed by the XDG user-dirs spec, unlike the data root.
func userBinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

func userDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// desktopFileName must match the Wayland app-id, not our binary name.
// Wails builds its GtkApplication id as "org.wails.<Name>" (hardcoded
// in linux_cgo.go), GTK4 hands that to the compositor as the window
// app-id, and KWin/GNOME then look for "<app-id>.desktop". A file named
// ssh-tool.desktop is found by the launcher (which matches on Exec and
// Name) but never by the window, which is why the menu entry showed our
// icon while the window and tray kept the generic Wails one.
const desktopFileName = "org.wails.ssh-tool.desktop"

func desktopEntryPath() string {
	d := userDataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "applications", desktopFileName)
}

// GetInstallState reports where the binary lives. Called once by the
// frontend on startup.
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

	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	st.ExePath = exePath

	if entry := desktopEntryPath(); entry != "" {
		if _, err := os.Stat(entry); err == nil {
			st.DesktopEntry = true
		}
	}

	dir := filepath.Dir(exePath)
	if isPackagePath(dir) {
		st.Kind = "package"
		return st
	}

	binDir := userBinDir()
	if binDir != "" && dir == binDir {
		st.Kind = "user"
		// Already in the right place; the only thing that could still
		// be missing is the desktop entry, which is worth offering.
		st.CanOffer = !st.DesktopEntry
		st.TargetPath = exePath
		return st
	}

	// Loose: running from Downloads, the desktop, a USB stick.
	if binDir == "" {
		return st
	}
	st.TargetPath = filepath.Join(binDir, "ssh-tool")
	st.CanOffer = true
	return st
}

// isPackagePath mirrors the updater's list. Kept separate rather than
// exported from there because the two answer different questions: the
// updater asks "may I overwrite this", we ask "should I offer to
// install". They happen to use the same prefixes today.
func isPackagePath(dir string) bool {
	clean := filepath.Clean(dir)
	for _, p := range []string{
		"/usr/bin", "/usr/local/bin", "/usr/sbin", "/bin", "/sbin",
		"/opt", "/snap", "/var/lib/flatpak", "/app",
	} {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return true
		}
	}
	return false
}

// RelaunchFromInstall restarts the app from the freshly installed copy.
//
// Separate from AppRelaunch because the path differs: the running
// process came from ~/Downloads, and restarting that would leave the
// window bound to a binary with no desktop entry, which is the problem
// the install just solved. Called after the user accepts the restart.
func (a *App) RelaunchFromInstall(path string) error {
	if path == "" {
		return fmt.Errorf("no installed path to restart from")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("installed binary is missing: %w", err)
	}
	return a.relaunchAs(path)
}

// InstallToUserPrefix copies the running binary into ~/.local/bin and
// writes the desktop entry and icons, the same layout
// build/linux/install-user.sh produces.
//
// Everything lands under $HOME, so this never needs root, and because
// the user owns the result the in-app updater keeps working - which is
// the whole reason to prefer this over a distro package.
//
// Returns the path the binary was installed to.
func (a *App) InstallToUserPrefix() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running binary: %w", err)
	}
	return installFrom(exePath)
}

// installFrom is InstallToUserPrefix with the source path injected, so
// tests can drive a real install against a throwaway HOME.
func installFrom(exePath string) (string, error) {
	st := installStateFor(exePath)
	if st.Kind == "package" {
		return "", fmt.Errorf("this build was installed by your package manager; nothing to do")
	}

	binDir := userBinDir()
	dataDir := userDataDir()
	if binDir == "" || dataDir == "" {
		return "", fmt.Errorf("cannot locate your home directory")
	}

	appDir := filepath.Join(dataDir, "applications")
	iconDir := filepath.Join(dataDir, "icons", "hicolor")
	for _, d := range []string{
		binDir, appDir,
		filepath.Join(iconDir, "128x128", "apps"),
		filepath.Join(iconDir, "scalable", "apps"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", d, err)
		}
	}

	target := filepath.Join(binDir, "ssh-tool")

	// Copy unless we are already the file in question (the "user" state,
	// where only the desktop entry is missing).
	if st.ExePath != target {
		if err := copyExecutable(st.ExePath, target); err != nil {
			return "", err
		}
	}

	// Exec must be absolute: a desktop entry launched by the session
	// does not reliably inherit a PATH containing ~/.local/bin.
	//
	// StartupWMClass has to match the ProgramName set in buildApp, or
	// the window will not bind to this entry and GTK falls back to the
	// generic Wails icon.
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=ssh-tool
GenericName=SSH connection manager
Comment=Cross-platform SSH connection manager
Exec=%s %%U
Icon=org.wails.ssh-tool
Categories=Network;Development;RemoteAccess;
Terminal=false
Keywords=ssh;terminal;sftp;tunnel;wireguard;
Version=1.0
StartupNotify=true
StartupWMClass=ssh-tool
MimeType=x-scheme-handler/ssh-tool;
`, target)

	if err := os.WriteFile(desktopEntryPath(), []byte(entry), 0o644); err != nil {
		return "", fmt.Errorf("write desktop entry: %w", err)
	}

	// Icons come from the embedded assets, so this works no matter what
	// the user kept next to the downloaded binary.
	// appIcon is the same embedded PNG the window uses, so the launcher
	// icon cannot drift from the one in the title bar.
	if len(appIcon) > 0 {
		_ = os.WriteFile(filepath.Join(iconDir, "128x128", "apps", "org.wails.ssh-tool.png"), appIcon, 0o644)
	}
	if len(appIconSVG) > 0 {
		_ = os.WriteFile(filepath.Join(iconDir, "scalable", "apps", "org.wails.ssh-tool.svg"), appIconSVG, 0o644)
	}

	// Earlier builds wrote ssh-tool.desktop (and ssh-tool.png/svg), which
	// the compositor never matched to a window. Left behind they show up
	// as a second, identical entry in the launcher, so clear them out.
	for _, stale := range []string{
		filepath.Join(appDir, "ssh-tool.desktop"),
		filepath.Join(iconDir, "128x128", "apps", "ssh-tool.png"),
		filepath.Join(iconDir, "scalable", "apps", "ssh-tool.svg"),
	} {
		_ = os.Remove(stale)
	}

	refreshDesktopCaches(appDir, iconDir)
	return target, nil
}

// refreshDesktopCaches tells the running session about the new entry.
// Without the icon cache refresh the .desktop resolves Icon=ssh-tool to
// nothing until the next login, which looks exactly like the bug we are
// fixing. Both are best-effort: the files are already on disk and will
// be picked up eventually regardless.
func refreshDesktopCaches(appDir, iconDir string) {
	if bin, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(bin, "-q", appDir).Run()
	}
	if bin, err := exec.LookPath("gtk-update-icon-cache"); err == nil {
		_ = exec.Command(bin, "-q", "-t", "-f", iconDir).Run()
	}
}

// copyExecutable writes src to dst via a temp file in the destination
// directory, so a failure part-way through cannot leave a truncated
// binary where a working one used to be.
func copyExecutable(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install to %s: %w", dst, err)
	}
	return nil
}
