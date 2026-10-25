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

// Every tree connection with a jump host (a connection from the tree or a
// host typed, set on it or a folder) is routed to its own path: an address
// pinned, a name in the names map; NoJumpConnections leaves them out.
func TestJumpConnections(t *testing.T) {
	a := newDbTestApp(t)
	jump, err := a.db.CreateConnection(store.NewConnection{Name: "jump", Hostname: "jump.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	viaRef := &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "jump.example.com", ConnectionID: &jump.ID}}
	typed := &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "other.example.com"}}
	folder, err := a.db.CreateFolder(store.NewFolder{Name: "behind", Settings: store.InheritableSettings{JumpHost: typed}})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, c := range []store.NewConnection{
		{Name: "a", Hostname: "10.40.1.5", Overrides: store.InheritableSettings{JumpHost: viaRef}},
		{Name: "b", Hostname: "app.internal.example", FolderID: &folder.ID},
		{Name: "direct", Hostname: "10.40.1.7"},
	} {
		got, err := a.db.CreateConnection(c)
		if err != nil {
			t.Fatal(err)
		}
		ids[c.Name] = got.ID
	}
	if err := a.db.SetSetting(sharedProxyKey, `{"port":1080}`); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	defer sharedRoutesInvalidate()
	if id, why, err := a.sharedPickWith("10.40.1.5", nil); err != nil || jumpConnOf(id) != ids["a"] || !strings.Contains(why, "pinned") {
		t.Errorf("address: %q %q %v", id, why, err)
	}
	if id, _, err := a.sharedPickWith("APP.internal.example", nil); err != nil || jumpConnOf(id) != ids["b"] {
		t.Errorf("name through a folder's typed jump host: %q, %v", id, err)
	}
	if _, _, err := a.sharedPickWith("10.40.1.7", nil); err == nil {
		t.Error("a connection without a jump host was routed")
	}
	if w, err := a.NetworkSharedProxyWhich("10.40.1.5"); err != nil || w.Profile != "a (its jump host)" {
		t.Errorf("which = %+v, %v", w, err)
	}

	// A CI job resolves the name with the runner's extra hosts and sends
	// the address: it follows the connection's path.
	if _, err := a.CIRunnerSet(CIRunnerConfig{URL: "https://gitlab.example.com", ExtraHosts: []string{"app.internal.example:10.40.9.9"}}, "glrt-x"); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	if id, _, err := a.sharedPickWith("10.40.9.9", nil); err != nil || jumpConnOf(id) != ids["b"] {
		t.Errorf("extra host address: %q, %v", id, err)
	}

	if err := a.db.SetSetting(sharedProxyKey, `{"port":1080,"no_jump_connections":true}`); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	if _, _, err := a.sharedPickWith("10.40.1.5", nil); err == nil {
		t.Error("jump connections still routed with no_jump_connections")
	}
}

// The same address pinned by a profile and by a connection's jump path
// goes the connection's way; a bastion's typed network has no such
// preference over a profile pin.
func TestJumpBeatsProfileOnTie(t *testing.T) {
	a := &App{}
	r := func(cidr, id string) sharedRoute { return sharedRoute{netip.MustParsePrefix(cidr), id, id, true} }
	got := a.mostSpecific([]sharedRoute{r("10.40.1.5/32", "profile"), r("10.40.1.5/32", jumpRouteID("x"))}, map[string]int{"profile": 0})
	if jumpConnOf(got.profileID) != "x" {
		t.Fatalf("jump vs profile: %q", got.profileID)
	}
	got = a.mostSpecific([]sharedRoute{r("10.40.1.5/32", bastionRouteID("b")), r("10.40.1.5/32", "profile")}, map[string]int{"profile": 0})
	if got.profileID != "profile" {
		t.Fatalf("bastion network vs ranked profile: %q", got.profileID)
	}
}
