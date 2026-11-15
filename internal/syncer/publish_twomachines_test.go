package syncer

import (
	"database/sql"
	"testing"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/store"
)

// A publishes a local folder; its second machine B (same profile) syncs
// the catalog BEFORE pulling A's profile. B does not know the published
// uuids belong to its own local rows, so its catalog sync inserts the
// records as new rows next to the still-local originals. The profile pull
// that follows must leave B exactly like A: the converted rows with the
// same local ids, no copies, the local data on them intact.
func TestPublishSecondMachineCatalogBeforeProfile(t *testing.T) {
	publishSecondMachine(t, false)
}

// The same, with B adding a forward of its own in between: B's profile is
// dirty, so it does not pull; its push meets A's newer generation and the
// user sees the ordinary profile conflict. Nothing of B's is dropped
// silently.
func TestPublishSecondMachineEditInBetween(t *testing.T) {
	publishSecondMachine(t, true)
}

func publishSecondMachine(t *testing.T, editInBetween bool) {
	r := newMVRemote(t)
	a := newMVNew(t, "A")
	b := newMVNew(t, "B")

	cat, err := a.db.CreateCatalog(store.NewCatalog{Name: "Team", BaseURL: "https://catalog.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := a.db.CreateFolder(store.NewFolder{Name: "Mine"})
	conn, _ := a.db.CreateConnection(store.NewConnection{Name: "web-1", Hostname: "web-1.example.com", FolderID: &f.ID})
	rh, rp := "127.0.0.1", uint16(5432)
	fwd, _ := a.db.CreatePortForward(store.NewPortForward{ConnectionID: conn.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp, AutoStart: true})
	if err := a.push(t, r); err != nil {
		t.Fatal(err)
	}
	b.tick(t, r)
	if c, err := b.db.GetConnection(conn.ID); err != nil || c.CatalogID != nil {
		t.Fatalf("B does not hold the local original: %+v %v", c, err)
	}

	// A publishes (server accepted) and pushes its profile.
	plan, err := catalog.BuildPublish(a.db, catalog.PublishOptions{CatalogID: cat.ID, RootID: f.ID})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	var acc []catalog.Accepted
	for _, rec := range plan.Records {
		acc = append(acc, catalog.Accepted{Kind: rec.Kind, UUID: rec.UUID, Rev: 1})
	}
	if err := catalog.CompletePublish(a.db, plan, acc, "bob", nil); err != nil {
		t.Fatal(err)
	}
	a.tick(t, r)

	// B's catalog sync runs first and inserts the records as new rows.
	err = b.db.WithCatalogWriter(func(tx *sql.Tx) error {
		ids := map[string]string{}
		for _, pf := range plan.Request.Folders {
			parent := cat.RootFolderID
			if pf.ParentUUID != nil {
				parent = ids[*pf.ParentUUID]
			}
			id, err := b.db.UpsertCatalogFolderTx(tx, cat.ID, store.CatalogFolderRow{RemoteUUID: pf.UUID, Rev: 1,
				ParentLocalID: parent, Name: pf.Name, RawJSON: `{"access":"write"}`}, "")
			if err != nil {
				return err
			}
			ids[pf.UUID] = id
		}
		for _, pc := range plan.Request.Connections {
			if _, err := b.db.UpsertCatalogConnectionTx(tx, cat.ID, store.CatalogConnectionRow{RemoteUUID: pc.UUID, Rev: 1,
				FolderLocalID: ids[pc.FolderUUID], Name: pc.Name, Hostname: pc.Hostname, Protocol: "ssh", RawJSON: `{}`}, ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := b.db.ListConnections(nil)
	if len(all) != 2 {
		t.Fatalf("B before the pull: %d connections, the scenario expects the copy next to the original", len(all))
	}
	if b.dirty() {
		t.Fatal("catalog rows made B's profile dirty; it would push the copies instead of pulling")
	}
	if editInBetween {
		rh2, rp2 := "127.0.0.1", uint16(9090)
		own, err := b.db.CreatePortForward(store.NewPortForward{ConnectionID: conn.ID, Kind: "local", RemoteHost: &rh2, RemotePort: &rp2})
		if err != nil {
			t.Fatal(err)
		}
		err = b.push(t, r)
		if !IsGenerationConflict(err) {
			t.Fatalf("B's push: %v, want a generation conflict", err)
		}
		b.tick(t, r)
		if _, err := b.db.GetPortForward(own.ID); err != nil {
			t.Fatal("B's own forward was dropped without the user deciding")
		}
		if !b.dirty() {
			t.Fatal("B lost its unpushed change")
		}
		return
	}

	// The profile pull settles it.
	b.tick(t, r)
	all, _ = b.db.ListConnections(nil)
	if len(all) != 1 || all[0].ID != conn.ID || all[0].CatalogID == nil {
		t.Fatalf("B after the pull: %+v", all)
	}
	p, err := b.db.GetPortForward(fwd.ID)
	if err != nil || p.CatalogID == nil || !p.AutoStart {
		t.Fatalf("B's forward after the pull: %+v %v", p, err)
	}
	if treeHash(t, a.db) != treeHash(t, b.db) {
		t.Fatal("A and B differ after the pull")
	}
}
