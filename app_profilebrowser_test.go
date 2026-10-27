package main

import (
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// The browser proxy sends the profile's networks, its connections' names
// and names only the customer's DNS knows into the tunnel; everything
// else goes out directly.
func TestBrowserPick(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	np, err := db.CreateNetworkProfile("customer", `{"peers":[{"public_key":"x","allowed_ips":["10.9.0.0/16"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	id := np.ID
	if _, err := db.CreateConnection(store.NewConnection{Name: "intranet", Hostname: "intranet.customer.example",
		Overrides: store.InheritableSettings{NetworkProfileID: &id}}); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		"10.9.1.1":                  id,
		"192.0.2.1":                 "",
		"intranet.customer.example": id,
		"wiki.customer.invalid":     id, // unknown to local DNS: try inside the VPN
		"localhost":                 "",
	} {
		got, _, err := a.browserPick(id, host)
		if err != nil || got != want {
			t.Errorf("%s -> %q, %v (want %q)", host, got, err, want)
		}
	}

	// A name the local resolver puts inside the profile's networks (a hosts
	// entry) is dialed by that address: the customer's DNS may not know it.
	lo, err := db.CreateNetworkProfile("loopback", `{"peers":[{"public_key":"y","allowed_ips":["127.0.0.0/8","::1/128"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got, dial, err := a.browserPick(lo.ID, "localhost")
	if err != nil || got != lo.ID || (dial != "127.0.0.1" && dial != "::1") {
		t.Errorf("localhost -> %q dial %q, %v", got, dial, err)
	}
	if _, dial, _ := a.browserPick(id, "intranet.customer.example"); dial != "intranet.customer.example" {
		t.Errorf("a connection's name is dialed as is, got %q", dial)
	}
}
