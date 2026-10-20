package sshconfig

import "testing"

func TestParseBlocks(t *testing.T) {
	text := `
Host web
  HostName web.example.com
  User deploy

Host a b
  HostName shared.example.com
  Port 2222

Match host *.internal
  User matchuser

Host *
  User root
  ProxyJump bastion

Host web
  User later
  IdentityFile ~/.ssh/one
`
	es, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Entry{}
	for _, e := range es {
		by[e.Alias] = e
	}
	if len(es) != 3 {
		t.Fatalf("entries %+v", es)
	}
	web := by["web"]
	if web.HostName != "web.example.com" || web.User != "deploy" || len(web.ProxyJump) != 0 || len(web.IdentFile) != 1 {
		t.Fatalf("web: %+v", web)
	}
	for _, a := range []string{"a", "b"} {
		if e := by[a]; e.HostName != "shared.example.com" || e.Port != 2222 || e.User != "" {
			t.Fatalf("%s: %+v", a, e)
		}
	}
}

func TestSplitHop(t *testing.T) {
	cases := map[string][3]any{
		"admin@bastion.example.com:2222": {"admin", "bastion.example.com", uint16(2222)},
		"bastion.example.com":            {"", "bastion.example.com", uint16(0)},
		"[2001:db8::1]:22":               {"", "2001:db8::1", uint16(22)},
		"u@[2001:db8::1]":                {"u", "2001:db8::1", uint16(0)},
	}
	for in, want := range cases {
		u, h, p := splitHop(in)
		if u != want[0] || h != want[1] || p != want[2] {
			t.Errorf("%s: %q %q %d", in, u, h, p)
		}
	}
}
