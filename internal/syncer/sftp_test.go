package syncer

import (
	"bytes"
	"net"
	"testing"

	"github.com/pkg/sftp"
)

// newInMemSFTP starts an SFTP server over an in-process net.Pipe (full
// duplex, no network, no SSH layer) and returns an *SFTP transport with its
// client pre-injected. This exercises EnsureDir/Get/Put/Move against real
// SFTP wire semantics - including the posix-rename extension - rooted at the
// real filesystem (the test uses t.TempDir paths).
func newInMemSFTP(t *testing.T, dir string) *SFTP {
	t.Helper()
	cliConn, srvConn := net.Pipe()

	server, err := sftp.NewServer(srvConn)
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	go func() { _ = server.Serve() }()

	cli, err := sftp.NewClientPipe(cliConn, cliConn)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close(); _ = server.Close() })

	return &SFTP{Dir: dir, client: cli}
}

func TestSFTPTransportRoundTrip(t *testing.T) {
	// Verify the transport satisfies the interface and round-trips data the
	// way Push/Pull rely on: EnsureDir, Put, Get, and an overwriting Move.
	var _ Transport = (*SFTP)(nil)

	// The in-mem server roots at the OS temp dir; use a unique subdir.
	dir := t.TempDir() + "/sync"
	tr := newInMemSFTP(t, dir)

	if err := tr.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	// Idempotent - a second call on an existing dir is not an error.
	if err := tr.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir (second): %v", err)
	}

	// Get of a missing name is ErrNotFound (the 404 contract Pull keys on).
	if _, err := tr.Get("meta.json"); err != ErrNotFound {
		t.Fatalf("Get missing: want ErrNotFound, got %v", err)
	}

	meta := []byte(`{"generation":1}`)
	if err := tr.Put("meta.json", meta); err != nil {
		t.Fatalf("Put meta: %v", err)
	}
	got, err := tr.Get("meta.json")
	if err != nil {
		t.Fatalf("Get meta: %v", err)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("Get meta: got %q want %q", got, meta)
	}

	// The snapshot upload pattern: Put temp, Move over live (overwriting).
	snap := []byte("ciphertext-v1")
	if err := tr.Put("snapshot.stb.uploading", snap); err != nil {
		t.Fatalf("Put temp: %v", err)
	}
	if err := tr.Move("snapshot.stb.uploading", "snapshot.stb"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	got, err = tr.Get("snapshot.stb")
	if err != nil {
		t.Fatalf("Get snapshot: %v", err)
	}
	if !bytes.Equal(got, snap) {
		t.Fatalf("Get snapshot: got %q want %q", got, snap)
	}
	// The temp name is gone after the move.
	if _, err := tr.Get("snapshot.stb.uploading"); err != ErrNotFound {
		t.Fatalf("temp after Move: want ErrNotFound, got %v", err)
	}

	// Move OVER an existing destination (the real overwrite case): a second
	// snapshot replaces the first.
	snap2 := []byte("ciphertext-v2-longer")
	if err := tr.Put("snapshot.stb.uploading", snap2); err != nil {
		t.Fatalf("Put temp2: %v", err)
	}
	if err := tr.Move("snapshot.stb.uploading", "snapshot.stb"); err != nil {
		t.Fatalf("Move overwrite: %v", err)
	}
	got, err = tr.Get("snapshot.stb")
	if err != nil {
		t.Fatalf("Get snapshot2: %v", err)
	}
	if !bytes.Equal(got, snap2) {
		t.Fatalf("overwrite: got %q want %q", got, snap2)
	}
}
