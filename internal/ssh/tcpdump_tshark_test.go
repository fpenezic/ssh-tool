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

// dhcpRecord builds a tshark -T fields line for one DHCP packet.
func dhcpRecord(xid, msgType string, extra map[int]string) string {
	f := map[int]string{
		tsFieldProto:         "DHCP",
		tsFieldIP4Src:        "0.0.0.0",
		tsFieldIP4Dst:        "255.255.255.255",
		tsFieldUDPSrcPort:    "68",
		tsFieldUDPDstPort:    "67",
		tsFieldLen:           "342",
		tsFieldDHCPXid:       xid,
		tsFieldDHCPType:      msgType,
		tsFieldDHCPClientMAC: "aa:bb:cc:dd:ee:ff",
	}
	for k, v := range extra {
		f[k] = v
	}
	return tsharkRecord(f)
}

// The DORA view groups by transaction id, so every packet of one exchange
// must come back with the same xid and a named stage. This is the decoder's
// most useful trick and it must not be tcpdump-only.
func TestTsharkDecodesFullDORAExchange(t *testing.T) {
	stages := []struct{ code, want string }{
		{"1", "Discover"},
		{"2", "Offer"},
		{"3", "Request"},
		{"5", "ACK"},
	}
	for _, st := range stages {
		p, ok := parseTsharkLine(dhcpRecord("0x3f2a1b0c", st.code, nil))
		if !ok {
			t.Fatalf("%s: record did not parse", st.want)
		}
		if p.Decoded == nil {
			t.Fatalf("%s: no decode attached - the Decode tab would be empty", st.want)
		}
		if p.Decoded.Type != "dhcp" {
			t.Errorf("%s: decode type = %q, want dhcp", st.want, p.Decoded.Type)
		}
		if got := p.Decoded.Fields["xid"]; got != "0x3f2a1b0c" {
			t.Errorf("%s: xid = %q - a wrong key splits the transaction", st.want, got)
		}
		if got := p.Decoded.Fields["msg_type"]; got != st.want {
			t.Errorf("message type %s = %q, want %q", st.code, got, st.want)
		}
	}
}

// tshark may print the id with or without the 0x prefix; both spellings must
// land on ONE transaction or the DORA view shows the exchange twice.
func TestTsharkDHCPXidIsNormalised(t *testing.T) {
	with, _ := parseTsharkLine(dhcpRecord("0xabcd1234", "1", nil))
	without, _ := parseTsharkLine(dhcpRecord("abcd1234", "1", nil))
	if with.Decoded == nil || without.Decoded == nil {
		t.Fatal("both spellings must decode")
	}
	if with.Decoded.Fields["xid"] != without.Decoded.Fields["xid"] {
		t.Errorf("xid spellings diverge: %q vs %q",
			with.Decoded.Fields["xid"], without.Decoded.Fields["xid"])
	}
}

// yiaddr is 0.0.0.0 on Discover and Request. Showing that as the assigned
// address would put a bogus "assigned 0.0.0.0" on the transaction.
func TestTsharkDHCPIgnoresEmptyAssignedAddress(t *testing.T) {
	p, _ := parseTsharkLine(dhcpRecord("0x1", "1", map[int]string{
		tsFieldDHCPYourIP: "0.0.0.0",
	}))
	if _, present := p.Decoded.Fields["assigned_ip"]; present {
		t.Error("0.0.0.0 is 'no address in this message', not an assignment")
	}

	ack, _ := parseTsharkLine(dhcpRecord("0x1", "5", map[int]string{
		tsFieldDHCPYourIP: "10.0.0.55",
	}))
	if got := ack.Decoded.Fields["assigned_ip"]; got != "10.0.0.55" {
		t.Errorf("a real assignment must survive, got %q", got)
	}
	if !strings.Contains(ack.Decoded.Summary, "10.0.0.55") {
		t.Errorf("summary should name the assigned address: %q", ack.Decoded.Summary)
	}
}

func TestTsharkDHCPCarriesTheLeaseDetails(t *testing.T) {
	p, _ := parseTsharkLine(dhcpRecord("0x1", "5", map[int]string{
		tsFieldDHCPYourIP:   "10.0.0.55",
		tsFieldDHCPServerID: "10.0.0.1",
		tsFieldDHCPLease:    "86400",
		tsFieldDHCPMask:     "255.255.255.0",
		tsFieldDHCPRouter:   "10.0.0.1",
		tsFieldDHCPDomain:   "example.com",
	}))
	want := map[string]string{
		"client_mac":  "aa:bb:cc:dd:ee:ff",
		"server_id":   "10.0.0.1",
		"lease_time":  "86400s",
		"subnet_mask": "255.255.255.0",
		"gateway":     "10.0.0.1",
		"domain":      "example.com",
	}
	for k, v := range want {
		if got := p.Decoded.Fields[k]; got != v {
			t.Errorf("field %s = %q, want %q", k, got, v)
		}
	}
}

// Every non-DHCP packet carries empty DHCP columns; attaching a decode there
// would put junk rows in the Decode tab.
func TestTsharkNonDHCPGetsNoDecode(t *testing.T) {
	p, ok := parseTsharkLine(tsharkRecord(map[int]string{
		tsFieldProto: "TLSv1.3", tsFieldIP4Src: "10.0.0.1", tsFieldIP4Dst: "10.0.0.2",
		tsFieldTCPSrcPort: "443", tsFieldTCPDstPort: "51234", tsFieldLen: "1420",
	}))
	if !ok {
		t.Fatal("record did not parse")
	}
	if p.Decoded != nil {
		t.Errorf("non-DHCP packet got a decode: %+v", p.Decoded)
	}
}

// The DHCP columns are appended after the original ones; if the command and
// the parser ever disagree on the order, every field reads the wrong column.
func TestTsharkCommandRequestsTheDHCPFields(t *testing.T) {
	cmd, _ := buildTsharkCommand(TcpdumpOptions{Iface: "eth0"}, "")
	for _, f := range []string{
		"dhcp.id", "dhcp.option.dhcp", "dhcp.ip.your", "dhcp.hw.mac_addr",
	} {
		if !strings.Contains(cmd, " -e "+f) {
			t.Errorf("command does not request %q, so the Decode tab stays empty", f)
		}
	}
}

// tsharkFields and the tsField* index constants are two hand-maintained lists
// that must stay the same length and in the same order. If they drift, every
// field after the divergence reads a NEIGHBOURING column - a silent corruption
// that shows up as nonsense values rather than an error. This is the one check
// that cannot be skipped when adding a field.
func TestTsharkFieldListMatchesIndexConstants(t *testing.T) {
	if len(tsharkFields) != tsFieldCount {
		t.Fatalf("tsharkFields has %d entries but tsFieldCount is %d - "+
			"every column after the mismatch is read from the wrong index",
			len(tsharkFields), tsFieldCount)
	}
}

// Spot-check that a few named constants point at the field they claim to, so
// a reordering inside the list is caught as well as a length change.
func TestTsharkIndexConstantsPointAtTheRightFields(t *testing.T) {
	want := map[int]string{
		tsFieldTime:          "frame.time",
		tsFieldProto:         "_ws.col.Protocol",
		tsFieldInfo:          "_ws.col.Info",
		tsFieldDHCPXid:       "dhcp.id",
		tsFieldDHCPType:      "dhcp.option.dhcp",
		tsFieldDNSQName:      "dns.qry.name",
		tsFieldARPOpcode:     "arp.opcode",
		tsFieldICMPType:      "icmp.type",
		tsFieldTLSSNI:        "tls.handshake.extensions_server_name",
		tsFieldHTTPMethod:    "http.request.method",
		tsFieldNTPMode:       "ntp.flags.mode",
		tsFieldSNMPCommunity: "snmp.community",
		tsFieldSSHProtocol:   "ssh.protocol",
		tsFieldLDAPMsgID:     "ldap.messageID",
		tsFieldSMB2Cmd:       "smb2.cmd",
		tsFieldMQTTTopic:     "mqtt.topic",
	}
	for idx, name := range want {
		if idx >= len(tsharkFields) {
			t.Errorf("index %d is past the end of tsharkFields (%d)", idx, len(tsharkFields))
			continue
		}
		if tsharkFields[idx] != name {
			t.Errorf("index %d = %q, want %q", idx, tsharkFields[idx], name)
		}
	}
}

// Every decoder the tcpdump path has must also fire under tshark, or the
// Decode tab is quietly poorer on the engine that has better data.
func TestTsharkDecodesEveryProtocol(t *testing.T) {
	cases := []struct {
		name     string
		cols     map[int]string
		wantType string
		wantIn   string // substring the summary must carry
	}{
		{
			name:     "dns query",
			cols:     map[int]string{tsFieldDNSID: "0x1234", tsFieldDNSQName: "example.com", tsFieldDNSQType: "1"},
			wantType: "dns",
			wantIn:   "example.com",
		},
		{
			name: "dns response with answer",
			cols: map[int]string{
				tsFieldDNSID: "0x1234", tsFieldDNSQName: "example.com",
				tsFieldDNSIsResponse: "1", tsFieldDNSA: "93.184.216.34",
			},
			wantType: "dns",
			wantIn:   "93.184.216.34",
		},
		{
			name: "arp request",
			cols: map[int]string{
				tsFieldARPOpcode: "1", tsFieldARPDstIP: "10.0.0.5", tsFieldARPSrcIP: "10.0.0.1",
			},
			wantType: "arp",
			wantIn:   "Who has 10.0.0.5",
		},
		{
			name: "icmp echo request",
			cols: map[int]string{
				tsFieldICMPType: "8", tsFieldICMPID: "0x0003", tsFieldICMPSeq: "1",
			},
			wantType: "icmp",
			wantIn:   "echo request",
		},
		{
			name:     "icmp fragmentation needed",
			cols:     map[int]string{tsFieldICMPType: "3", tsFieldICMPCode: "4"},
			wantType: "icmp",
			wantIn:   "fragmentation needed",
		},
		{
			name:     "tls client hello",
			cols:     map[int]string{tsFieldTLSSNI: "api.example.com"},
			wantType: "tls",
			wantIn:   "api.example.com",
		},
		{
			name: "http request",
			cols: map[int]string{
				tsFieldHTTPMethod: "GET", tsFieldHTTPURI: "/index.html",
				tsFieldHTTPHost: "example.com",
			},
			wantType: "http",
			wantIn:   "example.com/index.html",
		},
		{
			name:     "http response",
			cols:     map[int]string{tsFieldHTTPStatus: "404"},
			wantType: "http",
			wantIn:   "404",
		},
		{
			name:     "ntp",
			cols:     map[int]string{tsFieldNTPMode: "3", tsFieldNTPStratum: "2"},
			wantType: "ntp",
			wantIn:   "stratum 2",
		},
		{
			name:     "snmp v2c",
			cols:     map[int]string{tsFieldSNMPVersion: "1", tsFieldSNMPCommunity: "public"},
			wantType: "snmp",
			wantIn:   "v2c",
		},
		{
			name:     "ssh banner",
			cols:     map[int]string{tsFieldSSHProtocol: "SSH-2.0-OpenSSH_9.6"},
			wantType: "ssh",
			wantIn:   "OpenSSH_9.6",
		},
		{
			name:     "ldap",
			cols:     map[int]string{tsFieldLDAPMsgID: "7", tsFieldLDAPOp: "searchRequest"},
			wantType: "ldap",
			wantIn:   "searchRequest",
		},
		{
			name:     "smb2",
			cols:     map[int]string{tsFieldSMB2Cmd: "Negotiate"},
			wantType: "smb",
			wantIn:   "Negotiate",
		},
		{
			name:     "mqtt publish",
			cols:     map[int]string{tsFieldMQTTType: "3", tsFieldMQTTTopic: "sensors/temp"},
			wantType: "mqtt",
			wantIn:   "sensors/temp",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cols := make([]string, tsFieldCount)
			for i, v := range c.cols {
				cols[i] = v
			}
			d := decodeTshark(cols)
			if d == nil {
				t.Fatalf("no decode produced - this protocol is missing from the Decode tab")
			}
			if d.Type != c.wantType {
				t.Errorf("type = %q, want %q", d.Type, c.wantType)
			}
			if !strings.Contains(d.Summary, c.wantIn) {
				t.Errorf("summary %q does not carry %q", d.Summary, c.wantIn)
			}
		})
	}
}

// Coverage guard: the tcpdump path decodes these types, so tshark must too.
// A new decoder added on one side and not the other is the failure this
// catches.
func TestTsharkCoversTheSameProtocolsAsTcpdump(t *testing.T) {
	// cwmp is deliberately absent: it is decoded out of an HTTP body, which
	// -T fields does not carry. Everything else has a dissector field.
	want := []string{"dhcp", "dns", "arp", "icmp", "tls", "http", "ntp", "snmp", "ssh", "ldap", "smb", "mqtt"}
	probes := map[string]map[int]string{
		"dhcp": {tsFieldDHCPXid: "0x1", tsFieldDHCPType: "1"},
		"dns":  {tsFieldDNSQName: "example.com"},
		"arp":  {tsFieldARPOpcode: "1"},
		"icmp": {tsFieldICMPType: "8"},
		"tls":  {tsFieldTLSSNI: "example.com"},
		"http": {tsFieldHTTPMethod: "GET"},
		"ntp":  {tsFieldNTPMode: "3"},
		"snmp": {tsFieldSNMPVersion: "1"},
		"ssh":  {tsFieldSSHProtocol: "SSH-2.0-x"},
		"ldap": {tsFieldLDAPOp: "bindRequest"},
		"smb":  {tsFieldSMB2Cmd: "Negotiate"},
		"mqtt": {tsFieldMQTTType: "1"},
	}
	for _, kind := range want {
		cols := make([]string, tsFieldCount)
		for i, v := range probes[kind] {
			cols[i] = v
		}
		d := decodeTshark(cols)
		if d == nil || d.Type != kind {
			t.Errorf("protocol %q does not decode under tshark (got %v)", kind, d)
		}
	}
}

// A packet that matches nothing must not produce an empty decode row.
func TestTsharkPlainPacketProducesNoDecode(t *testing.T) {
	cols := make([]string, tsFieldCount)
	cols[tsFieldProto] = "TCP"
	cols[tsFieldIP4Src] = "10.0.0.1"
	if d := decodeTshark(cols); d != nil {
		t.Errorf("plain TCP produced a decode: %+v", d)
	}
}

// An ICMP error row is only useful if it names WHICH destination failed. The
// address lives in the packet the error quotes, not in the outer header - a
// row reading just "destination unreachable" is what the author reported.
func TestTsharkICMPErrorNamesTheFailedDestination(t *testing.T) {
	cols := make([]string, tsFieldCount)
	cols[tsFieldICMPType] = "3"
	cols[tsFieldICMPCode] = "3"
	cols[tsFieldInfo] = "Destination unreachable (Port unreachable) 10.0.27.99"

	d := decodeTshark(cols)
	if d == nil {
		t.Fatal("no decode")
	}
	if got := d.Fields["target"]; got != "10.0.27.99" {
		t.Errorf("target = %q - without it the row cannot be acted on", got)
	}
	if !strings.Contains(d.Summary, "10.0.27.99") {
		t.Errorf("summary must name the destination, got %q", d.Summary)
	}
	// id/seq belong to echo packets; on an error they would be noise.
	for _, k := range []string{"id", "seq"} {
		if _, present := d.Fields[k]; present {
			t.Errorf("%q has no meaning on an ICMP error", k)
		}
	}
}

// The dissector often does NOT name an address ("Host administratively
// prohibited"). The row must still render, just without a target - it must
// not invent one out of something address-shaped in the text.
func TestTsharkICMPErrorWithoutANamedAddress(t *testing.T) {
	cols := make([]string, tsFieldCount)
	cols[tsFieldICMPType] = "3"
	cols[tsFieldICMPCode] = "10"
	cols[tsFieldInfo] = "Destination unreachable (Host administratively prohibited)"

	d := decodeTshark(cols)
	if d == nil {
		t.Fatal("the row must still decode without an address")
	}
	if got, present := d.Fields["target"]; present {
		t.Errorf("invented a target %q from text with no address", got)
	}
	if !strings.Contains(d.Summary, "unreachable") {
		t.Errorf("summary lost the error kind: %q", d.Summary)
	}
}

func TestICMPErrorTargetExtraction(t *testing.T) {
	cases := []struct{ info, want string }{
		{"Destination unreachable (Port unreachable) 10.0.27.99", "10.0.27.99"},
		{"Time-to-live exceeded (Time to live exceeded in transit) 192.168.4.1", "192.168.4.1"},
		{"Destination unreachable (Host administratively prohibited)", ""},
		{"", ""},
		// A bare number sequence is not an address.
		{"Destination unreachable (code 13)", ""},
	}
	for _, c := range cases {
		if got := icmpErrorTarget(c.info); got != c.want {
			t.Errorf("icmpErrorTarget(%q) = %q, want %q", c.info, got, c.want)
		}
	}
}

// The Decode tab renders every field as its own row, so a field that merely
// restates the summary shows up as a duplicate line under each entry - which
// is exactly what the reported screenshot showed.
func TestTsharkDecodeFieldsDoNotRestateTheSummary(t *testing.T) {
	cases := []struct {
		name string
		cols map[int]string
	}{
		{"icmp unreachable", map[int]string{tsFieldICMPType: "3", tsFieldICMPCode: "1"}},
		{"icmp echo", map[int]string{tsFieldICMPType: "8", tsFieldICMPSeq: "5"}},
		{"ssh banner", map[int]string{tsFieldSSHProtocol: "SSH-2.0-OpenSSH_9.6"}},
		{"ldap", map[int]string{tsFieldLDAPOp: "bindRequest", tsFieldLDAPMsgID: "3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cols := make([]string, tsFieldCount)
			for i, v := range c.cols {
				cols[i] = v
			}
			d := decodeTshark(cols)
			if d == nil {
				t.Fatal("no decode")
			}
			if _, present := d.Fields["op"]; present {
				t.Errorf("field \"op\" duplicates the summary %q as its own row", d.Summary)
			}
		})
	}
}

// Echo packets are the case where id/seq DO belong - they are how a request
// is paired with its reply.
func TestTsharkICMPEchoKeepsIdAndSeq(t *testing.T) {
	cols := make([]string, tsFieldCount)
	cols[tsFieldICMPType] = "8"
	cols[tsFieldICMPID] = "9"
	cols[tsFieldICMPSeq] = "5531"

	d := decodeTshark(cols)
	if d.Fields["id"] != "9" || d.Fields["seq"] != "5531" {
		t.Errorf("echo must keep id/seq, got %+v", d.Fields)
	}
	if !strings.Contains(d.Summary, "5531") {
		t.Errorf("summary should carry the sequence: %q", d.Summary)
	}
}

// tshark REJECTS the whole capture (exit 1) on a single unknown -e field, so
// one invented field name breaks the engine entirely. That is exactly what
// happened: "icmp.ip.dst" looked reasonable - it is how the protocol TREE
// nests - but Wireshark has no such field, and tshark exited with status 1.
//
// A unit test cannot validate names against Wireshark's dictionary. What it
// can do is pin the list, so that adding a field is a deliberate act that
// updates this test, and whoever does it has to confirm the name exists
// (tshark -G fields | grep, or the display-filter reference) rather than
// inferring it from the tree layout.
func TestTsharkFieldListIsPinned(t *testing.T) {
	// Every name here has to exist in Wireshark's dictionary. Verify a new
	// one with:  tshark -G fields | awk -F"\t" '$3=="<name>"'
	want := []string{
		"frame.time", "_ws.col.Protocol",
		"ip.src", "ipv6.src", "ip.dst", "ipv6.dst",
		"tcp.srcport", "udp.srcport", "tcp.dstport", "udp.dstport",
		"frame.len", "_ws.col.Info",

		"dhcp.id", "dhcp.option.dhcp", "dhcp.ip.your", "dhcp.hw.mac_addr",
		"dhcp.option.requested_ip_address", "dhcp.option.dhcp_server_id",
		"dhcp.option.ip_address_lease_time", "dhcp.option.subnet_mask",
		"dhcp.option.router", "dhcp.option.domain_name",

		"dns.id", "dns.qry.name", "dns.qry.type", "dns.flags.response",
		"dns.a", "dns.cname",
		"arp.opcode", "arp.src.proto_ipv4", "arp.dst.proto_ipv4", "arp.src.hw_mac",
		"icmp.type", "icmp.code", "icmp.ident", "icmp.seq",
		"tls.handshake.extensions_server_name", "tls.handshake.type",
		"http.request.method", "http.request.uri", "http.host",
		"http.response.code", "http.user_agent", "http.content_type",
		"ntp.flags.mode", "ntp.flags.vn", "ntp.stratum",
		"snmp.version", "snmp.community",
		"ssh.protocol",
		"ldap.messageID", "ldap.protocolOp",
		"smb2.cmd", "smb.cmd",
		"mqtt.msgtype", "mqtt.topic", "mqtt.ver",
	}
	if len(tsharkFields) != len(want) {
		t.Fatalf("tsharkFields has %d entries, the pinned list has %d - "+
			"adding a field means confirming it exists in Wireshark and "+
			"updating this list", len(tsharkFields), len(want))
	}
	for i, f := range tsharkFields {
		if f != want[i] {
			t.Errorf("field %d = %q, pinned as %q", i, f, want[i])
		}
	}
}

// A capture retried with only the core fields (after the host's Wireshark
// rejected a decode field) emits SHORT rows. Parsing must handle them without
// panicking and still produce usable packets - a reduced capture is the
// fallback that keeps the engine working at all.
func TestTsharkParsesReducedCoreOnlyRows(t *testing.T) {
	core := make([]string, tsharkCoreFieldCount)
	core[tsFieldTime] = "Aug 24, 2026 01:15:42.123456789 CEST"
	core[tsFieldProto] = "TLSv1.3"
	core[tsFieldIP4Src] = "10.0.0.1"
	core[tsFieldIP4Dst] = "10.0.0.2"
	core[tsFieldTCPSrcPort] = "443"
	core[tsFieldTCPDstPort] = "51234"
	core[tsFieldLen] = "1420"
	core[tsFieldInfo] = "Application Data"

	p, ok := parseTsharkLine(strings.Join(core, tsharkSep))
	if !ok {
		t.Fatal("a core-only row must still parse")
	}
	if p.SrcIP != "10.0.0.1" || p.DstPort != 51234 {
		t.Errorf("core columns lost: %s -> %s:%d", p.SrcIP, p.DstIP, p.DstPort)
	}
	if p.Info != "Application Data" {
		t.Errorf("info = %q", p.Info)
	}
	// No decode fields were requested, so there must be no decode - and
	// crucially, no out-of-range read while looking for one.
	if p.Decoded != nil {
		t.Errorf("core-only capture produced a decode: %+v", p.Decoded)
	}
}

// Every decoder must tolerate a short row, since decodeTshark runs on all of
// them. An out-of-range index here would panic the capture goroutine.
func TestTsharkDecodersToleratesShortRows(t *testing.T) {
	for n := 0; n <= tsFieldCount; n++ {
		cols := make([]string, n)
		for i := range cols {
			cols[i] = "x"
		}
		// Must not panic at any length.
		_ = decodeTshark(cols)
	}
}

func TestTsharkCoreOnlyCommandDropsDecodeFields(t *testing.T) {
	full, _ := buildTsharkCommand(TcpdumpOptions{Iface: "eth0"}, "")
	if !strings.Contains(full, " -e dhcp.id") {
		t.Fatal("the normal command should request the decode fields")
	}
	reduced, _ := buildTsharkCommand(TcpdumpOptions{Iface: "eth0", tsharkCoreOnly: true}, "")
	if strings.Contains(reduced, " -e dhcp.id") {
		t.Error("the reduced command must drop the decode fields")
	}
	// The packet-shape columns are what keeps Flat / Flows / insights working.
	for _, f := range []string{"frame.time", "ip.src", "tcp.srcport", "_ws.col.Info"} {
		if !strings.Contains(reduced, " -e "+f) {
			t.Errorf("reduced command lost core field %q", f)
		}
	}
}

func TestUnknownFieldErrorDetection(t *testing.T) {
	for _, s := range []string{
		"tshark: Some fields aren't valid: icmp.ip.dst",
		"tshark: The field \"foo.bar\" is not valid",
		"tshark: unknown field foo.bar",
	} {
		if !isTsharkUnknownFieldError(s) {
			t.Errorf("should be recognised as an unknown-field error: %q", s)
		}
	}
	for _, s := range []string{
		"tshark: The capture filter \"port abc\" is invalid",
		"tshark: You don't have permission to capture on that device",
		"",
	} {
		if isTsharkUnknownFieldError(s) {
			t.Errorf("must not be treated as an unknown-field error: %q", s)
		}
	}
}
