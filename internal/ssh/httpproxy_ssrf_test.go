package ssh

import (
	"net"
	"testing"
)

// TestIsInternalIP covers the range classifier behind the give-internet SSRF
// guard: internal / private / loopback / link-local must be blocked, public
// addresses must pass.
func TestIsInternalIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"0.0.0.0", "::",
		"10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", // cloud metadata
		"fe80::1",         // link-local v6
		"fc00::1", "fd12::abcd", // unique-local v6
		"100.64.0.1", "100.127.255.255", // CGNAT (RFC 6598)
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if !isInternalIP(ip) {
			t.Errorf("isInternalIP(%s) = false, want true (should be blocked)", s)
		}
	}

	public := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34",
		"2606:2800:220:1:248:1893:25c8:1946", // example.com v6
		"100.63.255.255",                     // just below CGNAT
		"100.128.0.1",                        // just above CGNAT
		"172.15.0.1", "172.32.0.1",           // just outside 172.16/12
	}
	for _, s := range public {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if isInternalIP(ip) {
			t.Errorf("isInternalIP(%s) = true, want false (should be allowed)", s)
		}
	}
}

// TestIsInternalDestHostLiterals checks the host-string wrapper for IP literals
// (no DNS needed) and the fail-closed behaviour on an unresolvable name.
func TestIsInternalDestHostLiterals(t *testing.T) {
	cases := []struct {
		host        string
		wantBlocked bool
	}{
		{"127.0.0.1", true},
		{"10.1.2.3", true},
		{"[::1]", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"", true}, // empty -> blocked
		// A syntactically-invalid name that cannot resolve must fail closed.
		{"this-name-does-not-exist.invalid", true},
	}
	for _, c := range cases {
		blocked, reason := isInternalDestHost(c.host)
		if blocked != c.wantBlocked {
			t.Errorf("isInternalDestHost(%q) blocked=%v reason=%q, want blocked=%v",
				c.host, blocked, reason, c.wantBlocked)
		}
	}
}
