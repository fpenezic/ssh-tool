package store

import (
	"os"
	"path/filepath"
	"testing"
)

func walSize(t *testing.T, dbPath string) int64 {
	t.Helper()
	fi, err := os.Stat(dbPath + "-wal")
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}
	return fi.Size()
}

// The bug this guards: an automatic (passive) checkpoint copies pages
// back into the database but then RECYCLES the WAL rather than
// shrinking it, so the file stays at its high-water mark forever. Only
// a TRUNCATE checkpoint reclaims the space, and Close() has to do it.
func TestCloseTruncatesWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Enough churn to push the WAL well past a trivial size.
	for i := 0; i < 3000; i++ {
		if err := db.AppendAudit("test.event", "target", map[string]string{
			"i":       string(rune('a' + i%26)),
			"padding": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	auditPath := filepath.Join(dir, "audit.db")
	grew := walSize(t, auditPath)
	if grew == 0 {
		t.Skip("WAL never materialised on this platform; nothing to assert")
	}

	// Measure the checkpoint itself, on the live handle, BEFORE
	// closing. Closing the pool drops every connection, which lets
	// SQLite tidy the WAL on its own regardless of checkpoint mode -
	// so asserting after Close() would pass even with the bug and
	// prove nothing.
	checkpointTruncate(db.audit)
	after := walSize(t, auditPath)
	if after >= grew {
		t.Errorf("audit WAL was %d bytes before the checkpoint and %d after; expected it to shrink", grew, after)
	}
	if after != 0 {
		t.Errorf("audit WAL should be truncated to 0, got %d bytes", after)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The data must survive the truncation - the whole point is that
	// checkpointing moves it INTO the db file, not that it drops it.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	st, err := reopened.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Total != 3000 {
		t.Errorf("after truncate+reopen: %d events, want 3000", st.Total)
	}
}

// Close must not wedge if it runs twice (shutdown paths can race).
func TestCloseIsIdempotent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AppendAudit("test.event", "", nil); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// Second close returns an error from database/sql, but must not
	// panic on the already-closed stopWAL channel.
	_ = db.Close()
}
