package catalog

import (
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// localSubtree: a local folder with a connection and a forward, carrying
// the publisher's local data.
func localSubtree(t *testing.T, db *store.DB) (folder, conn, fwd string) {
	t.Helper()
	f, err := db.CreateFolder(store.NewFolder{Name: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CreateConnection(store.NewConnection{Name: "web-1", Hostname: "web-1.example.com", FolderID: &f.ID})
	if err != nil {
		t.Fatal(err)
	}
	fav := true
	if _, err := db.UpdateConnection(store.UpdateConnection{ID: c.ID, Favorite: &fav}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetConnectionPasswordKey(c.ID, "conn-pass:x"); err != nil {
		t.Fatal(err)
	}
	rh, rp := "127.0.0.1", uint16(5432)
	p, err := db.CreatePortForward(store.NewPortForward{ConnectionID: c.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp, AutoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetPortForwardBookmarks(p.ID, []store.ProxyBookmark{{Name: "ui", URL: "http://localhost:5432"}}); err != nil {
		t.Fatal(err)
	}
	return f.ID, c.ID, p.ID
}

// The server accepted a publish but this app never converted the rows
// (crash, lost response): the feed converts them in place, no copy.
func TestFeedConvertsAnUnfinishedPublish(t *testing.T) {
	e := newSyncEnv(t)
	fid, cid, pid := localSubtree(t, e.db)
	if err := e.db.RecordPublishPending(e.cat.ID, []store.PublishPending{
		{Kind: "folders", RemoteUUID: "f-pub", LocalID: fid,
			Extra: `{"bindings":[{"slot_uuid":"s-pub","kind":"cred","target_id":"cred-own"}]}`},
		{Kind: "connections", RemoteUUID: "c-pub", LocalID: cid,
			Extra: `{"layer":{"remote_uuid":"c-pub","kind":"connections","json":"{\"port\":2200}"}}`},
		{Kind: "forwards", RemoteUUID: "p-pub", LocalID: pid},
	}); err != nil {
		t.Fatal(err)
	}
	if busy, _ := e.db.PendingPublishForLocal([]string{fid}); !busy {
		t.Fatal("pending publish not reported for its folder")
	}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, folderRec("f-pub", 1, nil, "Mine")),
		up(KindConnections, connRec("c-pub", 1, "f-pub", "web-1")),
		up(KindForwards, fwdRec("p-pub", 1, "c-pub")),
	}}})
	res := e.sync()
	if res.PublishConverted != 3 {
		t.Fatalf("converted %d, want 3 (%+v)", res.PublishConverted, res)
	}
	f, _ := e.db.GetFolder(fid)
	c, _ := e.db.GetConnection(cid)
	p, _ := e.db.GetPortForward(pid)
	if f.RemoteUUID == nil || *f.RemoteUUID != "f-pub" || *f.ParentID != e.cat.RootFolderID {
		t.Fatalf("folder not converted in place: %+v", f)
	}
	if c.RemoteUUID == nil || *c.FolderID != fid || !c.Favorite || c.PasswordVaultKey == nil {
		t.Fatalf("connection not converted in place, or lost local data: %+v", c)
	}
	if p.RemoteUUID == nil || !p.AutoStart || len(p.Bookmarks) != 1 {
		t.Fatalf("forward not converted in place, or lost local data: %+v", p)
	}
	if n := len(e.mustConns()); n != 1 {
		t.Fatalf("%d connections: the feed made a copy", n)
	}
	if busy, _ := e.db.PendingPublishForLocal([]string{fid, cid, pid}); busy {
		t.Fatal("pending rows left after conversion")
	}
	// The publisher's side, as a completed publish writes it.
	bs, _ := e.db.ListCatalogBindings(e.cat.ID)
	if len(bs) != 1 || bs[0].SlotUUID != "s-pub" || bs[0].TargetID != "cred-own" {
		t.Fatalf("bindings after recovery: %+v", bs)
	}
	ovs, _ := e.db.ListCatalogOverrides(e.cat.ID)
	if len(ovs) != 1 || ovs[0].RemoteUUID != "c-pub" || !strings.Contains(ovs[0].JSON, "2200") {
		t.Fatalf("layers after recovery: %+v", ovs)
	}
}

// A publish that never reached the server: a complete snapshot without
// its records drops the pending note and leaves the rows local.
func TestSnapshotDropsAPublishThatNeverArrived(t *testing.T) {
	e := newSyncEnv(t)
	fid, cid, _ := localSubtree(t, e.db)
	_ = e.db.RecordPublishPending(e.cat.ID, []store.PublishPending{
		{Kind: "folders", RemoteUUID: "f-pub", LocalID: fid},
		{Kind: "connections", RemoteUUID: "c-pub", LocalID: cid},
	})
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{up(KindFolders, folderRec("other", 1, nil, "Other"))}}})
	res := e.sync()
	if res.PublishDropped != 2 {
		t.Fatalf("dropped %d, want 2", res.PublishDropped)
	}
	if c, _ := e.db.GetConnection(cid); c.CatalogID != nil {
		t.Fatal("local row converted without its record")
	}
	if busy, _ := e.db.PendingPublishForLocal([]string{fid, cid}); busy {
		t.Fatal("pending rows kept")
	}
}

// The local row went away before the feed brought the record: a new row,
// not a failure.
func TestPendingPublishWithDeletedLocalRow(t *testing.T) {
	e := newSyncEnv(t)
	fid, cid, _ := localSubtree(t, e.db)
	_ = e.db.RecordPublishPending(e.cat.ID, []store.PublishPending{{Kind: "connections", RemoteUUID: "c-pub", LocalID: cid}})
	if err := e.db.DeleteFolder(fid); err != nil {
		t.Fatal(err)
	}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, folderRec("f-pub", 1, nil, "Mine")),
		up(KindConnections, connRec("c-pub", 1, "f-pub", "web-1")),
	}}})
	e.sync()
	if c := e.conn("c-pub"); c == nil || c.ID == cid {
		t.Fatalf("record not inserted as a new row: %+v", c)
	}
}
