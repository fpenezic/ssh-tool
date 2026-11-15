package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"ssh-tool/internal/store"
)

// feedServer serves GET /api/v1/changes from a test-controlled function of
// the cursor, plus meta and a token endpoint for an always-valid session.
type feedServer struct {
	mu    sync.Mutex
	next  func(since string) (Feed, int)
	calls []string
	srv   *httptest.Server
}

func newFeedServer(t *testing.T) *feedServer {
	fs := &feedServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"server":{"name":"Test"},"protocol":{"major":1,"revision":1},"auth":{"authorization_endpoint":"%[1]s/a","token_endpoint":"%[1]s/oauth/token","client_id":"ssh-tool"}}`, fs.srv.URL)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
	})
	mux.HandleFunc("GET /api/v1/changes", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		since := r.URL.Query().Get("since")
		fs.calls = append(fs.calls, since)
		feed, status := fs.next(since)
		fs.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
			return
		}
		json.NewEncoder(w).Encode(feed)
	})
	fs.srv = httptest.NewServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

// serve sets a fixed sequence of pages keyed by the cursor they answer.
func (fs *feedServer) serve(pages map[string]Feed) {
	fs.mu.Lock()
	fs.next = func(since string) (Feed, int) {
		if p, ok := pages[since]; ok {
			return p, 0
		}
		return Feed{Cursor: since, Items: []Item{}}, 0
	}
	fs.mu.Unlock()
}

func up(kind string, rec map[string]any) Item {
	b, _ := json.Marshal(rec)
	return Item{Kind: kind, Op: "upsert", Record: b}
}

func del(kind, uuid string, rev int64) Item {
	return Item{Kind: kind, Op: "delete", UUID: uuid, Rev: rev, Reason: "deleted"}
}

func folderRec(uuid string, rev int64, parent any, name string) map[string]any {
	return map[string]any{"uuid": uuid, "rev": rev, "parent_uuid": parent, "name": name, "settings": map[string]any{}, "access": "read", "doc_md": ""}
}

func connRec(uuid string, rev int64, folder, name string) map[string]any {
	return map[string]any{"uuid": uuid, "rev": rev, "folder_uuid": folder, "name": name, "hostname": name + ".example.com",
		"protocol": "ssh", "settings": map[string]any{"port": 2222}, "tags": []string{"t"}, "notes": "", "future_field": 1}
}

func fwdRec(uuid string, rev int64, conn string) map[string]any {
	return map[string]any{"uuid": uuid, "rev": rev, "connection_uuid": conn, "kind": "local", "local_port": 15432,
		"remote_host": "127.0.0.1", "remote_port": 5432, "auto_start": true, "description": "pg"}
}

type syncEnv struct {
	t   *testing.T
	db  *store.DB
	cat *store.Catalog
	fs  *feedServer
	c   *Client
}

func newSyncEnv(t *testing.T) *syncEnv {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fs := newFeedServer(t)
	cat, err := db.CreateCatalog(store.NewCatalog{Name: "Team", BaseURL: fs.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(fs.srv.URL, "ssh-tool/test", &memTokens{rt: "rt"})
	if err != nil {
		t.Fatal(err)
	}
	return &syncEnv{t: t, db: db, cat: cat, fs: fs, c: c}
}

func (e *syncEnv) sync() SyncResult {
	e.t.Helper()
	cat, err := e.db.GetCatalog(e.cat.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := Sync(context.Background(), e.db, cat, e.c, nil)
	if err != nil {
		e.t.Fatalf("sync: %v", err)
	}
	return res
}

func (e *syncEnv) conn(uuid string) *store.Connection {
	e.t.Helper()
	for _, c := range e.mustConns() {
		if c.RemoteUUID != nil && *c.RemoteUUID == uuid {
			c := c
			return &c
		}
	}
	return nil
}

func (e *syncEnv) mustConns() []store.Connection {
	cs, err := e.db.ListConnections(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return cs
}

func (e *syncEnv) folder(uuid string) *store.Folder {
	fs, _ := e.db.ListFolders()
	for _, f := range fs {
		if f.RemoteUUID != nil && *f.RemoteUUID == uuid {
			f := f
			return &f
		}
	}
	return nil
}

func (e *syncEnv) fwd(uuid string) *store.PortForward {
	fws, _ := e.db.ListAllPortForwards()
	for _, f := range fws {
		if f.RemoteUUID != nil && *f.RemoteUUID == uuid {
			f := f
			return &f
		}
	}
	return nil
}

// The first sync is a snapshot over several pages, with a connection that
// arrives before its folder and a forward before its connection.
func TestSyncInitialSnapshotAcrossPages(t *testing.T) {
	e := newSyncEnv(t)
	e.fs.serve(map[string]Feed{
		"": {Reset: true, More: true, Cursor: "c1", Items: []Item{
			up(KindFolders, folderRec("space", 1, nil, "Space")),
			up(KindForwards, fwdRec("fw1", 1, "web")),
			up(KindConnections, connRec("web", 1, "cust", "web-1")),
			up("gadgets", map[string]any{"uuid": "g"}),
		}},
		"c1": {Reset: true, More: false, Cursor: "c2", Items: []Item{
			up(KindFolders, folderRec("cust", 1, "space", "Customer A")),
			up(KindSlots, map[string]any{"uuid": "s1", "rev": 1, "folder_uuid": "space", "type": "cred", "name": "admin", "kind": "slot", "doc_md": "ask ops"}),
		}},
	})
	res := e.sync()
	if !res.Reset || res.Pages != 2 || res.Skipped != 1 || res.Fallback != 0 {
		t.Fatalf("result: %+v", res)
	}
	space, cust := e.folder("space"), e.folder("cust")
	if space == nil || cust == nil || *space.ParentID != e.cat.RootFolderID || *cust.ParentID != space.ID {
		t.Fatalf("folders not linked: %+v %+v", space, cust)
	}
	web := e.conn("web")
	if web == nil || *web.FolderID != cust.ID || web.Name != "web-1" || web.Tags[0] != "t" {
		t.Fatalf("connection: %+v", web)
	}
	fw := e.fwd("fw1")
	if fw == nil || fw.ConnectionID != web.ID {
		t.Fatalf("forward: %+v", fw)
	}
	if fw.AutoStart {
		t.Error("catalog auto_start started a forward: it is a suggestion only")
	}
	slots, _ := e.db.ListCatalogSlots(e.cat.ID)
	if len(slots) != 1 || slots[0].DocMD != "ask ops" {
		t.Fatalf("slots: %+v", slots)
	}
	cat, _ := e.db.GetCatalog(e.cat.ID)
	if cat.Cursor != "c2" || cat.LastSyncAt == nil {
		t.Fatalf("cursor not stored: %+v", cat)
	}
	// The raw record is kept, unknown fields included.
	var raw string
	_ = e.db.Conn().QueryRow(`SELECT catalog_json FROM connections WHERE id = ?`, web.ID).Scan(&raw)
	if !json.Valid([]byte(raw)) || !containsKey(raw, "future_field") {
		t.Fatalf("raw record not kept: %s", raw)
	}
	// Catalog rows are read-only to everything but the engine.
	name := "x"
	if _, err := e.db.UpdateConnection(store.UpdateConnection{ID: web.ID, Name: &name}); !store.IsCatalogOwned(err) {
		t.Fatalf("catalog row writable: %v", err)
	}
}

func containsKey(raw, k string) bool {
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	_, ok := m[k]
	return ok
}

func seedBasic(e *syncEnv) {
	e.fs.serve(map[string]Feed{
		"": {Reset: true, Cursor: "c1", Items: []Item{
			up(KindFolders, folderRec("space", 1, nil, "Space")),
			up(KindFolders, folderRec("cust", 1, "space", "Customer A")),
			up(KindConnections, connRec("web", 1, "cust", "web-1")),
			up(KindConnections, connRec("db", 1, "cust", "db-1")),
			up(KindForwards, fwdRec("fw1", 1, "web")),
		}},
	})
	e.sync()
}

func TestSyncIncrementalRevRule(t *testing.T) {
	e := newSyncEnv(t)
	seedBasic(e)
	localID := e.conn("web").ID
	e.fs.serve(map[string]Feed{"c1": {Cursor: "c2", Items: []Item{
		up(KindConnections, connRec("web", 3, "cust", "web-renamed")),
		up(KindConnections, connRec("db", 1, "cust", "db-stale")), // same rev: ignored
	}}})
	e.sync()
	if w := e.conn("web"); w.Name != "web-renamed" || w.ID != localID || w.RemoteRev != 3 {
		t.Fatalf("upsert by rev: %+v", w)
	}
	if d := e.conn("db"); d.Name != "db-1" {
		t.Fatalf("equal rev applied: %+v", d)
	}
}

// Tombstones: a record without local data goes; one with local data stays
// as "removed"; a folder stays while something is kept below it.
func TestSyncTombstonesAndLocalData(t *testing.T) {
	e := newSyncEnv(t)
	seedBasic(e)
	web := e.conn("web")
	// Local data on web: the user's own forward.
	rh, rp := "127.0.0.1", uint16(80)
	if _, err := e.db.CreatePortForward(store.NewPortForward{ConnectionID: web.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp}); err != nil {
		t.Fatal(err)
	}
	e.fs.serve(map[string]Feed{"c1": {Cursor: "c2", Items: []Item{
		del(KindFolders, "space", 2), // parent first in the feed on purpose
		del(KindFolders, "cust", 2),
		del(KindConnections, "web", 2),
		del(KindConnections, "db", 2),
		del(KindForwards, "fw1", 2),
		del(KindConnections, "never-held", 1),
	}}})
	res := e.sync()
	if e.conn("db") != nil {
		t.Error("connection without local data kept")
	}
	if e.fwd("fw1") != nil {
		t.Error("catalog forward without local data kept")
	}
	w := e.conn("web")
	if w == nil || w.CatalogState != "removed" {
		t.Fatalf("connection with a local forward not kept as removed: %+v", w)
	}
	if c, s := e.folder("cust"), e.folder("space"); c == nil || s == nil || c.CatalogState != "removed" || s.CatalogState != "removed" {
		t.Fatalf("ancestors of a kept row not kept: %+v %+v", c, s)
	}
	if res.Kept != 3 || res.Removed != 2 {
		t.Errorf("result: %+v", res)
	}

	// The record comes back (restored on the server): revived in place.
	e.fs.serve(map[string]Feed{"c2": {Cursor: "c3", Items: []Item{
		up(KindFolders, folderRec("space", 4, nil, "Space")),
		up(KindFolders, folderRec("cust", 4, "space", "Customer A")),
		up(KindConnections, connRec("web", 4, "cust", "web-1")),
	}}})
	e.sync()
	if w2 := e.conn("web"); w2 == nil || w2.CatalogState != "" || w2.ID != w.ID {
		t.Fatalf("not revived in place: %+v", w2)
	}
}

// A reset snapshot removes what it no longer contains, under the same rule.
func TestSyncResetRemovesUnseen(t *testing.T) {
	e := newSyncEnv(t)
	seedBasic(e)
	if err := e.db.SetCatalogOverride(e.cat.ID, "db", "connections", `{"port":22}`); err != nil {
		t.Fatal(err)
	}
	e.fs.serve(map[string]Feed{"c1": {Reset: true, Cursor: "c9", Items: []Item{
		up(KindFolders, folderRec("space", 1, nil, "Space")),
		up(KindFolders, folderRec("cust", 1, "space", "Customer A")),
	}}})
	res := e.sync()
	if !res.Reset {
		t.Fatal("not a reset")
	}
	if e.conn("web") != nil || e.fwd("fw1") != nil {
		t.Error("unseen records without local data kept")
	}
	if d := e.conn("db"); d == nil || d.CatalogState != "removed" {
		t.Fatalf("unseen record with a local layer not kept: %+v", d)
	}
	if c := e.folder("cust"); c == nil || c.CatalogState != "" {
		t.Fatalf("folder in the snapshot changed state: %+v", c)
	}
}

func TestSyncRefusesLocalProtocol(t *testing.T) {
	e := newSyncEnv(t)
	seedBasic(e)
	bad := connRec("web", 5, "cust", "web-1")
	bad["protocol"] = " Local "
	e.fs.serve(map[string]Feed{"c1": {Cursor: "c2", Items: []Item{
		up(KindConnections, bad),
		up(KindConnections, func() map[string]any { r := connRec("new", 1, "cust", "new"); r["protocol"] = "local"; return r }()),
	}}})
	res := e.sync()
	if e.conn("new") != nil {
		t.Error("local-protocol connection stored")
	}
	if w := e.conn("web"); w != nil {
		t.Errorf("held connection that turned local kept: %+v", w)
	}
	if res.Skipped != 2 {
		t.Errorf("skipped %d", res.Skipped)
	}
}

// A pass that breaks off keeps the old cursor; the next pass re-applies
// from there without duplicating anything.
func TestSyncInterruptedPassResumes(t *testing.T) {
	e := newSyncEnv(t)
	fail := true
	e.fs.mu.Lock()
	e.fs.next = func(since string) (Feed, int) {
		switch since {
		case "":
			return Feed{Reset: true, More: true, Cursor: "c1", Items: []Item{
				up(KindFolders, folderRec("space", 1, nil, "Space")),
				up(KindConnections, connRec("web", 1, "space", "web-1")),
			}}, 0
		case "c1":
			if fail {
				return Feed{}, 500
			}
			return Feed{Reset: true, Cursor: "c2", Items: []Item{up(KindConnections, connRec("db", 1, "space", "db-1"))}}, 0
		}
		return Feed{Cursor: since, Items: []Item{}}, 0
	}
	e.fs.mu.Unlock()
	cat, _ := e.db.GetCatalog(e.cat.ID)
	if _, err := Sync(context.Background(), e.db, cat, e.c, nil); err == nil {
		t.Fatal("expected the second page to fail")
	}
	if cat, _ := e.db.GetCatalog(e.cat.ID); cat.Cursor != "" {
		t.Fatalf("cursor advanced past an unfinished pass: %q", cat.Cursor)
	}
	fail = false
	e.sync()
	if n := len(e.mustConns()); n != 2 {
		t.Fatalf("%d connections after resuming, want 2", n)
	}
}

func TestRemovedRowActions(t *testing.T) {
	e := newSyncEnv(t)
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, map[string]any{"uuid": "space", "rev": 1, "parent_uuid": nil, "name": "Space", "access": "read",
			"settings": map[string]any{"username": "deploy"}}),
		up(KindConnections, connRec("web", 1, "space", "web-1")),
		up(KindConnections, connRec("db", 1, "space", "db-1")),
	}}})
	e.sync()
	// Until materialisation exists, give the folder its settings the way
	// it will (the engine writes settings_json).
	sp := e.folder("space")
	if err := e.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE folders SET settings_json = '{"username":"deploy"}' WHERE id = ?`, sp.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"web", "db"} {
		if err := e.db.SetCatalogOverride(e.cat.ID, u, "connections", `{"port":2200}`); err != nil {
			t.Fatal(err)
		}
	}
	e.fs.serve(map[string]Feed{"c1": {Cursor: "c2", Items: []Item{del(KindFolders, "space", 2), del(KindConnections, "web", 2), del(KindConnections, "db", 2)}}})
	e.sync()
	web, db := e.conn("web"), e.conn("db")
	if web == nil || db == nil {
		t.Fatal("rows with local layers not kept")
	}
	// Still read-only while removed.
	name := "x"
	if _, err := e.db.UpdateConnection(store.UpdateConnection{ID: web.ID, Name: &name}); !store.IsCatalogOwned(err) {
		t.Fatalf("removed row writable: %v", err)
	}
	// "Keep as local": top level, inherited settings folded in.
	if err := KeepConnectionLocal(e.db, web.ID); err != nil {
		t.Fatal(err)
	}
	kept, _ := e.db.GetConnection(web.ID)
	if kept.CatalogID != nil || kept.FolderID != nil || kept.Overrides.Username == nil || *kept.Overrides.Username != "deploy" {
		t.Fatalf("kept connection: %+v", kept)
	}
	if _, err := e.db.UpdateConnection(store.UpdateConnection{ID: web.ID, Name: &name}); err != nil {
		t.Fatalf("kept connection not editable: %v", err)
	}
	// "Delete" on the other; the empty removed folder goes with it.
	if err := e.db.DeleteRemovedCatalogRow("connections", db.ID); err != nil {
		t.Fatal(err)
	}
	if e.conn("db") != nil || e.folder("space") != nil {
		t.Fatal("delete left the row or its empty removed folder")
	}
	// Neither action applies to a row still in its catalog.
	seed := newSyncEnv(t)
	seedBasic(seed)
	if err := seed.db.DeleteRemovedCatalogRow("connections", seed.conn("web").ID); !errors.Is(err, store.ErrNotRemoved) {
		t.Fatalf("delete of a live catalog row: %v", err)
	}
	if err := KeepConnectionLocal(seed.db, seed.conn("web").ID); !errors.Is(err, store.ErrNotRemoved) {
		t.Fatalf("keep of a live catalog row: %v", err)
	}
}

// Skeleton folders are stored like any folder, marked in their record.
func TestSyncSkeletonFolder(t *testing.T) {
	e := newSyncEnv(t)
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, map[string]any{"uuid": "top", "rev": 1, "parent_uuid": nil, "name": "Top", "access": "skeleton", "doc_md": nil,
			"settings": map[string]any{"auth_ref": "s1"}}),
		up(KindFolders, folderRec("mine", 1, "top", "Mine")),
		up(KindSlots, map[string]any{"uuid": "s1", "rev": 1, "folder_uuid": "top", "type": "cred", "name": "admin", "kind": "slot", "doc_md": "how"}),
	}}})
	e.sync()
	top, mine := e.folder("top"), e.folder("mine")
	if top == nil || mine == nil || *mine.ParentID != top.ID {
		t.Fatalf("skeleton chain: %+v %+v", top, mine)
	}
	if !top.CatalogSkeleton || mine.CatalogSkeleton {
		t.Fatalf("skeleton flag on the folder rows: top %v, mine %v", top.CatalogSkeleton, mine.CatalogSkeleton)
	}
	var raw string
	_ = e.db.Conn().QueryRow(`SELECT catalog_json FROM folders WHERE id = ?`, top.ID).Scan(&raw)
	var f Folder
	_ = json.Unmarshal([]byte(raw), &f)
	if !f.Skeleton() {
		t.Fatalf("skeleton mark lost: %s", raw)
	}
}

func TestKeepFolderLocal(t *testing.T) {
	e := newSyncEnv(t)
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, map[string]any{"uuid": "space", "rev": 1, "parent_uuid": nil, "name": "Space", "access": "read",
			"settings": map[string]any{"username": "deploy"}}),
		up(KindFolders, folderRec("cust", 1, "space", "Customer A")),
		up(KindConnections, connRec("web", 1, "cust", "web-1")),
		up(KindForwards, fwdRec("fw", 1, "web")),
	}}})
	e.sync()
	_ = e.db.SetCatalogOverride(e.cat.ID, "cust", "folders", `{"port":2200}`)
	_ = e.db.SetCatalogOverride(e.cat.ID, "web", "connections", `{"port":2201}`)
	// Only "cust" and below leave the catalog.
	e.fs.serve(map[string]Feed{"c1": {Cursor: "c2", Items: []Item{del(KindForwards, "fw", 2), del(KindConnections, "web", 2), del(KindFolders, "cust", 2)}}})
	e.sync()
	cust := e.folder("cust")
	if cust == nil || cust.CatalogState != "removed" {
		t.Fatalf("not kept: %+v", cust)
	}
	if err := KeepFolderLocal(e.db, cust.ID); err != nil {
		t.Fatal(err)
	}
	f, _ := e.db.GetFolder(cust.ID)
	if f.CatalogID != nil || f.ParentID != nil || f.Settings.Username == nil || *f.Settings.Username != "deploy" || *f.Settings.Port != 2200 {
		t.Fatalf("kept folder: %+v", f)
	}
	conns, _ := e.db.ListConnections(&cust.ID)
	if len(conns) != 1 || conns[0].CatalogID != nil {
		t.Fatalf("connection below: %+v", conns)
	}
	if ov, _ := e.db.ListCatalogOverrides(e.cat.ID); len(ov) != 0 {
		t.Errorf("layers of kept rows left: %+v", ov)
	}
	// Editable now; the space it came from is still a catalog folder.
	n := "mine"
	if _, err := e.db.UpdateFolder(store.UpdateFolder{ID: cust.ID, Name: &n}); err != nil {
		t.Fatalf("kept folder not editable: %v", err)
	}
	if sp := e.folder("space"); sp == nil || sp.CatalogState != "" {
		t.Fatalf("live ancestor changed: %+v", sp)
	}
}
