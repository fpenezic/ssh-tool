package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTempDataDir points store.DataDir() at a throwaway directory for the
// duration of a test. DataDir has no override of its own, so we move every env
// var it consults on the platforms we build for.
func useTempDataDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir) // linux
	t.Setenv("HOME", dir)          // darwin / fallback
	t.Setenv("APPDATA", dir)       // windows
}

// TestMcpSanitizeSegment pins the property the download sandbox depends on:
// whatever the LLM (or the remote host, via a hostile filename) hands us, the
// result is a single harmless path segment. A regression here is a local
// arbitrary-write bug, not a cosmetic one.
func TestMcpSanitizeSegment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"auth.log", "auth.log"},
		{"core.dump-2", "core.dump-2"},
		{"..", "file"},
		{"../../etc/passwd", "_.._etc_passwd"}, // slashes -> _, leading dots stripped
		{".ssh", "ssh"},                        // no hidden files in the sandbox
		{"", "file"},
		{"a b;rm -rf /", "a_b_rm_-rf__"},
		{"naiveté.txt", "naivet_.txt"}, // non-ASCII runes collapse to one _
	}
	for _, c := range cases {
		got := mcpSanitizeSegment(c.in)
		if got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, `/\`) || got == "." || got == ".." {
			t.Errorf("sanitize(%q) = %q escapes its segment", c.in, got)
		}
	}
}

// TestMcpSanitizeSegmentBounded checks the length cap, so a 4 KB remote
// filename can't produce a path the OS rejects (or a surprise elsewhere).
func TestMcpSanitizeSegmentBounded(t *testing.T) {
	got := mcpSanitizeSegment(strings.Repeat("x", 4096))
	if len(got) > 128 {
		t.Fatalf("length %d exceeds the 128-byte cap", len(got))
	}
}

// TestMcpDownloadDestStaysInSandbox drives the real destination builder with a
// traversal attempt and asserts the result is still inside the sandbox root.
func TestMcpDownloadDestStaysInSandbox(t *testing.T) {
	useTempDataDir(t)
	root, err := filepath.Abs(mcpDownloadRoot())
	if err != nil {
		t.Fatal(err)
	}

	for _, remote := range []string{
		"/var/log/auth.log",
		"/etc/../../../../root/.ssh/authorized_keys",
		"..",
		"/",
	} {
		dest, err := mcpDownloadDest("sess-1", remote)
		if err != nil {
			t.Fatalf("dest(%q): %v", remote, err)
		}
		abs, err := filepath.Abs(dest)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			t.Errorf("dest(%q) = %q escaped sandbox %q", remote, abs, root)
		}
	}
}

// TestMcpDownloadDestNoClobber covers the forensics case: pulling the same
// filename twice must not overwrite the first copy.
func TestMcpDownloadDestNoClobber(t *testing.T) {
	useTempDataDir(t)

	first, err := mcpDownloadDest("sess-1", "/var/log/auth.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := mcpDownloadDest("sess-1", "/var/log/auth.log")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("second download would clobber %s", first)
	}
	if got := filepath.Base(second); got != "auth-2.log" {
		t.Errorf("second dest base = %q, want auth-2.log", got)
	}
}

// TestMcpHumanBytes keeps the approval prompt's size readable - the user
// decides whether to allow a download largely on this string.
func TestMcpHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	}
	for _, c := range cases {
		if got := mcpHumanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
