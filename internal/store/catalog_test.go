package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// catalogFixture is a store with one catalog holding one folder, one
// connection in it and one forward on that connection, all written the
// way the catalog engine writes them (inside WithCatalogWriter), plus a
// local folder and connection outside the catalog.
type catalogFixture struct {
	db        *DB
	cat       *Catalog
	folderID  string
	connID    string
	fwdID     string
	localFold *Folder
	localConn *Connection
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cat, err := db.CreateCatalog(NewCatalog{Name: "Team", BaseURL: "https://catalog.example.com"})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	f := &catalogFixture{db: db, cat: cat, folderID: newID(), connID: newID(), fwdID: newID()}
	ts := now()
	err = db.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO folders (id, parent_id, name, settings_json, created_at, updated_at, catalog_id, remote_uuid, remote_rev)
			VALUES (?, ?, 'Customer A', '{}', ?, ?, ?, 'f-uuid', 1)`, f.folderID, cat.RootFolderID, ts, ts, cat.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev)
			VALUES (?, ?, 'web-1', 'web-1.example.com', ?, ?, ?, 'c-uuid', 1)`, f.connID, f.folderID, ts, ts, cat.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO port_forwards (id, connection_id, kind, local_port, remote_host, remote_port, catalog_id, remote_uuid, remote_rev)
			VALUES (?, ?, 'local', 15432, '127.0.0.1', 5432, ?, 'p-uuid', 1)`, f.fwdID, f.connID, cat.ID)
		return err
	})
	if err != nil {
		t.Fatalf("seed catalog rows: %v", err)
	}
	if f.localFold, err = db.CreateFolder(NewFolder{Name: "Mine"}); err != nil {
		t.Fatal(err)
	}
	if f.localConn, err = db.CreateConnection(NewConnection{Name: "own", Hostname: "own.example.com", FolderID: &f.localFold.ID}); err != nil {
		t.Fatal(err)
	}
	return f
}

func wantOwned(t *testing.T, what string, err error) {
	t.Helper()
	if !IsCatalogOwned(err) {
		t.Errorf("%s: want a catalog_owned refusal, got %v", what, err)
	}
}

func wantOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

func TestCatalogSchemaSplitKeepsTriggersWhole(t *testing.T) {
	stmts := splitSQLStatements(catalogSchemaSQL)
	triggers := 0
	for _, s := range stmts {
		if isTriggerStart(s) {
			triggers++
			if !strings.HasSuffix(strings.TrimSpace(s), "END") {
				t.Errorf("trigger split apart: %q", s)
			}
		}
	}
	if triggers != 9 {
		t.Errorf("want 9 trigger statements, got %d", triggers)
	}
}

func TestCreateCatalogMakesRootFolder(t *testing.T) {
	f := newCatalogFixture(t)
	root, err := f.db.GetFolder(f.cat.RootFolderID)
	if err != nil {
		t.Fatal(err)
	}
	if root.ParentID != nil || root.CatalogID == nil || *root.CatalogID != f.cat.ID || root.RemoteUUID != nil {
		t.Fatalf("root folder wrong: %+v", root)
	}
	if root.Name != "Team" {
		t.Errorf("root name %q", root.Name)
	}
	c, err := f.db.GetConnection(f.connID)
	if err != nil {
		t.Fatal(err)
	}
	if c.CatalogID == nil || c.RemoteUUID == nil || *c.RemoteUUID != "c-uuid" || c.RemoteRev != 1 || c.CatalogState != "" {
		t.Fatalf("catalog columns not scanned: %+v", c)
	}
	fw, err := f.db.GetPortForward(f.fwdID)
	if err != nil {
		t.Fatal(err)
	}
	if fw.RemoteUUID == nil || *fw.RemoteUUID != "p-uuid" || fw.Disabled {
		t.Fatalf("forward catalog columns not scanned: %+v", fw)
	}
	if f.localConn.CatalogID != nil || f.localConn.RemoteUUID != nil {
		t.Fatalf("local connection carries catalog columns: %+v", f.localConn)
	}
}

// Every store write path that can reach a catalog row, called directly.
// The triggers are the net under every IPC and MCP path, so this test
// does not go through any of them.
func TestCatalogGuardFolders(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	name := "renamed"
	var err error

	_, err = db.CreateFolder(NewFolder{Name: "x", ParentID: &f.folderID})
	wantOwned(t, "create folder inside catalog folder", err)
	_, err = db.CreateFolder(NewFolder{Name: "x", ParentID: &f.cat.RootFolderID})
	wantOwned(t, "create folder inside catalog root", err)
	_, err = db.UpdateFolder(UpdateFolder{ID: f.folderID, Name: &name})
	wantOwned(t, "rename catalog folder", err)
	_, err = db.UpdateFolder(UpdateFolder{ID: f.cat.RootFolderID, Name: &name})
	wantOwned(t, "rename catalog root", err)
	_, err = db.UpdateFolder(UpdateFolder{ID: f.cat.RootFolderID, ParentID: &f.localFold.ID})
	wantOwned(t, "move catalog root under a local folder", err)
	_, err = db.UpdateFolder(UpdateFolder{ID: f.localFold.ID, ParentID: &f.folderID})
	wantOwned(t, "move local folder into catalog folder", err)
	sorted := int64(7)
	_, err = db.UpdateFolder(UpdateFolder{ID: f.folderID, SortOrder: &sorted})
	wantOwned(t, "reorder catalog folder", err)
	_, err = db.UpdateFolder(UpdateFolder{ID: f.folderID, Settings: &InheritableSettings{Username: &name}})
	wantOwned(t, "folder settings", err)
	wantOwned(t, "folder settings tx", db.WithTx(func(tx *sql.Tx) error {
		return db.UpdateFolderSettingsTx(tx, f.folderID, InheritableSettings{Username: &name})
	}))
	wantOwned(t, "rename folder tx", db.WithTx(func(tx *sql.Tx) error { return db.RenameFolderTx(tx, f.folderID, "x") }))
	wantOwned(t, "folder named icon", db.SetFolderNamedIcon(f.folderID, "server", "blue"))
	wantOwned(t, "delete catalog folder", db.DeleteFolder(f.folderID))
	wantOwned(t, "delete catalog root", db.DeleteFolder(f.cat.RootFolderID))

	// An update that changes nothing passes (the full-row rewrite pattern).
	_, err = db.UpdateFolder(UpdateFolder{ID: f.folderID})
	wantOK(t, "no-op folder update", err)
	// Local folders are untouched by the guard.
	_, err = db.UpdateFolder(UpdateFolder{ID: f.localFold.ID, Name: &name})
	wantOK(t, "rename local folder", err)
}

func TestCatalogGuardConnections(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	name := "renamed"
	var err error

	_, err = db.CreateConnection(NewConnection{Name: "x", Hostname: "h", FolderID: &f.folderID})
	wantOwned(t, "create connection inside catalog folder", err)
	_, err = db.CreateConnection(NewConnection{Name: "x", Hostname: "h", FolderID: &f.cat.RootFolderID})
	wantOwned(t, "create connection inside catalog root", err)
	wantOwned(t, "create connection tx inside catalog folder", db.WithTx(func(tx *sql.Tx) error {
		_, err := db.CreateConnectionTx(tx, NewConnection{Name: "x", Hostname: "h", FolderID: &f.folderID})
		return err
	}))
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Name: &name})
	wantOwned(t, "rename catalog connection", err)
	host := "other.example.com"
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Hostname: &host})
	wantOwned(t, "change hostname", err)
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Overrides: &InheritableSettings{Username: &name}})
	wantOwned(t, "change overrides", err)
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Tags: &[]string{"x"}})
	wantOwned(t, "change tags", err)
	notes := "n"
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Notes: &notes})
	wantOwned(t, "change notes", err)
	proto := "local"
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Protocol: &proto})
	wantOwned(t, "change protocol", err)
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, ClearFolder: true})
	wantOwned(t, "move catalog connection to the root", err)
	_, err = db.UpdateConnection(UpdateConnection{ID: f.localConn.ID, FolderID: &f.folderID})
	wantOwned(t, "move local connection into catalog folder", err)
	_, err = db.BatchUpdateConnectionOverrides([]string{f.connID}, BatchOverridePatch{Patch: InheritableSettings{Username: &name}})
	wantOwned(t, "batch override", err)
	_, err = db.BatchUpdateConnectionOverrides([]string{f.connID}, BatchOverridePatch{AddTags: []string{"t"}})
	wantOwned(t, "batch tags", err)
	wantOwned(t, "rename tx", db.WithTx(func(tx *sql.Tx) error { return db.RenameConnectionTx(tx, f.connID, "x") }))
	wantOwned(t, "hostname tx", db.WithTx(func(tx *sql.Tx) error { return db.SetConnectionHostnameTx(tx, f.connID, "x") }))
	wantOwned(t, "notes tx", db.WithTx(func(tx *sql.Tx) error { return db.SetConnectionNotesTx(tx, f.connID, "x") }))
	wantOwned(t, "move tx", db.WithTx(func(tx *sql.Tx) error { return db.MoveConnectionTx(tx, f.connID, nil) }))
	wantOwned(t, "patch overrides tx", db.WithTx(func(tx *sql.Tx) error {
		return db.PatchConnectionOverridesTx(tx, f.connID, InheritableSettings{Username: &name}, nil)
	}))
	wantOwned(t, "named icon tx", db.WithTx(func(tx *sql.Tx) error { return db.SetConnectionNamedIconTx(tx, f.connID, "server", "") }))
	wantOwned(t, "named icon", db.SetConnectionNamedIcon(f.connID, "server", "blue"))
	wantOwned(t, "delete catalog connection", db.DeleteConnection(f.connID))

	// Local columns stay writable on a catalog connection.
	fav := true
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Favorite: &fav})
	wantOK(t, "favourite", err)
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, OpenHidden: &fav})
	wantOK(t, "open hidden", err)
	wantOK(t, "touch last used", db.TouchLastUsed(f.connID))
	wantOK(t, "password key", db.SetConnectionPasswordKey(f.connID, "conn-pass:x"))
	wantOK(t, "clear password key", db.ClearConnectionPasswordKey(f.connID))
	wantOK(t, "vnc password key", db.SetConnectionVncPasswordKey(f.connID, "vnc:x"))
	c, _ := db.GetConnection(f.connID)
	if !c.Favorite || !c.OpenHidden || c.Name != "web-1" {
		t.Errorf("local columns not written or owned ones changed: %+v", c)
	}
}

func TestCatalogGuardForwards(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	port := uint16(16000)
	var err error

	_, err = db.UpdatePortForward(UpdatePortForward{ID: f.fwdID, LocalPort: &port})
	wantOwned(t, "change catalog forward local port", err)
	host := "10.0.0.1"
	_, err = db.UpdatePortForward(UpdatePortForward{ID: f.fwdID, RemoteHost: &host})
	wantOwned(t, "change catalog forward remote host", err)
	desc := "x"
	_, err = db.UpdatePortForward(UpdatePortForward{ID: f.fwdID, Description: &desc})
	wantOwned(t, "change catalog forward description", err)
	wantOwned(t, "delete catalog forward", db.DeletePortForward(f.fwdID))

	auto := true
	_, err = db.UpdatePortForward(UpdatePortForward{ID: f.fwdID, AutoStart: &auto})
	wantOK(t, "auto start (local decision)", err)
	mode := "isolated"
	_, err = db.UpdatePortForward(UpdatePortForward{ID: f.fwdID, BrowserMode: &mode})
	wantOK(t, "browser mode", err)
	wantOK(t, "bookmarks", db.SetPortForwardBookmarks(f.fwdID, []ProxyBookmark{{Name: "ui", URL: "http://localhost:15432"}}))
	wantOK(t, "disabled", db.SetPortForwardDisabled(f.fwdID, true))

	// The user's own forward on a catalog connection is ordinary data.
	rh := "127.0.0.1"
	rp := uint16(22)
	own, err := db.CreatePortForward(NewPortForward{ConnectionID: f.connID, Kind: "local", RemoteHost: &rh, RemotePort: &rp})
	if err != nil {
		t.Fatalf("local forward on catalog connection: %v", err)
	}
	_, err = db.UpdatePortForward(UpdatePortForward{ID: own.ID, LocalPort: &port})
	wantOK(t, "edit own forward", err)
	wantOK(t, "delete own forward", db.DeletePortForward(own.ID))

	// Nobody but the engine creates a catalog forward.
	_, err = db.conn.Exec(`INSERT INTO port_forwards (id, connection_id, kind, remote_uuid) VALUES ('x', ?, 'dynamic', 'u')`, f.connID)
	wantOwned(t, "insert catalog forward", err)
}

// Turning a local row into a catalog row (or the reverse) is the engine's
// job (publish, "keep as local"), never a plain write.
func TestCatalogGuardConversion(t *testing.T) {
	f := newCatalogFixture(t)
	_, err := f.db.conn.Exec(`UPDATE connections SET catalog_id = ?, remote_uuid = 'u' WHERE id = ?`, f.cat.ID, f.localConn.ID)
	wantOwned(t, "local to catalog", err)
	_, err = f.db.conn.Exec(`UPDATE connections SET catalog_id = NULL, remote_uuid = NULL WHERE id = ?`, f.connID)
	wantOwned(t, "catalog to local", err)
	_, err = f.db.conn.Exec(`UPDATE connections SET catalog_state = 'removed' WHERE id = ?`, f.connID)
	wantOwned(t, "state change", err)
}

func TestCatalogWriterMarkerNeverLeaks(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	count := func() int {
		var n int
		if err := db.conn.QueryRow(`SELECT COUNT(*) FROM catalog_writer`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET name = 'web-1b' WHERE id = ?`, f.connID)
		return err
	})
	wantOK(t, "engine write", err)
	if n := count(); n != 0 {
		t.Fatalf("marker left after commit: %d", n)
	}
	boom := errors.New("boom")
	err = db.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE connections SET name = 'web-1c' WHERE id = ?`, f.connID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("marker left after rollback: %d", n)
	}
	c, _ := db.GetConnection(f.connID)
	if c.Name != "web-1b" {
		t.Errorf("rolled-back write landed: %q", c.Name)
	}
	// And the guard is back on.
	name := "x"
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Name: &name})
	wantOwned(t, "after writer", err)
}

func TestCatalogWriterStaleMarkerClearedOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO catalog_writer (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	_ = db.conn.QueryRow(`SELECT COUNT(*) FROM catalog_writer`).Scan(&n)
	if n != 0 {
		t.Fatalf("stale marker survived open")
	}
}

func TestCatalogBindingsAndOverrides(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	wantOK(t, "bind", db.SetCatalogBinding(f.cat.ID, "s1", "cred", "cred-1"))
	wantOK(t, "rebind", db.SetCatalogBinding(f.cat.ID, "s1", "cred", "cred-2"))
	if err := db.SetCatalogBinding(f.cat.ID, "s2", "bogus", "x"); err == nil {
		t.Error("bad binding kind accepted")
	}
	bs, _ := db.ListCatalogBindings(f.cat.ID)
	if len(bs) != 1 || bs[0].TargetID != "cred-2" {
		t.Fatalf("bindings: %+v", bs)
	}
	wantOK(t, "override", db.SetCatalogOverride(f.cat.ID, "c-uuid", "connections", `{"port":2222}`))
	os, _ := db.ListCatalogOverrides(f.cat.ID)
	if len(os) != 1 || os[0].JSON != `{"port":2222}` {
		t.Fatalf("overrides: %+v", os)
	}
	wantOK(t, "empty override deletes", db.SetCatalogOverride(f.cat.ID, "c-uuid", "connections", "{}"))
	os, _ = db.ListCatalogOverrides(f.cat.ID)
	if len(os) != 0 {
		t.Fatalf("empty layer kept a row: %+v", os)
	}
	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		return db.UpsertCatalogSlotTx(tx, CatalogSlot{CatalogID: f.cat.ID, UUID: "s1", Rev: 1, Type: "cred", Name: "admin", Kind: "slot"})
	})
	wantOK(t, "slot upsert", err)
	slots, _ := db.ListCatalogSlots(f.cat.ID)
	if len(slots) != 1 || slots[0].Name != "admin" {
		t.Fatalf("slots: %+v", slots)
	}
}

func TestCatalogConfigRenamesRoot(t *testing.T) {
	f := newCatalogFixture(t)
	wantOK(t, "config", f.db.UpdateCatalogConfig(f.cat.ID, "Ops", 30))
	c, _ := f.db.GetCatalog(f.cat.ID)
	if c.Name != "Ops" || c.SyncIntervalSec != 60 {
		t.Errorf("config: %+v", c)
	}
	root, _ := f.db.GetFolder(f.cat.RootFolderID)
	if root.Name != "Ops" {
		t.Errorf("root not renamed: %q", root.Name)
	}
	wantOK(t, "sync state", f.db.SetCatalogSyncState(f.cat.ID, "cur-1", nil, ""))
	wantOK(t, "sync state keeps cursor", f.db.SetCatalogSyncState(f.cat.ID, "", nil, "offline"))
	c, _ = f.db.GetCatalog(f.cat.ID)
	if c.Cursor != "cur-1" || c.LastError != "offline" {
		t.Errorf("sync state: cursor %q error %q", c.Cursor, c.LastError)
	}
}

// The profile-sync fingerprint must not move when only the catalog changed
// (every machine syncing that catalog would push), and must move when the
// user changed something of their own on a catalog row.
func TestFingerprintIgnoresCatalogContent(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	fp := db.ContentFingerprint()

	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		ts := now() + 100
		if _, err := tx.Exec(`UPDATE connections SET name = 'web-2', remote_rev = 2, updated_at = ? WHERE id = ?`, ts, f.connID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE port_forwards SET local_port = 15433, remote_rev = 2 WHERE id = ?`, f.fwdID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid)
			VALUES (?, ?, 'web-3', 'h', ?, ?, ?, 'c2')`, newID(), f.folderID, ts, ts, f.cat.ID); err != nil {
			return err
		}
		return db.UpsertCatalogSlotTx(tx, CatalogSlot{CatalogID: f.cat.ID, UUID: "s9", Type: "net", Name: "vpn"})
	})
	wantOK(t, "catalog change", err)
	wantOK(t, "cursor", db.SetCatalogSyncState(f.cat.ID, "cur-2", nil, ""))
	if got := db.ContentFingerprint(); got != fp {
		t.Fatalf("catalog-only change moved the fingerprint:\n%s\n%s", fp, got)
	}

	steps := []struct {
		name string
		do   func() error
	}{
		{"favourite", func() error {
			v := true
			_, err := db.UpdateConnection(UpdateConnection{ID: f.connID, Favorite: &v})
			return err
		}},
		{"binding", func() error { return db.SetCatalogBinding(f.cat.ID, "s9", "net", "np-1") }},
		{"override", func() error { return db.SetCatalogOverride(f.cat.ID, "c-uuid", "connections", `{"port":2}`) }},
		{"forward disabled", func() error { return db.SetPortForwardDisabled(f.fwdID, true) }},
		{"catalog renamed", func() error { return db.UpdateCatalogConfig(f.cat.ID, "Renamed", 300) }},
	}
	for _, s := range steps {
		before := db.ContentFingerprint()
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if db.ContentFingerprint() == before {
			t.Errorf("%s did not move the fingerprint", s.name)
		}
	}
}

// A profile that never used a catalog keeps the fp3 body: no catalog
// segment, the same forward hash.
func TestFingerprintUnchangedWithoutCatalogs(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, _ := db.CreateConnection(NewConnection{Name: "a", Hostname: "h"})
	rh, rp := "127.0.0.1", uint16(80)
	if _, err := db.CreatePortForward(NewPortForward{ConnectionID: c.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp}); err != nil {
		t.Fatal(err)
	}
	if fp := db.ContentFingerprint(); strings.Contains(fp, "catalog=") {
		t.Fatalf("catalog segment without catalogs: %s", fp)
	}
}

func TestMirrorCarriesCatalogs(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	src, err := Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := src.CreateCatalog(NewCatalog{Name: "Team", BaseURL: "https://catalog.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	connID := newID()
	err = src.WithCatalogWriter(func(tx *sql.Tx) error {
		ts := now()
		_, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev)
			VALUES (?, ?, 'web-1', 'h', ?, ?, ?, 'c-uuid', 3)`, connID, cat.RootFolderID, ts, ts, cat.ID)
		return err
	})
	wantOK(t, "seed", err)
	wantOK(t, "cursor", src.SetCatalogSyncState(cat.ID, "cur-9", nil, ""))
	wantOK(t, "binding", src.SetCatalogBinding(cat.ID, "s1", "cred", "cred-1"))
	wantOK(t, "override", src.SetCatalogOverride(cat.ID, "c-uuid", "connections", `{"port":2222}`))
	// What a push uploads: a copy with the catalog rows shadowed.
	snap := filepath.Join(dir, "snap.db")
	if _, err := src.conn.Exec(`VACUUM INTO ?`, snap); err != nil {
		t.Fatal(err)
	}
	_ = src.Close()
	if err := ShadowSnapshot(snap, "nonce-1"); err != nil {
		t.Fatal(err)
	}

	dst := newCatalogFixture(t).db // has its own catalog rows, which the pull replaces
	if _, err := dst.MirrorFromWith(snap, ShadowTrust{FromSync: true, Nonce: "nonce-1"}); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	cats, _ := dst.ListCatalogs()
	if len(cats) != 1 || cats[0].ID != cat.ID || cats[0].Cursor != "cur-9" {
		t.Fatalf("catalogs not mirrored: %+v", cats)
	}
	c, err := dst.GetConnection(connID)
	if err != nil || c.RemoteRev != 3 {
		t.Fatalf("catalog connection not mirrored: %+v %v", c, err)
	}
	if bs, _ := dst.ListCatalogBindings(cat.ID); len(bs) != 1 {
		t.Errorf("bindings not mirrored: %+v", bs)
	}
	if os, _ := dst.ListCatalogOverrides(cat.ID); len(os) != 1 {
		t.Errorf("overrides not mirrored: %+v", os)
	}
	var n int
	_ = dst.conn.QueryRow(`SELECT COUNT(*) FROM catalog_writer`).Scan(&n)
	if n != 0 {
		t.Fatal("mirror left the writer marker")
	}
	name := "x"
	_, err = dst.UpdateConnection(UpdateConnection{ID: connID, Name: &name})
	wantOwned(t, "guard after mirror", err)
}

// The startup sweep of dead network profile references must not abort on
// a profile holding catalog rows (the guard refuses its UPDATE for them),
// and must leave catalog rows to materialisation.
func TestSweepOrphanNetworkRefsSkipsCatalogRows(t *testing.T) {
	f := newCatalogFixture(t)
	wantOK(t, "dead ref on a catalog row", f.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET overrides_json = '{"network_profile_id":"gone"}' WHERE id = ?`, f.connID)
		return err
	}))
	dead := "gone-too"
	if _, err := f.db.UpdateConnection(UpdateConnection{ID: f.localConn.ID, Overrides: &InheritableSettings{NetworkProfileID: &dead}}); err != nil {
		t.Fatal(err)
	}
	n, err := f.db.SweepOrphanNetworkRefs()
	if err != nil {
		t.Fatalf("sweep aborted: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1 (the local row only)", n)
	}
	local, _ := f.db.GetConnection(f.localConn.ID)
	if local.Overrides.NetworkProfileID != nil {
		t.Error("local orphan not cleared")
	}
	cat, _ := f.db.GetConnection(f.connID)
	if cat.Overrides.NetworkProfileID == nil {
		t.Error("catalog row rewritten by the sweep")
	}
}

func TestCatalogRefErrorNamesTheSlot(t *testing.T) {
	f := newCatalogFixture(t)
	wantOK(t, "slot", f.db.WithCatalogWriter(func(tx *sql.Tx) error {
		return f.db.UpsertCatalogSlotTx(tx, CatalogSlot{CatalogID: f.cat.ID, UUID: "s1", Type: "cred", Name: "customer-admin", Kind: "slot"})
	}))
	_, err := f.db.GetCredential(CatalogUnboundRef(f.cat.ID, "s1"))
	if err == nil || !strings.Contains(err.Error(), `"customer-admin"`) {
		t.Fatalf("message does not name the slot: %v", err)
	}
}
