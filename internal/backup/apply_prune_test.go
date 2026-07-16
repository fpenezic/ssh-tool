package backup

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// seedStoreVal is seedStore with a caller-chosen marker value so a swap can be
// verified by which value survives.
func seedStoreVal(t *testing.T, path, val string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT);`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (v) VALUES (?)`, val); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
}

func markerOf(t *testing.T, storePath string) string {
	t.Helper()
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT v FROM t LIMIT 1`).Scan(&v); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return v
}

// TestApplyPendingSwaps completes the flow the existing round-trip test stops
// short of: after Restore stages the new files, ApplyPending swaps them into
// the live paths and cleans up.
func TestApplyPendingSwaps(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	vaultPath := filepath.Join(dir, "vault.enc")

	// Backup carries "from-backup".
	seedStoreVal(t, storePath, "from-backup")
	if err := os.WriteFile(vaultPath, []byte("vault-from-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "backups", "snap"+backupExt)
	if err := Create(dest, storePath, vaultPath, "pw", "test"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Overwrite the live files with "live" data, then Restore + ApplyPending.
	if err := os.Remove(storePath); err != nil {
		t.Fatal(err)
	}
	seedStoreVal(t, storePath, "live")
	if err := os.WriteFile(vaultPath, []byte("vault-live"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Restore(dest, "pw", storePath, vaultPath); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// Live is still "live" until the swap.
	if got := markerOf(t, storePath); got != "live" {
		t.Fatalf("live changed before ApplyPending: %q", got)
	}

	applied, err := ApplyPending(storePath, vaultPath)
	if err != nil {
		t.Fatalf("ApplyPending: %v", err)
	}
	if !applied {
		t.Fatal("ApplyPending reported nothing applied")
	}
	if got := markerOf(t, storePath); got != "from-backup" {
		t.Fatalf("store not swapped: marker=%q want from-backup", got)
	}
	if vb, _ := os.ReadFile(vaultPath); string(vb) != "vault-from-backup" {
		t.Fatalf("vault not swapped: %q", vb)
	}
	// READY flag and staged files are cleaned up.
	if _, err := os.Stat(filepath.Join(dir, PendingDir, "READY")); !os.IsNotExist(err) {
		t.Fatal("READY not removed after ApplyPending")
	}
}

func TestApplyPendingNoStaging(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	vaultPath := filepath.Join(dir, "vault.enc")
	seedStoreVal(t, storePath, "live")
	_ = os.WriteFile(vaultPath, []byte("v"), 0o600)

	applied, err := ApplyPending(storePath, vaultPath)
	if err != nil {
		t.Fatalf("ApplyPending (nothing staged): %v", err)
	}
	if applied {
		t.Fatal("ApplyPending should be a no-op with no pending-restore dir")
	}
	if got := markerOf(t, storePath); got != "live" {
		t.Fatalf("no-op ApplyPending changed the store: %q", got)
	}
}

// TestPruneKeepsNewest verifies prune-N retention: the newest `keep`
// auto-backups survive, older ones are deleted, and manual backups are left
// untouched.
func TestPruneKeepsNewest(t *testing.T) {
	bDir := t.TempDir()

	base := time.Now().Add(-10 * time.Hour)
	// Five auto backups with increasing mtimes.
	var autoNames []string
	for i := 0; i < 5; i++ {
		name := autoBackupPrefix + time.Time{}.Add(time.Duration(i)*time.Hour).Format("20060102-150405") + backupExt
		autoNames = append(autoNames, name)
		p := filepath.Join(bDir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Distinct, increasing mtimes so "newest" is well defined.
		mt := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// A manual backup (no auto prefix) that must never be pruned.
	manual := filepath.Join(bDir, "manual"+backupExt)
	if err := os.WriteFile(manual, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := pruneOld(bDir, 2); err != nil {
		t.Fatalf("pruneOld: %v", err)
	}

	// The two newest auto backups (i=4, i=3) survive; i=0..2 are gone.
	for i, name := range autoNames {
		_, err := os.Stat(filepath.Join(bDir, name))
		exists := err == nil
		wantExists := i >= 3
		if exists != wantExists {
			t.Errorf("auto backup %d exists=%v, want %v", i, exists, wantExists)
		}
	}
	// Manual survives.
	if _, err := os.Stat(manual); err != nil {
		t.Fatalf("manual backup was pruned: %v", err)
	}
	// keep=0 is a no-op (never deletes everything).
	if err := pruneOld(bDir, 0); err != nil {
		t.Fatalf("pruneOld keep=0: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bDir, autoNames[4])); err != nil {
		t.Fatal("keep=0 pruned the newest auto backup")
	}
}

// TestLastAutoBackupAt drives the scheduler's interval gate: it returns the
// newest auto-backup's mtime (and zero when there are none), which runOnce
// compares against the interval to decide whether to back up.
func TestLastAutoBackupAt(t *testing.T) {
	bDir := t.TempDir()

	// No auto backups yet -> zero time (so a scheduler always runs the first).
	if got := lastAutoBackupAt(bDir); !got.IsZero() {
		t.Fatalf("empty dir: got %v, want zero", got)
	}

	// A manual backup must NOT count as an auto backup.
	if err := os.WriteFile(filepath.Join(bDir, "manual"+backupExt), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastAutoBackupAt(bDir); !got.IsZero() {
		t.Fatalf("manual-only dir: got %v, want zero", got)
	}

	// Two auto backups; the newest mtime wins.
	older := filepath.Join(bDir, autoBackupPrefix+"20200101-000000"+backupExt)
	newer := filepath.Join(bDir, autoBackupPrefix+"20200102-000000"+backupExt)
	_ = os.WriteFile(older, []byte("x"), 0o600)
	_ = os.WriteFile(newer, []byte("x"), 0o600)
	oldT := time.Now().Add(-48 * time.Hour)
	newT := time.Now().Add(-1 * time.Hour)
	_ = os.Chtimes(older, oldT, oldT)
	_ = os.Chtimes(newer, newT, newT)

	got := lastAutoBackupAt(bDir)
	if got.Sub(newT).Abs() > time.Second {
		t.Fatalf("lastAutoBackupAt: got %v, want ~%v (newest)", got, newT)
	}
}
