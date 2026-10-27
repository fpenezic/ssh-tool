//go:build lab

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/wg"
)

// The browser proxy with a name only the local hosts file knows, the way
// an OpenShift route behind a VPN is often reached: the customer's DNS
// (here: none at all) cannot resolve it, so the proxy has to dial the
// address the hosts entry gives, inside the tunnel.
//
// WG_LAB_CONF: a wg-quick config whose AllowedIPs contain the target.
// WG_LAB_URL: an http URL by that hosts-only name. WG_LAB_EXPECT: text
// the page returns.
func TestWireGuardLabBrowserProxyHostsName(t *testing.T) {
	conf, url, expect := os.Getenv("WG_LAB_CONF"), os.Getenv("WG_LAB_URL"), os.Getenv("WG_LAB_EXPECT")
	if conf == "" || url == "" || expect == "" {
		t.Skip("WG_LAB_CONF / WG_LAB_URL / WG_LAB_EXPECT not set")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	db, err := store.Open(filepath.Join(tmp, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v := creds.NewVault()
	v.SetPath(filepath.Join(tmp, "vault.enc"))
	if err := v.Init("lab passphrase", false); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, vault: v, credSvc: &creds.Service{DB: db, Vault: v}, nbman: tunnelhelper.NewManager(nil), wgman: wg.NewManager(), sessionMeta: map[string]sessionMetaEntry{}}
	defer a.wgman.StopAll()
	defer stopBrowserProxies()

	text, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.NetworkProfileCreate("lab wg", string(text))
	if err != nil {
		t.Fatal(err)
	}
	row, err := a.db.GetNetworkProfile(info.ID)
	if err != nil || row == nil {
		t.Fatalf("profile: %v", err)
	}
	port, err := a.ensureBrowserProxy(row)
	if err != nil {
		t.Fatal(err)
	}
	proxy := fmt.Sprintf("socks5h://127.0.0.1:%d", port)
	out, err := exec.Command("curl", "-sS", "-m", "15", "--noproxy", "", "-x", proxy, url).CombinedOutput()
	if err != nil || !strings.Contains(string(out), expect) {
		t.Fatalf("through the VPN by the hosts-only name: %v %s", err, out)
	}
	t.Logf("got %q", strings.TrimSpace(string(out)))
}
