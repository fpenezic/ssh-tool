package main

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/wg"
)

// A proxy moved to a port that is taken keeps listening on its old one:
// the old listener is stopped to free its port, so it has to come back.
func TestSetLocalProxyRestoresOldListener(t *testing.T) {
	tmp := t.TempDir()
	db, err := store.Open(filepath.Join(tmp, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v := creds.NewVault()
	v.SetPath(filepath.Join(tmp, "vault.enc"))
	if err := v.Init("test passphrase", false); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, vault: v, nbman: tunnelhelper.NewManager(nil), wgman: wg.NewManager()}

	free := func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return p
	}
	oldPort := free()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	takenPort := taken.Addr().(*net.TCPAddr).Port

	row, err := db.CreateNetworkProfile("p", `{"kind":"wireguard"}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLocalProxy(row.ID) })
	if _, err := a.NetworkProfileSetLocalProxy(row.ID, LocalProxyConfig{Enabled: true, Bind: "127.0.0.1", Port: oldPort}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.NetworkProfileSetLocalProxy(row.ID, LocalProxyConfig{Enabled: true, Bind: "127.0.0.1", Port: takenPort}); err == nil {
		t.Fatal("a taken port was accepted")
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort)))
	if err != nil {
		t.Fatalf("the old listener did not come back: %v", err)
	}
	c.Close()
	got, err := db.GetNetworkProfile(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := profileLocalProxy(got.ConfigJSON); !cfg.Enabled || cfg.Port != oldPort {
		t.Errorf("stored config changed: %+v", cfg)
	}
}
