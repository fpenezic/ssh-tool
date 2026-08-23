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
	tsFieldCount
)

// tsharkSep separates the -T fields columns. Tab is tshark's default and
// cannot appear inside any of the fields we request.
const tsharkSep = "\t"

// DetectTshark reports whether `tshark` is on the remote host's PATH. The
// capture modal calls it once so it can offer the engine toggle only where it
// would work; a missing binary is not an error, it just means tcpdump.
func DetectTshark(client *ssh.Client) (bool, error) {
	sess, err := client.NewSession()
	if err != nil {
		return false, err
	}
	defer sess.Close()
	out, runErr := sess.Output("command -v tshark 2>/dev/null")
	if runErr != nil {
		// Non-zero exit means "not found", which is an answer, not a failure.
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
