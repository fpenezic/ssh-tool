package main

import "testing"

func TestParseQuickTarget(t *testing.T) {
	cases := []struct {
		in, user, host string
		port           int
		bad            bool
	}{
		{in: "host.example.com", host: "host.example.com"},
		{in: "root@host.example.com", user: "root", host: "host.example.com"},
		{in: "root@10.0.0.5:2222", user: "root", host: "10.0.0.5", port: 2222},
		{in: "  admin@srv:22  ", user: "admin", host: "srv", port: 22},
		{in: "2001:db8::1", host: "2001:db8::1"},
		{in: "ops@[2001:db8::1]:2200", user: "ops", host: "2001:db8::1", port: 2200},
		{in: "[2001:db8::1]", host: "2001:db8::1"},
		{in: "a@b@host", user: "a@b", host: "host"},
		{in: "host:0", bad: true},
		{in: "host:99999", bad: true},
		{in: "host:ssh", bad: true},
		{in: "", bad: true},
		{in: "user@", bad: true},
		{in: "[2001:db8::1", bad: true},
		{in: "two words", bad: true},
	}
	for _, c := range cases {
		u, h, p, err := parseQuickTarget(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q: want error, got %q %q %d", c.in, u, h, p)
			}
			continue
		}
		if err != nil || u != c.user || h != c.host || p != c.port {
			t.Errorf("%q: got %q %q %d %v, want %q %q %d", c.in, u, h, p, err, c.user, c.host, c.port)
		}
	}
}
