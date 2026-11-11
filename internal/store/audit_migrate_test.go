package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Audit rows left in store.db - from before the split, or brought back by
// restoring an old backup over an audit log that already has events - move
// into audit.db on open, once, without duplicating rows already there.
func TestMigrateAuditRowsIntoNonEmptySide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendAudit("vault.unlock", "", nil); err != nil {
		t.Fatal(err)
	}
	// Legacy rows in the main store, one of them already in audit.db too
	// (a run cut short after the side commit).
	if _, err := db.conn.Exec(`INSERT INTO audit_events (ts, action, target, metadata_json) VALUES (1, 'ssh.connect', 'old-host', '{}'), (2, 'ssh.disconnect', 'old-host', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.audit.Exec(`INSERT INTO audit_events (ts, action, target, metadata_json) VALUES (1, 'ssh.connect', 'old-host', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count := func(c *sql.DB, where string) int {
		var n int
		if err := c.QueryRow(`SELECT COUNT(*) FROM audit_events ` + where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(db.conn, ""); n != 0 {
		t.Fatalf("%d audit rows left in store.db", n)
	}
	if n := count(db.audit, "WHERE target = 'old-host'"); n != 2 {
		t.Fatalf("audit.db holds %d legacy rows, want 2 (no duplicate)", n)
	}
	if n := count(db.audit, "WHERE action = 'vault.unlock'"); n != 1 {
		t.Fatalf("audit.db lost its own row: %d", n)
	}
}
