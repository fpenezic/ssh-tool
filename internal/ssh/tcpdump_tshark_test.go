package ssh

import (
	"strings"
	"testing"
)

// tsharkRecord builds a -T fields line the way tshark emits one, so the tests
// describe packets by field name rather than by counting tabs.
func tsharkRecord(fields map[int]string) string {
	cols := make([]string, tsFieldCount)
	for i, v := range fields {
		cols[i] = v
	}
	return strings.Join(cols, tsharkSep)
}

func TestBuildTsharkCommandBounded(t *testing.T) {
	cmd, err := buildTsharkCommand(TcpdumpOptions{
		Iface:    "eth0",
		MaxCount: 100,
	}, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{"tshark -l -n -i eth0", "-s 160", "-c 100", "-T fields"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q\ngot: %s", want, cmd)
		}
	}
	// Every requested field must be on the command line, or parseTsharkLine
	// reads a column that was never emitted.
	for _, f := range tsharkFields {
		if !strings.Contains(cmd, " -e "+f) {
			t.Errorf("command does not request field %q", f)
		}
	}
}

func TestBuildTsharkCommandContinuousOmitsCount(t *testing.T) {
	cmd, err := buildTsharkCommand(TcpdumpOptions{Iface: "any", MaxCount: -1}, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(cmd, " -c ") {
		t.Errorf("continuous capture must not cap the packet count: %s", cmd)
	}
}

func TestBuildTsharkCommandVerboseWidensSnaplen(t *testing.T) {
	cmd, _ := buildTsharkCommand(TcpdumpOptions{Iface: "eth0", Verbose: true}, "")
	if !strings.Contains(cmd, "-s 1024") {
		t.Errorf("verbose capture needs the wider snaplen: %s", cmd)
	}
}

// The user's filter is a CAPTURE filter and must reach -f. tshark's -Y is a
// display filter in a different language and would reject a BPF expression.
func TestBuildTsharkCommandUsesCaptureFilterFlag(t *testing.T) {
	cmd, _ := buildTsharkCommand(TcpdumpOptions{
		Iface:     "eth0",
		BPFFilter: "host 10.0.0.1",
	}, "")
	if !strings.Contains(cmd, "-f ") {
		t.Errorf("BPF must be passed as a capture filter (-f): %s", cmd)
	}
	if strings.Contains(cmd, "-Y ") {
		t.Errorf("BPF must not be passed as a display filter (-Y): %s", cmd)
	}
}

func TestBuildTsharkCommandCombinesFilterWithSSHExclusion(t *testing.T) {
	cmd, _ := buildTsharkCommand(TcpdumpOptions{
		Iface:      "eth0",
		BPFFilter:  "port 443",
		ExcludeSSH: true,
	}, "not (host 10.0.0.9 and port 22)")
	if !strings.Contains(cmd, "(port 443) and not (host 10.0.0.9 and port 22)") {
		t.Errorf("filter and SSH exclusion must be AND-ed with explicit precedence:\n%s", cmd)
	}
}

func TestBuildTsharkCommandSSHExclusionAloneNeedsNoParens(t *testing.T) {
	cmd, _ := buildTsharkCommand(TcpdumpOptions{
		Iface:      "eth0",
		ExcludeSSH: true,
	}, "not (host 10.0.0.9 and port 22)")
	if !strings.Contains(cmd, "not (host 10.0.0.9 and port 22)") {
		t.Errorf("SSH exclusion missing: %s", cmd)
	}
}

// An interface name is attacker-influenced only in the sense that it comes
// from a remote listing, but it is interpolated into a shell command, so it
// must be quoted like every other such value in this package.
func TestBuildTsharkCommandQuotesInterface(t *testing.T) {
	cmd, _ := buildTsharkCommand(TcpdumpOptions{Iface: "eth0; rm -rf /"}, "")
	if strings.Contains(cmd, "-i eth0; rm") {
		t.Fatalf("interface was interpolated unquoted: %s", cmd)
	}
}

func TestBuildTsharkCommandRequiresInterface(t *testing.T) {
	if _, err := buildTsharkCommand(TcpdumpOptions{}, ""); err == nil {
		t.Fatal("expected an error when no interface is given")
	}
}

func TestParseTsharkLineTCP(t *testing.T) {
	line := tsharkRecord(map[int]string{
		tsFieldTime:       "Aug 24, 2026 01:15:42.123456789 CEST",
		tsFieldProto:      "TLSv1.3",
		tsFieldIP4Src:     "10.0.0.1",
		tsFieldIP4Dst:     "10.0.0.2",
		tsFieldTCPSrcPort: "443",
		tsFieldTCPDstPort: "51234",
		tsFieldLen:        "1420",
		tsFieldInfo:       "Application Data",
	})
	p, ok := parseTsharkLine(line)
	if !ok {
		t.Fatal("expected the record to parse")
	}
	if p.Proto != "tcp" {
		t.Errorf("transport must win over the dissector label: got %q", p.Proto)
	}
	if p.SrcIP != "10.0.0.1" || p.SrcPort != 443 {
		t.Errorf("source = %s:%d", p.SrcIP, p.SrcPort)
	}
	if p.DstIP != "10.0.0.2" || p.DstPort != 51234 {
		t.Errorf("destination = %s:%d", p.DstIP, p.DstPort)
	}
	if p.Length != 1420 {
		t.Errorf("length = %d", p.Length)
	}
	if p.Timestamp != "01:15:42.123456789" {
		t.Errorf("timestamp = %q, want the time of day only", p.Timestamp)
	}
	if p.Info != "Application Data" {
		t.Errorf("info = %q", p.Info)
	}
	if p.FlowKey == "" {
		t.Error("a TCP packet must get a flow key")
	}
}

// Both directions of one conversation must land on the same flow key, or the
// insight analyzer counts them as two half-open flows.
func TestParseTsharkLineFlowKeyIsDirectionIndependent(t *testing.T) {
	fwd, _ := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "TCP", tsFieldIP4Src: "10.0.0.1", tsFieldIP4Dst: "10.0.0.2",
		tsFieldTCPSrcPort: "51234", tsFieldTCPDstPort: "443", tsFieldLen: "60",
	}))
	rev, _ := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "TCP", tsFieldIP4Src: "10.0.0.2", tsFieldIP4Dst: "10.0.0.1",
		tsFieldTCPSrcPort: "443", tsFieldTCPDstPort: "51234", tsFieldLen: "60",
	}))
	if fwd.FlowKey != rev.FlowKey {
		t.Errorf("flow keys differ by direction: %q vs %q", fwd.FlowKey, rev.FlowKey)
	}
}

func TestParseTsharkLineUDP(t *testing.T) {
	p, ok := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "DNS", tsFieldIP4Src: "10.0.0.5", tsFieldIP4Dst: "1.1.1.1",
		tsFieldUDPSrcPort: "51000", tsFieldUDPDstPort: "53", tsFieldLen: "74",
		tsFieldInfo: "Standard query 0x1234 A example.com",
	}))
	if !ok {
		t.Fatal("expected the record to parse")
	}
	if p.Proto != "udp" {
		t.Errorf("proto = %q, want udp", p.Proto)
	}
	if p.DstPort != 53 {
		t.Errorf("dst port = %d", p.DstPort)
	}
}

func TestParseTsharkLineIPv6(t *testing.T) {
	p, ok := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "TCP", tsFieldIP6Src: "2001:db8::1", tsFieldIP6Dst: "2001:db8::2",
		tsFieldTCPSrcPort: "443", tsFieldTCPDstPort: "51234", tsFieldLen: "80",
	}))
	if !ok {
		t.Fatal("expected the record to parse")
	}
	if p.SrcIP != "2001:db8::1" || p.DstIP != "2001:db8::2" {
		t.Errorf("v6 addresses lost: %s -> %s", p.SrcIP, p.DstIP)
	}
}

func TestParseTsharkLineICMP(t *testing.T) {
	p, ok := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "ICMP", tsFieldIP4Src: "10.0.0.1", tsFieldIP4Dst: "10.0.0.9",
		tsFieldLen: "98", tsFieldInfo: "Echo (ping) request",
	}))
	if !ok {
		t.Fatal("expected the record to parse")
	}
	if p.Proto != "icmp" {
		t.Errorf("proto = %q, want icmp", p.Proto)
	}
	if p.SrcPort != 0 || p.DstPort != 0 {
		t.Error("ICMP carries no ports")
	}
}

// ARP has no IP layer, so the addresses have to come out of the info string -
// the ARP off-subnet insight check keys on them.
func TestParseTsharkLineARPRecoversAddresses(t *testing.T) {
	p, ok := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "ARP", tsFieldLen: "42",
		tsFieldInfo: "Who has 10.0.0.5? Tell 10.0.0.1",
	}))
	if !ok {
		t.Fatal("expected the ARP record to parse")
	}
	if p.Proto != "arp" {
		t.Errorf("proto = %q", p.Proto)
	}
	if p.DstIP != "10.0.0.5" || p.SrcIP != "10.0.0.1" {
		t.Errorf("ARP addresses = %s -> %s, want 10.0.0.1 -> 10.0.0.5", p.SrcIP, p.DstIP)
	}
	if p.FlowKey == "" {
		t.Error("ARP with both addresses should get a flow key")
	}
}

func TestParseTsharkLineRejectsNonRecords(t *testing.T) {
	for _, line := range []string{
		"",
		"   ",
		"Capturing on 'eth0'",
		"tshark: Lua: Error during loading",
	} {
		if _, ok := parseTsharkLine(line); ok {
			t.Errorf("banner line parsed as a packet: %q", line)
		}
	}
}

func TestTsharkAppProtoOnlyWhenItAddsInformation(t *testing.T) {
	if got := tsharkAppProto("TLSv1.3", "tcp"); got != "TLSv1.3" {
		t.Errorf("app proto = %q, want TLSv1.3", got)
	}
	if got := tsharkAppProto("TCP", "tcp"); got != "" {
		t.Errorf("a column that repeats the transport adds nothing, got %q", got)
	}
	if got := tsharkAppProto("", "tcp"); got != "" {
		t.Errorf("empty column = %q", got)
	}
}

// buildCaptureCommand is the branch point between the two engines; these
// guard that the engine field actually routes and that the default is
// unchanged for every existing caller.
func TestBuildCaptureCommandRoutesByEngine(t *testing.T) {
	tsharkCmd, err := buildCaptureCommand(TcpdumpOptions{Iface: "eth0", Engine: "tshark"}, "", 100, false)
	if err != nil {
		t.Fatalf("tshark: %v", err)
	}
	if !strings.HasPrefix(tsharkCmd, "tshark ") {
		t.Errorf("engine=tshark must run tshark: %s", tsharkCmd)
	}

	for _, engine := range []string{"", "tcpdump", "TCPDUMP-typo"} {
		cmd, err := buildCaptureCommand(TcpdumpOptions{Iface: "eth0", Engine: engine}, "", 100, false)
		if err != nil {
			t.Fatalf("engine %q: %v", engine, err)
		}
		if !strings.HasPrefix(cmd, "tcpdump ") {
			t.Errorf("engine %q must fall back to tcpdump, got: %s", engine, cmd)
		}
	}
}

// Continuous mode is signalled to StartTcpdump by a separate bool, but
// buildTsharkCommand reads it off MaxCount - the bridge between the two must
// not drop it, or a continuous tshark capture would stop at the packet cap.
func TestBuildCaptureCommandPropagatesContinuousToTshark(t *testing.T) {
	cmd, err := buildCaptureCommand(TcpdumpOptions{Iface: "eth0", Engine: "tshark", MaxCount: 250}, "", 250, true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(cmd, " -c ") {
		t.Errorf("continuous must not cap the count: %s", cmd)
	}
}

func TestBuildCaptureCommandTcpdumpUnchangedByRefactor(t *testing.T) {
	cmd, err := buildCaptureCommand(TcpdumpOptions{
		Iface:      "eth0",
		BPFFilter:  "port 443",
		Verbose:    true,
		ExcludeSSH: true,
	}, "not (host 10.0.0.9 and port 22)", 500, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{
		"tcpdump -l -nn -tttt", "-s 1024", "-v -X", "-i eth0", "-c 500",
		`'port 443' and not \(host 10.0.0.9 and port 22\)`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("tcpdump command lost %q after the refactor:\n%s", want, cmd)
		}
	}
}

func TestUseTsharkPredicate(t *testing.T) {
	if !(TcpdumpOptions{Engine: "tshark"}).useTshark() {
		t.Error("engine=tshark should select tshark")
	}
	if (TcpdumpOptions{Engine: " tshark "}).useTshark() != true {
		t.Error("surrounding whitespace should not defeat the engine check")
	}
	for _, e := range []string{"", "tcpdump", "wireshark"} {
		if (TcpdumpOptions{Engine: e}).useTshark() {
			t.Errorf("engine %q must not select tshark", e)
		}
	}
}
