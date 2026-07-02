package backup

import (
	"database/sql"
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
	// Flip a byte inside the KDF param region (right after magic+salt-len byte+salt).
	flipAt := len(magic) + 1 + saltLen + 1
	raw[flipAt] ^= 0xFF
	_ = os.WriteFile(dest, raw, 0o600)
	if err := Restore(dest, "pass", storePath, vaultPath); err == nil {
		t.Fatal("tamper accepted, expected AEAD rejection")
	}
}
