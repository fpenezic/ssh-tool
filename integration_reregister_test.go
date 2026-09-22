//go:build linux && !android

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The point of the whole exercise: after an install, a handler that was
// registered from ~/Downloads must point at the installed copy. Left
// alone it keeps launching the download - which works until that file
// is deleted, and then fails with nothing explaining why.
func TestReRegisterMovedIntegrationsRepointsURLScheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	appDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A handler registered from the downloads copy.
	old := filepath.Join(home, "Downloads", "ssh-tool-linux-amd64")
	entry := filepath.Join(appDir, "ssh-tool-url.desktop")
	if err := os.WriteFile(entry, []byte(
		"[Desktop Entry]\nType=Application\nExec=\""+old+"\" %u\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	newPath := filepath.Join(home, ".local", "bin", "ssh-tool")
	// registerURLSchemeAt also shells out to xdg-mime, which may not be
	// present; the file write is what matters and happens first.
	_ = registerURLSchemeAt(newPath)

	body, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := execTargetFromDesktopEntry(string(body)); got != newPath {
		t.Errorf("Exec target = %q, want %q", got, newPath)
	}
	if strings.Contains(string(body), "Downloads") {
		t.Error("the old path should be gone from the entry")
	}
}

// Re-registering must not CREATE registrations the user never asked
// for. Installing is not consent to add a context-menu entry.
func TestReRegisterSkipsWhatWasNeverRegistered(t *testing.T) {
	// HOME is redirected below, but the "is anything registered?" probes
	// shell out to xdg-mime, which reads the real user's XDG state and
	// ignores it. On a machine where the developer has actually
	// registered the scheme, the probe says yes and the function then
	// writes into the temp HOME - the test would report a bug that only
	// exists because the host is registered. CI is clean, so it passes
	// there and fails only locally.
	if urlSchemeStatus() != "" || explorerMenuStatus() != "" {
		t.Skip("host has ssh-tool integrations registered; the probes cannot be isolated")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	reRegisterMovedIntegrations(filepath.Join(home, ".local", "bin", "ssh-tool"))

	for _, p := range []string{
		filepath.Join(home, ".local/share/applications/ssh-tool-url.desktop"),
		filepath.Join(home, ".local/share/kio/servicemenus/ssh-tool-open-dir.desktop"),
		filepath.Join(home, ".local/share/nautilus/scripts/Open in ssh-tool"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("created a registration that did not exist: %s", p)
		}
	}
}

// IntegrationStatus is declared twice - once here and once in
// app_mobile_stubs.go - because app.go is shared and mobile has no
// registrations to report. The JSON shape has to stay identical or the
// frontend's type stops matching one of the two builds.
func TestIntegrationStatusJSONShape(t *testing.T) {
	b, err := json.Marshal(IntegrationStatus{
		Registered: true, Detail: "d", Target: "t", Stale: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"registered":true,"detail":"d","target":"t","stale":true}`
	if string(b) != want {
		t.Errorf("JSON shape drifted:\n got %s\nwant %s", b, want)
	}
}
