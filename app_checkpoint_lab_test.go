//go:build lab

package main

// End-to-end check of the Check Point profile glue against a real
// gateway: the login answered from a vault credential (loginFiller), the
// pinned gateway certificate, the routes the plugin reports (what the app
// proxy routes by) and a dial through the tunnel. Needs the
// ssh-tool-checkpoint binary and a gateway with a username + password
// login option. ONE login per run: the plugin gives up after three
// refusals, but a wrong password still counts toward the gateway's
// lockout. Not run in CI:
//
//	CP_PLUGIN=.../ssh-tool-checkpoint CP_LAB_SERVER=vpn.example.com \
//	CP_LAB_PIN=pin-sha256:... CP_LAB_USER=alice CP_LAB_PASSWORD=... \
//	CP_LAB_DIAL=10.0.0.53:53 go test -tags lab -run CheckpointLab -v .

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/wg"
)

func TestCheckpointLab(t *testing.T) {
	plugin, server := os.Getenv("CP_PLUGIN"), os.Getenv("CP_LAB_SERVER")
	user, pass, pin := os.Getenv("CP_LAB_USER"), os.Getenv("CP_LAB_PASSWORD"), os.Getenv("CP_LAB_PIN")
	target := os.Getenv("CP_LAB_DIAL")
	if plugin == "" || server == "" || user == "" || pass == "" || target == "" {
		t.Skip("CP_PLUGIN / CP_LAB_SERVER / CP_LAB_USER / CP_LAB_PASSWORD / CP_LAB_DIAL not set")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	bin, err := os.ReadFile(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pluginsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir(), pluginBinaryName(kindCheckpoint)), bin, 0o755); err != nil {
		t.Fatal(err)
	}
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
	defer a.nbman.StopAll()

	cred, err := a.credSvc.Create(creds.CreateInput{Kind: "password", Name: "lab password", Password: pass})
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.NetworkProfileCreateCheckpoint(CheckpointProfileInput{Name: "lab", Config: CheckpointConfig{
		Server: server, Username: user, PasswordCredentialID: cred.Credential.ID,
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pin != "" {
		// What "Trust & remember" stores; the dialog itself needs the UI.
		if err := a.ocSavePins(info.ID, "", []string{pin}); err != nil {
			t.Fatal(err)
		}
	}
	row, err := db.GetNetworkProfile(info.ID)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	d, err := a.ensureCheckpointTunnel(row)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Logf("up in %s", time.Since(start).Round(time.Millisecond))

	var ti struct {
		Addresses []string `json:"addresses"`
		Routes    []string `json:"routes"`
		DNS       []string `json:"dns"`
	}
	if err := json.Unmarshal([]byte(a.NetworkProfileTunnelInfo(info.ID)), &ti); err != nil {
		t.Fatalf("tunnel info: %v", err)
	}
	t.Logf("address %v, %d routes, dns %v", ti.Addresses, len(ti.Routes), ti.DNS)
	if len(ti.Addresses) == 0 || len(ti.Routes) == 0 {
		t.Fatalf("tunnel info without address or routes: %+v", ti)
	}
	row, _ = db.GetNetworkProfile(info.ID)
	if routes, _ := a.profileRoutes(row, LocalProxyConfig{}); len(routes) == 0 {
		t.Fatal("the app proxy learned no routes from the plugin")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("dial %s through the tunnel: %v", target, err)
	}
	c.Close()
	t.Logf("dialled %s through the tunnel", target)

	a.tunnelStop(info.ID) // stdin EOF: the plugin signs out at the gateway
	if a.nbman.Get(info.ID) != nil {
		t.Fatal("the plugin is still running after stop")
	}
}
