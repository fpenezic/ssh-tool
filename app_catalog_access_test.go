package main

import (
	"database/sql"
	"testing"

	"ssh-tool/internal/catalog"
)

// Catalog revision 23: a connection carries its own effective access, so
// a connection under a folder the user only reads (or sees as a skeleton)
// can be editable, its forwards with it; without the field (older
// servers) the folder decides.
func TestCatalogRecordEditable(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	setConn := func(access string) {
		t.Helper()
		raw := `{"uuid":"c-uuid","rev":1,"folder_uuid":"f-uuid","name":"web-1","hostname":"web-1.example.com"` + access + `}`
		if err := a.db.WithCatalogWriter(func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE connections SET catalog_json = ? WHERE id = ?`, raw, ca.connID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setFolder := func(access string) {
		t.Helper()
		if err := a.db.WithCatalogWriter(func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE folders SET catalog_json = ? WHERE id = 'cf'`,
				`{"uuid":"f-uuid","rev":1,"parent_uuid":null,"name":"Customer A","access":"`+access+`"}`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	editable := func() bool {
		_, _, fraw, err := a.db.CatalogFolderOf(catalog.KindConnections, ca.connID)
		if err != nil {
			t.Fatal(err)
		}
		return a.catalogRecordEditable(catalog.KindConnections, ca.connID, fraw)
	}

	cases := []struct {
		folder, conn string
		want         bool
	}{
		{"skeleton", `,"access":"write"`, true},
		{"read", `,"access":"write"`, true},
		{"read", `,"access":"manage"`, true},
		{"write", `,"access":"read"`, false},
		{"read", ``, false},  // older server: the folder decides
		{"write", ``, true},  // older server: the folder decides
		{"manage", ``, true}, // older server: the folder decides
	}
	for _, c := range cases {
		setFolder(c.folder)
		setConn(c.conn)
		if got := editable(); got != c.want {
			t.Errorf("folder %s, connection%s: editable %v, want %v", c.folder, c.conn, got, c.want)
		}
	}

}
