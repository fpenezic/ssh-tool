package main

import (
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func TestCollapseRoutes(t *testing.T) {
	got := collapseRoutes([]string{"10.243.158.138/32", "10.243.0.0/16", "10.12.0.0/16", "10.243.158.0/24", "192.0.2.7/32"})
	want := []string{"10.12.0.0/16", "10.243.0.0/16", "192.0.2.7/32"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestDisplayHost(t *testing.T) {
	for in, want := range map[string]string{"0.0.0.0": "127.0.0.1", "": "127.0.0.1", "::": "::1", "172.17.0.1": "172.17.0.1"} {
		if got := displayHost(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

// The shared proxy picks the most specific route; a profile in priority
// takes whatever its networks contain; a pinned address beats both;
// names of connections map straight to their profile; an uncovered
// destination is refused.
func TestSharedPick(t *testing.T) {
	a := &App{}
	r := func(cidr, id string, pinned bool) sharedRoute {
		return sharedRoute{netip.MustParsePrefix(cidr), id, id, pinned}
	}
	sharedTable.Lock()
	sharedTable.at = time.Now()
	sharedTable.routes = []sharedRoute{
		r("10.0.0.0/8", "wide", false),
		r("10.0.0.0/8", "other", false),
		r("10.1.0.0/16", "b", false),
		r("10.7.0.9/32", "other", true),
		r("192.0.2.0/24", "c", false),
	}
	sharedTable.hosts = map[string]string{"app01.customer.example": "c"}
	sharedTable.Unlock()
	defer sharedRoutesInvalidate()
	pick := func(host string, prio ...string) string {
		t.Helper()
		id, _, err := a.sharedPickWith(host, prio)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		return id
	}
	for host, want := range map[string]string{
		"10.1.2.3":               "b",
		"192.0.2.5":              "c",
		"APP01.customer.example": "c",
		"10.7.0.9":               "other",
	} {
		if got := pick(host); got != want {
			t.Errorf("%s -> %q (want %q)", host, got, want)
		}
	}
	// Two profiles with the same /8: priority decides, also over a more
	// specific route of a profile not listed; outside its networks the
	// routes still decide; a pinned address beats the priority.
	if got := pick("10.9.9.9", "wide"); got != "wide" {
		t.Errorf("priority wide: %q", got)
	}
	if got := pick("10.9.9.9", "other", "wide"); got != "other" {
		t.Errorf("priority other: %q", got)
	}
	if got := pick("10.1.2.3", "wide"); got != "wide" {
		t.Errorf("priority over a more specific route: %q", got)
	}
	if got := pick("192.0.2.5", "wide"); got != "c" {
		t.Errorf("priority outside its networks: %q", got)
	}
	if got := pick("10.7.0.9", "wide"); got != "other" {
		t.Errorf("pin under priority: %q", got)
	}
	if _, _, err := a.sharedPickWith("203.0.113.1", nil); err == nil {
		t.Error("uncovered destination accepted")
	}
}
