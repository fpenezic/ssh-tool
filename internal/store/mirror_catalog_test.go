package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// legacySnapshot writes a store file shaped like one from an app before
// schema 29 (no catalog columns, no catalog tables) holding whatever rows
// the old machine had after pulling this profile and editing it.
func legacySnapshot(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := []string{
		`CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO schema_meta VALUES ('version', '28')`,
		`CREATE TABLE folders (id TEXT PRIMARY KEY, parent_id TEXT, name TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0,
			settings_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE connections (id TEXT PRIMARY KEY, folder_id TEXT, name TEXT NOT NULL, hostname TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0, overrides_json TEXT NOT NULL DEFAULT '{}', tags_json TEXT NOT NULL DEFAULT '[]',
			notes TEXT NOT NULL DEFAULT '', favorite INTEGER NOT NULL DEFAULT 0, sensitive INTEGER NOT NULL DEFAULT 0,
			open_hidden INTEGER NOT NULL DEFAULT 0, password_vault_key TEXT, vnc_password_vault_key TEXT,
			protocol TEXT NOT NULL DEFAULT 'ssh', created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE port_forwards (id TEXT PRIMARY KEY, connection_id TEXT NOT NULL, kind TEXT NOT NULL, local_addr TEXT,
			local_port INTEGER, remote_host TEXT, remote_port INTEGER, auto_start INTEGER NOT NULL DEFAULT 0,
			description TEXT NOT NULL DEFAULT '', bookmarks TEXT NOT NULL DEFAULT '[]', browser_mode TEXT NOT NULL DEFAULT '')`,
	}
	for _, s := range append(schema, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func countWhere(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.conn.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The case 2 hazard: an old app pulled this machine's profile, saw the
// catalog rows as plain ones, changed things and pushed. Pulling that back
// must keep every catalog row, the attachment, the bindings and the layers,
// take the user's own changes, and duplicate nothing.
func TestMirrorFromLegacySnapshotKeepsCatalog(t *testing.T) {
	f := newCatalogFixture(t)
	db := f.db
	// Catalog state on this machine.
	wantOK(t, "binding", db.SetCatalogBinding(f.cat.ID, "s1", "cred", "cred-1"))
	wantOK(t, "override", db.SetCatalogOverride(f.cat.ID, "c-uuid", "connections", `{"port":2222}`))
	conn2 := newID()
	wantOK(t, "second catalog connection", db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid)
			VALUES (?, ?, 'web-2', 'h2', 0, 0, ?, 'c2-uuid')`, conn2, f.folderID, f.cat.ID)
		return err
	}))
	// The user's own forward on the catalog connection, absent from the
	// old machine's snapshot.
	rh, rp := "127.0.0.1", uint16(22)
	ownFwd, err := db.CreatePortForward(NewPortForward{ConnectionID: f.connID, Kind: "local", RemoteHost: &rh, RemotePort: &rp})
	if err != nil {
		t.Fatal(err)
	}

	snap := filepath.Join(t.TempDir(), "legacy.db")
	legacySnapshot(t, snap,
		// Demoted copies of the root and the catalog folder, one renamed there.
		`INSERT INTO folders (id, name) VALUES ('`+f.cat.RootFolderID+`', 'Team')`,
		`INSERT INTO folders (id, parent_id, name) VALUES ('`+f.folderID+`', '`+f.cat.RootFolderID+`', 'edited there')`,
		// The local folder, and a folder the old machine created inside
		// what it saw as a plain folder.
		`INSERT INTO folders (id, name) VALUES ('`+f.localFold.ID+`', 'Mine')`,
		`INSERT INTO folders (id, parent_id, name) VALUES ('new-folder', '`+f.folderID+`', 'made there')`,
		// The catalog connection, renamed and favourited there; web-2 was
		// deleted there (absent).
		`INSERT INTO connections (id, folder_id, name, hostname, favorite) VALUES ('`+f.connID+`', '`+f.folderID+`', 'renamed there', 'evil.example.com', 1)`,
		`INSERT INTO connections (id, folder_id, name, hostname) VALUES ('`+f.localConn.ID+`', '`+f.localFold.ID+`', 'own', 'own.example.com')`,
		// A connection created there inside the catalog folder.
		`INSERT INTO connections (id, folder_id, name, hostname) VALUES ('new-conn', '`+f.folderID+`', 'made there', 'x.example.com')`,
		// The catalog forward with its local port changed and auto start on.
		`INSERT INTO port_forwards (id, connection_id, kind, local_port, remote_host, remote_port, auto_start, browser_mode)
			VALUES ('`+f.fwdID+`', '`+f.connID+`', 'local', 1, '10.9.9.9', 1, 1, 'isolated')`,
		// A forward the user added there to the catalog connection, and one
		// on a connection that does not exist any more.
		`INSERT INTO port_forwards (id, connection_id, kind, remote_host, remote_port) VALUES ('added-there', '`+f.connID+`', 'local', '127.0.0.1', 8080)`,
		`INSERT INTO port_forwards (id, connection_id, kind, remote_host, remote_port) VALUES ('orphan', 'gone-conn', 'local', '127.0.0.1', 8080)`,
	)

	info, err := db.MirrorFromWith(snap, ShadowTrust{FromSync: true})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if !info.Legacy || !info.KeptCatalog {
		t.Fatalf("mirror info: %+v", info)
	}

	// The attachment and the user's catalog data are intact.
	if cats, _ := db.ListCatalogs(); len(cats) != 1 {
		t.Fatalf("catalog attachment lost: %+v", cats)
	}
	if bs, _ := db.ListCatalogBindings(f.cat.ID); len(bs) != 1 {
		t.Errorf("bindings lost: %+v", bs)
	}
	if os, _ := db.ListCatalogOverrides(f.cat.ID); len(os) != 1 {
		t.Errorf("local layers lost: %+v", os)
	}
	// Catalog rows kept as the catalog has them.
	c, err := db.GetConnection(f.connID)
	if err != nil || c.Name != "web-1" || c.Hostname != "web-1.example.com" || c.RemoteUUID == nil {
		t.Fatalf("catalog connection not kept: %+v %v", c, err)
	}
	if !c.Favorite {
		t.Error("favourite set on the old machine not adopted")
	}
	if _, err := db.GetConnection(conn2); err != nil {
		t.Error("catalog connection deleted on the old machine was removed here")
	}
	fold, _ := db.GetFolder(f.folderID)
	if fold.Name != "Customer A" || fold.CatalogID == nil {
		t.Errorf("catalog folder not kept: %+v", fold)
	}
	fw, _ := db.GetPortForward(f.fwdID)
	if fw.LocalPort == nil || *fw.LocalPort != 15432 || *fw.RemoteHost != "127.0.0.1" {
		t.Errorf("catalog forward changed: %+v", fw)
	}
	if !fw.AutoStart || fw.BrowserMode != "isolated" {
		t.Errorf("forward local settings not adopted: %+v", fw)
	}
	// No duplicates of anything.
	if n := countWhere(t, db, `SELECT COUNT(*) FROM connections WHERE name = 'renamed there' OR hostname = 'evil.example.com'`); n != 0 {
		t.Errorf("demoted copy came back as a local row (%d)", n)
	}
	if n := countWhere(t, db, `SELECT COUNT(*) FROM folders WHERE name = 'edited there'`); n != 0 {
		t.Errorf("demoted folder copy came back (%d)", n)
	}
	// Rows made there inside the catalog folder land at the top level.
	nf, err := db.GetFolder("new-folder")
	if err != nil || nf.ParentID != nil {
		t.Errorf("folder made there not moved to the top: %+v %v", nf, err)
	}
	nc, err := db.GetConnection("new-conn")
	if err != nil || nc.FolderID != nil {
		t.Errorf("connection made there not moved to the top: %+v %v", nc, err)
	}
	// Forwards: the own one is kept, the one added there is on the
	// catalog connection, the orphan is dropped.
	// The user's own forward on a catalog connection never reaches an
	// older app (shadowing), so its absence there removes nothing here.
	if _, err := db.GetPortForward(ownFwd.ID); err != nil {
		t.Error("own forward on a catalog connection lost to an older snapshot")
	}
	if added, err := db.GetPortForward("added-there"); err != nil || added.ConnectionID != f.connID {
		t.Errorf("forward added on the old machine missing: %+v %v", added, err)
	}
	if _, err := db.GetPortForward("orphan"); err == nil {
		t.Error("orphaned forward inserted")
	}
	// Local rows still mirror as before.
	if lc, err := db.GetConnection(f.localConn.ID); err != nil || lc.CatalogID != nil {
		t.Errorf("local connection: %+v %v", lc, err)
	}
	// Guard on, marker gone, FKs consistent.
	if n := countWhere(t, db, `SELECT COUNT(*) FROM catalog_writer`); n != 0 {
		t.Fatal("writer marker left behind")
	}
	name := "x"
	_, err = db.UpdateConnection(UpdateConnection{ID: f.connID, Name: &name})
	wantOwned(t, "guard after legacy mirror", err)
	rows, err := db.conn.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign key violations after the legacy mirror")
	}
}
