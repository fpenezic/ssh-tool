package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "tx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestWithTxRollback: an error partway through a tx leaves the store untouched.
func TestWithTxRollback(t *testing.T) {
	db := openTestDB(t)

	sentinel := errors.New("boom")
	err := db.WithTx(func(tx *sql.Tx) error {
		if _, err := db.CreateFolderTx(tx, NewFolder{Name: "one"}); err != nil {
			return err
		}
		if _, err := db.CreateFolderTx(tx, NewFolder{Name: "two"}); err != nil {
			return err
		}
		return sentinel // force rollback after two inserts
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}

	folders, err := db.ListFolders()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(folders) != 0 {
		t.Fatalf("rollback should leave zero folders, got %d", len(folders))
	}
}

// TestWithTxCommit: a clean tx commits all inserts, and a forward staged
// against a connection created in the same tx resolves to the real id.
func TestWithTxCommit(t *testing.T) {
	db := openTestDB(t)

	var folderID, connID, fwdID string
	err := db.WithTx(func(tx *sql.Tx) (err error) {
		folderID, err = db.CreateFolderTx(tx, NewFolder{Name: "prod"})
		if err != nil {
			return err
		}
		connID, err = db.CreateConnectionTx(tx, NewConnection{
			FolderID: &folderID, Name: "web-1", Hostname: "10.0.0.11", Protocol: "ssh",
		})
		if err != nil {
			return err
		}
		fwdID, err = db.CreatePortForwardTx(tx, NewPortForward{
			ConnectionID: connID, Kind: "dynamic",
		})
		if err != nil {
			return err
		}
		return db.SetPortForwardBookmarksTx(tx, fwdID, []ProxyBookmark{
			{Name: "wiki", URL: "http://wiki.internal"},
		})
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	conns, err := db.ListConnections(&folderID)
	if err != nil {
		t.Fatalf("list conns: %v", err)
	}
	if len(conns) != 1 || conns[0].ID != connID {
		t.Fatalf("want the created connection under the folder, got %+v", conns)
	}
	fwds, err := db.ListPortForwards(connID)
	if err != nil {
		t.Fatalf("list forwards: %v", err)
	}
	if len(fwds) != 1 {
		t.Fatalf("want 1 forward, got %d", len(fwds))
	}
	if len(fwds[0].Bookmarks) != 1 || fwds[0].Bookmarks[0].Name != "wiki" {
		t.Fatalf("bookmark not persisted: %+v", fwds[0].Bookmarks)
	}
}

// TestUpdateFolderSettingsTx: setting inheritable defaults on a folder inside a
// tx persists them, and a missing folder returns ErrNotFound.
func TestUpdateFolderSettingsTx(t *testing.T) {
	db := openTestDB(t)

	f, err := db.CreateFolder(NewFolder{Name: "prod"})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}

	user := "admin"
	err = db.WithTx(func(tx *sql.Tx) error {
		return db.UpdateFolderSettingsTx(tx, f.ID, InheritableSettings{
			Username: &user,
			JumpHost: &JumpHostOverride{Kind: "chain", Chain: &JumpHostSpec{Hostname: "1.2.3.4"}},
		})
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}

	got, err := db.GetFolder(f.ID)
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if got.Settings.Username == nil || *got.Settings.Username != "admin" {
		t.Fatalf("username default not persisted: %+v", got.Settings.Username)
	}
	if got.Settings.JumpHost == nil || got.Settings.JumpHost.Chain == nil ||
		got.Settings.JumpHost.Chain.Hostname != "1.2.3.4" {
		t.Fatalf("jump default not persisted: %+v", got.Settings.JumpHost)
	}

	// Missing folder -> ErrNotFound, and the tx rolls back.
	err = db.WithTx(func(tx *sql.Tx) error {
		return db.UpdateFolderSettingsTx(tx, "does-not-exist", InheritableSettings{Username: &user})
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for missing folder, got %v", err)
	}
}
