package ssh

import "testing"

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":                  "''",
		"eth0":              "eth0",
		"nginx.service":     "nginx.service",
		"/var/log/syslog":   "/var/log/syslog",
		"port 22":           "'port 22'",
		"a;reboot":          "'a;reboot'",
		"a|b":               "'a|b'",
		"a&&b":              "'a&&b'",
		"$(id)":             "'$(id)'",
		"a>b":               "'a>b'",
		"it's":              `'it'\''s'`,
		"web-1_prod":        "web-1_prod",
		"host and port 443": "'host and port 443'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
