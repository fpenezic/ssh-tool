package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vacuumCopy(t *testing.T, db *DB) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "snap.db")
	if _, err := db.conn.Exec(`VACUUM INTO ?`, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func rawCount(t *testing.T, path, q string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func layoutOf(conns ...string) string {
	var sessions []any
	var root any
	for i, id := range conns {
		sessions = append(sessions, map[string]any{"kind": "ssh", "connectionId": id})
		leaf := map[string]any{"kind": "pane", "ref": i}
		if root == nil {
			root = leaf
		} else {
			root = map[string]any{"kind": "split", "direction": "vertical", "ratio": 0.5, "a": root, "b": leaf}
		}
	}
	b, _ := json.Marshal(map[string]any{"version": 2, "tabs": []any{map[string]any{"sessions": sessions, "root": root}}})
	return string(b)
}

// shadowFixture: the catalog fixture plus a local forward on the catalog
// connection and a workspace with a local and a catalog pane.
func shadowFixture(t *testing.T) (*catalogFixture, string, string) {
	f := newCatalogFixture(t)
	rh, rp := "127.0.0.1", uint16(22)
	own, err := f.db.CreatePortForward(NewPortForward{ConnectionID: f.connID, Kind: "local", RemoteHost: &rh, RemotePort: &rp})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.db.CreateWorkspace("ops", layoutOf(f.localConn.ID, f.connID))
	if err != nil {
		t.Fatal(err)
	}
	return f, own.ID, ws.ID
}

func TestShadowSnapshotMovesCatalogRowsOut(t *testing.T) {
	f, ownFwd, wsID := shadowFixture(t)
	snap := vacuumCopy(t, f.db)
	if err := ShadowSnapshot(snap, "n-1"); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM folders WHERE catalog_id IS NOT NULL`:                     0,
		`SELECT COUNT(*) FROM connections WHERE catalog_id IS NOT NULL`:                 0,
		`SELECT COUNT(*) FROM port_forwards WHERE connection_id = '` + f.connID + `'`:   0,
		`SELECT COUNT(*) FROM catalog_shadow_folders`:                                   2,
		`SELECT COUNT(*) FROM catalog_shadow_connections`:                               1,
		`SELECT COUNT(*) FROM catalog_shadow_port_forwards`:                             2,
		`SELECT COUNT(*) FROM catalog_shadow_workspaces`:                                1,
		`SELECT COUNT(*) FROM connections WHERE id = '` + f.localConn.ID + `'`:          1,
		`SELECT COUNT(*) FROM catalog_writer`:                                           0,
		`SELECT COUNT(*) FROM catalog_shadow_port_forwards WHERE id = '` + ownFwd + `'`: 1,
	} {
		if got := rawCount(t, snap, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	db, _ := sql.Open("sqlite", snap+"?mode=ro")
	defer db.Close()
	var layout, nonce string
	_ = db.QueryRow(`SELECT layout_json FROM workspaces WHERE id = ?`, wsID).Scan(&layout)
	if strings.Contains(layout, f.connID) || !strings.Contains(layout, f.localConn.ID) {
		t.Errorf("workspace not stripped to its local pane: %s", layout)
	}
	_ = db.QueryRow(`SELECT nonce FROM catalog_shadow_meta`).Scan(&nonce)
	if nonce != "n-1" {
		t.Errorf("nonce %q", nonce)
	}
	// The live store is untouched.
	if n := countWhere(t, f.db, `SELECT COUNT(*) FROM connections WHERE catalog_id IS NOT NULL`); n != 1 {
		t.Errorf("live store lost catalog rows: %d", n)
	}
}

// Live pull of a shadowed snapshot onto another current machine: the
// catalog comes back whole, the workspace with its catalog pane.
func TestMirrorTakesTrustedShadowBack(t *testing.T) {
	f, ownFwd, wsID := shadowFixture(t)
	snap := vacuumCopy(t, f.db)
	if err := ShadowSnapshot(snap, "n-1"); err != nil {
		t.Fatal(err)
	}
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	info, err := dst.MirrorFromWith(snap, ShadowTrust{FromSync: true, Nonce: "n-1"})
	if err != nil || info.Legacy {
		t.Fatalf("mirror: %+v %v", info, err)
	}
	if c, err := dst.GetConnection(f.connID); err != nil || c.RemoteUUID == nil {
		t.Fatalf("catalog connection not taken back: %+v %v", c, err)
	}
	if fw, err := dst.GetPortForward(ownFwd); err != nil || fw.ConnectionID != f.connID {
		t.Fatalf("own forward not taken back: %+v %v", fw, err)
	}
	ws, _ := dst.GetWorkspace(wsID)
	if ws == nil || !strings.Contains(ws.LayoutJSON, f.connID) {
		t.Fatalf("workspace not restored to its full layout: %+v", ws)
	}
	if n := countWhere(t, dst, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'catalog_shadow_%'`); n != 0 {
		t.Errorf("shadow tables leaked into the live store: %d", n)
	}
	name := "x"
	_, err = dst.UpdateConnection(UpdateConnection{ID: f.connID, Name: &name})
	wantOwned(t, "guard after unshadow", err)
}

// A shadow whose nonce does not match the pull's meta (an older app that
// cold-restored a new snapshot carries it along) is not taken back.
func TestMirrorIgnoresStaleShadow(t *testing.T) {
	src, _, _ := shadowFixture(t)
	snap := vacuumCopy(t, src.db)
	if err := ShadowSnapshot(snap, "old-nonce"); err != nil {
		t.Fatal(err)
	}
	dst := newCatalogFixture(t) // its own catalog, different ids
	info, err := dst.db.MirrorFromWith(snap, ShadowTrust{FromSync: true, Nonce: ""})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Legacy || !info.KeptCatalog {
		t.Fatalf("stale shadow trusted: %+v", info)
	}
	if _, err := dst.db.GetConnection(src.connID); err == nil {
		t.Error("stale shadow rows were taken back")
	}
	if _, err := dst.db.GetConnection(dst.connID); err != nil {
		t.Error("own catalog rows lost")
	}
	// A local backup trusts its own shadow.
	if ok, _ := SnapshotIsTrusted(snap, ShadowTrust{}); !ok {
		t.Error("backup shadow not trusted")
	}
}

// Restart path, trusted: the file is staged as is and Open takes the
// catalog rows back.
func TestColdPathTrustedShadowUnshadowsOnOpen(t *testing.T) {
	f, ownFwd, wsID := shadowFixture(t)
	snap := vacuumCopy(t, f.db)
	if err := ShadowSnapshot(snap, "n-1"); err != nil {
		t.Fatal(err)
	}
	other := newCatalogFixture(t)
	out, cleanup, err := other.db.PrepareStagedStore(snap, ShadowTrust{FromSync: true, Nonce: "n-1"})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil || out != snap {
		t.Fatalf("trusted snapshot not staged as is: %q %v", out, err)
	}
	// The swap: the staged file becomes the live store.
	db, err := Open(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if c, err := db.GetConnection(f.connID); err != nil || c.CatalogID == nil {
		t.Fatalf("catalog connection not back after open: %+v %v", c, err)
	}
	if _, err := db.GetPortForward(ownFwd); err != nil {
		t.Fatal("own forward not back after open")
	}
	ws, _ := db.GetWorkspace(wsID)
	if ws == nil || !strings.Contains(ws.LayoutJSON, f.connID) {
		t.Fatalf("workspace not restored on open: %+v", ws)
	}
	if n := countWhere(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'catalog_shadow_%'`); n != 0 {
		t.Error("shadow tables left after open")
	}
}

// Restart path, untrusted snapshot, this machine has catalog data: a
// merged copy is staged, so the swap keeps the catalog.
func TestColdPathLegacyStagesMergedCopy(t *testing.T) {
	f := newCatalogFixture(t)
	wantOK(t, "binding", f.db.SetCatalogBinding(f.cat.ID, "s1", "cred", "c1"))
	snap := filepath.Join(t.TempDir(), "legacy.db")
	legacySnapshot(t, snap,
		`INSERT INTO connections (id, name, hostname) VALUES ('from-old', 'from old', 'o.example.com')`)
	out, cleanup, err := f.db.PrepareStagedStore(snap, ShadowTrust{FromSync: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if out == snap {
		t.Fatal("legacy snapshot staged as is while this machine has catalog data")
	}
	staged, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if _, err := staged.GetConnection(f.connID); err != nil {
		t.Fatal("catalog row missing from the merged copy")
	}
	if _, err := staged.GetConnection("from-old"); err != nil {
		t.Fatal("snapshot's own row missing from the merged copy")
	}
	if bs, _ := staged.ListCatalogBindings(f.cat.ID); len(bs) != 1 {
		t.Fatal("binding missing from the merged copy")
	}
}

// Without catalog data here the restart path is today's, byte for byte.
func TestColdPathWithoutCatalogIsUnchanged(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "plain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snap := filepath.Join(t.TempDir(), "legacy.db")
	legacySnapshot(t, snap, `INSERT INTO connections (id, name, hostname) VALUES ('a', 'a', 'h')`)
	before, _ := os.ReadFile(snap)
	out, cleanup, err := db.PrepareStagedStore(snap, ShadowTrust{FromSync: true})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil || out != snap {
		t.Fatalf("staged %q, %v", out, err)
	}
	after, _ := os.ReadFile(snap)
	if !bytes.Equal(before, after) {
		t.Fatal("snapshot changed on the no-catalog path")
	}
	// A stale shadow in it is dropped so Open does not take it back.
	src, _, _ := shadowFixture(t)
	stale := vacuumCopy(t, src.db)
	if err := ShadowSnapshot(stale, "stale"); err != nil {
		t.Fatal(err)
	}
	out, _, err = db.PrepareStagedStore(stale, ShadowTrust{FromSync: true})
	if err != nil || out != stale {
		t.Fatal(err)
	}
	if n := rawCount(t, stale, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'catalog_shadow_%'`); n != 0 {
		t.Errorf("stale shadow tables kept: %d", n)
	}
}

// An older app whole-file-restoring a shadowed snapshot opens it with its
// own code: its tree tables simply hold no catalog rows.
func TestOldAppWholeFileRestoreSeesNoCatalogRows(t *testing.T) {
	f, _, _ := shadowFixture(t)
	snap := vacuumCopy(t, f.db)
	if err := ShadowSnapshot(snap, "n-1"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM folders WHERE id = '` + f.folderID + `' OR id = '` + f.cat.RootFolderID + `'`,
		`SELECT COUNT(*) FROM connections WHERE id = '` + f.connID + `'`,
		`SELECT COUNT(*) FROM port_forwards WHERE connection_id NOT IN (SELECT id FROM connections)`,
	} {
		if n := rawCount(t, snap, q); n != 0 {
			t.Errorf("%s = %d", q, n)
		}
	}
	// Its own writes work (the triggers only bite catalog rows).
	db, _ := sql.Open("sqlite", snap)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO connections (id, name, hostname, created_at, updated_at) VALUES ('o', 'o', 'h', 0, 0)`); err != nil {
		t.Fatalf("old app's insert refused: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM connections`); err != nil {
		t.Fatalf("old app's mirror-style delete refused: %v", err)
	}
}

func TestStripWorkspaceLayout(t *testing.T) {
	cat := map[string]bool{"c": true}
	full := layoutOf("l", "c", "l2")
	got, changed := StripWorkspaceLayout(full, cat)
	if !changed {
		t.Fatal("not changed")
	}
	var v struct {
		Tabs []struct {
			Sessions []map[string]any `json:"sessions"`
			Root     map[string]any   `json:"root"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Tabs) != 1 || len(v.Tabs[0].Sessions) != 2 {
		t.Fatalf("sessions not renumbered: %s", got)
	}
	if strings.Contains(got, `"c"`) {
		t.Fatalf("catalog session left: %s", got)
	}
	// Only catalog panes: the tab goes.
	got, _ = StripWorkspaceLayout(layoutOf("c"), cat)
	if strings.Contains(got, "sessions") {
		t.Fatalf("empty tab kept: %s", got)
	}
	// Version 1 tabs.
	got, changed = StripWorkspaceLayout(`{"version":1,"tabs":[{"connectionId":"c"},{"connectionId":"l"}]}`, cat)
	if !changed || strings.Contains(got, `"c"`) || !strings.Contains(got, `"l"`) {
		t.Fatalf("v1: %s", got)
	}
	// Nothing to strip: unchanged input.
	in := layoutOf("l")
	if out, changed := StripWorkspaceLayout(in, cat); changed || out != in {
		t.Fatal("layout without catalog panes changed")
	}
}
