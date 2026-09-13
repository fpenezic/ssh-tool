//go:build linux && !android

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// asReleaseBuild stamps a release version for the duration of a test.
// installFrom refuses a development build, and `go test` is one - these
// tests are about the install mechanics, not that guard (which
// TestInstallFromRefusesDevBuild covers).
func asReleaseBuild(t *testing.T) {
	t.Helper()
	orig := appVersion
	appVersion = "v0.94.0"
	t.Cleanup(func() { appVersion = orig })
}

// The whole point of the offer: a binary in Downloads ends up in
// ~/.local/bin with a launcher entry and icons, without root.
func TestInstallToUserPrefixFromLoose(t *testing.T) {
	asReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dl, "ssh-tool-linux-amd64")
	payload := []byte("#!/bin/sh\necho fake\n")
	if err := os.WriteFile(exe, payload, 0o755); err != nil {
		t.Fatal(err)
	}

	target, err := installFrom(exe)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	want := filepath.Join(home, ".local", "bin", "ssh-tool")
	if target != want {
		t.Errorf("target = %q, want %q", target, want)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("installed binary unreadable: %v", err)
	}
	if string(got) != string(payload) {
		t.Error("installed binary does not match the source")
	}
	if fi, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", fi.Mode())
	}

	entry := filepath.Join(home, ".local", "share", "applications", desktopFileName)
	body, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("desktop entry missing: %v", err)
	}
	text := string(body)
	// Exec must be absolute or launching from the menu fails, since the
	// session's PATH does not reliably include ~/.local/bin.
	if !contains(text, "Exec="+want) {
		t.Errorf("desktop entry does not point at the installed binary:\n%s", text)
	}
	// Must match buildApp's ProgramName or the window shows the generic
	// Wails icon instead of ours.
	if !contains(text, "StartupWMClass=ssh-tool\n") {
		t.Errorf("StartupWMClass missing or wrong:\n%s", text)
	}

	for _, icon := range []string{
		filepath.Join(home, ".local", "share", "icons", "hicolor", "128x128", "apps", "org.wails.ssh-tool.png"),
		filepath.Join(home, ".local", "share", "icons", "hicolor", "scalable", "apps", "org.wails.ssh-tool.svg"),
	} {
		fi, err := os.Stat(icon)
		if err != nil {
			t.Errorf("icon not installed: %s", icon)
			continue
		}
		if fi.Size() == 0 {
			t.Errorf("icon is empty: %s", icon)
		}
	}

	// After installing, the state must settle: nothing left to offer.
	st := installStateFor(target)
	if st.Kind != "user" || st.CanOffer {
		t.Errorf("after install: Kind=%q CanOffer=%v, want user/false", st.Kind, st.CanOffer)
	}
}

// A partially-written binary must never replace a working one, so the
// copy goes through a temp file in the destination directory.
func TestInstallLeavesNoTempOnSuccess(t *testing.T) {
	asReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	exe := filepath.Join(home, "ssh-tool-dl")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := installFrom(exe); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".local", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".new" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// Replacing an older install must overwrite the binary and leave the
// desktop entry pointing at the same path - the entry is what the
// launcher opens, so a stale one would send the user back to whatever
// used to be there.
func TestInstallReplacesOlderCopy(t *testing.T) {
	asReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(binDir, "ssh-tool")
	if err := os.WriteFile(installed, []byte("OLD BUILD"), 0o755); err != nil {
		t.Fatal(err)
	}

	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(dl, "ssh-tool-linux-amd64")
	if err := os.WriteFile(newer, []byte("NEW BUILD"), 0o755); err != nil {
		t.Fatal(err)
	}

	target, err := installFrom(newer)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if target != installed {
		t.Errorf("target = %q, want %q", target, installed)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW BUILD" {
		t.Errorf("installed binary = %q, the old one survived", got)
	}
	// No .new left behind from the atomic swap.
	if _, err := os.Stat(installed + ".new"); err == nil {
		t.Error("temp file left behind after replace")
	}

	body, err := os.ReadFile(desktopEntryPath())
	if err != nil {
		t.Fatalf("desktop entry missing after replace: %v", err)
	}
	if !contains(string(body), "Exec="+installed) {
		t.Errorf("desktop entry should point at the installed path:\n%s", body)
	}
}

// Uninstalling removes what an install put in place and nothing else.
// The wording in Settings promises user data is untouched, so the test
// puts a file in the data directory and checks it survives.
func TestUninstallRemovesOnlyIntegration(t *testing.T) {
	asReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	exe := filepath.Join(home, "dl-ssh-tool")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	installed, err := installFrom(exe)
	if err != nil {
		t.Fatal(err)
	}

	// Stand-in for the user's real data.
	dataDir := filepath.Join(home, ".local", "share", "ssh-tool")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dataDir, "store.db")
	if err := os.WriteFile(store, []byte("connections"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	removed, err := app.UninstallUserPrefix()
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !removed {
		t.Error("something was installed, so something should have been removed")
	}

	for _, gone := range []string{
		installed,
		desktopEntryPath(),
		filepath.Join(home, ".local/share/icons/hicolor/128x128/apps/org.wails.ssh-tool.png"),
		filepath.Join(home, ".local/share/icons/hicolor/scalable/apps/org.wails.ssh-tool.svg"),
	} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("should have been removed: %s", gone)
		}
	}

	if _, err := os.Stat(store); err != nil {
		t.Error("user data must survive an uninstall")
	}
}

// Uninstalling twice, or with nothing installed, is not an error - it
// reports that there was nothing to do.
func TestUninstallWithNothingInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	app := &App{}
	removed, err := app.UninstallUserPrefix()
	if err != nil {
		t.Fatalf("uninstall on a clean home should not error: %v", err)
	}
	if removed {
		t.Error("nothing was installed, so nothing should be reported as removed")
	}
}
