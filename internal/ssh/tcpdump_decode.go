package ssh

import (
	"regexp"
	"strconv"
	"strings"
)

// Decode walks the verbose-mode payload lines of one tcpdump packet
// and returns a structured dissection when the protocol is one we
// recognise. payload is everything *after* the header line - typically
// indented "  key: value" lines.
//
// Caller already has the parsed header (proto, ports, etc) in
// ParsedPacket. We use those to route to the right decoder.
// Decode dispatches to a per-protocol decoder using the built-in
// port table. For custom ports (e.g. HTTP on 9000) use
// DecodeWithOverrides.
func Decode(p *ParsedPacket, payloadLines []string) *PacketDecode {
	return DecodeWithOverrides(p, payloadLines, nil)
}

// DecodeWithOverrides is Decode plus a user-supplied port → proto
// map. Override hits run first so an "http" override on port 9000
// pulls HTTP decoding even though 9000 isn't in the built-in list.
// Override values are case-insensitive proto names matching the
// Type field a decoder emits ("http", "tls", "dns", "ntp", "snmp",
// "ldap", "smb", "mqtt", "ssh", "dhcp"). Unknown names are ignored.
func DecodeWithOverrides(p *ParsedPacket, payloadLines []string, overrides map[int]string) *PacketDecode {
	if d := decodeByOverride(p, payloadLines, overrides); d != nil {
		return decorateDecoded(p, d)
	}
	var d *PacketDecode
	switch {
	case p.Proto == "arp":
		// ARP carries its info on the header line - no payload needed.
		d = decodeARPInfo(p.Info)
	case p.Proto == "icmp" || p.Proto == "icmpv6":
		// ICMP: echo / unreachable / time-exceeded etc.
		d = decodeICMP(p)
	case (p.Proto == "udp" || p.Proto == "tcp") && (p.SrcPort == 53 || p.DstPort == 53):
		// DNS query/reply: header line carries the gist; verbose
		// adds extra record detail.
		d = decodeDNS(p, payloadLines)
	case p.Proto == "udp" && (isDHCPPort(p.SrcPort) || isDHCPPort(p.DstPort)):
		// DHCP / BOOTP - classic 67↔68 plus custom relay flows
		// where one side is 67/68 and the other is arbitrary.
		d = decodeDHCP(p, payloadLines)
	case p.Proto == "udp" && (p.SrcPort == 123 || p.DstPort == 123):
		d = decodeNTP(p, payloadLines)
	case p.Proto == "udp" && (p.SrcPort == 161 || p.DstPort == 161 ||
		p.SrcPort == 162 || p.DstPort == 162):
		d = decodeSNMP(p, payloadLines)
	case p.Proto == "tcp" && (p.DstPort == 443 || p.SrcPort == 443 ||
		p.DstPort == 8443 || p.SrcPort == 8443):
		// TLS ClientHello on common HTTPS ports.
		d = decodeTLS(p, payloadLines)
	case p.Proto == "tcp" && isHTTPPort(p.SrcPort, p.DstPort):
		d = decodeHTTP(p, payloadLines)
	case p.Proto == "tcp" && (p.SrcPort == 22 || p.DstPort == 22):
		d = decodeSSHBanner(p, payloadLines)
	case p.Proto == "tcp" && (p.SrcPort == 389 || p.DstPort == 389):
		d = decodeLDAP(p, payloadLines)
	case p.Proto == "tcp" && (p.SrcPort == 445 || p.DstPort == 445):
		d = decodeSMB(p, payloadLines)
	case p.Proto == "tcp" && (p.SrcPort == 1883 || p.DstPort == 1883 ||
		p.SrcPort == 8883 || p.DstPort == 8883):
		d = decodeMQTT(p, payloadLines)
	case p.Proto == "tcp" && (p.SrcPort == 7547 || p.DstPort == 7547):
		// CWMP / TR-069 (CPE WAN Management Protocol): SOAP/XML over
		// HTTP on 7547. Falls back to the HTTP decoder if the body
		// isn't recognisably CWMP (e.g. a bare ACK or a continuation
		// segment with no method element).
		if d = decodeCWMP(p, payloadLines); d == nil {
			d = decodeHTTP(p, payloadLines)
		}
	}
	return decorateDecoded(p, d)
}

// decodeByOverride consults the user-supplied port → proto map. If
// either endpoint port has an override and the named decoder
// recognises the packet, returns its result. Returns nil when no
// override applies or the named decoder declined the packet (so
// the caller falls through to the built-in port dispatch).
func decodeByOverride(p *ParsedPacket, payloadLines []string, overrides map[int]string) *PacketDecode {
	if len(overrides) == 0 {
		return nil
	}
	proto := ""
	if v, ok := overrides[p.DstPort]; ok && v != "" {
		proto = strings.ToLower(strings.TrimSpace(v))
	} else if v, ok := overrides[p.SrcPort]; ok && v != "" {
		proto = strings.ToLower(strings.TrimSpace(v))
	}
	if proto == "" {
		return nil
	}
	switch proto {
	case "http":
		return decodeHTTP(p, payloadLines)
	case "tls", "https":
		return decodeTLS(p, payloadLines)
	case "dns":
		return decodeDNS(p, payloadLines)
	case "dhcp", "bootp":
		return decodeDHCP(p, payloadLines)
	case "ntp":
		return decodeNTP(p, payloadLines)
	case "snmp":
		return decodeSNMP(p, payloadLines)
	case "ldap":
		return decodeLDAP(p, payloadLines)
	case "smb", "smb2", "cifs":
		return decodeSMB(p, payloadLines)
	case "mqtt":
		return decodeMQTT(p, payloadLines)
	case "ssh":
		return decodeSSHBanner(p, payloadLines)
	case "cwmp", "tr069", "tr-069":
		return decodeCWMP(p, payloadLines)
	}
	return nil
}

// decorateDecoded backfills src/dst into the Fields map so the
// Decode tab renders "where" alongside "what". Per-decoder Summary
// already has its proto-specific phrasing; src/dst stays in the
// field table. Returns d unchanged when nil so callers can pipe.
func decorateDecoded(p *ParsedPacket, d *PacketDecode) *PacketDecode {
	if d == nil {
		return nil
	}
	if d.Fields == nil {
		d.Fields = map[string]string{}
	}
	if p.SrcIP != "" && d.Fields["src"] == "" {
		if p.SrcPort > 0 {
			d.Fields["src"] = p.SrcIP + ":" + strconv.Itoa(p.SrcPort)
		} else {
			d.Fields["src"] = p.SrcIP
		}
	}
	if p.DstIP != "" && d.Fields["dst"] == "" {
		if p.DstPort > 0 {
			d.Fields["dst"] = p.DstIP + ":" + strconv.Itoa(p.DstPort)
		} else {
			d.Fields["dst"] = p.DstIP
		}
	}
	return d
}

func decodeTLS(p *ParsedPacket, payload []string) *PacketDecode {
	bytes := ExtractHexPayload(payload)
	if len(bytes) == 0 {
		return nil
	}
	sni := DecodeTLSClientHello(bytes)
	if sni == "" {
		return nil
	}
	return &PacketDecode{
		Type:    "tls",
		Summary: "TLS ClientHello SNI: " + sni,
		Fields: map[string]string{
			"sni": sni,
		},
	}
}

func isDHCPPort(p int) bool {
	return p == 67 || p == 68
}

// ----- DHCP -----

var (
	reDHCPMsgType = regexp.MustCompile(`DHCP-Message\s*(?:Option\s*53)?[^:]*:\s*(\w+)`)
	reDHCPRequested = regexp.MustCompile(`Requested-IP[^:]*:\s*([0-9.]+)`)
	reDHCPServerID  = regexp.MustCompile(`Server-ID[^:]*:\s*([0-9.]+)`)
	reDHCPClientID  = regexp.MustCompile(`Client-Ethernet-Address\s*([0-9a-f:]+)`)
	reDHCPYIAddr    = regexp.MustCompile(`Your-IP\s+([0-9.]+)`)
	reDHCPGateway   = regexp.MustCompile(`Default-Gateway[^:]*:\s*([0-9.]+)`)
	reDHCPSubnet    = regexp.MustCompile(`Subnet-Mask[^:]*:\s*([0-9.]+)`)
	reDHCPDomain    = regexp.MustCompile(`Domain-Name[^:]*:\s*"?([^"\n]+?)"?\s*$`)
	reDHCPLeaseTime = regexp.MustCompile(`Lease-Time[^:]*:\s*(\d+)`)
	// BOOTP fixed-header fields tcpdump prints in verbose mode.
	// Present even when there's no DHCP option block.
	reBOOTPClientIP = regexp.MustCompile(`Client-IP\s+([0-9.]+)`)
	reBOOTPServerIP = regexp.MustCompile(`Server-IP\s+([0-9.]+)`)
	reBOOTPGwIP     = regexp.MustCompile(`Gateway-IP\s+([0-9.]+)`)
	// Header line: "BOOTP/DHCP, Request from aa:bb..." or
	// "BOOTP/DHCP, Reply" or "BOOTP/DHCP, unknown (0x89)".
	reDHCPHeader  = regexp.MustCompile(`BOOTP/DHCP,\s*([A-Za-z]+(?:\s*\([^)]*\))?)`)
	reHeaderXid   = regexp.MustCompile(`xid\s+0x([0-9a-fA-F]+)`)
)

// reDHCPBogus matches tcpdump's BOOTP/DHCP header for a packet that
// isn't actually BOOTP: an "unknown (0xNN)" op, or a hardware type /
// length / hops field that's nonsensical for Ethernet BOOTP (real DHCP
// is htype 1, hlen 6, hops 0). PacketCable / DOCSIS MTA provisioning
// rides UDP src port 67 to a non-68 client port, so tcpdump mislabels
// it BOOTP/DHCP and prints garbage (htype 136, bogus Your-IP, a single
// repeated xid). We refuse to decode those rather than emit a fake DORA
// transaction with a dozen "Reply" stages and scrambled IPs.
var reDHCPBogus = regexp.MustCompile(`BOOTP/DHCP,\s*unknown\b|htype\s+(?:[02-9]\d|1[0-9]\d)|hlen\s+(?:[02-9]\d|1[0-9]\d)`)

func decodeDHCP(p *ParsedPacket, payload []string) *PacketDecode {
	// Bail on non-BOOTP traffic tcpdump mislabels (PacketCable/DOCSIS on
	// port 67 -> 2011, etc). Let it fall through to a plain UDP row.
	if reDHCPBogus.MatchString(p.Raw) {
		return nil
	}
	d := &PacketDecode{
		Type:   "dhcp",
		Fields: map[string]string{},
	}
	// Header line first - direction + xid live there even when the
	// payload has no DHCP option block. tcpdump prints e.g.
	// "BOOTP/DHCP, Request from aa:bb..." or "BOOTP/DHCP, Reply"
	// or for non-standard ops "BOOTP/DHCP, unknown (0x89)" - in
	// that last case we fall back to port direction to infer
	// request vs reply.
	if m := reDHCPHeader.FindStringSubmatch(p.Raw); m != nil {
		d.Fields["direction"] = strings.TrimSpace(m[1])
	}
	if strings.HasPrefix(d.Fields["direction"], "unknown") || d.Fields["direction"] == "" {
		// Infer from port direction. tcpdump source/dest semantics:
		//   client → server : dst port 67 (BOOTREQUEST)
		//   server → client : src port 67 (BOOTREPLY)
		// Same logic when the client port is 68 (well-known) or any
		// other (custom relay setups).
		switch {
		case p.DstPort == 67:
			d.Fields["bootp_op"] = "Request"
			if d.Fields["direction"] == "" {
				d.Fields["direction"] = "Request"
			}
		case p.SrcPort == 67:
			d.Fields["bootp_op"] = "Reply"
			if d.Fields["direction"] == "" {
				d.Fields["direction"] = "Reply"
			}
		}
	}
	if m := reHeaderXid.FindStringSubmatch(p.Raw); m != nil {
		d.Fields["xid"] = "0x" + m[1]
	}
	body := strings.Join(payload, "\n")
	if m := reDHCPMsgType.FindStringSubmatch(body); m != nil {
		d.Fields["msg_type"] = m[1]
	}
	if m := reDHCPRequested.FindStringSubmatch(body); m != nil {
		d.Fields["requested_ip"] = m[1]
	}
	if m := reDHCPServerID.FindStringSubmatch(body); m != nil {
		d.Fields["server_id"] = m[1]
	}
	if m := reDHCPClientID.FindStringSubmatch(body); m != nil {
		d.Fields["client_mac"] = m[1]
	}
	if m := reDHCPYIAddr.FindStringSubmatch(body); m != nil {
		d.Fields["assigned_ip"] = m[1]
	}
	if m := reDHCPGateway.FindStringSubmatch(body); m != nil {
		d.Fields["gateway"] = m[1]
	}
	if m := reDHCPSubnet.FindStringSubmatch(body); m != nil {
		d.Fields["subnet_mask"] = m[1]
	}
	if m := reDHCPDomain.FindStringSubmatch(body); m != nil {
		d.Fields["domain"] = strings.TrimSpace(m[1])
	}
	if m := reDHCPLeaseTime.FindStringSubmatch(body); m != nil {
		d.Fields["lease_time"] = m[1] + "s"
	}
	// BOOTP fixed-header fields. Present in plain BOOTP-over-UDP /
	// custom relay flows that don't carry the DHCP option block.
	if m := reBOOTPClientIP.FindStringSubmatch(body); m != nil {
		d.Fields["client_ip"] = m[1]
	}
	if m := reBOOTPServerIP.FindStringSubmatch(body); m != nil && d.Fields["server_id"] == "" {
		d.Fields["server_id"] = m[1]
	}
	if m := reBOOTPGwIP.FindStringSubmatch(body); m != nil && d.Fields["gateway"] == "" {
		d.Fields["gateway"] = m[1]
	}
	// Your-IP from BOOTP block also fills assigned_ip when DHCP
	// options didn't.
	if m := reDHCPYIAddr.FindStringSubmatch(body); m != nil && d.Fields["assigned_ip"] == "" {
		d.Fields["assigned_ip"] = m[1]
	}

	// Build a one-line summary. DORA stages are well-known so make
	// them shout in the UI.
	msg := d.Fields["msg_type"]
	switch strings.ToLower(msg) {
	case "discover":
		d.Summary = "DHCPDISCOVER" + macSuffix(d)
	case "offer":
		ip := firstNonEmpty(d.Fields["assigned_ip"], d.Fields["requested_ip"])
		d.Summary = "DHCPOFFER → " + ip
	case "request":
		ip := firstNonEmpty(d.Fields["requested_ip"], d.Fields["assigned_ip"])
		d.Summary = "DHCPREQUEST " + ip + macSuffix(d)
	case "ack":
		ip := firstNonEmpty(d.Fields["assigned_ip"], d.Fields["requested_ip"])
		d.Summary = "DHCPACK → " + ip
	case "nak":
		d.Summary = "DHCPNAK"
	case "release":
		d.Summary = "DHCPRELEASE"
	case "decline":
		d.Summary = "DHCPDECLINE"
	case "inform":
		d.Summary = "DHCPINFORM"
	default:
		// No DHCP option block - fall back to BOOTP-style summary.
		// Translate the BOOTP op into the DHCP cycle stage when we
		// can: a Request from a client with no assigned IP is a
		// DISCOVER-ish; a Reply with an assigned IP is OFFER/ACK
		// territory. Be honest about the ambiguity - label it
		// "BOOTREQUEST" / "BOOTREPLY" rather than guess DORA.
		op := d.Fields["bootp_op"]
		if op == "" {
			op = d.Fields["direction"]
		}
		yip := d.Fields["assigned_ip"]
		cip := d.Fields["client_ip"]
		switch op {
		case "Request", "BOOTREQUEST":
			if cip != "" {
				d.Summary = "BOOTREQUEST from " + cip
			} else {
				d.Summary = "BOOTREQUEST"
			}
		case "Reply", "BOOTREPLY":
			if yip != "" {
				d.Summary = "BOOTREPLY → " + yip
			} else {
				d.Summary = "BOOTREPLY"
			}
		default:
			if yip != "" {
				d.Summary = "BOOTP ? → " + yip
			} else {
				d.Summary = "BOOTP ?"
			}
		}
	}
	return d
}

func macSuffix(d *PacketDecode) string {
	if m := d.Fields["client_mac"]; m != "" {
		return " from " + m
	}
	return ""
}

// ----- DNS -----

var (
	// tcpdump verbose for DNS: "10.0.0.1.40000 > 1.1.1.1.53: 1234+ A? example.com. (28)"
	// or replies: ".53 > .40000: 1234 1/0/0 A 93.184.216.34 (44)"
	reDNSQuery = regexp.MustCompile(`(\d+)\+?\s+([A-Z]+)\?\s+(\S+?)\.?\s+\(\d+\)`)
	reDNSReply = regexp.MustCompile(`(\d+)\*?\s+(\d+)/(\d+)/(\d+)\s+`)
	reDNSAns   = regexp.MustCompile(`\b(A|AAAA|CNAME|MX|NS|PTR|TXT|SOA|SRV)\s+(\S+)`)
)

func decodeDNS(p *ParsedPacket, payload []string) *PacketDecode {
	d := &PacketDecode{
		Type:   "dns",
		Fields: map[string]string{},
	}
	body := p.Raw
	if len(payload) > 0 {
		body += " " + strings.Join(payload, " ")
	}

	if m := reDNSQuery.FindStringSubmatch(body); m != nil {
		d.Fields["txid"] = m[1]
		d.Fields["qtype"] = m[2]
		d.Fields["qname"] = m[3]
		d.Summary = "DNS query " + m[2] + " " + m[3]
		return d
	}
	if m := reDNSReply.FindStringSubmatch(body); m != nil {
		d.Fields["txid"] = m[1]
		d.Fields["answers"] = m[2]
		d.Fields["authorities"] = m[3]
		d.Fields["additionals"] = m[4]
		// Try to extract the first answer record.
		if a := reDNSAns.FindStringSubmatch(body); a != nil {
			d.Fields["rrtype"] = a[1]
			d.Fields["rdata"] = a[2]
			d.Summary = "DNS reply " + a[1] + " " + a[2]
		} else {
			d.Summary = "DNS reply (" + m[2] + " answers)"
		}
		return d
	}
	return nil
}

// ----- ARP -----

var (
	reARPWhoHas = regexp.MustCompile(`who-has\s+([0-9.]+)\s+tell\s+([0-9.]+)`)
	reARPIsAt   = regexp.MustCompile(`([0-9.]+)\s+is-at\s+([0-9a-f:]+)`)
)

// decodeARPInfo runs on the ARP info string (already extracted into
// ParsedPacket.Info during the header parse). Distinguishes request
// from reply and pulls out the target / sender pair.
func decodeARPInfo(info string) *PacketDecode {
	d := &PacketDecode{
		Type:   "arp",
		Fields: map[string]string{},
	}
	if m := reARPWhoHas.FindStringSubmatch(info); m != nil {
		d.Fields["op"] = "request"
		d.Fields["target"] = m[1]
		d.Fields["sender"] = m[2]
		d.Summary = "Who has " + m[1] + "? Tell " + m[2]
		return d
	}
	if m := reARPIsAt.FindStringSubmatch(info); m != nil {
		d.Fields["op"] = "reply"
		d.Fields["target"] = m[1]
		d.Fields["target_mac"] = m[2]
		d.Summary = m[1] + " is at " + m[2]
		return d
	}
	if strings.Contains(strings.ToLower(info), "request") {
		d.Fields["op"] = "request"
		d.Summary = "ARP request"
		return d
	}
	if strings.Contains(strings.ToLower(info), "reply") {
		d.Fields["op"] = "reply"
		d.Summary = "ARP reply"
		return d
	}
	return d
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ----- ICMP -----

var (
	reICMPEchoReq = regexp.MustCompile(`echo request,?\s+id\s+(\d+),?\s+seq\s+(\d+)`)
	reICMPEchoRep = regexp.MustCompile(`echo reply,?\s+id\s+(\d+),?\s+seq\s+(\d+)`)
	reICMPUnreach = regexp.MustCompile(`(?i)(host|net|port|protocol)\s+unreachable\s+for\s+([0-9a-f.:]+)`)
	reICMPTTL     = regexp.MustCompile(`(?i)time\s+exceeded.*for\s+([0-9a-f.:]+)`)
)

// decodeICMP picks apart the info string tcpdump prints for ICMP
// (echo, unreachable, time-exceeded) - these are the four reasons
// 99% of operators run a ping-style capture in the first place.
func decodeICMP(p *ParsedPacket) *PacketDecode {
	d := &PacketDecode{
		Type:   "icmp",
		Fields: map[string]string{},
	}
	info := p.Info
	if info == "" {
		info = p.Raw
	}
	if m := reICMPEchoReq.FindStringSubmatch(info); m != nil {
		d.Fields["op"] = "echo_request"
		d.Fields["id"] = m[1]
		d.Fields["seq"] = m[2]
		d.Summary = "ICMP echo request seq=" + m[2]
		return d
	}
	if m := reICMPEchoRep.FindStringSubmatch(info); m != nil {
		d.Fields["op"] = "echo_reply"
		d.Fields["id"] = m[1]
		d.Fields["seq"] = m[2]
		d.Summary = "ICMP echo reply seq=" + m[2]
		return d
	}
	if m := reICMPUnreach.FindStringSubmatch(info); m != nil {
		d.Fields["op"] = strings.ToLower(m[1]) + "_unreachable"
		d.Fields["target"] = m[2]
		d.Summary = strings.Title(m[1]) + " unreachable: " + m[2]
		return d
	}
	if m := reICMPTTL.FindStringSubmatch(info); m != nil {
		d.Fields["op"] = "time_exceeded"
		d.Fields["target"] = m[1]
		d.Summary = "ICMP time exceeded for " + m[1]
		return d
	}
	low := strings.ToLower(info)
	switch {
	case strings.Contains(low, "redirect"):
		d.Fields["op"] = "redirect"
		d.Summary = "ICMP redirect"
	case strings.Contains(low, "router solicitation"):
		d.Fields["op"] = "router_solicitation"
		d.Summary = "ICMPv6 router solicitation"
	case strings.Contains(low, "router advertisement"):
		d.Fields["op"] = "router_advertisement"
		d.Summary = "ICMPv6 router advertisement"
	case strings.Contains(low, "neighbor solicitation"):
		d.Fields["op"] = "neighbor_solicitation"
		d.Summary = "ICMPv6 neighbor solicitation"
	case strings.Contains(low, "neighbor advertisement"):
		d.Fields["op"] = "neighbor_advertisement"
		d.Summary = "ICMPv6 neighbor advertisement"
	default:
		d.Summary = "ICMP"
	}
	return d
}

// ----- HTTP -----

func isHTTPPort(src, dst int) bool {
	httpPorts := map[int]bool{80: true, 8000: true, 8080: true, 8081: true, 8888: true}
	return httpPorts[src] || httpPorts[dst]
}

var (
	reHTTPReq    = regexp.MustCompile(`(?m)^(GET|POST|PUT|DELETE|HEAD|OPTIONS|PATCH|TRACE|CONNECT)\s+(\S+)\s+HTTP/(\d\.\d)`)
	reHTTPStatus = regexp.MustCompile(`(?m)^HTTP/(\d\.\d)\s+(\d{3})\s+(.*)$`)
	reHTTPHost   = regexp.MustCompile(`(?im)^Host:\s*(\S+)`)
	reHTTPUA     = regexp.MustCompile(`(?im)^User-Agent:\s*(.+?)\r?$`)
	reHTTPCT     = regexp.MustCompile(`(?im)^Content-Type:\s*(\S+)`)
)

// decodeHTTP reconstructs the ASCII payload from tcpdump's -X hex
// dump and pulls out the request line / status line plus a couple
// of headers. Skips packets whose payload doesn't start with a
// known HTTP token - TCP keepalives and SYN/ACK noise on port 80
// are common and we don't want every one of them in the Decode tab.
func decodeHTTP(p *ParsedPacket, payload []string) *PacketDecode {
	text := extractASCIIPayload(payload)
	if text == "" {
		return nil
	}
	d := &PacketDecode{
		Type:   "http",
		Fields: map[string]string{},
	}
	if m := reHTTPReq.FindStringSubmatch(text); m != nil {
		d.Fields["op"] = "request"
		d.Fields["method"] = m[1]
		d.Fields["path"] = m[2]
		d.Fields["version"] = "HTTP/" + m[3]
		if h := reHTTPHost.FindStringSubmatch(text); h != nil {
			d.Fields["host"] = h[1]
		}
		if ua := reHTTPUA.FindStringSubmatch(text); ua != nil {
			d.Fields["user_agent"] = strings.TrimSpace(ua[1])
		}
		host := d.Fields["host"]
		if host != "" {
			d.Summary = m[1] + " " + host + m[2]
		} else {
			d.Summary = m[1] + " " + m[2]
		}
		return d
	}
	if m := reHTTPStatus.FindStringSubmatch(text); m != nil {
		d.Fields["op"] = "response"
		d.Fields["version"] = "HTTP/" + m[1]
		d.Fields["status"] = m[2]
		d.Fields["reason"] = strings.TrimSpace(strings.TrimRight(m[3], "\r"))
		if ct := reHTTPCT.FindStringSubmatch(text); ct != nil {
			d.Fields["content_type"] = ct[1]
		}
		d.Summary = "HTTP " + m[2] + " " + d.Fields["reason"]
		return d
	}
	return nil
}

// extractASCIIPayload reconstructs the TCP/UDP payload as a string
// suitable for text-protocol parsing. Works off the hex column so
// it doesn't depend on tcpdump's "." placeholder logic or the
// exact spacing of the ASCII gloss (which varies by tcpdump
// version + locale). Skips IP + transport headers when we can find
// them; falls back to the raw byte stream so a malformed header
// doesn't kill the decode entirely.
func extractASCIIPayload(lines []string) string {
	bytes := ExtractHexPayload(lines)
	if len(bytes) == 0 {
		return ""
	}
	off := findTCPPayloadOffset(bytes)
	if off < 0 {
		off = findUDPPayloadOffset(bytes)
	}
	if off < 0 || off >= len(bytes) {
		// No recognisable header - try the whole stream. Text
		// protocols usually start with an ASCII method/verb so
		// the regex match will still anchor correctly.
		return printableASCII(bytes)
	}
	return printableASCII(bytes[off:])
}

// printableASCII filters non-printable bytes out so a regex
// anchored on a token like "GET " or "HTTP/" lands cleanly. We
// keep \r and \n so line splitting + Host: lookups still work.
func printableASCII(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == '\r' || c == '\n' || (c >= 0x20 && c < 0x7f) {
			out = append(out, c)
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}

// ----- SSH banner -----

var reSSHBanner = regexp.MustCompile(`SSH-(\d+\.\d+)-(\S+)`)

// decodeSSHBanner catches the plaintext version exchange that opens
// every SSH session. Only the very first data packet in each
// direction carries it; the rest is encrypted. Filter prevents the
// rest of the session from spamming false hits.
func decodeSSHBanner(p *ParsedPacket, payload []string) *PacketDecode {
	text := extractASCIIPayload(payload)
	if !strings.Contains(text, "SSH-") {
		return nil
	}
	m := reSSHBanner.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	side := "server"
	if p.DstPort == 22 {
		side = "client"
	}
	d := &PacketDecode{
		Type:   "ssh",
		Fields: map[string]string{},
	}
	d.Fields["op"] = "banner"
	d.Fields["side"] = side
	d.Fields["version"] = m[1]
	d.Fields["software"] = m[2]
	d.Summary = "SSH banner (" + side + "): " + m[2]
	return d
}

// ----- NTP -----

// NTP packet (RFC 5905) is fixed 48 bytes:
//
//	byte 0: LI (2 bits) | VN (3 bits) | Mode (3 bits)
//	byte 1: Stratum
//	byte 2: Poll interval (signed log2 seconds)
//	byte 3: Precision  (signed log2 seconds)
//
// We pluck mode + stratum out of the first two bytes.
func decodeNTP(p *ParsedPacket, payload []string) *PacketDecode {
	bytes := ExtractHexPayload(payload)
	// Skip IP + UDP headers when we got the full L2 dump. The TLS
	// decoder does the same dance - both protocols sit on top of
	// UDP/TCP whose lengths vary by IPv4 vs IPv6.
	udpStart := findUDPPayloadOffset(bytes)
	if udpStart < 0 || len(bytes)-udpStart < 2 {
		return nil
	}
	body := bytes[udpStart:]
	mode := body[0] & 0x07
	version := (body[0] >> 3) & 0x07
	stratum := body[1]

	d := &PacketDecode{
		Type:   "ntp",
		Fields: map[string]string{},
	}
	d.Fields["mode"] = ntpModeName(mode)
	d.Fields["version"] = strconv.Itoa(int(version))
	d.Fields["stratum"] = strconv.Itoa(int(stratum))
	switch mode {
	case 3:
		d.Summary = "NTP client query (v" + d.Fields["version"] + ")"
	case 4:
		d.Summary = "NTP server reply (stratum " + d.Fields["stratum"] + ")"
	case 5:
		d.Summary = "NTP broadcast"
	case 6:
		d.Summary = "NTP control message"
	case 7:
		d.Summary = "NTP private (mode 7)"
	default:
		d.Summary = "NTP " + d.Fields["mode"]
	}
	return d
}

func ntpModeName(m byte) string {
	switch m {
	case 0:
		return "reserved"
	case 1:
		return "symmetric_active"
	case 2:
		return "symmetric_passive"
	case 3:
		return "client"
	case 4:
		return "server"
	case 5:
		return "broadcast"
	case 6:
		return "control"
	case 7:
		return "private"
	}
	return "?"
}

// findIPStart returns the byte index in the hex dump where the IP
// header begins. tcpdump's `-X` is layer-3 - the hex usually starts
// with the IP header directly. On some captures (especially when
// `-e` is in play) it starts with a 14-byte Ethernet header. We
// auto-detect by sniffing the first byte: 0x45 = IPv4 IHL=5 most
// common, 0x40..0x4f = any IPv4, 0x60..0x6f = IPv6. Returns -1 if
// neither layout looks sane.
func findIPStart(b []byte) int {
	if len(b) < 20 {
		return -1
	}
	// Layer-3 dump: IP header at offset 0.
	if (b[0]&0xf0) == 0x40 || (b[0]&0xf0) == 0x60 {
		return 0
	}
	// Layer-2 dump: 14-byte Ethernet header, then IP. Sanity check
	// the ethertype.
	if len(b) >= 14 {
		etherType := uint16(b[12])<<8 | uint16(b[13])
		if etherType == 0x0800 || etherType == 0x86dd {
			return 14
		}
	}
	return -1
}

// findUDPPayloadOffset returns the byte index where the UDP payload
// begins, or -1 when the headers don't look sane. Walks IPv4 /
// IPv6 header lengths and skips the 8-byte UDP header.
func findUDPPayloadOffset(b []byte) int {
	ipStart := findIPStart(b)
	if ipStart < 0 || len(b) < ipStart+20 {
		return -1
	}
	ver := b[ipStart] & 0xf0
	switch ver {
	case 0x40:
		ihl := int(b[ipStart]&0x0f) * 4
		if ihl < 20 {
			return -1
		}
		off := ipStart + ihl + 8
		if off > len(b) {
			return -1
		}
		return off
	case 0x60:
		off := ipStart + 40 + 8
		if off > len(b) {
			return -1
		}
		return off
	}
	return -1
}

// ----- SNMP -----

// SNMP packets are BER-encoded SEQUENCEs. We don't run a full
// decoder; we extract the version integer and the community
// string (v1/v2c) which is the single field most operators
// actually look for ("are these still SNMP v1 clear-text?").
func decodeSNMP(p *ParsedPacket, payload []string) *PacketDecode {
	bytes := ExtractHexPayload(payload)
	off := findUDPPayloadOffset(bytes)
	if off < 0 || len(bytes)-off < 10 {
		return nil
	}
	body := bytes[off:]
	// Outer SEQUENCE
	if body[0] != 0x30 {
		return nil
	}
	cur := skipBERHeader(body)
	if cur < 0 || cur >= len(body) {
		return nil
	}
	// version INTEGER
	if body[cur] != 0x02 {
		return nil
	}
	verLen := int(body[cur+1])
	if cur+2+verLen > len(body) {
		return nil
	}
	version := int(body[cur+2])
	cur += 2 + verLen
	d := &PacketDecode{
		Type:   "snmp",
		Fields: map[string]string{},
	}
	d.Fields["version"] = snmpVersionName(version)
	// community OCTET STRING (v1/v2c) or securityParameters (v3)
	if cur < len(body) && body[cur] == 0x04 && version <= 1 {
		cLen := int(body[cur+1])
		if cur+2+cLen <= len(body) {
			d.Fields["community"] = string(body[cur+2 : cur+2+cLen])
		}
	}
	// PDU tag tells us request vs response. tcpdump's verbose
	// info string also calls these out so we cross-check.
	if cur < len(body) {
		switch body[cur] {
		case 0xa0:
			d.Fields["pdu"] = "GetRequest"
		case 0xa1:
			d.Fields["pdu"] = "GetNextRequest"
		case 0xa2:
			d.Fields["pdu"] = "GetResponse"
		case 0xa3:
			d.Fields["pdu"] = "SetRequest"
		case 0xa4:
			d.Fields["pdu"] = "Trap"
		case 0xa5:
			d.Fields["pdu"] = "GetBulkRequest"
		case 0xa6:
			d.Fields["pdu"] = "InformRequest"
		case 0xa7:
			d.Fields["pdu"] = "TrapV2"
		}
	}
	parts := []string{"SNMP " + d.Fields["version"]}
	if pdu := d.Fields["pdu"]; pdu != "" {
		parts = append(parts, pdu)
	}
	if cs := d.Fields["community"]; cs != "" {
		parts = append(parts, "community="+cs)
	}
	d.Summary = strings.Join(parts, " ")
	return d
}

func snmpVersionName(v int) string {
	switch v {
	case 0:
		return "v1"
	case 1:
		return "v2c"
	case 3:
		return "v3"
	}
	return "v?"
}

// skipBERHeader returns the offset just past the outer tag + length
// of a BER TLV. Handles short and long-form lengths. Returns -1 on
// malformed input.
func skipBERHeader(b []byte) int {
	if len(b) < 2 {
		return -1
	}
	lenByte := b[1]
	if lenByte&0x80 == 0 {
		return 2
	}
	n := int(lenByte & 0x7f)
	if n == 0 || 2+n > len(b) {
		return -1
	}
	return 2 + n
}

// ----- LDAP -----

// decodeLDAP looks at the LDAPMessage envelope (a BER SEQUENCE)
// and pulls out the operation tag. Full DN parse is too noisy for
// a Decode tab - we surface the op + messageID so the user knows
// "what" and "tied to which response", which is enough to debug
// most LDAP issues over tcpdump.
func decodeLDAP(p *ParsedPacket, payload []string) *PacketDecode {
	bytes := ExtractHexPayload(payload)
	off := findTCPPayloadOffset(bytes)
	if off < 0 || len(bytes)-off < 6 {
		return nil
	}
	body := bytes[off:]
	if body[0] != 0x30 {
		return nil
	}
	cur := skipBERHeader(body)
	if cur < 0 || cur+3 > len(body) {
		return nil
	}
	// messageID INTEGER
	if body[cur] != 0x02 {
		return nil
	}
	idLen := int(body[cur+1])
	if cur+2+idLen > len(body) {
		return nil
	}
	msgID := 0
	for i := 0; i < idLen; i++ {
		msgID = (msgID << 8) | int(body[cur+2+i])
	}
	cur += 2 + idLen
	if cur >= len(body) {
		return nil
	}
	op := ldapOpName(body[cur])
	d := &PacketDecode{
		Type:   "ldap",
		Fields: map[string]string{},
	}
	d.Fields["message_id"] = strconv.Itoa(msgID)
	d.Fields["op"] = op
	d.Summary = "LDAP " + op + " (msg " + strconv.Itoa(msgID) + ")"
	return d
}

func ldapOpName(tag byte) string {
	switch tag {
	case 0x60:
		return "bindRequest"
	case 0x61:
		return "bindResponse"
	case 0x42:
		return "unbindRequest"
	case 0x63:
		return "searchRequest"
	case 0x64:
		return "searchResEntry"
	case 0x65:
		return "searchResDone"
	case 0x66:
		return "modifyRequest"
	case 0x67:
		return "modifyResponse"
	case 0x68:
		return "addRequest"
	case 0x69:
		return "addResponse"
	case 0x4a:
		return "delRequest"
	case 0x6b:
		return "delResponse"
	case 0x6c:
		return "modDNRequest"
	case 0x6d:
		return "modDNResponse"
	case 0x6e:
		return "compareRequest"
	case 0x6f:
		return "compareResponse"
	case 0x50:
		return "abandonRequest"
	case 0x77:
		return "extendedRequest"
	case 0x78:
		return "extendedResponse"
	}
	return "op_0x" + strconv.FormatInt(int64(tag), 16)
}

// findTCPPayloadOffset mirrors findUDPPayloadOffset for TCP. The TCP
// header is variable-length; data offset (4 bits) lives in byte 12,
// scaled by 4.
func findTCPPayloadOffset(b []byte) int {
	ipStart := findIPStart(b)
	if ipStart < 0 || len(b) < ipStart+20 {
		return -1
	}
	ver := b[ipStart] & 0xf0
	var transportStart int
	switch ver {
	case 0x40:
		ihl := int(b[ipStart]&0x0f) * 4
		if ihl < 20 {
			return -1
		}
		transportStart = ipStart + ihl
	case 0x60:
		transportStart = ipStart + 40
	default:
		return -1
	}
	if transportStart+13 > len(b) {
		return -1
	}
	dataOff := int(b[transportStart+12]>>4) * 4
	if dataOff < 20 {
		return -1
	}
	off := transportStart + dataOff
	if off > len(b) {
		return -1
	}
	return off
}

// ----- SMB -----

// decodeSMB recognises the SMB1 (0xFF 'SMB') and SMB2 (0xFE 'SMB')
// header magic and emits the dialect + command. Most fleets still
// see a mix of SMB2 and the occasional SMB1 client during
// remediation work, so calling that out is the high-value bit.
func decodeSMB(p *ParsedPacket, payload []string) *PacketDecode {
	bytes := ExtractHexPayload(payload)
	off := findTCPPayloadOffset(bytes)
	if off < 0 {
		return nil
	}
	body := bytes[off:]
	// NetBIOS Session Service header: 4 bytes (type + length).
	if len(body) >= 4 && body[0] == 0x00 {
		body = body[4:]
	}
	if len(body) < 8 {
		return nil
	}
	d := &PacketDecode{
		Type:   "smb",
		Fields: map[string]string{},
	}
	switch {
	case body[0] == 0xff && body[1] == 'S' && body[2] == 'M' && body[3] == 'B':
		d.Fields["dialect"] = "SMB1"
		d.Fields["command"] = smb1Command(body[4])
		d.Summary = "SMB1 " + d.Fields["command"]
	case body[0] == 0xfe && body[1] == 'S' && body[2] == 'M' && body[3] == 'B':
		// SMB2 header: command at offset 12 (uint16 LE).
		if len(body) < 14 {
			return nil
		}
		cmd := uint16(body[12]) | uint16(body[13])<<8
		d.Fields["dialect"] = "SMB2/3"
		d.Fields["command"] = smb2Command(cmd)
		d.Summary = "SMB2 " + d.Fields["command"]
	case body[0] == 0xfd && body[1] == 'S' && body[2] == 'M' && body[3] == 'B':
		d.Fields["dialect"] = "SMB3 (encrypted)"
		d.Summary = "SMB3 encrypted message"
	default:
		return nil
	}
	return d
}

func smb1Command(c byte) string {
	switch c {
	case 0x72:
		return "Negotiate"
	case 0x73:
		return "SessionSetupAndX"
	case 0x75:
		return "TreeConnectAndX"
	case 0xa2:
		return "NTCreateAndX"
	case 0x2e:
		return "ReadAndX"
	case 0x2f:
		return "WriteAndX"
	case 0x04:
		return "Close"
	}
	return "cmd_0x" + strconv.FormatInt(int64(c), 16)
}

func smb2Command(c uint16) string {
	switch c {
	case 0x00:
		return "Negotiate"
	case 0x01:
		return "SessionSetup"
	case 0x02:
		return "Logoff"
	case 0x03:
		return "TreeConnect"
	case 0x04:
		return "TreeDisconnect"
	case 0x05:
		return "Create"
	case 0x06:
		return "Close"
	case 0x08:
		return "Read"
	case 0x09:
		return "Write"
	case 0x0e:
		return "QueryDirectory"
	case 0x10:
		return "QueryInfo"
	case 0x11:
		return "SetInfo"
	}
	return "cmd_0x" + strconv.FormatInt(int64(c), 16)
}

// ----- MQTT -----

// decodeMQTT reads the fixed header byte of an MQTT control packet
// to identify the type, then for PUBLISH packets pulls the topic
// out of the variable header (length-prefixed UTF-8). Other types
// just get the name - that's enough to see the broker conversation
// flow during IoT debugging.
func decodeMQTT(p *ParsedPacket, payload []string) *PacketDecode {
	bytes := ExtractHexPayload(payload)
	off := findTCPPayloadOffset(bytes)
	if off < 0 || len(bytes)-off < 2 {
		return nil
	}
	body := bytes[off:]
	pktType := body[0] >> 4
	name := mqttPacketName(pktType)
	if name == "" {
		return nil
	}
	d := &PacketDecode{
		Type:   "mqtt",
		Fields: map[string]string{},
	}
	d.Fields["packet_type"] = name
	// Skip remaining-length variable bytes (1..4 bytes, top bit
	// signals continuation) to land on the variable header.
	cur := 1
	for cur < 5 && cur < len(body) {
		b := body[cur]
		cur++
		if b&0x80 == 0 {
			break
		}
	}
	if pktType == 3 && cur+2 <= len(body) {
		// PUBLISH: variable header starts with 2-byte topic length.
		topicLen := int(body[cur])<<8 | int(body[cur+1])
		cur += 2
		if cur+topicLen <= len(body) {
			topic := string(body[cur : cur+topicLen])
			d.Fields["topic"] = topic
			d.Summary = "MQTT PUBLISH " + topic
			return d
		}
	}
	if pktType == 1 && cur+10 <= len(body) {
		// CONNECT: protocol name length-prefixed then version.
		nameLen := int(body[cur])<<8 | int(body[cur+1])
		if cur+2+nameLen+1 <= len(body) {
			d.Fields["protocol"] = string(body[cur+2 : cur+2+nameLen])
			d.Fields["mqtt_version"] = strconv.Itoa(int(body[cur+2+nameLen]))
		}
	}
	d.Summary = "MQTT " + name
	return d
}

func mqttPacketName(t byte) string {
	switch t {
	case 1:
		return "CONNECT"
	case 2:
		return "CONNACK"
	case 3:
		return "PUBLISH"
	case 4:
		return "PUBACK"
	case 5:
		return "PUBREC"
	case 6:
		return "PUBREL"
	case 7:
		return "PUBCOMP"
	case 8:
		return "SUBSCRIBE"
	case 9:
		return "SUBACK"
	case 10:
		return "UNSUBSCRIBE"
	case 11:
		return "UNSUBACK"
	case 12:
		return "PINGREQ"
	case 13:
		return "PINGRESP"
	case 14:
		return "DISCONNECT"
	}
	return ""
}

// ----- CWMP / TR-069 -----
//
// CWMP (TR-069) is SOAP 1.1 / XML carried over HTTP, almost always on
// TCP 7547 (CPE) or an ACS-side port. A message is an HTTP request or
// response whose body holds a <soap:Envelope> with a single RPC method
// under <soap:Body> - cwmp:Inform, cwmp:GetParameterValues,
// cwmp:SetParameterValues(Response), cwmp:Download, etc. We pull the
// method name, and for the common ones a few salient fields
// (Inform device identity + event, and the first ParameterValueStruct
// Name/Value pairs) so the Decode tab shows what the exchange is doing
// without dumping the whole XML.
var (
	// SOAPAction header (request) e.g. SOAPAction: "cwmp:GetParameterValues"
	reCWMPSOAPAction = regexp.MustCompile(`(?i)SOAPAction:\s*"?\s*(?:cwmp:)?([A-Za-z]+)`)
	// The RPC element inside the body: <cwmp:Inform> or <cwmp:GetParameterValuesResponse>.
	// Namespace prefix varies (cwmp:, soap-env body uses it) so match any prefix.
	reCWMPMethod = regexp.MustCompile(`<(?:\w+:)?(Inform|InformResponse|GetParameterValues|GetParameterValuesResponse|SetParameterValues|SetParameterValuesResponse|GetParameterNames|GetParameterNamesResponse|SetParameterAttributes|GetParameterAttributes|AddObject|DeleteObject|Reboot|RebootResponse|Download|DownloadResponse|Upload|TransferComplete|TransferCompleteResponse|GetRPCMethods|GetRPCMethodsResponse|FactoryReset|RequestDownload|ScheduleInform|SetParameterAttributesResponse|AutonomousTransferComplete)\b`)
	// Inform identity fields live in <DeviceId>: Manufacturer / OUI /
	// ProductClass / SerialNumber.
	reCWMPOUI          = regexp.MustCompile(`<OUI>\s*([^<]+?)\s*</OUI>`)
	reCWMPProductClass = regexp.MustCompile(`<ProductClass>\s*([^<]+?)\s*</ProductClass>`)
	reCWMPSerial       = regexp.MustCompile(`<SerialNumber>\s*([^<]+?)\s*</SerialNumber>`)
	reCWMPManufacturer = regexp.MustCompile(`<Manufacturer>\s*([^<]+?)\s*</Manufacturer>`)
	// Inform event codes: <EventStruct><EventCode>4 VALUE CHANGE</EventCode>.
	reCWMPEvent = regexp.MustCompile(`<EventCode>\s*([^<]+?)\s*</EventCode>`)
	// ParameterValueStruct Name/Value pairs. xsi:type on Value is
	// optional and namespace prefixes vary, so keep the pattern loose.
	reCWMPParamName  = regexp.MustCompile(`<Name>\s*([^<]+?)\s*</Name>`)
	reCWMPParamValue = regexp.MustCompile(`<Value\b[^>]*>([^<]*)</Value>`)
)

// looksLikeCWMP is a cheap gate so we don't run the XML regexes on
// arbitrary HTTP. True when the text carries a cwmp method element or a
// cwmp SOAPAction.
func looksLikeCWMP(text string) bool {
	// A cwmp: namespace or SOAPAction is the strong signal. A bare
	// ParameterValueStruct (with no method) is a continuation segment of
	// a larger CWMP body, which we still want to decode.
	return strings.Contains(text, "cwmp:") ||
		strings.Contains(text, "ParameterValueStruct") ||
		reCWMPSOAPAction.MatchString(text)
}

func decodeCWMP(p *ParsedPacket, payload []string) *PacketDecode {
	text := extractASCIIPayload(payload)
	if text == "" || !looksLikeCWMP(text) {
		return nil
	}
	d := &PacketDecode{Type: "cwmp", Fields: map[string]string{}}

	method := ""
	if m := reCWMPMethod.FindStringSubmatch(text); m != nil {
		method = m[1]
	} else if m := reCWMPSOAPAction.FindStringSubmatch(text); m != nil {
		method = m[1]
	}
	hasParams := reCWMPParamName.MatchString(text)
	if method == "" {
		// No method element. This is almost always a continuation TCP
		// segment of a larger SOAP body (the method tag was in an earlier
		// segment). If it still carries ParameterValueStructs, label it a
		// continuation and show the params. If it carries nothing useful,
		// decline so the packet falls back to the plain HTTP / raw view
		// instead of showing a meaningless "CWMP CWMP" row.
		if !hasParams {
			return nil
		}
		method = "(continuation)"
	}
	d.Fields["method"] = method

	// HTTP framing direction, if visible, helps tell CPE->ACS from
	// ACS->CPE at a glance.
	if reHTTPReq.MatchString(text) {
		d.Fields["http"] = "request"
	} else if reHTTPStatus.MatchString(text) {
		d.Fields["http"] = "response"
	}

	// Inform identity + event.
	if mf := reCWMPManufacturer.FindStringSubmatch(text); mf != nil {
		d.Fields["manufacturer"] = mf[1]
	}
	if oui := reCWMPOUI.FindStringSubmatch(text); oui != nil {
		d.Fields["oui"] = oui[1]
	}
	if pc := reCWMPProductClass.FindStringSubmatch(text); pc != nil {
		d.Fields["product_class"] = pc[1]
	}
	if sn := reCWMPSerial.FindStringSubmatch(text); sn != nil {
		d.Fields["serial"] = sn[1]
	}
	if events := reCWMPEvent.FindAllStringSubmatch(text, -1); len(events) > 0 {
		evs := make([]string, 0, len(events))
		for _, e := range events {
			evs = append(evs, e[1])
		}
		d.Fields["events"] = strings.Join(evs, ", ")
	}

	// First few ParameterValueStruct Name/Value pairs. Names and Values
	// are emitted in document order, so zipping the two slices pairs them
	// well enough for the common case (Name immediately followed by
	// Value). Cap at 6 so a big GetParameterValuesResponse doesn't flood
	// the field table - the full XML is still in the raw hex view.
	names := reCWMPParamName.FindAllStringSubmatch(text, -1)
	values := reCWMPParamValue.FindAllStringSubmatch(text, -1)
	const maxParams = 6
	n := len(names)
	if len(values) < n {
		n = len(values)
	}
	if n > maxParams {
		n = maxParams
	}
	for i := 0; i < n; i++ {
		name := names[i][1]
		val := strings.TrimSpace(values[i][1])
		if val == "" {
			val = "(empty)"
		}
		d.Fields["param."+name] = val
	}
	if len(names) > maxParams {
		d.Fields["params_truncated"] = strconv.Itoa(len(names)-maxParams) + " more"
	}

	// Summary line.
	switch {
	case d.Fields["serial"] != "":
		d.Summary = "CWMP " + method + " (" + d.Fields["serial"] + ")"
	case d.Fields["events"] != "":
		d.Summary = "CWMP " + method + " [" + d.Fields["events"] + "]"
	case len(names) > 0:
		d.Summary = "CWMP " + method + " (" + strconv.Itoa(len(names)) + " param" + plural(len(names)) + ")"
	default:
		d.Summary = "CWMP " + method
	}
	return d
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
