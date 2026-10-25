package main

import (
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
