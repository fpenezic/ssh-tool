package ssh

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestScanRemoteGlobDirAndCompressed(t *testing.T) {
	cli, _ := sftpOverLatency(t, 0).SFTPClient()
	root := filepath.ToSlash(t.TempDir())
	for _, f := range []string{"access.log", "error.log", "access.log.1.gz", "notes.txt", "app/a.log", "app/sub/b.log"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rels := func(pattern string, skipGz bool) []string {
		t.Helper()
		items, _, err := scanRemote(cli, pattern, skipGz)
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Rel)
		}
		sort.Strings(out)
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	}
	eq(rels(root+"/*.log*", false), "access.log", "access.log.1.gz", "error.log")
	eq(rels(root+"/*.log*", true), "access.log", "error.log")
	eq(rels(root+"/app", false), "app/a.log", "app/sub/b.log")
	eq(rels(root+"/notes.txt", false), "notes.txt")
	if _, _, err := scanRemote(cli, root+"/*.nope", false); err == nil {
		t.Error("a glob with no match must fail the host")
	}
}

func TestSafeLocalPath(t *testing.T) {
	base := t.TempDir()
	got, err := safeLocalPath(base, "logs/a:b?.log")
	if err != nil || got != filepath.Join(base, "logs", "a_b_.log") {
		t.Errorf("got %q %v", got, err)
	}
	got, err = safeLocalPath(base, "../../etc/passwd")
	if err != nil || got != filepath.Join(base, "_", "_", "etc", "passwd") {
		t.Errorf(".. must stay inside base: %q %v", got, err)
	}
	if SafeFileName("CON.log") != "_CON.log" || SafeFileName("trail. ") != "trail" {
		t.Error("windows reserved names / trailing dots")
	}
}
