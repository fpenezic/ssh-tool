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
