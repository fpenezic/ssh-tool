package syncer

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/backup"
	"ssh-tool/internal/store"
)

// Profile sync between this version and the released v1.2.1 at the same
// time, over one remote. The released side is its own schema and live-pull
// mirror, copied verbatim (released_v121_test.go); the current side is the
// real Push, Extract and MirrorFromWith, plus the auto-sync decisions the
// app makes (dirty by fingerprint, the catalog repush marker).

const mvPass = "pw"

type mvRemote struct {
	dav  *WebDAV
	fake *fakeDAV
	down bool
}

func newMVRemote(t *testing.T) *mvRemote {
	fake := &fakeDAV{files: map[string][]byte{}}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	return &mvRemote{dav: &WebDAV{BaseURL: srv.URL + "/dav/", Username: "u", Password: "p"}, fake: fake}
}

func (r *mvRemote) gen(t *testing.T) int64 {
	m, err := FetchMeta(r.dav)
	if err == ErrNotFound {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return m.Generation
}

// mvNew is a machine on the current version.
type mvNew struct {
	name, store, vault string
	db                 *store.DB
	gen                int64
	stamp              string
	marker             bool
	pushes             int
}

func newMVNew(t *testing.T, name string) *mvNew {
	dir := t.TempDir()
	m := &mvNew{name: name, store: filepath.Join(dir, "store.db"), vault: filepath.Join(dir, "vault.enc")}
	var err error
	if m.db, err = store.Open(m.store); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.db.Close() })
	if err := os.WriteFile(m.vault, []byte("vault-"+name), 0o600); err != nil {
		t.Fatal(err)
	}
	// A fresh install counts as synced to nothing: clean until it is edited.
	m.stamp = m.db.ContentFingerprint()
	return m
}

func (m *mvNew) dirty() bool { return m.db.ContentFingerprint() != m.stamp }

func (m *mvNew) push(t *testing.T, r *mvRemote) error {
	if r.down {
		return fmt.Errorf("offline")
	}
	res, err := Push(r.dav, m.store, m.vault, mvPass, "new", m.name, m.gen, false, nil)
	if err != nil {
		return err
	}
	m.gen = res.Generation
	m.stamp = m.db.ContentFingerprint()
	m.marker = false
	m.pushes++
	return nil
}

func pullSnapshot(t *testing.T, r *mvRemote) (*Meta, string) {
	t.Helper()
	meta, err := FetchMeta(r.dav)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := r.dav.Get(snapshotName)
	if err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "snap.stb")
	if err := os.WriteFile(env, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	storeTmp, vaultTmp, err := backup.Extract(env, mvPass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(storeTmp); os.Remove(vaultTmp) })
	return meta, storeTmp
}

// pull is SyncPullLive's store half.
func (m *mvNew) pull(t *testing.T, r *mvRemote) {
	meta, snap := pullSnapshot(t, r)
	info, err := m.db.MirrorFromWith(snap, store.ShadowTrust{FromSync: true, Nonce: meta.CatalogShadow})
	if err != nil {
		t.Fatalf("%s pull: %v", m.name, err)
	}
	m.gen = meta.Generation
	m.stamp = m.db.ContentFingerprint()
	m.marker = info.KeptCatalog
}

// tick is autoSyncTick with auto-apply on and the quiet period elapsed.
func (m *mvNew) tick(t *testing.T, r *mvRemote) {
	if r.down {
		if m.marker {
			push, drop := CatalogRepushAction(true, m.db.HasCatalogData(), -1, m.gen)
			if push || drop {
				t.Fatalf("%s: offline marker must wait, got push=%v drop=%v", m.name, push, drop)
			}
		}
		return
	}
	if m.dirty() {
		if err := m.push(t, r); err != nil && !IsGenerationConflict(err) {
			t.Fatalf("%s push: %v", m.name, err)
		}
	} else if m.marker {
		push, drop := CatalogRepushAction(true, m.db.HasCatalogData(), r.gen(t), m.gen)
		if drop {
			m.marker = false
		}
		if push {
			if err := m.push(t, r); err != nil {
				if !IsGenerationConflict(err) {
					t.Fatalf("%s repush: %v", m.name, err)
				}
				m.marker = false
			}
		}
	}
	if !m.dirty() && r.gen(t) > m.gen {
		m.pull(t, r)
	}
}

// mvOld is a machine on v1.2.1: its own schema, its mirror, its push (no
// shadowing, a meta.json without the catalog nonce). It is dirty only
// when the test edits it.
type mvOld struct {
	store, vault string
	db           *sql.DB
	gen          int64
	dirty        bool
	pushes       int
}

func newMVOld(t *testing.T) *mvOld {
	dir := t.TempDir()
	o := &mvOld{store: filepath.Join(dir, "store.db"), vault: filepath.Join(dir, "vault.enc")}
	db, err := sql.Open("sqlite", o.store+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := rel_runMigrations(db); err != nil {
		t.Fatalf("released migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	o.db = db
	if err := os.WriteFile(o.vault, []byte("vault-old"), 0o600); err != nil {
		t.Fatal(err)
	}
	return o
}

func (o *mvOld) push(t *testing.T, r *mvRemote) {
	res, err := Push(r.dav, o.store, o.vault, mvPass, "1.2.1", "old", o.gen, false, nil)
	if err != nil {
		t.Fatalf("old push: %v", err)
	}
	// v1.2.1 writes meta.json without the catalog nonce.
	meta, _ := FetchMeta(r.dav)
	meta.CatalogShadow = ""
	raw, _ := json.Marshal(meta)
	if err := r.dav.Put(metaName, raw); err != nil {
		t.Fatal(err)
	}
	o.gen = res.Generation
	o.dirty = false
	o.pushes++
}

func (o *mvOld) pull(t *testing.T, r *mvRemote) {
	meta, snap := pullSnapshot(t, r)
	if err := relMirrorFrom(o.db, snap); err != nil {
		t.Fatalf("released app's pull failed: %v", err)
	}
	o.gen = meta.Generation
}

func (o *mvOld) tick(t *testing.T, r *mvRemote) {
	if r.down {
		return
	}
	if o.dirty {
		o.push(t, r)
	}
	if !o.dirty && r.gen(t) > o.gen {
		o.pull(t, r)
	}
}

func (o *mvOld) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := o.db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	o.dirty = true
}

func (o *mvOld) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := o.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// catalogSeed is what machine N1 holds of one catalog.
type catalogSeed struct {
	catID, rootID, folderID, connID, fwdID, ownFwdID, wsID string
}

func workspaceLayout(connIDs ...string) string {
	var sessions []map[string]any
	var root any
	for i, id := range connIDs {
		sessions = append(sessions, map[string]any{"kind": "ssh", "connectionId": id})
		leaf := map[string]any{"kind": "pane", "ref": i}
		if root == nil {
			root = leaf
		} else {
			root = map[string]any{"kind": "split", "direction": "horizontal", "ratio": 0.5, "a": root, "b": leaf}
		}
	}
	b, _ := json.Marshal(map[string]any{"version": 2, "tabs": []any{map[string]any{"title": "ops", "sessions": sessions, "root": root}}})
	return string(b)
}

func seedCatalog(t *testing.T, db *store.DB, localConnID string) catalogSeed {
	t.Helper()
	cat, err := db.CreateCatalog(store.NewCatalog{Name: "Team", BaseURL: "https://catalog.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s := catalogSeed{catID: cat.ID, rootID: cat.RootFolderID, folderID: "cat-folder-1", connID: "cat-conn-1", fwdID: "cat-fwd-1"}
	err = db.WithCatalogWriter(func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO folders (id, parent_id, name, settings_json, created_at, updated_at, catalog_id, remote_uuid, remote_rev)
			 VALUES ('cat-folder-1', '` + s.rootID + `', 'Customer A', '{}', 1, 1, '` + s.catID + `', 'f-uuid', 1)`,
			`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev)
			 VALUES ('cat-conn-1', 'cat-folder-1', 'web-1', 'web-1.example.com', 1, 1, '` + s.catID + `', 'c-uuid', 1)`,
			`INSERT INTO port_forwards (id, connection_id, kind, local_port, remote_host, remote_port, catalog_id, remote_uuid, remote_rev)
			 VALUES ('cat-fwd-1', 'cat-conn-1', 'local', 15432, '127.0.0.1', 5432, '` + s.catID + `', 'p-uuid', 1)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rh, rp := "127.0.0.1", uint16(8080)
	own, err := db.CreatePortForward(store.NewPortForward{ConnectionID: s.connID, Kind: "local", RemoteHost: &rh, RemotePort: &rp})
	if err != nil {
		t.Fatal(err)
	}
	s.ownFwdID = own.ID
	// A credential the catalog client made for an external reference.
	if err := db.EnsureCatalogCredentialFolder(s.catID, "Team"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCatalogCredential(s.catID, "ext-slot", "Team: web admin", store.CredPassword,
		map[string]any{"bitwarden_ref": map[string]any{"server_id": "srv", "cipher_id": "c", "field": "password"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCatalogBinding(s.catID, "slot-1", "cred", "cred-x"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCatalogOverride(s.catID, "c-uuid", "connections", `{"port":2222}`); err != nil {
		t.Fatal(err)
	}
	ws, err := db.CreateWorkspace("ops", workspaceLayout(localConnID, s.connID))
	if err != nil {
		t.Fatal(err)
	}
	s.wsID = ws.ID
	return s
}

// assertCatalogIntact: a current machine holds the whole catalog state.
func assertCatalogIntact(t *testing.T, label string, db *store.DB, s catalogSeed) {
	t.Helper()
	c, err := db.GetConnection(s.connID)
	if err != nil || c.CatalogID == nil || c.RemoteUUID == nil || *c.RemoteUUID != "c-uuid" || c.Name != "web-1" {
		t.Fatalf("%s: catalog connection: %+v %v", label, c, err)
	}
	if f, err := db.GetPortForward(s.fwdID); err != nil || f.RemoteUUID == nil {
		t.Fatalf("%s: catalog forward: %+v %v", label, f, err)
	}
	if f, err := db.GetPortForward(s.ownFwdID); err != nil || f.ConnectionID != s.connID {
		t.Fatalf("%s: own forward on the catalog connection: %+v %v", label, f, err)
	}
	if cats, _ := db.ListCatalogs(); len(cats) != 1 {
		t.Fatalf("%s: catalogs: %+v", label, cats)
	}
	if bs, _ := db.ListCatalogBindings(s.catID); len(bs) != 1 {
		t.Fatalf("%s: bindings: %+v", label, bs)
	}
	if os, _ := db.ListCatalogOverrides(s.catID); len(os) != 1 {
		t.Fatalf("%s: overrides: %+v", label, os)
	}
	if _, err := db.GetCredential(store.CatalogCredentialID(s.catID, "ext-slot")); err != nil {
		t.Fatalf("%s: catalog-made credential: %v", label, err)
	}
	var dups int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM connections WHERE name = 'web-1'`).Scan(&dups)
	if dups != 1 {
		t.Fatalf("%s: %d connections named web-1", label, dups)
	}
}

// assertOldSeesNothing: the released app holds no catalog row and nothing
// broken by one.
func assertOldSeesNothing(t *testing.T, label string, o *mvOld, s catalogSeed) {
	t.Helper()
	for _, q := range []string{
		`SELECT COUNT(*) FROM folders WHERE id IN ('` + s.rootID + `', 'cat-folder-1')`,
		`SELECT COUNT(*) FROM connections WHERE id = 'cat-conn-1'`,
		`SELECT COUNT(*) FROM port_forwards WHERE connection_id = 'cat-conn-1' OR id = 'cat-fwd-1'`,
		`SELECT COUNT(*) FROM port_forwards WHERE connection_id NOT IN (SELECT id FROM connections)`,
		`SELECT COUNT(*) FROM credential_refs WHERE id LIKE 'catcred-%'`,
		`SELECT COUNT(*) FROM credential_folders WHERE id LIKE 'catcred-%'`,
	} {
		if n := o.count(t, q); n != 0 {
			t.Fatalf("%s: old app holds catalog data (%s = %d)", label, q, n)
		}
	}
	var layout string
	if err := o.db.QueryRow(`SELECT layout_json FROM workspaces WHERE id = ?`, s.wsID).Scan(&layout); err == nil {
		if strings.Contains(layout, s.connID) {
			t.Fatalf("%s: old app's workspace opens a catalog connection: %s", label, layout)
		}
	}
}

func treeHash(t *testing.T, db *store.DB) string {
	t.Helper()
	h := sha256.New()
	for _, q := range []string{
		`SELECT * FROM folders ORDER BY id`, `SELECT * FROM connections ORDER BY id`,
		`SELECT * FROM port_forwards ORDER BY id`, `SELECT * FROM workspaces ORDER BY id`,
	} {
		rows, err := db.Conn().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		n := 0
		for rows.Next() {
			n++
			_ = rows.Scan(ptrs...)
			for _, v := range vals {
				fmt.Fprintf(h, "%v\x00", v)
			}
		}
		rows.Close()
		fmt.Fprintf(h, "rows=%d\n", n)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// settle ticks every machine until nothing pushes or pulls any more and
// returns the pushes made while settling.
func settle(t *testing.T, r *mvRemote, news []*mvNew, old *mvOld) int {
	t.Helper()
	before := 0
	for _, n := range news {
		before += n.pushes
	}
	before += old.pushes
	for i := 0; i < 12; i++ {
		g := r.gen(t)
		for _, n := range news {
			n.tick(t, r)
		}
		old.tick(t, r)
		quiet := r.gen(t) == g
		for _, n := range news {
			quiet = quiet && !n.dirty() && !n.marker && n.gen == r.gen(t)
		}
		quiet = quiet && !old.dirty && old.gen == r.gen(t)
		if quiet {
			after := 0
			for _, n := range news {
				after += n.pushes
			}
			return after + old.pushes - before
		}
	}
	t.Fatalf("did not settle in 12 rounds (remote gen %d)", r.gen(t))
	return 0
}

func TestMixedVersionSyncSettles(t *testing.T) {
	r := newMVRemote(t)
	n1, n2 := newMVNew(t, "n1"), newMVNew(t, "n2")
	old := newMVOld(t)

	local, err := n1.db.CreateConnection(store.NewConnection{Name: "own-host", Hostname: "own.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	seed := seedCatalog(t, n1.db, local.ID)

	// The live store must not change while a snapshot is built and
	// shadowed: a bug there would delete the catalog rows on every push.
	before := treeHash(t, n1.db)
	if err := n1.push(t, r); err != nil {
		t.Fatal(err)
	}
	if after := treeHash(t, n1.db); after != before {
		t.Fatal("pushing changed the live store's tree tables")
	}
	if n1.dirty() {
		t.Fatal("dirty right after its own push")
	}

	extra := settle(t, r, []*mvNew{n1, n2}, old)
	t.Logf("round 0 (n1 publishes): %d pushes while settling", extra)
	if extra != 0 {
		t.Errorf("round 0: %d extra pushes", extra)
	}
	assertCatalogIntact(t, "n2 after round 0", n2.db, seed)
	assertOldSeesNothing(t, "old after round 0", old, seed)
	var layout string
	_ = n2.db.Conn().QueryRow(`SELECT layout_json FROM workspaces WHERE id = ?`, seed.wsID).Scan(&layout)
	if !strings.Contains(layout, seed.connID) {
		t.Fatalf("n2 lost the catalog pane of the workspace: %s", layout)
	}

	// Rounds of edits and deletions on the old machine.
	edits := []struct {
		name string
		do   func()
	}{
		{"old adds a connection", func() {
			old.exec(t, `INSERT INTO connections (id, name, hostname, created_at, updated_at) VALUES ('old-1', 'from old', 'o.example.com', 1, 1)`)
		}},
		{"old renames the user's own connection", func() {
			old.exec(t, `UPDATE connections SET name = 'own-host renamed' WHERE id = ?`, local.ID)
		}},
		{"old deletes its connection", func() {
			old.exec(t, `DELETE FROM connections WHERE id = 'old-1'`)
		}},
		{"old saves the workspace again, changed", func() {
			// A save there with the same tabs is the stripped form
			// itself and the full layout comes back; a real change
			// (another split here: the same host twice) wins.
			old.exec(t, `UPDATE workspaces SET layout_json = ? WHERE id = ?`, workspaceLayout(local.ID, local.ID), seed.wsID)
		}},
	}
	for i, e := range edits {
		e.do()
		extra := settle(t, r, []*mvNew{n1, n2}, old)
		t.Logf("round %d (%s): %d pushes while settling (1 from old)", i+1, e.name, extra)
		if extra < 1 || extra > 2 {
			t.Errorf("round %d: %d pushes; want the old push plus at most one repush", i+1, extra)
		}
		assertCatalogIntact(t, fmt.Sprintf("n1 after round %d", i+1), n1.db, seed)
		assertCatalogIntact(t, fmt.Sprintf("n2 after round %d", i+1), n2.db, seed)
		assertOldSeesNothing(t, fmt.Sprintf("old after round %d", i+1), old, seed)
	}
	// The old machine's edits reached the new ones.
	c, err := n2.db.GetConnection(local.ID)
	if err != nil || c.Name != "own-host renamed" {
		t.Errorf("old machine's rename did not arrive: %+v %v", c, err)
	}
	if _, err := n1.db.GetConnection("old-1"); err == nil {
		t.Error("connection deleted on the old machine survived")
	}
	// Re-saved there, so its version wins (without the catalog pane).
	_ = n1.db.Conn().QueryRow(`SELECT layout_json FROM workspaces WHERE id = ?`, seed.wsID).Scan(&layout)
	if strings.Contains(layout, seed.connID) {
		t.Errorf("a workspace the old app saved again came back with the catalog pane")
	}

	// Offline: n1 holds the marker, the old machine pushes three times,
	// n1 comes back several generations behind.
	old.exec(t, `INSERT INTO connections (id, name, hostname, created_at, updated_at) VALUES ('old-2', 'x', 'x', 1, 1)`)
	old.push(t, r)
	n1.pull(t, r)
	if !n1.marker {
		t.Fatal("no repush marker after pulling the old machine's snapshot")
	}
	r.down = true
	n1.tick(t, r)
	if !n1.marker {
		t.Fatal("offline tick dropped the marker")
	}
	r.down = false
	for i := 0; i < 3; i++ {
		old.exec(t, `UPDATE connections SET hostname = ? WHERE id = 'old-2'`, fmt.Sprintf("x%d", i))
		old.push(t, r)
	}
	extra = settle(t, r, []*mvNew{n1, n2}, old)
	t.Logf("offline round: %d pushes while settling", extra)
	if extra > 1 {
		t.Errorf("offline round: %d pushes; want at most one repush", extra)
	}
	assertCatalogIntact(t, "n1 after offline", n1.db, seed)
	assertCatalogIntact(t, "n2 after offline", n2.db, seed)
	assertOldSeesNothing(t, "old after offline", old, seed)
}

// Without shadowing, an untouched released machine cannot pull at all: a
// local forward on a catalog connection has no parent in its tables and
// its mirror fails the deferred foreign key check at commit.
func TestReleasedMirrorFailsOnUnshadowedCatalogForward(t *testing.T) {
	n := newMVNew(t, "n")
	local, _ := n.db.CreateConnection(store.NewConnection{Name: "own", Hostname: "h"})
	seedCatalog(t, n.db, local.ID)

	raw := filepath.Join(t.TempDir(), "raw.db")
	if _, err := n.db.Conn().Exec(`VACUUM INTO ?`, raw); err != nil {
		t.Fatal(err)
	}
	// Strip only the connections (what the old mirror would see if the
	// catalog rows were dropped but forwards were forgotten).
	conn, _ := sql.Open("sqlite", raw+"?_pragma=foreign_keys(0)")
	_, _ = conn.Exec(`DROP TRIGGER catalog_guard_connections_delete`)
	_, _ = conn.Exec(`DELETE FROM connections WHERE catalog_id IS NOT NULL`)
	conn.Close()
	old := newMVOld(t)
	if err := relMirrorFrom(old.db, raw); err == nil {
		t.Fatal("expected the released mirror to fail on an orphaned forward")
	}

	shadowed := filepath.Join(t.TempDir(), "shadowed.db")
	if _, err := n.db.Conn().Exec(`VACUUM INTO ?`, shadowed); err != nil {
		t.Fatal(err)
	}
	if err := store.ShadowSnapshot(shadowed, "n"); err != nil {
		t.Fatal(err)
	}
	if err := relMirrorFrom(old.db, shadowed); err != nil {
		t.Fatalf("released mirror on a shadowed snapshot: %v", err)
	}
}

func TestCatalogRepushAction(t *testing.T) {
	for _, c := range []struct {
		name               string
		pending, has       bool
		remote, local      int64
		wantPush, wantDrop bool
	}{
		{"no marker", false, true, 5, 5, false, false},
		{"no catalog data never pushes", true, false, 5, 5, false, true},
		{"offline keeps the marker", true, true, -1, 5, false, false},
		{"remote unchanged: push once", true, true, 5, 5, true, false},
		{"remote moved on: drop, pull as usual", true, true, 8, 5, false, true},
	} {
		push, drop := CatalogRepushAction(c.pending, c.has, c.remote, c.local)
		if push != c.wantPush || drop != c.wantDrop {
			t.Errorf("%s: push=%v drop=%v", c.name, push, drop)
		}
	}
}
