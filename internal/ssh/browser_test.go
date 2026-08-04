package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeBundle builds a fake .app with the given executables under
// Contents/MacOS and an optional XML Info.plist.
func makeBundle(t *testing.T, name string, execs []string, plist string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	macOS := filepath.Join(root, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range execs {
		if err := os.WriteFile(filepath.Join(macOS, e), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if plist != "" {
		if err := os.WriteFile(filepath.Join(root, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveAppBundle(t *testing.T) {
	// Chromium-family: binary named after the bundle.
	b := makeBundle(t, "Brave Browser.app", []string{"Brave Browser"}, "")
	if got, want := resolveAppBundle(b), filepath.Join(b, "Contents", "MacOS", "Brave Browser"); got != want {
		t.Errorf("bundle-named exec: got %q want %q", got, want)
	}
	// A trailing slash (what a file picker often yields) must not defeat it.
	if got, want := resolveAppBundle(b+"/"), filepath.Join(b, "Contents", "MacOS", "Brave Browser"); got != want {
		t.Errorf("trailing slash: got %q want %q", got, want)
	}

	// Firefox: lowercased binary name.
	f := makeBundle(t, "Firefox.app", []string{"firefox"}, "")
	if got, want := resolveAppBundle(f), filepath.Join(f, "Contents", "MacOS", "firefox"); got != want {
		t.Errorf("lowercase exec: got %q want %q", got, want)
	}

	// Neither convention matches: fall back to CFBundleExecutable.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>weird-bin</string>
</dict></plist>`
	p := makeBundle(t, "Odd.app", []string{"weird-bin", "helper"}, plist)
	if got, want := resolveAppBundle(p), filepath.Join(p, "Contents", "MacOS", "weird-bin"); got != want {
		t.Errorf("plist exec: got %q want %q", got, want)
	}

	// Single executable, no usable name hint: pick it.
	s := makeBundle(t, "Solo.app", []string{"only-one"}, "")
	if got, want := resolveAppBundle(s), filepath.Join(s, "Contents", "MacOS", "only-one"); got != want {
		t.Errorf("lone exec: got %q want %q", got, want)
	}

	// Ambiguous: several executables and no hint - leave the path alone
	// rather than launching the wrong binary.
	a := makeBundle(t, "Ambiguous.app", []string{"one", "two"}, "")
	if got := resolveAppBundle(a); got != a {
		t.Errorf("ambiguous bundle: got %q want unchanged %q", got, a)
	}

	// Plain binary paths pass through.
	plain := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(plain, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAppBundle(plain); got != plain {
		t.Errorf("plain path: got %q want %q", got, plain)
	}
}

func TestSanitizeProfileKey(t *testing.T) {
	cases := map[string]string{
		"a5716cb3-ad70-4769-9b4f-ccc08bec5fbd": "a5716cb3-ad70-4769-9b4f-ccc08bec5fbd",
		"../../etc":                            "etc",
		"a/b\\c":                               "abc",
		"":                                     "",
	}
	for in, want := range cases {
		if got := sanitizeProfileKey(in); got != want {
			t.Errorf("sanitizeProfileKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeProfileKey(strings.Repeat("x", 200)); len(got) > 64 {
		t.Errorf("key not capped: len=%d", len(got))
	}
}

func TestPersistentProfileDirIsPerKey(t *testing.T) {
	if isWSL() {
		t.Skip("WSL takes the Windows-path branch")
	}
	base := t.TempDir()
	a, err := persistentProfileDir(base, "chromium", "forward-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := persistentProfileDir(base, "chromium", "forward-b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two forwards share a profile dir: %s", a)
	}
	// No key keeps the historical shared dir.
	shared, err := persistentProfileDir(base, "chromium", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "browser-profiles", "chromium"); shared != want {
		t.Errorf("shared dir: got %q want %q", shared, want)
	}
	// Per-forward dirs must be siblings of it, not nested inside it - the
	// shared dir is itself a live Chromium profile for existing installs.
	if strings.HasPrefix(a, shared+string(os.PathSeparator)) {
		t.Errorf("per-forward dir %q nested inside shared profile %q", a, shared)
	}
}
