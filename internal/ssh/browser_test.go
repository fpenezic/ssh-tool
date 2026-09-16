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

// A local (-L) forward is reached directly on loopback, so port 0 must
// produce a command line with no proxy switches at all. Leaving
// --proxy-bypass-list behind would be the subtle failure: it is what
// forces loopback traffic through the proxy, so on its own it would send
// 127.0.0.1:<port> into a proxy that isn't there.
func TestChromiumArgsNoProxyWhenPortZero(t *testing.T) {
	args := chromiumArgs("/usr/bin/chromium", "/tmp/profile", "127.0.0.1", 0, "http://127.0.0.1:8080")
	for _, a := range args {
		if strings.Contains(a, "proxy") {
			t.Fatalf("port 0 must add no proxy switches, got %q in %v", a, args)
		}
	}
	if args[len(args)-1] != "http://127.0.0.1:8080" {
		t.Fatalf("url must be last, got %v", args)
	}
	if !strings.HasPrefix(args[0], "--user-data-dir=") {
		t.Fatalf("profile isolation must survive: %v", args)
	}
}

func TestChromiumArgsProxyWhenPortSet(t *testing.T) {
	args := chromiumArgs("/usr/bin/chromium", "/tmp/profile", "127.0.0.1", 1080, "https://example.com")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--proxy-server=socks5://") {
		t.Fatalf("missing proxy server: %v", args)
	}
	if !strings.Contains(joined, "--proxy-bypass-list=<-loopback>") {
		t.Fatalf("missing loopback bypass: %v", args)
	}
}

// proxy.type 0 has to be written, not merely omitted: a persistent
// profile keeps prefs.js between launches, so an absent pref would leave
// a previous SOCKS launch still proxying.
func TestFirefoxPrefsDisableProxyWhenPortZero(t *testing.T) {
	prefs := firefoxPrefs("127.0.0.1", 0)
	if !strings.Contains(prefs, `user_pref("network.proxy.type", 0);`) {
		t.Fatalf("expected explicit no-proxy pref, got:\n%s", prefs)
	}
	if strings.Contains(prefs, "network.proxy.socks") {
		t.Fatalf("port 0 must not set a socks proxy, got:\n%s", prefs)
	}
}

func TestFirefoxPrefsSetProxyWhenPortSet(t *testing.T) {
	prefs := firefoxPrefs("127.0.0.1", 1080)
	if !strings.Contains(prefs, `user_pref("network.proxy.type", 1);`) {
		t.Fatalf("expected proxy type 1, got:\n%s", prefs)
	}
	if !strings.Contains(prefs, `user_pref("network.proxy.socks_port", 1080);`) {
		t.Fatalf("expected socks port, got:\n%s", prefs)
	}
	if !strings.Contains(prefs, `user_pref("network.proxy.socks_remote_dns", true);`) {
		t.Fatalf("remote DNS must stay on so hostnames resolve through the tunnel:\n%s", prefs)
	}
}
