package inventory

import (
	"encoding/json"
	"testing"
)

func TestAutoHostnameAndAddresses(t *testing.T) {
	var withPub, privOnly hetznerServer
	withPub.Name, privOnly.Name = "web", "db"
	withPub.PublicNet.IPv4.IP = "203.0.113.5"
	priv := struct {
		IP string `json:"ip"`
	}{IP: "10.0.0.7"}
	b, _ := json.Marshal([]any{priv})
	_ = json.Unmarshal(b, &privOnly.PrivateNet)
	_ = json.Unmarshal(b, &withPub.PrivateNet)

	if h := pickHetznerHostname(HostnameAuto, withPub); h != "203.0.113.5" {
		t.Fatalf("auto with public = %q", h)
	}
	if h := pickHetznerHostname(HostnameAuto, privOnly); h != "10.0.0.7" {
		t.Fatalf("auto private-only = %q", h)
	}
	raw, _ := json.Marshal(privOnly)
	if pub, pr := Addresses("hetzner", raw); pub != "" || pr != "10.0.0.7" {
		t.Fatalf("Addresses = %q %q", pub, pr)
	}
	if pub, pr := Addresses("proxmox", raw); pub != "" || pr != "" {
		t.Fatal("proxmox has no address model")
	}
}
