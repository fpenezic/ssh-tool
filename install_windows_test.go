//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// asReleaseBuild stamps a release version for the duration of a test:
// the install paths refuse a development build, and `go test` is one.
func asReleaseBuild(t *testing.T) {
	t.Helper()
	orig := appVersion
	appVersion = "v0.94.0"
	t.Cleanup(func() { appVersion = orig })
}

// An exe run from Downloads is the case the offer exists for: it works,
// but there is nothing in the Start Menu to search for or pin.
func TestGetInstallStateLooseWindows(t *testing.T) {
	asReleaseBuild(t)
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "Local"))
	t.Setenv("APPDATA", filepath.Join(base, "Roaming"))

	dl := filepath.Join(base, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dl, "ssh-tool-windows-amd64.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if st.Kind != "loose" {
		t.Errorf("Kind = %q, want loose", st.Kind)
	}
	if !st.CanOffer {
		t.Error("a loose exe should produce an offer")
	}
	want := filepath.Join(base, "Local", "Programs", "ssh-tool", "ssh-tool.exe")
	if st.TargetPath != want {
		t.Errorf("TargetPath = %q, want %q", st.TargetPath, want)
	}
	if st.Replaces {
		t.Error("nothing is installed yet")
	}
}

// Windows paths are case-insensitive, so a path differing only in case
// is the same install - comparing them byte-wise would offer to install
// the app over itself.
func TestGetInstallStateWindowsIsCaseInsensitive(t *testing.T) {
	asReleaseBuild(t)
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "Local"))
	t.Setenv("APPDATA", filepath.Join(base, "Roaming"))

	progDir := filepath.Join(base, "Local", "Programs", "ssh-tool")
	if err := os.MkdirAll(progDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(progDir, "ssh-tool.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(filepath.Join(progDir, "SSH-TOOL.EXE"))
	if st.Kind != "user" {
		t.Errorf("Kind = %q, want user - the path differs only in case", st.Kind)
	}
}

// The upgrade case: a newer exe run from Downloads while an older one
// sits in the per-user program directory, which is what the Start Menu
// shortcut points at.
func TestGetInstallStateWindowsDetectsExistingInstall(t *testing.T) {
	asReleaseBuild(t)
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "Local"))
	t.Setenv("APPDATA", filepath.Join(base, "Roaming"))

	progDir := filepath.Join(base, "Local", "Programs", "ssh-tool")
	if err := os.MkdirAll(progDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(progDir, "ssh-tool.exe"), []byte("MZ old"), 0o755); err != nil {
		t.Fatal(err)
	}

	dl := filepath.Join(base, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dl, "ssh-tool-windows-amd64.exe")
	if err := os.WriteFile(exe, []byte("MZ new"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(exe)
	if !st.Replaces {
		t.Error("an existing install should be reported as a replace")
	}
	if !st.CanOffer {
		t.Error("an upgrade should still be offered")
	}
}

// Installing must overwrite the old exe rather than failing on the
// rename, which is what a plain os.Rename does on Windows.
func TestInstallFromReplacesOnWindows(t *testing.T) {
	asReleaseBuild(t)
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "Local"))
	t.Setenv("APPDATA", filepath.Join(base, "Roaming"))

	progDir := filepath.Join(base, "Local", "Programs", "ssh-tool")
	if err := os.MkdirAll(progDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(progDir, "ssh-tool.exe")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(base, "new.exe")
	if err := os.WriteFile(src, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The shortcut step needs PowerShell; the copy is what matters here,
	// and installFrom reports the target even when the shortcut fails.
	got, _ := installFrom(src)
	if got != target {
		t.Errorf("target = %q, want %q", got, target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "NEW" {
		t.Errorf("installed exe = %q, the old one survived", data)
	}
}
