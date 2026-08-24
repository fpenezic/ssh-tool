package ssh

import (
	"strings"
	"testing"
)

// sshExclusionFromAddr returns PLAIN BPF. The shell escaping the tcpdump
// command line needs is applied by shellEscapeBPF at the point of use,
// because tshark takes the same clause inside a quoted -f argument where a
// backslash would be a literal character and would break the filter.
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
			want: "not ( host 192.168.1.50 and port 54231 )",
			ok:   true,
		},
		{
			name: "wireguard tunnel ip",
			addr: "10.16.16.2:41000",
			want: "not ( host 10.16.16.2 and port 41000 )",
			ok:   true,
		},
		{
			name: "ipv6",
			addr: "[fe80::1]:22000",
			want: "not ( host fe80::1 and port 22000 )",
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

// The two engines need the SAME clause escaped differently. Getting this
// wrong is not a cosmetic bug: tshark rejects a filter containing literal
// backslashes, and a rejected capture filter means NO filter - so the SSH
// control connection floods back into the capture, which is the feedback
// loop the exclusion exists to prevent.
func TestExclusionEscapingDiffersPerEngine(t *testing.T) {
	clause, ok := sshExclusionFromAddr("10.0.0.9:54231")
	if !ok {
		t.Fatal("expected the clause to build")
	}
	if strings.Contains(clause, `\\`) {
		t.Fatalf("the clause must be plain BPF, got %q", clause)
	}

	tcpdumpCmd, err := buildCaptureCommand(
		TcpdumpOptions{Iface: "eth0", ExcludeSSH: true}, clause, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	// tcpdump takes the clause as bare shell words, so the parens must be
	// escaped or the shell tries to open a subshell.
	if !strings.Contains(tcpdumpCmd, `not \( host 10.0.0.9 and port 54231 \)`) {
		t.Errorf("tcpdump needs shell-escaped parens:\n%s", tcpdumpCmd)
	}

	tsharkCmd, err := buildCaptureCommand(
		TcpdumpOptions{Iface: "eth0", ExcludeSSH: true, Engine: "tshark"}, clause, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	// tshark takes it inside a quoted -f, where a backslash is literal.
	if !strings.Contains(tsharkCmd, `-f 'not ( host 10.0.0.9 and port 54231 )'`) {
		t.Errorf("tshark needs the clause unescaped inside the quoted -f:\n%s", tsharkCmd)
	}
	if strings.Contains(tsharkCmd, `\(`) {
		t.Errorf("a backslash inside tshark's -f breaks the filter:\n%s", tsharkCmd)
	}
}

// The SSH exclusion is on by default and is the reason a capture does not
// feed on its own output. If it ever silently drops out, captures on a busy
// SSH session melt down - so assert it is actually present.
func TestExclusionIsPresentForBothEngines(t *testing.T) {
	clause, _ := sshExclusionFromAddr("10.0.0.9:54231")
	for _, engine := range []string{"", "tshark"} {
		cmd, err := buildCaptureCommand(
			TcpdumpOptions{Iface: "eth0", ExcludeSSH: true, BPFFilter: "port 443", Engine: engine},
			clause, 100, false)
		if err != nil {
			t.Fatalf("engine %q: %v", engine, err)
		}
		if !strings.Contains(cmd, "10.0.0.9") || !strings.Contains(cmd, "54231") {
			t.Errorf("engine %q dropped the SSH exclusion:\n%s", engine, cmd)
		}
		if !strings.Contains(cmd, "port 443") {
			t.Errorf("engine %q dropped the user filter:\n%s", engine, cmd)
		}
	}
}
