package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

var (
	ownPNG = []byte("\x89PNG\r\n\x1a\nown-logo")
	catPNG = []byte("\x89PNG\r\n\x1a\ncatalog-logo")
)

// A catalog image whose bytes the user already holds maps to their own
// row (md5 is unique); new bytes get catimg-<sha256>. Rows from before the
// hash column are found after a backfill.
func TestCatalogImageCache(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	own, err := db.PutImage(ownPNG, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE images SET sha256 = NULL`); err != nil {
		t.Fatal(err)
	}
	if id, err := db.ImageIDBySHA256(sha256Hex(ownPNG)); err != nil || id != own {
		t.Fatalf("own image by hash after backfill: %q %v", id, err)
	}
	if id, err := db.PutCatalogImage(sha256Hex(ownPNG), "image/png", ownPNG); err != nil || id != own {
		t.Fatalf("same bytes from the catalog: %q %v, want %s", id, err, own)
	}
	cid, err := db.PutCatalogImage(sha256Hex(catPNG), "image/png", catPNG)
	if err != nil || cid != CatalogImagePrefix+sha256Hex(catPNG) {
		t.Fatalf("new catalog image: %q %v", cid, err)
	}
	if again, _ := db.PutCatalogImage(sha256Hex(catPNG), "image/png", catPNG); again != cid {
		t.Fatalf("second put: %q", again)
	}

	// Prune: kept while a record names it, or a row uses it.
	if n, err := db.PruneCatalogImages(map[string]bool{sha256Hex(catPNG): true}); err != nil || n != 0 {
		t.Fatalf("prune with a reference: %d %v", n, err)
	}
	c, err := db.CreateConnection(NewConnection{Name: "own", Hostname: "own.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConnectionIcon(c.ID, cid); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.PruneCatalogImages(nil); n != 0 {
		t.Fatalf("pruned an image a row uses: %d", n)
	}
	if err := db.SetConnectionIcon(c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.PruneCatalogImages(nil); n != 1 {
		t.Fatalf("unused catalog image not pruned: %d", n)
	}
	if _, _, ok, _ := db.GetImage(own); !ok {
		t.Fatal("prune touched the user's own image")
	}
}

// Snapshots leave cached catalog images out (every machine fetches its
// own); a live pull keeps this machine's cache.
func TestCatalogImagesStayOutOfSnapshots(t *testing.T) {
	f, _, _ := shadowFixture(t)
	cid, err := f.db.PutCatalogImage(sha256Hex(catPNG), "image/png", catPNG)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.db.PutCatalogImage(sha256Hex(ownPNG), "image/png", ownPNG)
	if err != nil {
		t.Fatal(err)
	}
	err = f.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET icon_image_id = ? WHERE id = ?`, cid, f.connID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A local row wearing a cached catalog image keeps it in the snapshot.
	if err := f.db.SetConnectionIcon(f.localConn.ID, other); err != nil {
		t.Fatal(err)
	}
	snap := vacuumCopy(t, f.db)
	if err := ShadowSnapshot(snap, "n-1"); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM images WHERE id = '` + cid + `'`:                                                     0,
		`SELECT COUNT(*) FROM images WHERE id = '` + other + `'`:                                                   1,
		`SELECT COUNT(*) FROM catalog_shadow_connections WHERE icon_image_id IS NOT NULL`:                          0,
		`SELECT COUNT(*) FROM connections WHERE id = '` + f.localConn.ID + `' AND icon_image_id = '` + other + `'`: 1,
	} {
		if got := rawCount(t, snap, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}

	// Pull it onto a machine with its own cache: the cache stays, the
	// snapshot's copy of the same bytes replaces the cached one.
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	keep, err := dst.PutCatalogImage(sha256Hex(catPNG), "image/png", catPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.PutCatalogImage(sha256Hex(ownPNG), "image/png", ownPNG); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.MirrorFromWith(snap, ShadowTrust{FromSync: true, Nonce: "n-1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := dst.GetImage(keep); !ok {
		t.Error("the pull dropped this machine's catalog image cache")
	}
	if id, _ := dst.ImageIDBySHA256(sha256Hex(ownPNG)); id != other {
		t.Errorf("same bytes: %q, want the snapshot's %s", id, other)
	}
	if n := countWhere(t, dst, `SELECT COUNT(*) FROM images WHERE id LIKE 'catimg-%'`); n != 2 || !strings.HasPrefix(other, CatalogImagePrefix) {
		t.Errorf("catimg rows after pull: %d", n)
	}
}
