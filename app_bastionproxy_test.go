package main

import (
	"net/netip"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// A bastion is stored with its networks normalised; it must name a real
// connection, have networks, and not take every address. Its routes join
// the shared table pinned, and the picker sends its addresses to it.
func TestBastionSetting(t *testing.T) {
	a := newDbTestApp(t)
	c, err := a.db.CreateConnection(store.NewConnection{Name: "jump", Hostname: "jump.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.normalizeBastions([]ProxyBastion{{ConnectionID: c.ID, Networks: []string{" 10.20.0.0/16", "10.20.1.5", ""}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || strings.Join(got[0].Networks, ",") != "10.20.0.0/16" {
		t.Fatalf("got %+v", got)
	}
	for name, in := range map[string][]ProxyBastion{
		"unknown connection": {{ConnectionID: "nope", Networks: []string{"10.0.0.0/8"}}},
		"no networks":        {{ConnectionID: c.ID}},
		"default route":      {{ConnectionID: c.ID, Networks: []string{"0.0.0.0/0"}}},
		"twice":              {{ConnectionID: c.ID, Networks: []string{"10.1.0.0/16"}}, {ConnectionID: c.ID, Networks: []string{"10.2.0.0/16"}}},
	} {
		if _, err := a.normalizeBastions(in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// A saved bastion whose connection is gone is dropped, not an error.
	gone, err := a.db.CreateConnection(store.NewConnection{Name: "gone", Hostname: "gone.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetSetting(sharedProxyKey, `{"port":1080,"bastions":[{"connection_id":"`+gone.ID+`","networks":["10.9.0.0/16"]}]}`); err != nil {
		t.Fatal(err)
	}
	if err := a.db.DeleteConnection(gone.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := a.normalizeBastions([]ProxyBastion{{ConnectionID: gone.ID, Networks: []string{"10.9.0.0/16"}}}); err != nil || len(got) != 0 {
		t.Fatalf("deleted bastion: %+v, %v", got, err)
	}

	if err := a.db.SetSetting(sharedProxyKey, `{"enabled":false,"port":1080,"bastions":[{"connection_id":"`+c.ID+`","networks":["10.20.0.0/16"]}]}`); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	defer sharedRoutesInvalidate()
	id, why, err := a.sharedPickWith("10.20.3.4", nil)
	if err != nil || bastionOf(id) != c.ID {
		t.Fatalf("pick = %q, %q, %v", id, why, err)
	}
	if !strings.Contains(why, "pinned") {
		t.Errorf("why = %q", why)
	}
	w, err := a.NetworkSharedProxyWhich("10.20.3.4")
	if err != nil || w.Profile != "jump (bastion)" {
		t.Fatalf("which = %+v, %v", w, err)
	}
}

// Connections that jump through a bastion go to it without a network
// typed: by the jump's connection reference or by the same host and port
// typed by hand; an address is pinned, a name joins the names map.
func TestBastionJumpConnections(t *testing.T) {
	a := newDbTestApp(t)
	jump, err := a.db.CreateConnection(store.NewConnection{Name: "jump", Hostname: "jump.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	viaRef := &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "jump.example.com", ConnectionID: &jump.ID}}
	viaTyped := &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "JUMP.example.com"}}
	elsewhere := &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "other.example.com"}}
	for _, c := range []store.NewConnection{
		{Name: "a", Hostname: "10.40.1.5", Overrides: store.InheritableSettings{JumpHost: viaRef}},
		{Name: "b", Hostname: "app.internal.example", Overrides: store.InheritableSettings{JumpHost: viaTyped}},
		{Name: "c", Hostname: "10.40.1.6", Overrides: store.InheritableSettings{JumpHost: elsewhere}},
	} {
		if _, err := a.db.CreateConnection(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.db.SetSetting(sharedProxyKey, `{"port":1080,"bastions":[{"connection_id":"`+jump.ID+`","networks":["192.0.2.0/24"]}]}`); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	defer sharedRoutesInvalidate()
	if id, _, err := a.sharedPickWith("10.40.1.5", nil); err != nil || bastionOf(id) != jump.ID {
		t.Errorf("connection address: %q, %v", id, err)
	}
	if id, _, err := a.sharedPickWith("APP.internal.example", nil); err != nil || bastionOf(id) != jump.ID {
		t.Errorf("connection name: %q, %v", id, err)
	}
	if _, _, err := a.sharedPickWith("10.40.1.6", nil); err == nil {
		t.Error("a connection jumping elsewhere was routed to the bastion")
	}
}

// With the same pinned address on a bastion and a profile, the bastion
// takes it.
func TestBastionBeatsProfileOnTie(t *testing.T) {
	a := &App{}
	r := func(cidr, id string) sharedRoute { return sharedRoute{netip.MustParsePrefix(cidr), id, id, true} }
	got := a.mostSpecific([]sharedRoute{r("10.40.1.5/32", "profile"), r("10.40.1.5/32", bastionRouteID("x"))}, map[string]int{"profile": 0})
	if bastionOf(got.profileID) != "x" {
		t.Fatalf("got %q", got.profileID)
	}
}
