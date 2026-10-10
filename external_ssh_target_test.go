package main

import (
	"testing"

	"ssh-tool/internal/store"
)

func TestCheckExternalSSHTarget(t *testing.T) {
	str := func(s string) *string { return &s }
	ok := []store.ResolvedSettings{
		{Hostname: "web-01.example.com"},
		{Hostname: "10.0.0.1", Username: str("root")},
		{Hostname: "fe80::1%eth0"},
		{Hostname: "[2001:db8::1]"},
		{Hostname: "h.example.com", Username: str("first.last@example.com")},
		{Hostname: "h.example.com", Username: str(`CORP\admin`)},
		{Hostname: "h", JumpHost: &store.JumpHostSpec{Hostname: "bastion.example.com", Username: str("ops")}},
	}
	for _, s := range ok {
		if err := checkExternalSSHTarget(&s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []store.ResolvedSettings{
		{Hostname: "-oProxyCommand=calc"},
		{Hostname: "a&calc"},
		{Hostname: "a;calc"},
		{Hostname: "a b"},
		{Hostname: "h", Username: str("-oProxyCommand=calc")},
		{Hostname: "h", Username: str("u|calc")},
		{Hostname: "h", Username: str("$env:x")},
		{Hostname: "h", JumpHost: &store.JumpHostSpec{Hostname: "-J"}},
		{Hostname: "h", JumpHost: &store.JumpHostSpec{Hostname: "b", Via: &store.JumpHostSpec{Hostname: "x^calc"}}},
	}
	for _, s := range bad {
		if err := checkExternalSSHTarget(&s); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}
