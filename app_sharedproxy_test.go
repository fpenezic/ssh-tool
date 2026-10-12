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

// The shared proxy picks the most specific route; the manual "use"
// profile takes whatever its networks contain; names of connections map
// straight to their profile; an uncovered destination is refused.
func TestSharedPick(t *testing.T) {
	a := &App{}
	r := func(cidr, id string) sharedRoute {
		return sharedRoute{netip.MustParsePrefix(cidr), id, id}
	}
	sharedTable.Lock()
	sharedTable.at = time.Now()
	sharedTable.routes = []sharedRoute{
		r("10.0.0.0/8", "wide"),
		r("10.1.0.0/16", "b"),
		r("192.0.2.0/24", "c"),
	}
	sharedTable.hosts = map[string]string{"app01.customer.example": "c"}
	sharedTable.Unlock()
	defer sharedRoutesInvalidate()
	for host, want := range map[string]string{
		"10.9.9.9":               "wide",
		"10.1.2.3":               "b",
		"192.0.2.5":              "c",
		"APP01.customer.example": "c",
	} {
		got, err := a.sharedPickWith(host, "")
		if err != nil || got != want {
			t.Errorf("%s -> %q, %v (want %q)", host, got, err, want)
		}
	}
	if _, err := a.sharedPickWith("203.0.113.1", ""); err == nil {
		t.Error("uncovered destination accepted")
	}
	// Manual override: "wide" takes 10.1.2.3 although "b" is more
	// specific; outside its networks the routes still decide.
	if got, _ := a.sharedPickWith("10.1.2.3", "wide"); got != "wide" {
		t.Errorf("override: %q", got)
	}
	if got, _ := a.sharedPickWith("192.0.2.5", "wide"); got != "c" {
		t.Errorf("override outside its networks: %q", got)
	}
}
