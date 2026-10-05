package resolver

import (
	"fmt"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

func refChain(ids ...string) *store.JumpHostOverride {
	var next *store.JumpHostSpec
	for i := len(ids) - 1; i >= 0; i-- {
		next = &store.JumpHostSpec{ConnectionID: ptr(ids[i]), Via: next}
	}
	return &store.JumpHostOverride{Kind: "chain", Chain: next}
}

func hopsOf(s *store.JumpHostSpec) []store.JumpHostSpec {
	var out []store.JumpHostSpec
	for cur := s; cur != nil; cur = cur.Via {
		out = append(out, *cur)
	}
	return out
}

// A hop that references a saved bastion takes that connection's resolved
// host, port, user and credential - including what the bastion inherits
// from its own folder - not the target's.
func TestJumpRefUsesBastionSettings(t *testing.T) {
	folders := []store.Folder{
		folder("infra", nil, store.InheritableSettings{Username: ptr("jumper"), AuthRef: ptr("cred-bastion")}),
		folder("apps", nil, store.InheritableSettings{Username: ptr("app"), AuthRef: ptr("cred-app")}),
	}
	bastion := conn("b1", ptr("infra"), "bastion.example.com", store.InheritableSettings{Port: ptr(uint16(2222))})
	target := conn("t1", ptr("apps"), "db.internal.example.com", store.InheritableSettings{JumpHost: refChain("b1")})

	rs := ResolveWith(target, folders)
	if err := ExpandJumpRefs(&rs, target.ID, folders, LookupFrom([]store.Connection{bastion, target})); err != nil {
		t.Fatal(err)
	}
	hops := hopsOf(rs.JumpHost)
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	h := hops[0]
	if h.Hostname != "bastion.example.com" || *h.Port != 2222 || *h.Username != "jumper" || *h.AuthRef != "cred-bastion" || h.Name != "b1" {
		t.Errorf("hop = %+v (user %v auth %v)", h, *h.Username, *h.AuthRef)
	}
	if h.ConnectionID != nil {
		t.Error("expanded hop still carries a reference")
	}
	if *rs.Username != "app" || *rs.AuthRef != "cred-app" {
		t.Error("target settings changed by the expansion")
	}
}

// A bastion that itself sits behind a bastion: its own chain comes first,
// and those hops default to the bastion's user, not the target's.
func TestJumpRefNestsBastionChain(t *testing.T) {
	inner := conn("inner", nil, "inner.example.com", store.InheritableSettings{
		Username: ptr("i"),
		JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "edge.example.com"}},
	})
	target := conn("t", nil, "t.example.com", store.InheritableSettings{Username: ptr("t"), JumpHost: refChain("inner")})
	rs := ResolveWith(target, nil)
	if err := ExpandJumpRefs(&rs, "t", nil, LookupFrom([]store.Connection{inner, target})); err != nil {
		t.Fatal(err)
	}
	hops := hopsOf(rs.JumpHost)
	if len(hops) != 2 || hops[0].Hostname != "edge.example.com" || hops[1].Hostname != "inner.example.com" {
		t.Fatalf("order: %+v", hops)
	}
	if *hops[0].Username != "i" {
		t.Errorf("bastion's own hop took user %q, want the bastion's %q", *hops[0].Username, "i")
	}
}

func TestJumpRefLoopAndMissing(t *testing.T) {
	a := conn("a", nil, "a.example.com", store.InheritableSettings{JumpHost: refChain("b")})
	b := conn("b", nil, "b.example.com", store.InheritableSettings{JumpHost: refChain("a")})
	rs := ResolveWith(a, nil)
	err := ExpandJumpRefs(&rs, "a", nil, LookupFrom([]store.Connection{a, b}))
	if err == nil || !strings.Contains(err.Error(), "loops back") {
		t.Errorf("loop: got %v", err)
	}

	gone := conn("g", nil, "g.example.com", store.InheritableSettings{JumpHost: refChain("deleted")})
	rs = ResolveWith(gone, nil)
	if err := ExpandJumpRefs(&rs, "g", nil, LookupFrom(nil)); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Errorf("missing: got %v", err)
	}
}

// The same bastion used twice in a row is odd but not a loop.
func TestJumpRefSameBastionTwiceIsNotALoop(t *testing.T) {
	b := conn("b", nil, "b.example.com", store.InheritableSettings{})
	tg := conn("t", nil, "t.example.com", store.InheritableSettings{JumpHost: refChain("b", "b")})
	rs := ResolveWith(tg, nil)
	if err := ExpandJumpRefs(&rs, "t", nil, LookupFrom([]store.Connection{b, tg})); err != nil {
		t.Fatal(err)
	}
	if n := len(hopsOf(rs.JumpHost)); n != 2 {
		t.Errorf("hops = %d", n)
	}
}

// A bastion only reachable over a VPN: its network profile moves to the
// target's first hop when the target has none of its own.
func TestJumpRefCarriesFirstHopNetworkProfile(t *testing.T) {
	b := conn("b", nil, "b.example.com", store.InheritableSettings{NetworkProfileID: ptr("wg-1")})
	tg := conn("t", nil, "t.example.com", store.InheritableSettings{JumpHost: refChain("b")})
	rs := ResolveWith(tg, nil)
	if err := ExpandJumpRefs(&rs, "t", nil, LookupFrom([]store.Connection{b, tg})); err != nil {
		t.Fatal(err)
	}
	if rs.NetworkProfileID == nil || *rs.NetworkProfileID != "wg-1" {
		t.Errorf("network profile = %v", rs.NetworkProfileID)
	}

	// Not when the referenced bastion is a later hop.
	tg2 := conn("t2", nil, "t2.example.com", store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain",
		Chain: &store.JumpHostSpec{Hostname: "edge.example.com", Via: &store.JumpHostSpec{ConnectionID: ptr("b")}}}})
	rs = ResolveWith(tg2, nil)
	if err := ExpandJumpRefs(&rs, "t2", nil, LookupFrom([]store.Connection{b, tg2})); err != nil {
		t.Fatal(err)
	}
	if rs.NetworkProfileID != nil {
		t.Errorf("later-hop bastion must not set the profile, got %v", *rs.NetworkProfileID)
	}
}

// Expansion builds new hops; the folder's stored chain stays a reference.
func TestJumpRefDoesNotMutateStoredChain(t *testing.T) {
	f := folder("f", nil, store.InheritableSettings{JumpHost: refChain("b")})
	b := conn("b", nil, "b.example.com", store.InheritableSettings{})
	tg := conn("t", ptr("f"), "t.example.com", store.InheritableSettings{})
	rs := ResolveWith(tg, []store.Folder{f})
	if err := ExpandJumpRefs(&rs, "t", []store.Folder{f}, LookupFrom([]store.Connection{b, tg})); err != nil {
		t.Fatal(err)
	}
	if f.Settings.JumpHost.Chain.ConnectionID == nil || f.Settings.JumpHost.Chain.Hostname != "" {
		t.Error("stored folder chain was modified")
	}
}

// An inventory host as the bastion: the reference carries folder + the
// provider's id, and resolves through DynamicHostLookup.
func TestJumpRefInventoryHost(t *testing.T) {
	ref := DynRef("pxmx", "qemu/101")
	if f, e, ok := ParseDynRef(ref); !ok || f != "pxmx" || e != "qemu/101" {
		t.Fatalf("ParseDynRef(%q) = %q %q %v", ref, f, e, ok)
	}
	old := DynamicHostLookup
	defer func() { DynamicHostLookup = old }()
	DynamicHostLookup = func(r string) (*store.Connection, error) {
		if r != ref {
			return nil, fmt.Errorf("jump host gone")
		}
		return &store.Connection{ID: r, Name: "vpn-01", Hostname: "192.0.2.7",
			Overrides: store.InheritableSettings{Username: ptr("root")}}, nil
	}
	tg := conn("t", nil, "t.example.com", store.InheritableSettings{JumpHost: refChain(ref)})
	rs := ResolveWith(tg, nil)
	if err := ExpandJumpRefs(&rs, "t", nil, LookupFrom(nil)); err != nil {
		t.Fatal(err)
	}
	h := hopsOf(rs.JumpHost)
	if len(h) != 1 || h[0].Hostname != "192.0.2.7" || h[0].Name != "vpn-01" || *h[0].Username != "root" {
		t.Errorf("hop = %+v", h)
	}

	gone := conn("g", nil, "g.example.com", store.InheritableSettings{JumpHost: refChain(DynRef("pxmx", "qemu/999"))})
	rs = ResolveWith(gone, nil)
	if err := ExpandJumpRefs(&rs, "g", nil, LookupFrom(nil)); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Errorf("missing inventory host: got %v", err)
	}
}

// The common layout: a folder's jump host is a bastion that lives in that
// same folder. The bastion inherits a chain through itself and must dial
// directly; its neighbours go through it; a bastion with an edge hop before
// it in the folder chain keeps that edge hop.
func TestJumpRefBastionInsideItsFolder(t *testing.T) {
	f := folder("f", nil, store.InheritableSettings{JumpHost: refChain("b")})
	b := conn("b", ptr("f"), "b.example.com", store.InheritableSettings{})
	app := conn("app", ptr("f"), "app.example.com", store.InheritableSettings{})
	all := LookupFrom([]store.Connection{b, app})

	rs := ResolveWith(b, []store.Folder{f})
	if err := ExpandJumpRefs(&rs, "b", []store.Folder{f}, all); err != nil {
		t.Fatalf("bastion itself: %v", err)
	}
	if rs.JumpHost != nil {
		t.Errorf("bastion must dial directly, got %+v", hopsOf(rs.JumpHost))
	}

	rs = ResolveWith(app, []store.Folder{f})
	if err := ExpandJumpRefs(&rs, "app", []store.Folder{f}, all); err != nil {
		t.Fatalf("neighbour: %v", err)
	}
	if h := hopsOf(rs.JumpHost); len(h) != 1 || h[0].Hostname != "b.example.com" {
		t.Errorf("neighbour hops = %+v", h)
	}

	edge := folder("e", nil, store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain",
		Chain: &store.JumpHostSpec{Hostname: "edge.example.com", Via: &store.JumpHostSpec{ConnectionID: ptr("b2")}}}})
	b2 := conn("b2", ptr("e"), "b2.example.com", store.InheritableSettings{})
	app2 := conn("app2", ptr("e"), "app2.example.com", store.InheritableSettings{})
	all2 := LookupFrom([]store.Connection{b2, app2})
	rs = ResolveWith(b2, []store.Folder{edge})
	if err := ExpandJumpRefs(&rs, "b2", []store.Folder{edge}, all2); err != nil {
		t.Fatal(err)
	}
	if h := hopsOf(rs.JumpHost); len(h) != 1 || h[0].Hostname != "edge.example.com" {
		t.Errorf("bastion behind an edge hop = %+v", h)
	}
	rs = ResolveWith(app2, []store.Folder{edge})
	if err := ExpandJumpRefs(&rs, "app2", []store.Folder{edge}, all2); err != nil {
		t.Fatal(err)
	}
	if h := hopsOf(rs.JumpHost); len(h) != 2 || h[0].Hostname != "edge.example.com" || h[1].Hostname != "b2.example.com" {
		t.Errorf("neighbour behind edge+bastion = %+v", h)
	}
}
