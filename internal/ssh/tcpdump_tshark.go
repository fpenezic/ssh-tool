package ssh

// tshark support for the packet capture. When `tshark` exists on the remote it
// is the better engine: Wireshark's dissectors name the application protocol
// (http, tls, dns, quic, ...) and summarise it, which tcpdump's brief output
// cannot do at all.
//
// The integration deliberately does NOT parse tshark's human-readable output.
// `-T fields` emits one tab-separated record per packet with exactly the
// columns we ask for, so the result is a direct field-by-field fill of
// ParsedPacket - no regex, no grammar drift between Wireshark versions. The
// tcpdump text parser stays untouched and remains the fallback.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// tsharkFields are the -e columns, in the order buildTsharkCommand requests
// them and parseTsharkLine reads them back. The two MUST stay in sync; the
// index constants below are the single source of truth for that ordering.
var tsharkFields = []string{
	"frame.time",       // absolute timestamp
	"_ws.col.Protocol", // dissector-chosen protocol name ("TLSv1.3", "DNS")
	"ip.src",
	"ipv6.src",
	"ip.dst",
	"ipv6.dst",
	"tcp.srcport",
	"udp.srcport",
	"tcp.dstport",
	"udp.dstport",
	"frame.len",
	"_ws.col.Info", // dissector one-line summary
	// DHCP fields, so the Decode tab's DORA view works under tshark too.
	// Grouping a Discover/Offer/Request/Ack exchange by transaction id is the
	// decoder's most useful trick, and it must not be a tcpdump-only feature.
	// These come from named dissector fields rather than regexes over a hex
	// dump, so they are exact where the tcpdump path is best-effort.
	// Empty on every non-DHCP packet, which costs one tab each.
	"dhcp.id",          // transaction id (xid) - the DORA grouping key
	"dhcp.option.dhcp", // message type: 1=Discover 2=Offer 3=Request 5=ACK
	"dhcp.ip.your",     // address the server assigns
	"dhcp.hw.mac_addr", // client MAC
	"dhcp.option.requested_ip_address",
	"dhcp.option.dhcp_server_id",
	"dhcp.option.ip_address_lease_time",
	"dhcp.option.subnet_mask",
	"dhcp.option.router",
	"dhcp.option.domain_name",

	// The remaining decoders, so the Decode tab is not poorer under tshark
	// than under tcpdump. Every one of these is a named dissector field, so
	// where the tcpdump path regexes a hex dump this reads the parsed value.
	"dns.id", "dns.qry.name", "dns.qry.type", "dns.flags.response", "dns.a", "dns.cname",
	"arp.opcode", "arp.src.proto_ipv4", "arp.dst.proto_ipv4", "arp.src.hw_mac",
	"icmp.type", "icmp.code", "icmp.ident", "icmp.seq",
	// The embedded original packet inside an ICMP error. Without this an
	// "unreachable" row says only that something was unreachable, never
	// WHICH address - which is the one thing you need from it. occurrence=f
	// gives the first ip.dst, i.e. the outer one, so the quoted header needs
	// its own field name.
	"icmp.ip.dst", "icmp.udp.dstport", "icmp.tcp.dstport",
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

const (
	tsFieldTime = iota
	tsFieldProto
	tsFieldIP4Src
	tsFieldIP6Src
	tsFieldIP4Dst
	tsFieldIP6Dst
	tsFieldTCPSrcPort
	tsFieldUDPSrcPort
	tsFieldTCPDstPort
	tsFieldUDPDstPort
	tsFieldLen
	tsFieldInfo
	tsFieldDHCPXid
	tsFieldDHCPType
	tsFieldDHCPYourIP
	tsFieldDHCPClientMAC
	tsFieldDHCPRequestedIP
	tsFieldDHCPServerID
	tsFieldDHCPLease
	tsFieldDHCPMask
	tsFieldDHCPRouter
	tsFieldDHCPDomain

	tsFieldDNSID
	tsFieldDNSQName
	tsFieldDNSQType
	tsFieldDNSIsResponse
	tsFieldDNSA
	tsFieldDNSCName

	tsFieldARPOpcode
	tsFieldARPSrcIP
	tsFieldARPDstIP
	tsFieldARPSrcMAC

	tsFieldICMPType
	tsFieldICMPCode
	tsFieldICMPID
	tsFieldICMPSeq
	tsFieldICMPOrigDst
	tsFieldICMPOrigUDPPort
	tsFieldICMPOrigTCPPort

	tsFieldTLSSNI
	tsFieldTLSHandshakeType

	tsFieldHTTPMethod
	tsFieldHTTPURI
	tsFieldHTTPHost
	tsFieldHTTPStatus
	tsFieldHTTPUserAgent
	tsFieldHTTPContentType

	tsFieldNTPMode
	tsFieldNTPVersion
	tsFieldNTPStratum

	tsFieldSNMPVersion
	tsFieldSNMPCommunity

	tsFieldSSHProtocol

	tsFieldLDAPMsgID
	tsFieldLDAPOp

	tsFieldSMB2Cmd
	tsFieldSMBCmd

	tsFieldMQTTType
	tsFieldMQTTTopic
	tsFieldMQTTVersion

	tsFieldCount
)

// tsharkSep separates the -T fields columns. Tab is tshark's default and
// cannot appear inside any of the fields we request.
const tsharkSep = "\t"

// DetectTshark reports whether `tshark` is on the remote host's PATH. The
// capture modal calls it once so it can offer the engine toggle only where it
// would work; a missing binary is not an error, it just means tcpdump.
func DetectTshark(client *ssh.Client) (bool, error) {
	return detectBinary(client, "tshark")
}

// DetectTcpdump reports whether `tcpdump` is on the remote host's PATH.
//
// This is not a formality: a host can perfectly well have tshark and not
// tcpdump (Wireshark's CLI package does not depend on it). Without this check
// the modal offered a two-engine picker defaulting to the one that is not
// installed, and the capture failed with "not installed" until the user
// happened to switch.
func DetectTcpdump(client *ssh.Client) (bool, error) {
	return detectBinary(client, "tcpdump")
}

// detectBinary reports whether a command is on the remote PATH. A missing
// binary is an answer, not an error - only a broken session is an error.
func detectBinary(client *ssh.Client, name string) (bool, error) {
	sess, err := client.NewSession()
	if err != nil {
		return false, err
	}
	defer sess.Close()
	out, runErr := sess.Output("command -v " + name + " 2>/dev/null")
	if runErr != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// reBareIPv4 pulls dotted-quad addresses out of a dissector info string.
var reBareIPv4 = regexp.MustCompile(`\b\d+\.\d+\.\d+\.\d+\b`)

// buildTsharkCommand composes the tshark invocation for a capture. It mirrors
// buildTcpdumpCommand's contract: the interface and BPF are shell-quoted, the
// caller applies any sudo prefix.
//
// Flag notes:
//   - `-l` line-buffers, so packets reach us as they are dissected rather than
//     in 4KB blocks. Without it a quiet link shows nothing for minutes.
//   - `-n` disables name resolution (same reason as tcpdump's -nn: a reverse
//     lookup per packet stalls the stream).
//   - `-f` is the capture (BPF) filter. tshark's `-Y` is a DIFFERENT language
//     (display filter, applied after dissection) - passing a BPF expression to
//     -Y fails, so the user's filter must go to -f.
//   - `-s` snaplen matches the tcpdump path's sizing rationale.
//   - `-c` caps the packet count for a bounded capture; continuous omits it.
func buildTsharkCommand(opts TcpdumpOptions, sshExcl string) (string, error) {
	if strings.TrimSpace(opts.Iface) == "" {
		return "", fmt.Errorf("interface required")
	}
	snaplen := 160
	if opts.Verbose {
		snaplen = 1024
	}

	var b strings.Builder
	b.WriteString("tshark -l -n -i ")
	b.WriteString(shellQuote(opts.Iface))
	fmt.Fprintf(&b, " -s %d", snaplen)

	continuous := opts.MaxCount < 0
	if !continuous {
		maxCount := opts.MaxCount
		if maxCount == 0 || maxCount > 5000 {
			maxCount = 5000
		}
		fmt.Fprintf(&b, " -c %d", maxCount)
	}

	// Capture filter: the user's BPF AND-ed with the SSH exclusion, exactly as
	// the tcpdump path composes it. Both are one -f argument.
	filter := strings.TrimSpace(opts.BPFFilter)
	if opts.ExcludeSSH && sshExcl != "" {
		if filter != "" {
			filter = "(" + filter + ") and " + sshExcl
		} else {
			filter = sshExcl
		}
	}
	if filter != "" {
		b.WriteString(" -f ")
		b.WriteString(shellQuote(filter))
	}

	// Field output. -E occurrence=f keeps one value per field: a tunnelled or
	// fragmented packet can carry several ip.src values, and without this they
	// arrive comma-joined and break the column count.
	b.WriteString(" -T fields -E separator=/t -E occurrence=f")
	for _, f := range tsharkFields {
		b.WriteString(" -e ")
		b.WriteString(f)
	}
	return b.String(), nil
}

// parseTsharkLine turns one `-T fields` record into a ParsedPacket. Returns
// false for anything that is not a packet record (tshark's startup banner,
// "Capturing on ...", blank lines).
func parseTsharkLine(line string) (ParsedPacket, bool) {
	if strings.TrimSpace(line) == "" {
		return ParsedPacket{}, false
	}
	cols := strings.Split(line, tsharkSep)
	if len(cols) < tsFieldCount {
		// Banner and status lines carry no tabs at all.
		return ParsedPacket{}, false
	}

	p := ParsedPacket{Raw: line}
	p.Timestamp = tsharkTime(cols[tsFieldTime])

	// IPv4 wins when present; a packet has one or the other.
	p.SrcIP = firstNonEmpty(cols[tsFieldIP4Src], cols[tsFieldIP6Src])
	p.DstIP = firstNonEmpty(cols[tsFieldIP4Dst], cols[tsFieldIP6Dst])
	if p.SrcIP == "" && p.DstIP == "" {
		// Non-IP (ARP, LLDP, STP). Keep it: the protocol column and info
		// string are still worth showing, and ARP feeds the insight checks.
		p.Proto = tsharkProto(cols[tsFieldProto], "", "")
		p.Info = strings.TrimSpace(cols[tsFieldInfo])
		p.Length, _ = strconv.Atoi(strings.TrimSpace(cols[tsFieldLen]))
		if p.Proto == "arp" {
			// Recover the addresses the ARP insight checks need from the
			// dissector's info string ("Who has 10.0.0.5? Tell 10.0.0.1").
			if ips := reBareIPv4.FindAllString(p.Info, -1); len(ips) >= 2 {
				p.DstIP, p.SrcIP = ips[0], ips[1]
				p.FlowKey = flowKey("arp", p.SrcIP, 0, p.DstIP, 0)
			}
		}
		return p, p.Proto != ""
	}

	tcpSrc := strings.TrimSpace(cols[tsFieldTCPSrcPort])
	udpSrc := strings.TrimSpace(cols[tsFieldUDPSrcPort])
	p.SrcPort, _ = strconv.Atoi(firstNonEmpty(tcpSrc, udpSrc))
	p.DstPort, _ = strconv.Atoi(firstNonEmpty(
		strings.TrimSpace(cols[tsFieldTCPDstPort]),
		strings.TrimSpace(cols[tsFieldUDPDstPort]),
	))
	p.Length, _ = strconv.Atoi(strings.TrimSpace(cols[tsFieldLen]))
	p.Info = strings.TrimSpace(cols[tsFieldInfo])
	p.Proto = tsharkProto(cols[tsFieldProto], tcpSrc, udpSrc)
	p.FlowKey = flowKey(p.Proto, p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
	// Feed the Decode tab. Unlike the tcpdump path this needs no verbose mode:
	// the fields are requested on every capture and are simply empty for
	// non-DHCP packets.
	if d := decodeTshark(cols); d != nil {
		p.Decoded = d
	}
	return p, true
}

// tsharkProto maps the dissector's protocol column onto the transport names
// the rest of the app keys on ("tcp", "udp", "icmp", "arp"). The column names
// the APPLICATION protocol when one was dissected - "TLSv1.3", "HTTP", "DNS" -
// which is more information than tcpdump gives, but the insight analyzer and
// the UI's colouring key on the transport, so the transport must win. Which
// port field was populated tells us that unambiguously.
func tsharkProto(col, tcpSrcPort, udpSrcPort string) string {
	if tcpSrcPort != "" {
		return "tcp"
	}
	if udpSrcPort != "" {
		return "udp"
	}
	switch name := strings.ToLower(strings.TrimSpace(col)); {
	case name == "":
		return ""
	case strings.HasPrefix(name, "icmpv6"):
		return "icmp6"
	case strings.HasPrefix(name, "icmp"):
		return "icmp"
	case strings.HasPrefix(name, "arp"):
		return "arp"
	case strings.HasPrefix(name, "tcp"):
		return "tcp"
	case strings.HasPrefix(name, "udp"):
		return "udp"
	default:
		return name
	}
}

// tsharkAppProto returns the dissector's application-protocol label when it
// adds something over the transport ("TLSv1.3" on a tcp packet). Empty when
// the column merely repeats the transport, so callers can skip the badge.
func tsharkAppProto(col, transport string) string {
	name := strings.TrimSpace(col)
	if name == "" {
		return ""
	}
	if strings.EqualFold(name, transport) {
		return ""
	}
	return name
}

// tsharkTime extracts the clock time from tshark's `frame.time`, which is an
// absolute stamp like "Aug 24, 2026 01:15:42.123456789 CEST". The UI shows
// time-of-day only, matching the tcpdump path's "13:45:12.345678".
func tsharkTime(v string) string {
	if m := reTime.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return strings.TrimSpace(v)
}

// dhcpMessageTypes maps the numeric DHCP message type (option 53) to the name
// the Decode tab shows as a DORA stage.
var dhcpMessageTypes = map[string]string{
	"1": "Discover",
	"2": "Offer",
	"3": "Request",
	"4": "Decline",
	"5": "ACK",
	"6": "NAK",
	"7": "Release",
	"8": "Inform",
}

// decodeTsharkDHCP builds a PacketDecode from tshark's named DHCP fields.
//
// The Decode tab's DORA view - grouping a Discover / Offer / Request / ACK
// exchange by transaction id - is the decoder's most useful trick, and it was
// tcpdump-only because our decoders parse tcpdump's hex payload dump, which
// tshark does not emit in -T fields mode. Rather than tell the user to switch
// engines, we read the same information from the dissector, which names these
// fields exactly instead of matching regexes over a hex dump.
//
// Returns nil for a packet with no DHCP transaction id, i.e. everything that
// is not DHCP.
func decodeTsharkDHCP(cols []string) *PacketDecode {
	xid := strings.TrimSpace(cols[tsFieldDHCPXid])
	if xid == "" {
		return nil
	}
	d := &PacketDecode{Type: "dhcp", Fields: map[string]string{}}
	// The grouping key. tshark prints it as 0x-prefixed already; normalise so
	// two spellings of one exchange cannot split into two transactions.
	if !strings.HasPrefix(xid, "0x") {
		xid = "0x" + xid
	}
	d.Fields["xid"] = xid

	put := func(key string, idx int) string {
		v := strings.TrimSpace(cols[idx])
		if v != "" {
			d.Fields[key] = v
		}
		return v
	}

	msgType := strings.TrimSpace(cols[tsFieldDHCPType])
	stage := dhcpMessageTypes[msgType]
	if stage == "" && msgType != "" {
		// An unknown type is still worth showing rather than dropping the
		// packet out of the transaction.
		stage = "type " + msgType
	}
	if stage != "" {
		d.Fields["msg_type"] = stage
	}

	put("client_mac", tsFieldDHCPClientMAC)
	assigned := put("assigned_ip", tsFieldDHCPYourIP)
	put("requested_ip", tsFieldDHCPRequestedIP)
	put("server_id", tsFieldDHCPServerID)
	put("subnet_mask", tsFieldDHCPMask)
	put("gateway", tsFieldDHCPRouter)
	put("domain", tsFieldDHCPDomain)
	if lease := strings.TrimSpace(cols[tsFieldDHCPLease]); lease != "" {
		d.Fields["lease_time"] = lease + "s"
	}

	// 0.0.0.0 in yiaddr means "no address in this message" (Discover and
	// Request carry it); showing it as an assigned address is misleading.
	if assigned == "0.0.0.0" {
		delete(d.Fields, "assigned_ip")
		assigned = ""
	}

	switch {
	case stage != "" && assigned != "":
		d.Summary = "DHCP " + stage + " - " + assigned
	case stage != "":
		d.Summary = "DHCP " + stage
	default:
		d.Summary = "DHCP " + xid
	}
	return d
}

// decodeTshark builds the Decode tab's PacketDecode from tshark's named
// dissector fields, covering the same protocols the tcpdump path decodes.
//
// The tcpdump decoders work by regexing tcpdump's hex/ASCII payload dump,
// which -T fields does not emit - so without this the Decode tab was empty
// under tshark. Reading the dissector's own fields is both simpler and more
// accurate: no payload truncation, no regex guesswork.
//
// Order matters only in that the first match wins; the protocols are mutually
// exclusive in practice because each one's key field is empty otherwise.
func decodeTshark(cols []string) *PacketDecode {
	if d := decodeTsharkDHCP(cols); d != nil {
		return d
	}
	if d := decodeTsharkDNS(cols); d != nil {
		return d
	}
	if d := decodeTsharkARP(cols); d != nil {
		return d
	}
	if d := decodeTsharkICMP(cols); d != nil {
		return d
	}
	if d := decodeTsharkTLS(cols); d != nil {
		return d
	}
	if d := decodeTsharkHTTP(cols); d != nil {
		return d
	}
	if d := decodeTsharkNTP(cols); d != nil {
		return d
	}
	if d := decodeTsharkSNMP(cols); d != nil {
		return d
	}
	if d := decodeTsharkSSH(cols); d != nil {
		return d
	}
	if d := decodeTsharkLDAP(cols); d != nil {
		return d
	}
	if d := decodeTsharkSMB(cols); d != nil {
		return d
	}
	if d := decodeTsharkMQTT(cols); d != nil {
		return d
	}
	return nil
}

// col trims one column; the empty string means "the dissector did not set it".
func col(cols []string, idx int) string {
	if idx >= len(cols) {
		return ""
	}
	return strings.TrimSpace(cols[idx])
}

// newDecode starts a decode of the given type with a fields map ready.
func newDecode(kind string) *PacketDecode {
	return &PacketDecode{Type: kind, Fields: map[string]string{}}
}

// setIf copies a column into a field when the dissector set it.
func setIf(d *PacketDecode, cols []string, key string, idx int) string {
	v := col(cols, idx)
	if v != "" {
		d.Fields[key] = v
	}
	return v
}

func decodeTsharkDNS(cols []string) *PacketDecode {
	qname := col(cols, tsFieldDNSQName)
	txid := col(cols, tsFieldDNSID)
	if qname == "" && txid == "" {
		return nil
	}
	d := newDecode("dns")
	if txid != "" {
		d.Fields["txid"] = txid
	}
	if qname != "" {
		d.Fields["qname"] = qname
	}
	if qt := col(cols, tsFieldDNSQType); qt != "" {
		d.Fields["qtype"] = dnsQTypeName(qt)
	}
	// dns.flags.response is "1" on an answer. The tcpdump decoder splits
	// query and response by wording; here it is a flag.
	isResponse := col(cols, tsFieldDNSIsResponse) == "1"
	d.Fields["op"] = "query"
	if isResponse {
		d.Fields["op"] = "response"
	}
	// A multi-answer response comes back comma-joined by tshark.
	answers := col(cols, tsFieldDNSA)
	if answers == "" {
		answers = col(cols, tsFieldDNSCName)
	}
	if answers != "" {
		d.Fields["rdata"] = answers
	}

	switch {
	case isResponse && answers != "" && qname != "":
		d.Summary = "DNS response " + qname + " -> " + answers
	case isResponse && qname != "":
		d.Summary = "DNS response " + qname
	case qname != "":
		d.Summary = "DNS query " + qname
	default:
		d.Summary = "DNS " + txid
	}
	return d
}

// dnsQTypeName turns tshark's numeric qry.type into the familiar mnemonic,
// matching what the tcpdump decoder shows.
func dnsQTypeName(v string) string {
	switch v {
	case "1":
		return "A"
	case "2":
		return "NS"
	case "5":
		return "CNAME"
	case "6":
		return "SOA"
	case "12":
		return "PTR"
	case "15":
		return "MX"
	case "16":
		return "TXT"
	case "28":
		return "AAAA"
	case "33":
		return "SRV"
	case "65":
		return "HTTPS"
	default:
		return v
	}
}

func decodeTsharkARP(cols []string) *PacketDecode {
	opcode := col(cols, tsFieldARPOpcode)
	if opcode == "" {
		return nil
	}
	d := newDecode("arp")
	target := setIf(d, cols, "target", tsFieldARPDstIP)
	sender := setIf(d, cols, "sender", tsFieldARPSrcIP)
	mac := setIf(d, cols, "target_mac", tsFieldARPSrcMAC)

	switch opcode {
	case "1":
		d.Fields["op"] = "request"
		d.Summary = "Who has " + target + "? Tell " + sender
	case "2":
		d.Fields["op"] = "reply"
		d.Summary = sender + " is at " + mac
	default:
		d.Fields["op"] = "opcode " + opcode
		d.Summary = "ARP opcode " + opcode
	}
	return d
}

func decodeTsharkICMP(cols []string) *PacketDecode {
	icmpType := col(cols, tsFieldICMPType)
	if icmpType == "" {
		return nil
	}
	d := newDecode("icmp")
	code := col(cols, tsFieldICMPCode)
	op := icmpTypeName(icmpType, code)

	switch icmpType {
	case "0", "8":
		// Echo: id and seq are the whole content, and pairing a request with
		// its reply is what the fields are for.
		setIf(d, cols, "id", tsFieldICMPID)
		seq := setIf(d, cols, "seq", tsFieldICMPSeq)
		d.Summary = "ICMP " + op
		if seq != "" {
			d.Summary += " seq=" + seq
		}
	case "3", "11", "5":
		// An error quotes the packet that caused it. WHICH destination failed
		// is the only thing worth reading off one of these rows, so pull it
		// out of the embedded header - id/seq are meaningless here and are
		// deliberately not set.
		target := setIf(d, cols, "target", tsFieldICMPOrigDst)
		port := col(cols, tsFieldICMPOrigTCPPort)
		if port == "" {
			port = col(cols, tsFieldICMPOrigUDPPort)
		}
		if port != "" {
			d.Fields["target_port"] = port
		}
		switch {
		case target != "" && port != "":
			d.Summary = "ICMP " + op + ": " + target + ":" + port
		case target != "":
			d.Summary = "ICMP " + op + ": " + target
		default:
			d.Summary = "ICMP " + op
		}
	default:
		d.Summary = "ICMP " + op
	}

	// `op` is deliberately NOT stored as a field: the summary above already
	// says it, and the Decode tab renders every field as its own row, so
	// storing it produced a duplicate line under each entry.
	return d
}

// icmpTypeName names the common ICMP types. Anything else keeps its numbers,
// which is still more use than dropping the packet from the Decode tab.
func icmpTypeName(t, code string) string {
	switch t {
	case "0":
		return "echo reply"
	case "3":
		switch code {
		case "0":
			return "network unreachable"
		case "1":
			return "host unreachable"
		case "3":
			return "port unreachable"
		case "4":
			return "fragmentation needed"
		default:
			return "destination unreachable"
		}
	case "5":
		return "redirect"
	case "8":
		return "echo request"
	case "11":
		return "time exceeded"
	default:
		return "type " + t + " code " + code
	}
}

func decodeTsharkTLS(cols []string) *PacketDecode {
	sni := col(cols, tsFieldTLSSNI)
	if sni == "" {
		// Only a ClientHello carries SNI, and SNI is the whole reason this
		// decode exists - matching the tcpdump path, which also returns
		// nothing without it.
		return nil
	}
	d := newDecode("tls")
	d.Fields["sni"] = sni
	d.Summary = "TLS ClientHello SNI: " + sni
	return d
}

func decodeTsharkHTTP(cols []string) *PacketDecode {
	method := col(cols, tsFieldHTTPMethod)
	status := col(cols, tsFieldHTTPStatus)
	if method == "" && status == "" {
		return nil
	}
	d := newDecode("http")
	setIf(d, cols, "host", tsFieldHTTPHost)
	setIf(d, cols, "user_agent", tsFieldHTTPUserAgent)
	setIf(d, cols, "content_type", tsFieldHTTPContentType)
	uri := setIf(d, cols, "path", tsFieldHTTPURI)

	if method != "" {
		d.Fields["op"] = "request"
		d.Fields["method"] = method
		d.Summary = method + " " + uri
		if host := d.Fields["host"]; host != "" {
			d.Summary = method + " " + host + uri
		}
		return d
	}
	d.Fields["op"] = "response"
	d.Fields["status"] = status
	d.Summary = "HTTP " + status
	return d
}

func decodeTsharkNTP(cols []string) *PacketDecode {
	mode := col(cols, tsFieldNTPMode)
	if mode == "" {
		return nil
	}
	d := newDecode("ntp")
	setIf(d, cols, "version", tsFieldNTPVersion)
	setIf(d, cols, "stratum", tsFieldNTPStratum)
	name := ntpModeName(byte(atoiSafe(mode)))
	d.Fields["mode"] = name
	d.Summary = "NTP " + name
	if st := d.Fields["stratum"]; st != "" {
		d.Summary += " stratum " + st
	}
	return d
}

func decodeTsharkSNMP(cols []string) *PacketDecode {
	version := col(cols, tsFieldSNMPVersion)
	community := col(cols, tsFieldSNMPCommunity)
	if version == "" && community == "" {
		return nil
	}
	d := newDecode("snmp")
	if version != "" {
		// tshark reports 0/1/3 for v1/v2c/v3.
		switch version {
		case "0":
			d.Fields["version"] = "v1"
		case "1":
			d.Fields["version"] = "v2c"
		case "3":
			d.Fields["version"] = "v3"
		default:
			d.Fields["version"] = version
		}
	}
	if community != "" {
		d.Fields["community"] = community
	}
	d.Summary = "SNMP " + d.Fields["version"]
	if community != "" {
		d.Summary += " community " + community
	}
	return d
}

func decodeTsharkSSH(cols []string) *PacketDecode {
	proto := col(cols, tsFieldSSHProtocol)
	if proto == "" {
		return nil
	}
	d := newDecode("ssh")
	// No "op" field: the summary is the banner itself, so a row saying
	// op=banner is pure duplication in a tab that renders every field.
	d.Fields["software"] = proto
	// "SSH-2.0-OpenSSH_9.6" - the version sits between the first two dashes.
	if parts := strings.SplitN(proto, "-", 3); len(parts) >= 2 {
		d.Fields["version"] = parts[1]
	}
	d.Summary = "SSH " + proto
	return d
}

func decodeTsharkLDAP(cols []string) *PacketDecode {
	msgID := col(cols, tsFieldLDAPMsgID)
	op := col(cols, tsFieldLDAPOp)
	if msgID == "" && op == "" {
		return nil
	}
	d := newDecode("ldap")
	if msgID != "" {
		d.Fields["message_id"] = msgID
	}
	// op is in the summary; storing it too would repeat the line.
	d.Summary = "LDAP " + op
	if msgID != "" {
		d.Summary += " (msg " + msgID + ")"
	}
	return d
}

func decodeTsharkSMB(cols []string) *PacketDecode {
	cmd := col(cols, tsFieldSMB2Cmd)
	dialect := "SMB2"
	if cmd == "" {
		cmd = col(cols, tsFieldSMBCmd)
		dialect = "SMB"
	}
	if cmd == "" {
		return nil
	}
	d := newDecode("smb")
	d.Fields["command"] = cmd
	d.Fields["dialect"] = dialect
	d.Summary = dialect + " " + cmd
	return d
}

func decodeTsharkMQTT(cols []string) *PacketDecode {
	msgType := col(cols, tsFieldMQTTType)
	if msgType == "" {
		return nil
	}
	d := newDecode("mqtt")
	setIf(d, cols, "topic", tsFieldMQTTTopic)
	setIf(d, cols, "mqtt_version", tsFieldMQTTVersion)
	name := mqttPacketTypeName(msgType)
	d.Fields["packet_type"] = name
	d.Fields["protocol"] = "MQTT"
	d.Summary = "MQTT " + name
	if topic := d.Fields["topic"]; topic != "" {
		d.Summary += " " + topic
	}
	return d
}

// mqttPacketTypeName maps the MQTT control packet type to its name.
func mqttPacketTypeName(v string) string {
	names := map[string]string{
		"1": "CONNECT", "2": "CONNACK", "3": "PUBLISH", "4": "PUBACK",
		"5": "PUBREC", "6": "PUBREL", "7": "PUBCOMP", "8": "SUBSCRIBE",
		"9": "SUBACK", "10": "UNSUBSCRIBE", "11": "UNSUBACK",
		"12": "PINGREQ", "13": "PINGRESP", "14": "DISCONNECT",
	}
	if n, ok := names[v]; ok {
		return n
	}
	return "type " + v
}

// atoiSafe parses a small integer, returning 0 on anything unparseable.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
