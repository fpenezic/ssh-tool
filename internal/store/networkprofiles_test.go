package store

import (
	"database/sql"
	"testing"
)

// Deleting a profile clears every reference to it: a connection or
// folder falls back to its inherited network, a server goes direct, and
// an explicit "direct" choice is left alone.
func TestDeleteNetworkProfileClearsRefs(t *testing.T) {
	db := openTestDB(t)
	p, err := db.CreateNetworkProfile("vpn", "{}")
	if err != nil {
		t.Fatal(err)
	}
	keep, err := db.CreateNetworkProfile("other", "{}")
	if err != nil {
		t.Fatal(err)
	}
	id, direct, other := p.ID, "", keep.ID
	f, err := db.CreateFolder(NewFolder{Name: "f"})
	if err != nil {
		t.Fatal(err)
	}
	user := "admin"
	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.UpdateFolderSettingsTx(tx, f.ID, InheritableSettings{Username: &user, NetworkProfileID: &id})
	}); err != nil {
		t.Fatal(err)
	}
	via, _ := db.CreateConnection(NewConnection{Name: "via", Hostname: "h", Overrides: InheritableSettings{NetworkProfileID: &id}})
	dir, _ := db.CreateConnection(NewConnection{Name: "direct", Hostname: "h", Overrides: InheritableSettings{NetworkProfileID: &direct}})
	oth, _ := db.CreateConnection(NewConnection{Name: "other", Hostname: "h", Overrides: InheritableSettings{NetworkProfileID: &other}})

	if err := db.DeleteNetworkProfile(id); err != nil {
		t.Fatal(err)
	}
	gf, _ := db.GetFolder(f.ID)
	if gf.Settings.NetworkProfileID != nil || gf.Settings.Username == nil {
		t.Fatalf("folder settings = %+v", gf.Settings)
	}
	for _, c := range []struct {
		id   string
		want *string
	}{{via.ID, nil}, {dir.ID, &direct}, {oth.ID, &other}} {
		got, _ := db.GetConnection(c.id)
		n := got.Overrides.NetworkProfileID
		if (n == nil) != (c.want == nil) || (n != nil && *n != *c.want) {
			t.Fatalf("connection %s network = %v, want %v", got.Name, n, c.want)
		}
	}
}

// A reference whose profile never arrived (sync) or was deleted before
// the cleanup existed is swept; live and direct ones stay.
func TestSweepOrphanNetworkRefs(t *testing.T) {
	db := openTestDB(t)
	live, _ := db.CreateNetworkProfile("vpn", "{}")
	gone, direct := "no-such-profile", ""
	a, _ := db.CreateConnection(NewConnection{Name: "a", Hostname: "h", Overrides: InheritableSettings{NetworkProfileID: &gone}})
	b, _ := db.CreateConnection(NewConnection{Name: "b", Hostname: "h", Overrides: InheritableSettings{NetworkProfileID: &live.ID}})
	c, _ := db.CreateConnection(NewConnection{Name: "c", Hostname: "h", Overrides: InheritableSettings{NetworkProfileID: &direct}})
	n, err := db.SweepOrphanNetworkRefs()
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if got, _ := db.GetConnection(a.ID); got.Overrides.NetworkProfileID != nil {
		t.Fatal("orphan kept")
	}
	if got, _ := db.GetConnection(b.ID); got.Overrides.NetworkProfileID == nil {
		t.Fatal("live reference cleared")
	}
	if got, _ := db.GetConnection(c.ID); got.Overrides.NetworkProfileID == nil {
		t.Fatal("explicit direct cleared")
	}
	if n, _ := db.SweepOrphanNetworkRefs(); n != 0 {
		t.Fatalf("second sweep = %d", n)
	}
}
