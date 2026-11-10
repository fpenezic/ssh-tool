package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/store"
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

func TestNormalizeDomain(t *testing.T) {
	for in, want := range map[string]string{
		"Example.Local":      "example.local",
		" *.corp.example. ":  "corp.example",
		".example.local":     "example.local",
		"_msdcs.example.lan": "_msdcs.example.lan",
		"corp":               "corp",
	} {
		if got, err := normalizeDomain(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "a..b", "exa mple.local", "ex/ample", "10.0.0.0/8", "1", "0.2.1", "example.123"} {
		if _, err := normalizeDomain(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDomainOf(t *testing.T) {
	ds := []string{"example.local", "prod.example.local", "corp"}
	for host, want := range map[string]string{
		"db1.prod.example.local":  "prod.example.local",
		"DB1.Example.Local.":      "example.local",
		"example.local":           "example.local",
		"notexample.local":        "",
		"host.corp":               "corp",
		"example.local.other.net": "",
	} {
		if got := domainOf(host, ds); got != want {
			t.Errorf("%s -> %q (want %q)", host, got, want)
		}
	}
}

// A name in a shared profile's DNS domain goes there by name (resolved
// inside the VPN), before any local lookup; the longest domain wins, and
// between profiles with one domain the priority decides. A short name
// is tried in the search domains of the first profile.
func TestSharedPickDomains(t *testing.T) {
	a := &App{}
	sharedTable.Lock()
	sharedTable.at = time.Now()
	sharedTable.routes = []sharedRoute{{netip.MustParsePrefix("10.0.0.0/8"), "net", "net", false}}
	sharedTable.hosts = map[string]string{"app01.customer.example": "c"}
	sharedTable.domains = []sharedDomain{
		{"example.local", "a", "a", true},
		{"prod.example.local", "b", "b", false},
		{"shared.example", "a", "a", false},
		{"shared.example", "b", "b", false},
	}
	sharedTable.search = []sharedSearch{
		{"b", "b", []string{"prod.example.local", "example.local"}},
		{"a", "a", []string{"example.local"}},
	}
	sharedTable.Unlock()
	defer sharedRoutesInvalidate()
	pick := func(host string, prio ...string) (string, string, []string) {
		t.Helper()
		id, why, dial, err := a.sharedPickHosts(host, prio)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		return id, why, dial
	}
	if id, why, dial := pick("DB1.example.local"); id != "a" || dial[0] != "db1.example.local" || why != "domain example.local (from the gateway)" {
		t.Errorf("example.local: %s %q %v", id, why, dial)
	}
	if id, _, _ := pick("db1.prod.example.local"); id != "b" {
		t.Errorf("longest domain: %s", id)
	}
	// Ties go by priority (after it, a tunnel that is up, which needs the
	// tunnel manager this test has none of).
	if id, _, _ := pick("x.shared.example", "a"); id != "a" {
		t.Errorf("same domain, by priority a: %s", id)
	}
	if id, _, _ := pick("x.shared.example", "b"); id != "b" {
		t.Errorf("same domain, by priority: %s", id)
	}
	// A connection's own name still wins over a domain.
	sharedTable.Lock()
	sharedTable.hosts["db9.example.local"] = "c"
	sharedTable.Unlock()
	if id, _, _ := pick("db9.example.local"); id != "c" {
		t.Errorf("connection name: %s", id)
	}
	// Short names: the search domains of the first profile, in order.
	s, names, ok := a.sharedBySearch("db1", sharedTable.search, map[string]int{"a": 0})
	if !ok || s.profileID != "a" || len(names) != 1 || names[0] != "db1.example.local" {
		t.Errorf("search by priority: %v %v %v", s, names, ok)
	}
	s, names, ok = a.sharedBySearch("DB1", sharedTable.search, map[string]int{"b": 0, "a": 1})
	if !ok || s.profileID != "b" || strings.Join(names, ",") != "db1.prod.example.local,db1.example.local" {
		t.Errorf("search domains in order: %v %v %v", s, names, ok)
	}
	if _, _, ok := a.sharedBySearch("db1.other", sharedTable.search, nil); ok {
		t.Error("a dotted name was expanded")
	}
}

// A domain added by hand beats a gateway's of another profile, whatever
// the lengths; gateway against gateway and hand against hand go by the
// longest suffix, then priority. Clashes are listed for the details.
func TestSharedDomainHandBeatsGateway(t *testing.T) {
	a := &App{}
	domains := []sharedDomain{
		{"vpn.example.com", "a", "A", true},
		{"example.com", "b", "B", false},
		{"corp.test", "a", "A", true},
		{"x.corp.test", "c", "C", true},
		{"lab.test", "a", "A", false},
		{"lab.test", "c", "C", true},
		{"same.test", "a", "A", true},
		{"same.test", "c", "C", true},
	}
	cases := []struct{ host, want, suffix string }{
		{"db1.vpn.example.com", "b", "example.com"}, // gateway A longer, hand B wins
		{"db1.example.com", "b", "example.com"},
		{"h.x.corp.test", "c", "x.corp.test"}, // gateway vs gateway: longest
		{"h.corp.test", "a", "corp.test"},
		{"h.lab.test", "a", "lab.test"}, // same suffix: hand wins over gateway
	}
	for _, c := range cases {
		d, ok := a.sharedByDomain(c.host, domains, map[string]int{"c": 0})
		if !ok || d.profileID != c.want || d.suffix != c.suffix {
			t.Errorf("%s: got %s %s, want %s %s", c.host, d.profileID, d.suffix, c.want, c.suffix)
		}
	}
	sharedTable.Lock()
	sharedTable.at = time.Now()
	sharedTable.routes, sharedTable.hosts, sharedTable.domains = nil, map[string]string{}, domains
	sharedTable.Unlock()
	defer sharedRoutesInvalidate()
	if id, why, _, err := a.sharedPickHosts("db1.vpn.example.com", nil); err != nil || id != "b" || why != "domain example.com (added by hand; beats vpn.example.com from the gateway of A)" {
		t.Errorf("why: %s %q %v", id, why, err)
	}
	// The same domain on two gateways goes by priority.
	if d, _ := a.sharedByDomain("h.same.test", domains, map[string]int{"a": 0, "c": 1}); d.profileID != "a" {
		t.Errorf("gateway tie by priority a: %s", d.profileID)
	}
	if d, _ := a.sharedByDomain("h.same.test", domains, map[string]int{"c": 0}); d.profileID != "c" {
		t.Errorf("gateway tie by priority c: %s", d.profileID)
	}
	got := strings.Join(domainClashes(domains, map[string]int{"c": 0}), "\n")
	for _, want := range []string{
		"domain vpn.example.com (A, from the gateway) inside example.com (B, by hand) - names in vpn.example.com go to B, added by hand",
		"domain x.corp.test (C, from the gateway) inside corp.test (A, from the gateway) - names in x.corp.test go to C",
		"domain lab.test: A and C - A wins (added by hand)",
		"domain same.test: A and C - C wins",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clashes miss %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 3 {
		t.Errorf("clashes:\n%s", got)
	}
	if l := domainClashes(domains, nil); !strings.Contains(strings.Join(l, "\n"), "same.test: A and C - whichever is connected wins") {
		t.Errorf("no priority: %v", l)
	}
}

// A domain the gateway reports and the user also added by hand counts as
// added by hand.
func TestProfileDomainsHandOverGateway(t *testing.T) {
	row := &store.NetworkProfile{ConfigJSON: `{"last_domains":["corp.example.com","gw.example.com"]}`}
	doms, gw, _ := profileDomains(row, LocalProxyConfig{Domains: []string{"Corp.Example.com."}})
	if strings.Join(doms, ",") != "corp.example.com,gw.example.com" || gw["corp.example.com"] || !gw["gw.example.com"] {
		t.Errorf("domains %v gateway %v", doms, gw)
	}
}

// The whole pick for a short name the local resolver does not know: it
// goes to the first profile's search domains, every expansion in order;
// a name with a trailing dot finds its connection before any domain.
func TestSharedPickShortNameAndTrailingDot(t *testing.T) {
	a := &App{}
	old := sharedLookup
	sharedLookup = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		return nil, errors.New("no such host")
	}
	defer func() { sharedLookup = old }()
	sharedTable.Lock()
	sharedTable.at = time.Now()
	sharedTable.routes = nil
	sharedTable.hosts = map[string]string{"db1.example.com": "conn"}
	sharedTable.domains = []sharedDomain{{"example.com", "a", "a", true}}
	sharedTable.search = []sharedSearch{{"a", "a", []string{"prod.example.com", "example.com"}}}
	sharedTable.Unlock()
	defer sharedRoutesInvalidate()
	id, why, dial, err := a.sharedPickHosts("db7", nil)
	if err != nil || id != "a" || strings.Join(dial, ",") != "db7.prod.example.com,db7.example.com" || !strings.Contains(why, "search domains") {
		t.Errorf("short name: %s %q %v %v", id, why, dial, err)
	}
	if id, _, _, err := a.sharedPickHosts("DB1.example.com.", nil); err != nil || id != "conn" {
		t.Errorf("trailing dot: %s %v", id, err)
	}
	if _, _, _, err := a.sharedPickHosts("db7.other.net", nil); err == nil {
		t.Error("an unknown dotted name was accepted")
	}
	// An address with a trailing dot goes by the networks, even when a
	// domain table somehow holds a numeric suffix.
	sharedTable.Lock()
	sharedTable.domains = append(sharedTable.domains, sharedDomain{"1", "a", "a", false})
	sharedTable.Unlock()
	if _, _, _, err := a.sharedPickHosts("192.0.2.1.", nil); err == nil {
		t.Error("an address with a trailing dot passed as a name")
	}
}

// What a helper reports is remembered: routes and domains; a later
// report with routes but no domains clears them (the gateway stopped
// pushing them), a report without routes changes only what it carries.
func TestRememberGateway(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	np, err := db.CreateNetworkProfile("oc", `{"kind":"openconnect","server":"vpn.example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	read := func() (domains, search []string) {
		t.Helper()
		row, err := db.GetNetworkProfile(np.ID)
		if err != nil {
			t.Fatal(err)
		}
		d, _, s := profileDomains(row, LocalProxyConfig{})
		return d, s
	}
	a.rememberGateway(np.ID, json.RawMessage(`{"routes":["10.1.0.0/16"],"search_domains":["Example.COM"],"split_dns":["corp.example.com","example.com"]}`))
	if d, s := read(); strings.Join(d, ",") != "example.com,corp.example.com" || strings.Join(s, ",") != "example.com" {
		t.Fatalf("first report: %v %v", d, s)
	}
	a.rememberGateway(np.ID, json.RawMessage(`{"search_domains":["other.example"]}`))
	if d, _ := read(); strings.Join(d, ",") != "other.example" {
		t.Errorf("partial report: %v", d)
	}
	a.rememberGateway(np.ID, json.RawMessage(`{"routes":["10.1.0.0/16"]}`))
	if d, s := read(); len(d) != 0 || len(s) != 0 {
		t.Errorf("complete report without domains kept %v %v", d, s)
	}
	// An empty routes list is a whole report too: routes and domains go.
	a.rememberGateway(np.ID, json.RawMessage(`{"routes":["10.1.0.0/16"],"search_domains":["example.com"]}`))
	a.rememberGateway(np.ID, json.RawMessage(`{"routes":[]}`))
	if row, _ := db.GetNetworkProfile(np.ID); strings.Contains(row.ConfigJSON, "last_routes") || strings.Contains(row.ConfigJSON, "last_domains") {
		t.Errorf("empty routes list kept the old state: %s", row.ConfigJSON)
	}
	// A full-tunnel report (default route only) is complete as well.
	a.rememberGateway(np.ID, json.RawMessage(`{"routes":["10.1.0.0/16"],"search_domains":["example.com"]}`))
	a.rememberGateway(np.ID, json.RawMessage(`{"routes":["0.0.0.0/0"]}`))
	if d, _ := read(); len(d) != 0 {
		t.Errorf("default-route-only report kept %v", d)
	}
	// An edit of the profile keeps what was learned.
	got := carryIdleStop(`{"last_domains":["example.com"],"last_search_domains":["example.com"]}`, `{"kind":"openconnect"}`)
	if !strings.Contains(got, `"last_domains"`) || !strings.Contains(got, `"last_search_domains"`) {
		t.Errorf("carry: %s", got)
	}
}

func TestNetbirdKeyProblem(t *testing.T) {
	if netbirdKeyProblem("A1B2C3D4-0000-4000-8000-000000000000") != "" {
		t.Error("a setup key was refused")
	}
	if netbirdKeyProblem(" nbp_abcdefghijklmnopqrstuvwxyz0123456789 ") == "" {
		t.Error("a personal access token was taken")
	}
}

func TestNetbirdKeyFromCommand(t *testing.T) {
	for in, want := range map[string]string{
		"netbird up --management-url https://vpn.example.com --setup-key K-1": "K-1",
		`netbird up -k "K-2"`:                  "K-2",
		"netbird up --setup-key=K-3":           "K-3",
		"netbird up --setup-key=OLD -k NEW":    "NEW",
		"netbird up --setup-key=":              "",
		"netbird up -k K # -k X":               "K",
		"A1B2C3D4-0000-4000-8000-000000000000": "A1B2C3D4-0000-4000-8000-000000000000",
	} {
		if got := netbirdKeyFromCommand(in); got != want {
			t.Errorf("%q -> %q (want %q)", in, got, want)
		}
	}
}
