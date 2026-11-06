package main

import (
	"testing"

	"ssh-tool/internal/store"
)

func dbRemoveFixture(t *testing.T, a *App, def *string) *store.Connection {
	t.Helper()
	on := true
	c, err := a.db.CreateConnection(store.NewConnection{Name: "db-host", Hostname: "db.example.com",
		Overrides: store.InheritableSettings{DbEnabled: &on, DbDefaultTarget: def,
			DbTargets: []store.DbTarget{{ID: "stage", Name: "stage"}, {ID: "reader", Name: "reader"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func reload(t *testing.T, a *App, id string) store.InheritableSettings {
	t.Helper()
	c, err := a.db.GetConnection(id)
	if err != nil || c == nil {
		t.Fatalf("connection gone: %v", err)
	}
	return c.Overrides
}

// Removing the own (default) database keeps the listed ones: it is hidden,
// the first listed one becomes the default, the tab stays on.
func TestDbRemoveOwnDefaultKeepsOthers(t *testing.T) {
	a := newResolveTestApp(t)
	c := dbRemoveFixture(t, a, nil)
	if err := a.DbRemove(c.ID, ""); err != nil {
		t.Fatal(err)
	}
	o := reload(t, a, c.ID)
	if len(o.DbTargets) != 2 || o.DbInTree == nil || *o.DbInTree || o.DbEnabled == nil || !*o.DbEnabled {
		t.Fatalf("own removal touched the rest: %+v", o)
	}
	if o.DbDefaultTarget == nil || *o.DbDefaultTarget != "stage" {
		t.Errorf("default = %v, want stage", o.DbDefaultTarget)
	}
	// Then the listed ones one by one; the last turns the tab off.
	if err := a.DbRemove(c.ID, "stage"); err != nil {
		t.Fatal(err)
	}
	if o = reload(t, a, c.ID); o.DbDefaultTarget == nil || *o.DbDefaultTarget != "reader" {
		t.Errorf("default after removing it = %v, want reader", o.DbDefaultTarget)
	}
	if err := a.DbRemove(c.ID, "reader"); err != nil {
		t.Fatal(err)
	}
	if o = reload(t, a, c.ID); o.DbEnabled == nil || *o.DbEnabled || o.DbInTree != nil {
		t.Errorf("nothing left but the tab stayed: %+v", o)
	}
}

// A listed default that goes hands the default back to the shown own database.
func TestDbRemoveListedDefault(t *testing.T) {
	a := newResolveTestApp(t)
	def := "reader"
	c := dbRemoveFixture(t, a, &def)
	if err := a.DbRemove(c.ID, "reader"); err != nil {
		t.Fatal(err)
	}
	o := reload(t, a, c.ID)
	if len(o.DbTargets) != 1 || o.DbTargets[0].ID != "stage" || o.DbDefaultTarget != nil {
		t.Errorf("after removing the listed default: %+v", o)
	}
	if err := a.DbRemove(c.ID, "gone"); err == nil {
		t.Error("removing an unknown database succeeded")
	}
}

// With nothing listed, removing the own database turns the tab off and
// leaves the connection.
func TestDbRemoveOnlyDatabase(t *testing.T) {
	a := newResolveTestApp(t)
	on := true
	c, _ := a.db.CreateConnection(store.NewConnection{Name: "solo", Hostname: "solo.example.com",
		Overrides: store.InheritableSettings{DbEnabled: &on}})
	if err := a.DbRemove(c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if o := reload(t, a, c.ID); o.DbEnabled == nil || *o.DbEnabled {
		t.Errorf("tab still on: %+v", o)
	}
}

// A database a folder lists: Remove hides it on that connection only, the
// default may be it, and Default falls through to the next shown one.
func TestDbRemoveInheritedTarget(t *testing.T) {
	a := newResolveTestApp(t)
	on := true
	f, err := a.db.CreateFolder(store.NewFolder{Name: "cluster", Settings: store.InheritableSettings{DbEnabled: &on,
		DbTargets: []store.DbTarget{{ID: "prod", Name: "prod"}, {ID: "stage", Name: "stage"}}}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) *store.Connection {
		c, err := a.db.CreateConnection(store.NewConnection{Name: name, Hostname: name + ".example.com", FolderID: &f.ID})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	n1, n2 := mk("node1"), mk("node2")
	if err := a.DbSetDefault(n1.ID, "stage"); err != nil {
		t.Fatalf("an inherited database can be the default: %v", err)
	}
	if err := a.DbRemove(n1.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.DbRemove(n1.ID, "stage"); err != nil {
		t.Fatal(err)
	}
	o := reload(t, a, n1.ID)
	if len(o.DbTargets) != 1 || !o.DbTargets[0].Override || o.DbTargets[0].ID != "stage" || o.DbTargets[0].InTree == nil || *o.DbTargets[0].InTree {
		t.Fatalf("expected one hiding override: %+v", o.DbTargets)
	}
	if o.DbDefaultTarget == nil || *o.DbDefaultTarget != "prod" {
		t.Errorf("default should move to prod: %v", o.DbDefaultTarget)
	}
	if o2 := reload(t, a, n2.ID); len(o2.DbTargets) != 0 {
		t.Errorf("node2 changed: %+v", o2.DbTargets)
	}
	ff, _ := a.db.GetFolder(f.ID)
	if ff == nil || len(ff.Settings.DbTargets) != 2 || ff.Settings.DbTargets[1].InTree != nil {
		t.Errorf("folder list changed: %+v", ff)
	}
	if err := a.DbRemove(n1.ID, "nope"); err == nil {
		t.Error("an unknown database should be refused")
	}
}

// An entry of the connection's own that shares its id with a folder's
// only adjusts that one: Remove hides it instead of letting the folder's
// version come back. With every row hidden, the Database tab goes off.
func TestDbRemoveSharedIDAndLastShown(t *testing.T) {
	a := newResolveTestApp(t)
	on := true
	f, err := a.db.CreateFolder(store.NewFolder{Name: "cluster", Settings: store.InheritableSettings{DbEnabled: &on,
		DbTargets: []store.DbTarget{{ID: "prod", Name: "prod"}}}})
	if err != nil {
		t.Fatal(err)
	}
	db := "app_local"
	c, err := a.db.CreateConnection(store.NewConnection{Name: "n1", Hostname: "n1.example.com", FolderID: &f.ID,
		Overrides: store.InheritableSettings{DbTargets: []store.DbTarget{{ID: "prod", Name: "prod", Database: &db}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DbRemove(c.ID, "prod"); err != nil {
		t.Fatal(err)
	}
	o := reload(t, a, c.ID)
	if len(o.DbTargets) != 1 || o.DbTargets[0].InTree == nil || *o.DbTargets[0].InTree {
		t.Fatalf("shared id should be hidden, not dropped: %+v", o.DbTargets)
	}
	if err := a.DbRemove(c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if o := reload(t, a, c.ID); o.DbEnabled == nil || *o.DbEnabled {
		t.Fatalf("nothing shown: the tab should go off: %+v", o)
	}
}
