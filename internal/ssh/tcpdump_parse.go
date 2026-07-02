package ssh

import (
	"regexp"
	"strconv"
	"strings"
)

// ParsedPacket is the structured form of a single tcpdump line. The
// fields are best-effort - when the parser can't infer something the
// strings stay empty. Cum (cumulative byte count carried by the line
// stream) lets the frontend deduplicate against late events the same
// way Terminal.svelte does for PTY output.
type ParsedPacket struct {
	Raw       string `json:"raw"`        // original tcpdump line (header line)
	Timestamp string `json:"timestamp"`  // "13:45:12.345678" (time only)
	Proto     string `json:"proto"`      // "tcp", "udp", "icmp", "arp", "other"
	SrcIP     string `json:"src_ip"`
	SrcPort   int    `json:"src_port"`   // 0 when n/a (ARP / ICMP / unknown)
	DstIP     string `json:"dst_ip"`
	DstPort   int    `json:"dst_port"`
	Length    int    `json:"length"`     // payload length, 0 when unknown
	Info      string `json:"info"`       // proto-specific summary (e.g. ARP request)
	// FlowKey is the canonical conversation key: lexicographically
	// smaller endpoint first, plus proto. Same key for both directions
	// of a TCP conversation. Empty for non-IP packets.
	FlowKey string `json:"flow_key"`
	// Decoded holds the structured payload-level dissection when
	// verbose mode is on and the proto is recognised (DHCP, DNS, ARP).
	// Empty Type means no decode was attempted or none matched.
	Decoded *PacketDecode `json:"decoded,omitempty"`
	// Seq is the capture's monotonic sequence number for this packet
	// (1-based), assigned server-side as it's appended to the history
	// ring. The frontend uses it to dedupe a post-detach snapshot
	// against the live stream: a live chunk with Seq <= the snapshot's
	// cum was already in the snapshot. 0 on packets that predate the
	// stamping (e.g. pre-Session banner lines).
	Seq int64 `json:"seq"`
}

// TcpdumpLineBatch is one coalesced flush of the live packet stream.
// The backend buffers packets and emits a batch on a fixed cadence
// rather than one event per packet, so a high-rate continuous capture
// can't flood the IPC queue (which leaked native memory unbounded).
//
// On a busy host (hundreds to thousands of packets/sec) the buffer is
// also capped at a small tail - the UI only shows the most recent rows
// anyway, so shipping every packet over IPC just to slice it off in the
// frontend wasted CPU and memory on both sides. Packets carries at most
// the last N seen since the previous flush; Skipped counts how many
// older ones were dropped from THIS batch; Total is the cumulative
// packet count for the whole capture so the UI can show the true number
// even though it only renders the tail. Full history stays on the
// backend ring (Snapshot).
type TcpdumpLineBatch struct {
	Packets []ParsedPacket `json:"packets"`
	Skipped int64          `json:"skipped"`
	Total   int64          `json:"total"`
}

// PacketDecode is the protocol-specific payload dissection. Type
// identifies the protocol; Fields holds key/value pairs the UI
// renders in the Decode tab; Summary is a one-line gloss for the
// flow row ("DHCPDISCOVER for 10.0.0.5" etc.).
type PacketDecode struct {
	Type    string            `json:"type"`    // "dhcp" | "dns" | "arp"
	Summary string            `json:"summary"` // one-line gloss
	Fields  map[string]string `json:"fields"`  // structured key/value pairs
}

// Regex compiled once. tcpdump's brief output isn't a strict grammar
// but follows enough patterns to cover the common cases.
var (
	// "13:45:12.345678" at start of line, after optional date.
	reTime = regexp.MustCompile(`(\d{2}:\d{2}:\d{2}\.\d+)`)

	// IPv4 host:port → host:port. tcpdump prints either
	//   "IP 10.0.0.1.443 > 10.0.0.2.51234: tcp 200"            (brief)
	// or
	//   "IP (tos 0x0, ttl 64, ..., proto UDP (17), length 383) 10.0.0.1.443 > 10.0.0.2.51234: BOOTP/DHCP, ..."   (verbose)
	// where the IP addresses come after the parenthesised header
	// preamble. Match the addr.port pair without anchoring on the
	// preceding token - `>` plus dotted IPv4 is a clean signal.
	reIP4 = regexp.MustCompile(`([0-9]+(?:\.[0-9]+){3})\.(\d+)\s*>\s*([0-9]+(?:\.[0-9]+){3})\.(\d+):\s*(\w+)?\s*(\d+)?`)

	// IPv6 variant; addresses are bracketed-free in tcpdump output.
	// "IP6 fe80::1.443 > fe80::2.1234: tcp 100"
	reIP6 = regexp.MustCompile(`([0-9a-fA-F:]+:[0-9a-fA-F]+)\.(\d+)\s*>\s*([0-9a-fA-F:]+:[0-9a-fA-F]+)\.(\d+):\s*(\w+)?\s*(\d+)?`)

	// Verbose tcpdump preamble: "(tos 0x0, ttl 64, ..., proto UDP (17), length 383)".
	// We pull the proto out of the parenthesised block when the brief
	// "tcp 200" / "UDP, length …" tail isn't present.
	reVerboseProto = regexp.MustCompile(`proto\s+(\w+)\s*\(`)

	// ARP - "ARP, Request who-has 10.0.0.5 tell 10.0.0.1, length 28"
	reARP = regexp.MustCompile(`\bARP,\s*(.+?)(?:,\s*length\s+(\d+))?$`)

	// ICMP (rare in -q output but covered).
	reICMP = regexp.MustCompile(`\bIP\s+([0-9]+(?:\.[0-9]+){3})\s*>\s*([0-9]+(?:\.[0-9]+){3}):\s*ICMP\s*(.*)`)
)

// ParseTcpdumpLine turns one tcpdump output line into a structured
// packet. Returns ok=false when nothing matches - callers should still
// keep the raw line for the flat view.
func ParseTcpdumpLine(line string) (ParsedPacket, bool) {
	p := ParsedPacket{Raw: line}
	if m := reTime.FindStringSubmatch(line); m != nil {
		p.Timestamp = m[1]
	}

	// IPv4 with ports
	if m := reIP4.FindStringSubmatch(line); m != nil {
		// Verbose preamble's "proto UDP (17)" is the authoritative
		// L4 proto when it's available - the brief tail can carry
		// flags/options/banner words like "BOOTP/DHCP" or "Flags [F.]"
		// that aren't proto names. Prefer the preamble; fall back to
		// the brief tail; final fallback "ip".
		if vm := reVerboseProto.FindStringSubmatch(line); vm != nil {
			p.Proto = strings.ToLower(vm[1])
		} else {
			p.Proto = strings.ToLower(m[5])
		}
		// "flags" / "bootp" tail words leak through if the brief
		// fallback hit - discard them. But "Flags [...]" is itself an
		// unambiguous TCP marker (UDP/ICMP carry no flags field), so a
		// line whose L4 word came back as "flags" is really TCP. Without
		// this the proto stayed "ip" and the insight analyzer's TCP
		// checks (half-open, RST storm) never ran on real captures.
		switch p.Proto {
		case "flags":
			p.Proto = "tcp"
		case "bootp":
			p.Proto = ""
		}
		// Catch the case where the L4 word was something else entirely
		// (e.g. ack/seq fell into m[5]) but the line clearly carries a
		// TCP flags field.
		if p.Proto == "" || p.Proto == "ip" {
			if strings.Contains(line, "Flags [") {
				p.Proto = "tcp"
			}
		}
		if p.Proto == "" {
			p.Proto = "ip"
		}
		p.SrcIP = m[1]
		p.SrcPort, _ = strconv.Atoi(m[2])
		p.DstIP = m[3]
		p.DstPort, _ = strconv.Atoi(m[4])
		if m[6] != "" {
			p.Length, _ = strconv.Atoi(m[6])
		}
		p.FlowKey = flowKey(p.Proto, p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
		return p, true
	}

	// IPv6 with ports
	if m := reIP6.FindStringSubmatch(line); m != nil {
		p.Proto = strings.ToLower(m[5])
		if p.Proto == "" {
			p.Proto = "ip6"
		}
		p.SrcIP = m[1]
		p.SrcPort, _ = strconv.Atoi(m[2])
		p.DstIP = m[3]
		p.DstPort, _ = strconv.Atoi(m[4])
		if m[6] != "" {
			p.Length, _ = strconv.Atoi(m[6])
		}
		p.FlowKey = flowKey(p.Proto, p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
		return p, true
	}

	// ICMP - bare IP, no ports.
	if m := reICMP.FindStringSubmatch(line); m != nil {
		p.Proto = "icmp"
		p.SrcIP = m[1]
		p.DstIP = m[2]
		p.Info = strings.TrimSpace(m[3])
		p.FlowKey = flowKey("icmp", p.SrcIP, 0, p.DstIP, 0)
		return p, true
	}

	// ARP
	if m := reARP.FindStringSubmatch(line); m != nil {
		p.Proto = "arp"
		p.Info = strings.TrimSpace(m[1])
		if m[2] != "" {
			p.Length, _ = strconv.Atoi(m[2])
		}
		// Pull "10.0.0.5" and "10.0.0.1" out of the info string so we
		// can flow-group ARP conversations too.
		ips := regexp.MustCompile(`\b\d+\.\d+\.\d+\.\d+\b`).FindAllString(p.Info, -1)
		if len(ips) >= 1 {
			p.DstIP = ips[0]
		}
		if len(ips) >= 2 {
			p.SrcIP = ips[1]
		}
		if p.SrcIP != "" && p.DstIP != "" {
			p.FlowKey = flowKey("arp", p.SrcIP, 0, p.DstIP, 0)
		}
		return p, true
	}

	return p, false
}

// flowKey makes a canonical, direction-independent key for a
// conversation. Lexicographic ordering keeps "A:1→B:2" and "B:2→A:1"
// on the same flow.
func flowKey(proto, aIP string, aPort int, bIP string, bPort int) string {
	left := aIP + ":" + strconv.Itoa(aPort)
	right := bIP + ":" + strconv.Itoa(bPort)
	if left > right {
		left, right = right, left
	}
	return proto + "|" + left + "|" + right
}
