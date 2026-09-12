//go:build linux && !android

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsPackagePath(t *testing.T) {
	pkg := []string{
		"/usr/bin", "/usr/bin/", "/usr/local/bin", "/opt/ssh-tool",
		"/snap/ssh-tool/current/bin", "/app/bin", "/bin", "/sbin",
	}
	for _, d := range pkg {
		if !isPackagePath(d) {
			t.Errorf("%s should be recognised as a package path", d)
		}
	}

	notPkg := []string{
		"/home/user/.local/bin",
		"/home/user/Downloads",
		"/home/user/Desktop",
		"/usr/binaries", // not /usr/bin
		"/opting",       // not /opt
		"/tmp",
	}
	for _, d := range notPkg {
		if isPackagePath(d) {
			t.Errorf("%s must not be treated as a package path", d)
		}
	}
}

// A binary sitting in Downloads is the case the offer exists for.
func TestGetInstallStateLoose(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dl, "ssh-tool-linux-amd64")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if st.Kind != "loose" {
		t.Errorf("Kind = %q, want loose", st.Kind)
	}
	if !st.CanOffer {
		t.Error("a loose binary should produce an offer")
	}
	if want := filepath.Join(home, ".local", "bin", "ssh-tool"); st.TargetPath != want {
		t.Errorf("TargetPath = %q, want %q", st.TargetPath, want)
	}
}

// Installed by a distro package: never offer, the package manager owns it.
func TestGetInstallStatePackage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := installStateFor("/usr/bin/ssh-tool")
	if st.Kind != "package" {
		t.Errorf("Kind = %q, want package", st.Kind)
	}
	if st.CanOffer {
		t.Error("a packaged install must never offer to install itself")
	}
}

// Already in ~/.local/bin with a desktop entry: the finished state, so
// there is nothing left to offer.
func TestGetInstallStateUserComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	binDir := filepath.Join(home, ".local", "bin")
	appDir := filepath.Join(home, ".local", "share", "applications")
	for _, d := range []string{binDir, appDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(binDir, "ssh-tool")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, desktopFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if st.Kind != "user" {
		t.Errorf("Kind = %q, want user", st.Kind)
	}
	if !st.DesktopEntry {
		t.Error("the existing desktop entry should be detected")
	}
	if st.CanOffer {
		t.Error("a complete user install has nothing to offer")
	}
}

// In the right directory but with no launcher entry - offer to finish
// the job rather than staying silent.
func TestGetInstallStateUserMissingEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "ssh-tool")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if st.Kind != "user" {
		t.Errorf("Kind = %q, want user", st.Kind)
	}
	if !st.CanOffer {
		t.Error("a missing desktop entry is worth offering to create")
	}
}

// XDG_DATA_HOME has to be honoured, or the entry is written where the
// session is not looking.
// The desktop entry has to be named after the Wayland app-id, which
// Wails derives from the application Name as "org.wails.<Name>". Get
// this wrong and the launcher still shows the icon (it matches on Exec
// and Name) while the window and tray fall back to the generic Wails
// one - which is exactly the bug this guards against, and it is
// invisible on X11.
func TestDesktopFileNameMatchesWailsAppID(t *testing.T) {
	const wailsAppID = "org.wails." + "ssh-tool" // appName, as passed to application.New
	if want := wailsAppID + ".desktop"; desktopFileName != want {
		t.Errorf("desktopFileName = %q, want %q - the compositor looks up "+
			"<app-id>.desktop and finds nothing otherwise", desktopFileName, want)
	}
}

// Icon= must resolve in the icon theme under the same app-id name: the
// compositor falls back to looking the app-id up directly.
func TestInstalledEntryUsesAppIDIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	exe := filepath.Join(home, "dl-ssh-tool")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := installFrom(exe); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(desktopEntryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(body), "Icon=org.wails.ssh-tool\n") {
		t.Errorf("Icon= should name the app-id:\n%s", body)
	}
	for _, p := range []string{
		filepath.Join(home, ".local/share/icons/hicolor/128x128/apps/org.wails.ssh-tool.png"),
		filepath.Join(home, ".local/share/icons/hicolor/scalable/apps/org.wails.ssh-tool.svg"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("icon not installed under the app-id name: %s", p)
		}
	}
}

func TestDesktopEntryPathHonoursXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "custom"))

	got := desktopEntryPath()
	want := filepath.Join(home, "custom", "applications", desktopFileName)
	if got != want {
		t.Errorf("desktopEntryPath = %q, want %q", got, want)
	}
}

// The upgrade case: a newer build run from ~/Downloads while an older
// copy is already installed. Reporting this as a plain first install
// would be wrong - the launcher entry exists and points at the old
// binary, so clicking the icon tomorrow goes back to it.
func TestGetInstallStateDetectsExistingInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stand-in for the installed copy. It answers --print-version the
	// way a real build does.
	installed := filepath.Join(binDir, "ssh-tool")
	script := "#!/bin/sh\n[ \"$1\" = \"--print-version\" ] && echo v0.94.0\n"
	if err := os.WriteFile(installed, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(dl, "ssh-tool-linux-amd64")
	if err := os.WriteFile(newer, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(newer)
	if st.Kind != "loose" {
		t.Errorf("Kind = %q, want loose", st.Kind)
	}
	if !st.CanOffer {
		t.Error("an upgrade should still be offered")
	}
	if !st.Replaces {
		t.Error("Replaces should be true when something is already installed")
	}
	if st.InstalledVersion != "v0.94.0" {
		t.Errorf("InstalledVersion = %q, want v0.94.0", st.InstalledVersion)
	}
}

// A first install has nothing to replace, and must not claim otherwise.
func TestGetInstallStateFirstInstallDoesNotReplace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dl, "ssh-tool-linux-amd64")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if st.Replaces {
		t.Error("nothing is installed, so nothing is being replaced")
	}
	if st.InstalledVersion != "" {
		t.Errorf("InstalledVersion = %q, want empty", st.InstalledVersion)
	}
}

// An installed binary that does not answer --print-version (an older
// build, a different architecture, a truncated download) still counts
// as installed. "Something is there, version unknown" is worth saying;
// silently calling it a first install is not.
func TestGetInstallStateUnreadableInstalledVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Not executable: running it fails, which is the point.
	if err := os.WriteFile(filepath.Join(binDir, "ssh-tool"), []byte("not a binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dl, "ssh-tool-linux-amd64")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if !st.Replaces {
		t.Error("a file is there, so installing would replace it")
	}
	if st.InstalledVersion != "" {
		t.Errorf("InstalledVersion = %q, want empty when it cannot be read", st.InstalledVersion)
	}
}
