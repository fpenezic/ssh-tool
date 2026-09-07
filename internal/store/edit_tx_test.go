package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// mkConn creates a connection with the given overrides and returns its id.
func mkConn(t *testing.T, db *DB, name string, ov InheritableSettings) string {
	t.Helper()
	c, err := db.CreateConnection(NewConnection{
		Name: name, Hostname: "host.example.com", Overrides: ov,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return c.ID
}

// The operation the LLM edit path exists for: drop a per-connection
// credential so the folder's is inherited, WITHOUT disturbing the other
// settings sitting beside it. Writing a fresh InheritableSettings (which is
// all plain UpdateConnection can do) would wipe the username and port too.
func TestPatchConnectionOverridesClearsOnlyNamedFields(t *testing.T) {
	db := openTestDB(t)
	port := uint16(2222)
	id := mkConn(t, db, "c1", InheritableSettings{
		Username: strp("root"),
		Port:     &port,
		AuthRef:  strp("cred-abc"),
	})

	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.PatchConnectionOverridesTx(tx, id, InheritableSettings{}, []string{"auth_ref"})
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	got, err := db.GetConnection(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Overrides.AuthRef != nil {
		t.Errorf("auth_ref: got %q, want cleared", *got.Overrides.AuthRef)
	}
	if got.Overrides.Username == nil || *got.Overrides.Username != "root" {
		t.Errorf("username was disturbed: %v", got.Overrides.Username)
	}
	if got.Overrides.Port == nil || *got.Overrides.Port != 2222 {
		t.Errorf("port was disturbed: %v", got.Overrides.Port)
	}
}

// A set patch merges field by field rather than replacing the blob.
func TestPatchConnectionOverridesMerges(t *testing.T) {
	db := openTestDB(t)
	id := mkConn(t, db, "c1", InheritableSettings{
		Username: strp("root"),
		AuthRef:  strp("cred-abc"),
	})

	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.PatchConnectionOverridesTx(tx, id,
			InheritableSettings{Username: strp("deploy")}, nil)
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	got, _ := db.GetConnection(id)
	if got.Overrides.Username == nil || *got.Overrides.Username != "deploy" {
		t.Errorf("username: got %v, want deploy", got.Overrides.Username)
	}
	if got.Overrides.AuthRef == nil || *got.Overrides.AuthRef != "cred-abc" {
		t.Errorf("auth_ref should have survived, got %v", got.Overrides.AuthRef)
	}
}

// A typo'd field name must be rejected. Silently ignoring it would report
// success while the setting stayed exactly where it was.
func TestPatchConnectionOverridesRejectsUnknownField(t *testing.T) {
	db := openTestDB(t)
	id := mkConn(t, db, "c1", InheritableSettings{AuthRef: strp("cred-abc")})

	err := db.WithTx(func(tx *sql.Tx) error {
		return db.PatchConnectionOverridesTx(tx, id, InheritableSettings{}, []string{"authref"})
	})
	if err == nil {
		t.Fatal("want an error for an unknown field name, got nil")
	}

	// And the rollback must have left the real setting untouched.
	got, _ := db.GetConnection(id)
	if got.Overrides.AuthRef == nil || *got.Overrides.AuthRef != "cred-abc" {
		t.Errorf("auth_ref changed despite the failed patch: %v", got.Overrides.AuthRef)
	}
}

// Settings this build does not model must survive an edit, or a store
// written by a newer version would quietly lose fields on every rename.
func TestPatchConnectionOverridesKeepsUnknownJSON(t *testing.T) {
	db := openTestDB(t)
	id := mkConn(t, db, "c1", InheritableSettings{Username: strp("root")})

	if _, err := db.conn.Exec(
		`UPDATE connections SET overrides_json = ? WHERE id = ?`,
		`{"username":"root","from_the_future":"keep me"}`, id,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.PatchConnectionOverridesTx(tx, id,
			InheritableSettings{Username: strp("deploy")}, nil)
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	var raw string
	if err := db.conn.QueryRow(
		`SELECT overrides_json FROM connections WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(raw, "from_the_future") {
		t.Errorf("unknown field was dropped: %s", raw)
	}
	if !strings.Contains(raw, "deploy") {
		t.Errorf("patch did not apply: %s", raw)
	}
}

func TestRenameAndMoveTx(t *testing.T) {
	db := openTestDB(t)
	f0, err := db.CreateFolder(NewFolder{Name: "old name"})
	if err != nil {
		t.Fatalf("folder: %v", err)
	}
	fid := f0.ID
	id := mkConn(t, db, "before", InheritableSettings{})

	if err := db.WithTx(func(tx *sql.Tx) error {
		if err := db.RenameConnectionTx(tx, id, "after"); err != nil {
			return err
		}
		if err := db.SetConnectionHostnameTx(tx, id, "new.example.com"); err != nil {
			return err
		}
		if err := db.MoveConnectionTx(tx, id, &fid); err != nil {
			return err
		}
		return db.RenameFolderTx(tx, fid, "new name")
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}

	got, _ := db.GetConnection(id)
	if got.Name != "after" {
		t.Errorf("name: got %q", got.Name)
	}
	if got.Hostname != "new.example.com" {
		t.Errorf("hostname: got %q", got.Hostname)
	}
	if got.FolderID == nil || *got.FolderID != fid {
		t.Errorf("folder: got %v, want %s", got.FolderID, fid)
	}
	f, _ := db.GetFolder(fid)
	if f.Name != "new name" {
		t.Errorf("folder name: got %q", f.Name)
	}
}

// Editing a row that has been deleted must report it rather than silently
// doing nothing - the LLM works from a listing that may be stale.
func TestEditTxMissingRow(t *testing.T) {
	db := openTestDB(t)
	err := db.WithTx(func(tx *sql.Tx) error {
		return db.RenameConnectionTx(tx, "does-not-exist", "x")
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
