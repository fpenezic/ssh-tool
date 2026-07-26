package ssh

import "testing"

func TestSSHExclusionFromAddr(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
		ok   bool
	}{
		{
			name: "direct ipv4",
			addr: "192.168.1.50:54231",
			want: `not \( host 192.168.1.50 and port 54231 \)`,
			ok:   true,
		},
		{
			name: "wireguard tunnel ip",
			addr: "10.16.16.2:41000",
			want: `not \( host 10.16.16.2 and port 41000 \)`,
			ok:   true,
		},
		{
			name: "ipv6",
			addr: "[fe80::1]:22000",
			want: `not \( host fe80::1 and port 22000 \)`,
			ok:   true,
		},
		{
			name: "no port",
			addr: "192.168.1.50",
			ok:   false,
		},
		{
			name: "empty",
			addr: "",
			ok:   false,
		},
		{
			name: "non-numeric port",
			addr: "host:ssh",
			ok:   false,
		},
		{
			name: "injection chars in host",
			addr: "1.2.3.4$(rm -rf):22",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sshExclusionFromAddr(tc.addr)
			if ok != tc.ok {
				t.Fatalf("sshExclusionFromAddr(%q) ok=%v, want %v (got %q)", tc.addr, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("sshExclusionFromAddr(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func TestSSHExclusionBPFNilClient(t *testing.T) {
	if got := sshExclusionBPF(nil); got != sshExclusionFallback {
		t.Fatalf("nil client should use fallback, got %q", got)
	}
}
