package wg

import (
	"strings"
	"testing"
)

const sampleConf = `
[Interface]
# comment line
PrivateKey = YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=
Address = 10.0.0.2/32, fd00::2/128
DNS = 10.0.0.1
MTU = 1380
Table = off
PostUp = echo up ; ignored

[Peer]
PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=
PresharedKey = /UwcSPg38hW/D9Y3tcS1FOV0K1wuURMbS0sesJEP5ak=
AllowedIPs = 10.0.0.0/24, 192.168.88.5
Endpoint = 127.0.0.1:51820
PersistentKeepalive = 25
`

func TestParseConf(t *testing.T) {
	p, err := ParseConf(sampleConf)
	if err != nil {
		t.Fatalf("ParseConf: %v", err)
	}
	if p.PrivateKey == "" {
		t.Errorf("private key not parsed")
	}
	if len(p.Addresses) != 2 || p.Addresses[0] != "10.0.0.2/32" {
		t.Errorf("addresses = %v", p.Addresses)
	}
	if len(p.DNS) != 1 || p.DNS[0] != "10.0.0.1" {
		t.Errorf("dns = %v", p.DNS)
	}
	if p.MTU != 1380 {
		t.Errorf("mtu = %d", p.MTU)
	}
	if len(p.Peers) != 1 {
		t.Fatalf("peers = %d", len(p.Peers))
	}
	pe := p.Peers[0]
	if !pe.HasPSK || pe.PresharedKey == "" {
		t.Errorf("psk not parsed")
	}
	if pe.Keepalive != 25 {
		t.Errorf("keepalive = %d", pe.Keepalive)
	}
	if len(pe.AllowedIPs) != 2 {
		t.Errorf("allowed ips = %v", pe.AllowedIPs)
	}
	if pe.Endpoint != "127.0.0.1:51820" {
		t.Errorf("endpoint = %q", pe.Endpoint)
	}

	addrs, err := p.InterfaceAddrs()
	if err != nil || len(addrs) != 2 || addrs[0].String() != "10.0.0.2" {
		t.Errorf("InterfaceAddrs = %v, %v", addrs, err)
	}
}

func TestParseConfErrors(t *testing.T) {
	cases := []struct{ name, conf, want string }{
		{"empty", "", "no Address"},
		{"no peer", "[Interface]\nAddress=10.0.0.2/32\n", "no [Peer]"},
		{"peer no endpoint", "[Interface]\nAddress=10.0.0.2/32\n[Peer]\nPublicKey=xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=\nAllowedIPs=0.0.0.0/0\n", "no Endpoint"},
		{"bad key", "[Interface]\nAddress=10.0.0.2/32\n[Peer]\nPublicKey=nope\nEndpoint=1.2.3.4:51820\nAllowedIPs=0.0.0.0/0\n", "bad PublicKey"},
		{"key outside section", "PrivateKey = x\n", "outside a section"},
		{"unknown key", "[Interface]\nBogus = 1\n", "unknown [Interface] key"},
	}
	for _, c := range cases {
		_, err := ParseConf(c.conf)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want contains %q", c.name, err, c.want)
		}
	}
}

func TestBuildUAPI(t *testing.T) {
	p, err := ParseConf(sampleConf)
	if err != nil {
		t.Fatal(err)
	}
	uapi, err := buildUAPI(p)
	if err != nil {
		t.Fatalf("buildUAPI: %v", err)
	}
	for _, want := range []string{
		"private_key=", "public_key=", "preshared_key=",
		"endpoint=127.0.0.1:51820",
		"persistent_keepalive_interval=25",
		"allowed_ip=10.0.0.0/24",
		"allowed_ip=192.168.88.5/32", // bare IP normalized
	} {
		if !strings.Contains(uapi, want) {
			t.Errorf("uapi missing %q:\n%s", want, uapi)
		}
	}
	if strings.Contains(uapi, "YAnz5TF") {
		t.Errorf("uapi contains base64 key, want hex only")
	}
}

// TestTunnelUp brings a real userspace device up against a
// non-routable endpoint - config plumbing only, no traffic.
func TestTunnelUp(t *testing.T) {
	p, err := ParseConf(sampleConf)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = "test"
	p.Name = "test"
	m := NewManager()
	tun, err := m.Ensure(p)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := m.Get("test"); got != tun {
		t.Errorf("Get returned %v", got)
	}
	st := m.Status("test")
	if !st.Running {
		t.Errorf("status not running: %+v", st)
	}
	m.Stop("test")
	if m.Get("test") != nil {
		t.Errorf("tunnel still present after Stop")
	}
}
