package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// localiseFixture: the catalog fixture with an unbound credential slot on
// web-1, a database login through a catalog-made credential, a second
// catalog connection jumping through web-1, and an empty skeleton folder.
func localiseFixture(t *testing.T) (*catalogFixture, string) {
	t.Helper()
	f := newCatalogFixture(t)
	catcred := CatalogCredentialID(f.cat.ID, "s9")
	if err := f.db.EnsureCatalogCredentialFolder(f.cat.ID, "Team"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.UpsertCatalogCredential(f.cat.ID, "s9", "bw-db", CredPassword, map[string]any{"bitwarden_ref": map[string]any{"cipher_id": "c1"}}); err != nil {
		t.Fatal(err)
	}
	ov := `{"port":2222,"auth_ref":"cat-unbound:` + f.cat.ID + `/s1","db_credential_ref":"` + catcred + `"}`
	err := f.db.WithCatalogWriter(func(tx *sql.Tx) error {
		if err := f.db.UpsertCatalogSlotTx(tx, CatalogSlot{CatalogID: f.cat.ID, UUID: "s1", Rev: 1, Type: "cred", Name: "customer-a-admin", Kind: "slot"}); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE connections SET overrides_json = ?, notes = 'ask ops' WHERE id = ?`, ov, f.connID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, overrides_json, created_at, updated_at, catalog_id, remote_uuid, remote_rev)
			VALUES ('c2', ?, 'db-1', 'db-1.example.com', ?, 1, 1, ?, 'c2-uuid', 1)`, f.folderID,
			`{"jump_host":{"kind":"chain","chain":{"connection_id":"`+f.connID+`"}}}`, f.cat.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO folders (id, parent_id, name, settings_json, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			VALUES ('sk', ?, 'Outline', '{}', 1, 1, ?, 'sk-uuid', 1, '{"access":"skeleton"}')`, f.cat.RootFolderID, f.cat.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetCatalogOverride(f.cat.ID, "c-uuid", "connections", `{"port":2222}`); err != nil {
		t.Fatal(err)
	}
	return f, catcred
}

func overridesOf(t *testing.T, db *DB, id string) map[string]any {
	t.Helper()
	c, err := db.GetConnection(id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c.Overrides)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func TestKeepCatalogLocal(t *testing.T) {
	f, catcred := localiseFixture(t)
	res, err := f.db.KeepCatalogLocal(f.cat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Connections != 2 || res.Forwards != 1 || res.Credentials != 1 || len(res.Emptied) != 1 {
		t.Fatalf("result: %+v", res)
	}
	if e := res.Emptied[0]; e.ID != f.connID || e.Field != "Credential" || e.Slot != "customer-a-admin" {
		t.Fatalf("emptied: %+v", e)
	}
	c, _ := f.db.GetConnection(f.connID)
	if c.CatalogID != nil || c.RemoteUUID != nil || c.FolderID == nil || *c.FolderID != f.folderID {
		t.Fatalf("web-1 not a local row in place: %+v", c)
	}
	if !strings.Contains(c.Notes, "ask ops") || !strings.Contains(c.Notes, `"customer-a-admin"`) {
		t.Fatalf("notes: %q", c.Notes)
	}
	m := overridesOf(t, f.db, f.connID)
	if _, has := m["auth_ref"]; has {
		t.Fatalf("unbound slot kept: %v", m)
	}
	nid, _ := m["db_credential_ref"].(string)
	if nid == "" || IsCatalogCredential(nid) || m["port"] == nil {
		t.Fatalf("overrides: %v", m)
	}
	if cr, err := f.db.GetCredential(nid); err != nil || cr.Name != "bw-db" {
		t.Fatalf("ordinary credential: %+v %v", cr, err)
	}
	if _, err := f.db.GetCredential(catcred); err == nil {
		t.Fatal("catalog-made credential still there")
	}
	// The jump host through web-1 still points at it (same ids).
	if j := overridesOf(t, f.db, "c2")["jump_host"]; !strings.Contains(mustJSON(j), f.connID) {
		t.Fatalf("jump host: %v", j)
	}
	if fw, _ := f.db.GetPortForward(f.fwdID); fw == nil || fw.CatalogID != nil {
		t.Fatalf("forward: %+v", fw)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM folders WHERE id = 'sk'`:                                                              0,
		`SELECT COUNT(*) FROM folders WHERE catalog_id IS NOT NULL`:                                                 0,
		`SELECT COUNT(*) FROM catalogs`:                                                                             0,
		`SELECT COUNT(*) FROM catalog_slots`:                                                                        0,
		`SELECT COUNT(*) FROM catalog_overrides`:                                                                    0,
		`SELECT COUNT(*) FROM folders WHERE id = '` + f.cat.RootFolderID + `'`:                                      1,
		`SELECT COUNT(*) FROM folders WHERE id = '` + f.folderID + `' AND parent_id = '` + f.cat.RootFolderID + `'`: 1,
	} {
		if got := countWhere(t, f.db, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	// A plain local row now: the guard lets the user edit it.
	name := "web-1-mine"
	if _, err := f.db.UpdateConnection(UpdateConnection{ID: f.connID, Name: &name}); err != nil {
		t.Fatalf("edit after keep: %v", err)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestCopyCatalogRows(t *testing.T) {
	f, catcred := localiseFixture(t)
	res, copied, err := f.db.CopyCatalogRows(CopySource{Kind: "folders", ID: f.folderID, Inherited: `{"username":"deploy"}`}, f.localFold.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Folders != 1 || res.Connections != 2 || res.Forwards != 1 || len(copied) != 2 || len(res.Emptied) != 1 || len(res.OutsideJumps) != 0 {
		t.Fatalf("result: %+v copied %v", res, copied)
	}
	top, err := f.db.GetFolder(res.RootID)
	if err != nil || top.CatalogID != nil || top.ParentID == nil || *top.ParentID != f.localFold.ID || top.Name != "Customer A" {
		t.Fatalf("copied folder: %+v %v", top, err)
	}
	if top.Settings.Username == nil || *top.Settings.Username != "deploy" {
		t.Fatalf("inherited not folded: %+v", top.Settings)
	}
	newWeb, newDB := "", ""
	for _, c := range copied {
		if c.From == f.connID {
			newWeb = c.To
		} else {
			newDB = c.To
		}
	}
	// The jump host inside the copy points at the copy.
	if j := mustJSON(overridesOf(t, f.db, newDB)["jump_host"]); !strings.Contains(j, newWeb) {
		t.Fatalf("copied jump host: %s", j)
	}
	m := overridesOf(t, f.db, newWeb)
	if nid, _ := m["db_credential_ref"].(string); nid == "" || IsCatalogCredential(nid) {
		t.Fatalf("copied credential ref: %v", m)
	}
	// The catalog is untouched.
	if c, _ := f.db.GetConnection(f.connID); c.CatalogID == nil {
		t.Fatal("original left the catalog")
	}
	if _, err := f.db.GetCredential(catcred); err != nil {
		t.Fatal("catalog-made credential gone after a copy")
	}
	if n := countWhere(t, f.db, `SELECT COUNT(*) FROM port_forwards WHERE connection_id = ? AND catalog_id IS NULL`, newWeb); n != 1 {
		t.Fatalf("copied forwards: %d", n)
	}
	// Independent: a catalog change does not reach the copy.
	err = f.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET hostname = 'moved.example.com' WHERE id = ?`, f.connID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := f.db.GetConnection(newWeb); c.Hostname != "web-1.example.com" {
		t.Fatalf("copy followed the catalog: %s", c.Hostname)
	}

	// A single connection, twice into the same folder: names stay unique,
	// and a jump host outside the copy is reported.
	r1, _, err := f.db.CopyCatalogRows(CopySource{Kind: "connections", ID: "c2"}, f.localFold.ID)
	if err != nil || len(r1.OutsideJumps) != 1 {
		t.Fatalf("copy c2: %+v %v", r1, err)
	}
	r2, _, err := f.db.CopyCatalogRows(CopySource{Kind: "connections", ID: "c2"}, f.localFold.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := f.db.GetConnection(r2.RootID)
	if c.Name != "db-1 (copy)" {
		t.Fatalf("second copy name: %q", c.Name)
	}
	if _, _, err := f.db.CopyCatalogRows(CopySource{Kind: "connections", ID: "c2"}, f.folderID); err == nil {
		t.Fatal("copied into a catalog folder")
	}
}

// Keeping a removed connection local gives it its own copy of a
// catalog-made credential; the catalog's goes with the catalog.
func TestKeepRemovedConnectionOwnsCredential(t *testing.T) {
	f, catcred := localiseFixture(t)
	err := f.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET catalog_state = 'removed' WHERE id = ?`, f.connID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := f.db.GetConnection(f.connID)
	if err := f.db.KeepRemovedConnectionLocal(f.connID, c.Overrides); err != nil {
		t.Fatal(err)
	}
	m := overridesOf(t, f.db, f.connID)
	nid, _ := m["db_credential_ref"].(string)
	if nid == "" || IsCatalogCredential(nid) {
		t.Fatalf("still the catalog's credential: %v", m)
	}
	if _, err := f.db.GetCredential(catcred); err != nil {
		t.Fatal("the catalog's own credential went with a single keep")
	}
}
