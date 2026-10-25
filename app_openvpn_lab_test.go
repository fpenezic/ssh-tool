//go:build lab

package main

// End-to-end check of the OpenVPN profile glue against a lab server:
// import an .ovpn, move its secrets to the vault, put it back together
// at connect, answer the login from credentials and dial through the
// tunnel. Needs the ssh-tool-openvpn binary (OVPN_PLUGIN) and a server
// with auth-user-pass + static challenge (OVPN_LAB_CONFIG, an .ovpn with
// everything inline). Not run in CI:
//
//	OVPN_PLUGIN=... OVPN_LAB_CONFIG=... go test -tags lab -run OpenVPNLab .

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/wg"
)

func openVPNLabApp(t *testing.T) (*App, string) {
	t.Helper()
	plugin, conf := os.Getenv("OVPN_PLUGIN"), os.Getenv("OVPN_LAB_CONFIG")
	if plugin == "" || conf == "" {
		t.Skip("OVPN_PLUGIN / OVPN_LAB_CONFIG not set")
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
	if err := os.WriteFile(filepath.Join(pluginsDir(), pluginBinaryName(kindOpenVPN)), bin, 0o755); err != nil {
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

	pass, err := a.credSvc.Create(creds.CreateInput{Kind: "password", Name: "lab password", Password: "test123"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := a.credSvc.Create(creds.CreateInput{Kind: "password", Name: "lab code", Password: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.NetworkProfileCreateOpenVPN(OpenVPNProfileInput{
		Name: "lab", ConfigText: string(text),
		Config: OpenVPNConfig{
			Username: "test", PasswordCredentialID: pass.Credential.ID,
			SecondPassword: ocSecondCredential, SecondCredentialID: code.Credential.ID,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return a, info.ID
}

func TestOpenVPNLab(t *testing.T) {
	a, id := openVPNLabApp(t)
	defer a.nbman.StopAll()
	db := a.db
	text, _ := os.ReadFile(os.Getenv("OVPN_LAB_CONFIG"))
	info := a.infoFor(*mustProfile(t, db, id))
	ov := info.OpenVPN
	if strings.Contains(ov.Config, "PRIVATE KEY") || strings.Contains(ov.Config, "Static key") {
		t.Fatal("a secret stayed in the stored configuration")
	}
	if ov.ClientCertCredentialID == "" || ov.StaticKey == "" || !ov.AuthUserPass || ov.StaticChallenge == "" {
		t.Fatalf("summary = %+v", ov)
	}
	row, err := db.GetNetworkProfile(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ov)
	if strings.Contains(row.ConfigJSON, "BEGIN") && strings.Contains(row.ConfigJSON, "KEY") {
		t.Fatalf("store holds key material: %s", b)
	}

	// Re-importing the file renews the same certificate credential.
	again, err := a.NetworkProfileUpdateOpenVPN(info.ID, OpenVPNProfileInput{Name: "lab", ConfigText: string(text), Config: *ov})
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.OpenVPN.ClientCertCredentialID != ov.ClientCertCredentialID {
		t.Fatal("re-import created a second certificate credential")
	}
	row, _ = db.GetNetworkProfile(info.ID)

	d, err := a.ensureOpenVPNTunnel(row)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", "172.31.88.10:80")
	if err != nil {
		t.Fatalf("dial through the tunnel: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")
	body, _ := io.ReadAll(c)
	if !strings.Contains(string(body), "hello-from-target") {
		t.Fatalf("response %q", body)
	}
	var _ net.Conn = c
}

// The local proxy end to end, the CI runner shape: a tun2socks gateway
// built from the generated command, and a "job" container sharing its
// network reaches the VPN target while other traffic stays local.
// OVPN_LAB_LOCALPROXY=1 on top of the TestOpenVPNLab variables.
func TestOpenVPNLabLocalProxy(t *testing.T) {
	if os.Getenv("OVPN_LAB_LOCALPROXY") == "" {
		t.Skip("OVPN_LAB_LOCALPROXY not set")
	}
	a, profileID := openVPNLabApp(t)
	defer a.nbman.StopAll()
	defer stopLocalProxies()
	row, _ := a.db.GetNetworkProfile(profileID)
	if _, err := a.ensureOpenVPNTunnel(row); err != nil { // learns the pushed routes
		t.Fatalf("connect: %v", err)
	}
	// A Linux docker host reaches the app on the bridge, not loopback.
	if _, err := a.NetworkProfileSetLocalProxy(profileID, LocalProxyConfig{Enabled: true, Bind: "0.0.0.0", Port: 11080}); err != nil {
		t.Fatal(err)
	}
	d, err := a.NetworkProfileLocalProxyDetails(profileID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("routes %v (%v)", d.Routes, d.Sources)
	if len(d.Routes) == 0 {
		t.Fatal("no routes for the gateway")
	}
	gw := "sshtool-gw-lab"
	_ = exec.Command("docker", "rm", "-f", gw).Run()
	cmd := strings.ReplaceAll(d.Gateway, "sshtool-gw-lab ", gw+" ")
	if out, err := exec.Command("sh", "-c", strings.ReplaceAll(cmd, "\\\n", " ")).CombinedOutput(); err != nil {
		t.Fatalf("gateway: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", gw).Run()
	time.Sleep(3 * time.Second)
	job := func(url string) (string, error) {
		out, err := exec.Command("docker", "run", "--rm", "--network", "container:"+gw, "busybox:1.37",
			"wget", "-q", "-T", "8", "-O", "-", url).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := job("http://172.31.88.10/"); err != nil || !strings.Contains(out, "hello-from-target") {
		logs, _ := exec.Command("docker", "logs", "--tail", "20", gw).CombinedOutput()
		t.Fatalf("job through the gateway: %v %q\n%s", err, out, logs)
	}
	t.Log("job reached the VPN target")
	// A "LAN" service (git, cache) on the default bridge: reached without
	// the tunnel, which could not route there at all.
	_ = exec.Command("docker", "rm", "-f", "sshtool-lab-lan").Run()
	if out, err := exec.Command("docker", "run", "-d", "--name", "sshtool-lab-lan", "busybox:1.37", "sh", "-c",
		"echo hello-lan > /index.html; httpd -f -p 80 -h /").CombinedOutput(); err != nil {
		t.Fatalf("lan service: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", "sshtool-lab-lan").Run()
	ipOut, _ := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "sshtool-lab-lan").Output()
	time.Sleep(time.Second)
	if out, err := job("http://" + strings.TrimSpace(string(ipOut)) + "/"); err != nil || out != "hello-lan" {
		t.Fatalf("local traffic through the gateway: %v %q", err, out)
	}
	t.Log("local traffic stayed local")
}

func mustProfile(t *testing.T, db *store.DB, id string) *store.NetworkProfile {
	t.Helper()
	row, err := db.GetNetworkProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// The shared proxy with two profiles on the lab server: the most
// specific route picks the profile, the manual "use" override wins over
// it, and the single gateway command routes a job through it.
// OVPN_LAB_SHARED=1 (server needs duplicate-cn: both profiles share the
// lab client certificate).
func TestOpenVPNLabSharedProxy(t *testing.T) {
	if os.Getenv("OVPN_LAB_SHARED") == "" {
		t.Skip("OVPN_LAB_SHARED not set")
	}
	a, idA := openVPNLabApp(t)
	defer a.nbman.StopAll()
	defer stopLocalProxies()
	text, _ := os.ReadFile(os.Getenv("OVPN_LAB_CONFIG"))
	rowA := mustProfile(t, a.db, idA)
	var cfgA OpenVPNConfig
	_ = json.Unmarshal([]byte(rowA.ConfigJSON), &cfgA)
	infoB, err := a.NetworkProfileCreateOpenVPN(OpenVPNProfileInput{Name: "lab-b", ConfigText: string(text), Config: OpenVPNConfig{
		Username: cfgA.Username, PasswordCredentialID: cfgA.PasswordCredentialID,
		SecondPassword: cfgA.SecondPassword, SecondCredentialID: cfgA.SecondCredentialID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	idB := infoB.ID
	// A learns the pushed 172.31.88.0/24; B only covers the target.
	if _, err := a.ensureOpenVPNTunnel(rowA); err != nil {
		t.Fatal(err)
	}
	a.tunnelStop(idA)
	if _, err := a.NetworkProfileSetLocalProxy(idA, LocalProxyConfig{Shared: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.NetworkProfileSetLocalProxy(idB, LocalProxyConfig{Shared: true, Networks: []string{"172.31.88.10"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.NetworkSharedProxySet(SharedProxyConfig{Enabled: true, Bind: "0.0.0.0", Port: 11081}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := a.sharedPick("172.31.88.10"); got != idB {
		t.Fatalf("most specific: got %s, want lab-b", got)
	}
	if err := a.NetworkSharedProxySet(SharedProxyConfig{Enabled: true, Bind: "0.0.0.0", Port: 11081, Use: idA}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := a.sharedPick("172.31.88.10"); got != idA {
		t.Fatalf("override: got %s, want lab", got)
	}
	d, err := a.NetworkSharedProxyDetails()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("routes %v overlaps %v", d.Routes, d.Overlaps)
	gw := "sshtool-gw"
	_ = exec.Command("docker", "rm", "-f", gw).Run()
	if strings.Contains(d.Gateway, "\n") {
		t.Fatal("gateway command is not one line")
	}
	if out, err := exec.Command("sh", "-c", d.Gateway).CombinedOutput(); err != nil {
		t.Fatalf("gateway: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", gw).Run()
	time.Sleep(3 * time.Second)
	out, err := exec.Command("docker", "run", "--rm", "--network", "container:"+gw, "busybox:1.37",
		"wget", "-q", "-T", "8", "-O", "-", "http://172.31.88.10/").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "hello-from-target") {
		t.Fatalf("job: %v %s", err, out)
	}
	if !a.tunnelStatus(idA).Running || a.tunnelStatus(idB).Running {
		t.Fatalf("override not honoured: A up %v, B up %v", a.tunnelStatus(idA).Running, a.tunnelStatus(idB).Running)
	}
	t.Log("job went through the overridden profile")
}

// The browser proxy: no credentials, the VPN target through the tunnel,
// another address directly. OVPN_LAB_LOCALPROXY=1.
func TestOpenVPNLabBrowserProxy(t *testing.T) {
	if os.Getenv("OVPN_LAB_LOCALPROXY") == "" {
		t.Skip("OVPN_LAB_LOCALPROXY not set")
	}
	a, id := openVPNLabApp(t)
	defer a.nbman.StopAll()
	defer stopBrowserProxies()
	row := mustProfile(t, a.db, id)
	if _, err := a.ensureOpenVPNTunnel(row); err != nil { // learns the routes
		t.Fatal(err)
	}
	row = mustProfile(t, a.db, id)
	port, err := a.ensureBrowserProxy(row)
	if err != nil {
		t.Fatal(err)
	}
	proxy := fmt.Sprintf("socks5h://127.0.0.1:%d", port)
	out, err := exec.Command("curl", "-sS", "-m", "10", "-x", proxy, "http://172.31.88.10/").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "hello-from-target") {
		t.Fatalf("through the VPN: %v %s", err, out)
	}
	// A local listener the VPN cannot reach: answered only if dialled directly.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\n\r\nhello-direct"))
			c.Close()
		}
	}()
	out, err = exec.Command("curl", "-sS", "-m", "10", "-x", proxy, "--noproxy", "", "http://"+ln.Addr().String()+"/").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "hello-direct") {
		t.Fatalf("direct: %v %s", err, out)
	}
	t.Log("intranet through the VPN, the rest direct")
}
