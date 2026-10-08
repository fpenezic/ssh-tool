package ssh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The three existing-file policies, through uploadDir as a fleet upload
// runs them: skip leaves a changed file alone, changed replaces it and
// then skips it on the next run (the uploaded copy took the local mtime),
// overwrite always sends.
func TestFleetUploadExistingPolicies(t *testing.T) {
	cli, _ := sftpOverLatency(t, 0).SFTPClient()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.conf"), []byte("new contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "conf")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(dst, "a.conf")
	if err := os.WriteFile(remote, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(policy string) int {
		t.Helper()
		pol := &uploadPolicy{
			skip: func(fi os.FileInfo, r string) bool { return skipExisting(cli, policy, fi, r) },
			after: func(fi os.FileInfo, r string) error {
				return cli.Chtimes(r, time.Now(), fi.ModTime())
			},
		}
		skipped, err := uploadDir(cli, src, filepath.ToSlash(dst), func(DirProgress) {}, nil, pol)
		if err != nil {
			t.Fatal(err)
		}
		return skipped
	}
	read := func() string {
		b, _ := os.ReadFile(remote)
		return string(b)
	}

	if n := run("skip"); n != 1 || read() != "old" {
		t.Errorf("skip: skipped=%d content=%q", n, read())
	}
	if n := run("changed"); n != 0 || read() != "new contents" {
		t.Errorf("changed (differs): skipped=%d content=%q", n, read())
	}
	if n := run("changed"); n != 1 {
		t.Errorf("changed (same): skipped=%d, want 1", n)
	}
	if n := run("overwrite"); n != 0 {
		t.Errorf("overwrite: skipped=%d", n)
	}
}

func TestParseFleetMode(t *testing.T) {
	if m, ok, err := ParseFleetMode("0755"); err != nil || !ok || m != 0o755 {
		t.Errorf("0755 -> %o %v %v", m, ok, err)
	}
	if _, ok, err := ParseFleetMode(""); err != nil || ok {
		t.Error("empty must mean no chmod")
	}
	for _, bad := range []string{"rwx", "0999", "17777"} {
		if _, _, err := ParseFleetMode(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFleetRemoteDir(t *testing.T) {
	cli, _ := sftpOverLatency(t, 0).SFTPClient()
	home, err := cli.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"":        home,
		"~":       home,
		"~/bin":   home + "/bin",
		"bin":     home + "/bin",
		"/opt/x/": "/opt/x",
	} {
		if got, err := fleetRemoteDir(cli, in); err != nil || got != filepath.ToSlash(want) {
			t.Errorf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
}
