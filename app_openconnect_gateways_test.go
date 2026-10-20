package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"ssh-tool/internal/store"
)

func TestOCNormalizeBackups(t *testing.T) {
	c := OpenConnectConfig{Server: "vpn.example.com", Backups: []OpenConnectGateway{
		{Server: " vpn2.example.com:10443 "}, {Server: ""}, {Server: "https://vpn.example.com"}, {Server: "vpn2.example.com:10443"},
	}}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, g := range c.gateways() {
		got = append(got, g.Server)
	}
	want := []string{"https://vpn.example.com", "https://vpn2.example.com:10443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gateways = %v, want %v", got, want)
	}
	c.Backups = []OpenConnectGateway{{Server: "https://"}}
	if c.normalize() == nil {
		t.Fatal("a backup without a host was accepted")
	}
}

// A cookie only goes back to the gateway that issued it.
func TestOCSessionPerGateway(t *testing.T) {
	ocSessionSet("p1", "https://a.example.com", "c1")
	defer ocSessionSet("p1", "", "")
	if got := ocSessionFor("p1", "https://a.example.com"); got != "c1" {
		t.Fatalf("same gateway: %q", got)
	}
	if got := ocSessionFor("p1", "https://b.example.com"); got != "" {
		t.Fatalf("other gateway got the cookie: %q", got)
	}
	if ocSessionGet("p1") != "c1" {
		t.Fatal("profile cookie lost")
	}
}

// A detached session resumes on the backup that holds it, before the main
// gateway.
func TestOCConnectOrder(t *testing.T) {
	gws := []OpenConnectGateway{{Server: "a"}, {Server: "b"}, {Server: "c"}}
	names := func(g []OpenConnectGateway) []string {
		out := []string{}
		for _, x := range g {
			out = append(out, x.Server)
		}
		return out
	}
	if got := names(ocConnectOrder("p2", gws)); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("no session: %v", got)
	}
	ocSessionSet("p2", "c", "cookie")
	defer ocSessionSet("p2", "", "")
	if got := names(ocConnectOrder("p2", gws)); !reflect.DeepEqual(got, []string{"c", "a", "b"}) {
		t.Fatalf("session on c: %v", got)
	}
	if got := names(gws); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("the profile's list changed: %v", got)
	}
}

func TestOCSavePinsPerGateway(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{db: db}
	cfg := OpenConnectConfig{Kind: kindOpenConnect, Server: "https://a.example.com", Flavor: "fortinet",
		Backups: []OpenConnectGateway{{Server: "https://b.example.com"}}}
	b, _ := json.Marshal(cfg)
	row, err := db.CreateNetworkProfile("p", string(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ocSavePins(row.ID, "https://b.example.com", []string{"pin-b"}); err != nil {
		t.Fatal(err)
	}
	if err := a.ocSavePins(row.ID, "", []string{"pin-a"}); err != nil {
		t.Fatal(err)
	}
	if a.ocSavePins(row.ID, "https://c.example.com", []string{"pin-c"}) == nil {
		t.Fatal("a pin for a gateway the profile does not have was saved")
	}
	row, _ = db.GetNetworkProfile(row.ID)
	var got OpenConnectConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Pins, []string{"pin-a"}) || len(got.Backups) != 1 || !reflect.DeepEqual(got.Backups[0].Pins, []string{"pin-b"}) || got.Flavor != "fortinet" {
		t.Fatalf("stored config: %+v", got)
	}
}
