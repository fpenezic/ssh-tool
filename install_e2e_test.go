//go:build linux && !android

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The whole point of the offer: a binary in Downloads ends up in
// ~/.local/bin with a launcher entry and icons, without root.
func TestInstallToUserPrefixFromLoose(t *testing.T) {
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

	entry := filepath.Join(home, ".local", "share", "applications", "ssh-tool.desktop")
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
		filepath.Join(home, ".local", "share", "icons", "hicolor", "128x128", "apps", "ssh-tool.png"),
		filepath.Join(home, ".local", "share", "icons", "hicolor", "scalable", "apps", "ssh-tool.svg"),
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
