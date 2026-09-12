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
	if err := os.WriteFile(filepath.Join(appDir, "ssh-tool.desktop"), []byte("x"), 0o644); err != nil {
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
func TestDesktopEntryPathHonoursXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "custom"))

	got := desktopEntryPath()
	want := filepath.Join(home, "custom", "applications", "ssh-tool.desktop")
	if got != want {
		t.Errorf("desktopEntryPath = %q, want %q", got, want)
	}
}
