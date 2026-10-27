package main

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/store"
)

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
		{Name: "c", Hostname: "c.internal.example", Overrides: store.InheritableSettings{JumpHost: viaRef}},
	} {
		got, err := a.db.CreateConnection(c)
		if err != nil {
			t.Fatal(err)
		}
		ids[c.Name] = got.ID
	}
	if err := a.db.SetSetting(sharedProxyKey, `{"port":1080,"jump_connections":true}`); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	defer sharedRoutesInvalidate()
	if id, why, err := a.sharedPickWith("10.40.1.5", nil); err != nil || jumpConnOf(id) != ids["a"] || !strings.Contains(why, "jump host") {
		t.Errorf("address: %q %q %v", id, why, err)
	}
	if id, _, err := a.sharedPickWith("APP.internal.example", nil); err != nil || jumpConnOf(id) != ids["b"] {
		t.Errorf("name through a folder's typed jump host: %q, %v", id, err)
	}
	if _, _, err := a.sharedPickWith("10.40.1.7", nil); err == nil {
		t.Error("a connection without a jump host was routed")
	}
	if w, err := a.NetworkSharedProxyWhich("10.40.1.5"); err != nil || w.Profile != "a" {
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
	// Two extra-host names on one address with different paths: the job
	// sends only the address, so neither path is pinned to it.
	if _, err := a.CIRunnerSet(CIRunnerConfig{URL: "https://gitlab.example.com", ExtraHosts: []string{
		"app.internal.example:10.40.9.9", "c.internal.example:10.40.9.9"}}, "glrt-x"); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	if id, _, err := a.sharedPickWith("10.40.9.9", nil); err == nil {
		t.Errorf("conflicting extra hosts pinned the address to %q", id)
	}

	if err := a.db.SetSetting(sharedProxyKey, `{"port":1080}`); err != nil {
		t.Fatal(err)
	}
	sharedRoutesInvalidate()
	if _, _, err := a.sharedPickWith("10.40.1.5", nil); err == nil {
		t.Error("jump connections routed without jump_connections")
	}
}

// The same address pinned by a profile and by a connection's jump path
// goes the connection's way.
func TestJumpBeatsProfileOnTie(t *testing.T) {
	a := &App{}
	r := func(cidr, id string) sharedRoute { return sharedRoute{netip.MustParsePrefix(cidr), id, id, true} }
	got := a.mostSpecific([]sharedRoute{r("10.40.1.5/32", "profile"), r("10.40.1.5/32", jumpRouteID("x"))}, map[string]int{"profile": 0})
	if jumpConnOf(got.profileID) != "x" {
		t.Fatalf("jump vs profile: %q", got.profileID)
	}
}

func TestJumpKeyLabel(t *testing.T) {
	key := "np=prof\x1fjump-a.example.com:22|root|cred\x1fjump-b.example.com:2222|ops|"
	if got := jumpKeyLabel(key); got != "jump-a.example.com > jump-b.example.com:2222" {
		t.Fatalf("got %q", got)
	}
}

// Two connections with the same address and different jump paths leave it
// unrouted by jump host; the same path twice still routes it.
func TestJumpDuplicateAddress(t *testing.T) {
	routes, _ := jumpConnRoutes([]store.Connection{
		{ID: "1", Name: "one", Hostname: "10.50.0.1", Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "jump-a.example.com"}}}},
		{ID: "2", Name: "two", Hostname: "10.50.0.1", Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "jump-b.example.com"}}}},
		{ID: "3", Name: "three", Hostname: "10.50.0.2", Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "jump-a.example.com"}}}},
		{ID: "4", Name: "four", Hostname: "10.50.0.2", Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "jump-a.example.com"}}}},
	}, nil)
	if len(routes) != 1 || routes[0].prefix.String() != "10.50.0.2/32" || jumpConnOf(routes[0].profileID) != "3" {
		t.Fatalf("routes = %+v", routes)
	}
}

// A name only the local resolver knows is decided by its local address,
// and that address - not the name - is what the proxy dials; a
// connection's own name is dialed as is.
func TestSharedPickDialsWhatWasAllowed(t *testing.T) {
	a := &App{}
	sharedTable.Lock()
	sharedTable.at = time.Now()
	sharedTable.routes = []sharedRoute{
		{netip.MustParsePrefix("127.0.0.1/32"), jumpRouteID("c"), jumpRouteName, true},
		{netip.MustParsePrefix("::1/128"), jumpRouteID("c"), jumpRouteName, true},
	}
	sharedTable.hosts = map[string]string{"app.internal.example": jumpRouteID("c")}
	sharedTable.Unlock()
	defer sharedRoutesInvalidate()
	_, _, dial, err := a.sharedPickAddr("localhost", nil)
	if err != nil || (dial != "127.0.0.1" && dial != "::1") {
		t.Fatalf("locally resolved name: dial %q, %v", dial, err)
	}
	_, _, dial, err = a.sharedPickAddr("app.internal.example", nil)
	if err != nil || dial != "app.internal.example" {
		t.Fatalf("connection name: dial %q, %v", dial, err)
	}
}
