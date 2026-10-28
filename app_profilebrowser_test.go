package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
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
	// The profile and what is dialed: serveSOCKS connects to the latter.
	for host, want := range map[string]struct{ pid, dial string }{
		"10.9.1.1":                  {id, "10.9.1.1"},
		"192.0.2.1":                 {"", "192.0.2.1"},
		"intranet.customer.example": {id, "intranet.customer.example"},
		"wiki.customer.invalid":     {id, "wiki.customer.invalid"}, // unknown to local DNS: try inside the VPN
		"localhost":                 {"", "localhost"},
	} {
		got, dial, err := a.browserPick(id, host)
		if err != nil || got != want.pid || dial != want.dial {
			t.Errorf("%s -> %q dial %q, %v (want %q dial %q)", host, got, dial, err, want.pid, want.dial)
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
	// The same for a connection's name: a hosts entry inside the networks
	// still decides the address.
	loID := lo.ID
	if _, err := db.CreateConnection(store.NewConnection{Name: "route", Hostname: "localhost",
		Overrides: store.InheritableSettings{NetworkProfileID: &loID}}); err != nil {
		t.Fatal(err)
	}
	got, dial, err = a.browserPick(lo.ID, "localhost")
	if err != nil || got != lo.ID || (dial != "127.0.0.1" && dial != "::1") {
		t.Errorf("connection name localhost -> %q dial %q, %v", got, dial, err)
	}
}

// Every address of a name inside the profile's networks, in the
// resolver's order (one outside is left out), so a dead one can be skipped.
func TestBrowserPickAllAddresses(t *testing.T) {
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
	saved := browserLookup
	defer func() { browserLookup = saved }()
	browserLookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("10.9.1.6"), netip.MustParseAddr("10.9.1.5")}, nil
	}
	pid, hosts, err := a.browserPickAll(np.ID, "app.customer.example")
	if err != nil || pid != np.ID || len(hosts) != 2 || hosts[0] != "10.9.1.6" || hosts[1] != "10.9.1.5" {
		t.Fatalf("got %q %v, %v", pid, hosts, err)
	}
}

// serveSOCKS goes on to the next address when one refuses the connection.
func TestServeSOCKSTriesNextAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("ok"))
		c.Close()
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	// 127.0.0.2 is loopback too, with nothing listening on that port: refused.
	pick := func(string) (string, []string, error) { return "", []string{"127.0.0.2", "127.0.0.1"}, nil }
	cli, srv := net.Pipe()
	defer cli.Close()
	go (&App{}).serveSOCKS(srv, nil, pick, "test")
	if _, err := cli.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	hello := make([]byte, 2)
	if _, err := io.ReadFull(cli, hello); err != nil || hello[1] != 0 {
		t.Fatalf("greeting %v, %v", hello, err)
	}
	host := "app.customer.example"
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := cli.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(cli, reply); err != nil || reply[1] != 0 {
		t.Fatalf("connect reply %v, %v", reply, err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(cli, got); err != nil || string(got) != "ok" {
		t.Fatalf("relay %q, %v", got, err)
	}
}
