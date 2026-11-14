package backup

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// seedStore creates a minimal SQLite file so VACUUM INTO has something
// real to snapshot.
func seedStore(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (v) VALUES ('hello');`); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestCreateAndRestore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	vaultPath := filepath.Join(dir, "vault.enc")
	seedStore(t, storePath)
	if err := os.WriteFile(vaultPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "backups", "snap.sshtool-backup")
	if err := Create(dest, storePath, vaultPath, "pass", "test"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Restore stages into pending-restore/.
	if err := Restore(dest, "pass", storePath, vaultPath); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending-restore", "READY")); err != nil {
		t.Fatalf("READY missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending-restore", "store.db.enc")); err != nil {
		t.Fatalf("staged store missing: %v", err)
	}
	// Sealed staged store should not be a SQLite file (would mean
	// the encrypt-on-stage step is a no-op).
	raw, _ := os.ReadFile(filepath.Join(dir, "pending-restore", "store.db.enc"))
	if len(raw) >= 16 && strings.HasPrefix(string(raw[:16]), "SQLite format 3") {
		t.Fatalf("staged store is plaintext SQLite")
	}

	// Pre-restore safety snapshot lives encrypted next to backups.
	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	gotPreRestore := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pre-restore-") && strings.HasSuffix(e.Name(), ".sshtool-backup") {
			gotPreRestore = true
			// Confirm the safety snapshot is encrypted (not plain
			// SQLite). Magic should be the backup magic, never the
			// SQLite "SQLite format 3" header.
			raw, _ := os.ReadFile(filepath.Join(dir, "backups", e.Name()))
			if len(raw) >= 16 && strings.HasPrefix(string(raw[:16]), "SQLite format 3") {
				t.Fatalf("pre-restore snapshot is plaintext SQLite")
			}
		}
	}
	if !gotPreRestore {
		t.Fatal("expected encrypted pre-restore-* file under backups/")
	}

	// List should NOT include the pre-restore snapshot.
	infos, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if strings.HasPrefix(info.Filename, "pre-restore-") {
			t.Fatalf("pre-restore snapshot leaked into List()")
		}
	}
}

func TestRestore_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	vaultPath := filepath.Join(dir, "vault.enc")
	seedStore(t, storePath)
	_ = os.WriteFile(vaultPath, []byte("v"), 0o600)
	dest := filepath.Join(dir, "backups", "snap.sshtool-backup")
	if err := Create(dest, storePath, vaultPath, "right", "test"); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dest, "wrong", storePath, vaultPath); err == nil {
		t.Fatal("expected wrong-passphrase error")
	}
}

func TestRestore_TamperRejected(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	vaultPath := filepath.Join(dir, "vault.enc")
	seedStore(t, storePath)
	_ = os.WriteFile(vaultPath, []byte("v"), 0o600)
	dest := filepath.Join(dir, "backups", "snap.sshtool-backup")
	if err := Create(dest, storePath, vaultPath, "pass", "test"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dest)
	// Flip a salt byte: the header still parses (a flipped KDF parameter
	// was refused by the bounds check before decryption ran), so this
	// reaches the AEAD and must fail there.
	flipAt := len(magic) + 1 + 3
	raw[flipAt] ^= 0xFF
	_ = os.WriteFile(dest, raw, 0o600)
	if err := Restore(dest, "pass", storePath, vaultPath); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("tampered backup: %v, want ErrWrongPassphrase", err)
	}
}

// PrepareStore sees the snapshot's store, can replace what is staged, and
// can refuse the restore before anything is staged.
func TestRestoreWith_PrepareStore(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	vaultPath := filepath.Join(dir, "vault.enc")
	seedStore(t, storePath)
	if err := os.WriteFile(vaultPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "snap.sshtool-backup")
	if err := Create(dest, storePath, vaultPath, "pass", "test"); err != nil {
		t.Fatalf("create: %v", err)
	}

	refuse := errors.New("refused")
	var seen string
	err := RestoreWith(dest, "pass", storePath, vaultPath, func(path string) (string, func(), error) {
		db, err := sql.Open("sqlite", path)
		if err == nil {
			_ = db.QueryRow(`SELECT v FROM t`).Scan(&seen)
			db.Close()
		}
		return "", nil, refuse
	})
	if !errors.Is(err, refuse) {
		t.Fatalf("want the PrepareStore error, got %v", err)
	}
	if seen != "hello" {
		t.Errorf("PrepareStore did not get the snapshot's store: %q", seen)
	}
	if _, err := os.Stat(filepath.Join(dir, PendingDir)); !os.IsNotExist(err) {
		t.Fatalf("something was staged despite the refusal: %v", err)
	}

	// A replacement is what gets staged (and swapped in).
	other := filepath.Join(t.TempDir(), "other.db")
	db, _ := sql.Open("sqlite", other)
	_, _ = db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (v) VALUES ('replaced')`)
	db.Close()
	cleaned := false
	if err := RestoreWith(dest, "pass", storePath, vaultPath, func(string) (string, func(), error) {
		return other, func() { cleaned = true }, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !cleaned {
		t.Error("cleanup not called")
	}
	if _, err := ApplyPending(storePath, vaultPath); err != nil {
		t.Fatal(err)
	}
	db, _ = sql.Open("sqlite", storePath)
	defer db.Close()
	var v string
	_ = db.QueryRow(`SELECT v FROM t`).Scan(&v)
	if v != "replaced" {
		t.Fatalf("staged store is not the replacement: %q", v)
	}
}
