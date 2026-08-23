package ssh

// Additional network-health detectors layered on the same rolling state as
// tcpdump_insights.go: MTU black holes, duplicate-IP / ARP conflicts, and DNS
// queries that never get an answer.
//
// These three share a property that shaped the implementation: each is a
// classic "everything looks up but nothing works" fault, invisible in a
// connectivity test. A ping succeeds while large transfers hang (MTU); two
// hosts answer for one address and sessions break at random (duplicate IP); a
// resolver swallows queries so every name lookup stalls for seconds (DNS).

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// dnsGrace is how long a query waits for its answer before the sweep
	// calls it unanswered. A healthy resolver replies in milliseconds, even
	// over a WAN; the common client-side timeout is 5s, so 3s flags the fault
	// while the user is still looking at the capture.
	dnsGrace = 3 * time.Second
	// dnsMaxPending bounds the outstanding-query table. A scanner or a busy
	// resolver can emit queries far faster than they are answered.
	dnsMaxPending = 5000
	// arpMaxOwners bounds the IP -> MAC ownership table.
	arpMaxOwners = 5000
)

// reICMPFragNeeded matches the two spellings of the "packet too big" ICMP
// message. IPv4 tcpdump writes "unreachable - need to frag (mtu 1400)";
// IPv6 and tshark write "packet too big".
var reICMPFragNeeded = regexp.MustCompile(`need to frag|packet too big|fragmentation needed`)

// reMTUValue pulls the next-hop MTU out of the ICMP message when the router
// includes it, which is what makes the finding actionable.
var reMTUValue = regexp.MustCompile(`mtu\s*[= ]\s*(\d+)`)

// dnsQuery is one outstanding DNS request awaiting its reply.
type dnsQuery struct {
	client  string
	server  string
	flowKey string
	name    string // queried name when the dissector gave us one
	asked   time.Time
	flared  bool
}

// arpOwner records which MAC last claimed an IP, so a second claimant is a
// duplicate-address conflict.
type arpOwner struct {
	mac  string
	seen time.Time
}

// checkMTU flags an ICMP "fragmentation needed" / "packet too big". This is
// the direct, unambiguous signal of a path-MTU problem, and it is worth
// surfacing loudly: when the ICMP is itself filtered (the common misconfigured
// firewall), the sender never learns and the connection black-holes instead.
// We see the message only because the capture host received it.
func (ia *InsightAnalyzer) checkMTU(p ParsedPacket) bool {
	info := strings.ToLower(p.Info + " " + p.Raw)
	if !reICMPFragNeeded.MatchString(info) {
		return false
	}
	mtu := ""
	if m := reMTUValue.FindStringSubmatch(info); m != nil {
		mtu = m[1]
	}
	detail := fmt.Sprintf(
		"%s reported that a packet from %s was too big to forward and must be "+
			"fragmented. ", p.SrcIP, p.DstIP)
	if mtu != "" {
		detail += fmt.Sprintf(
			"The next hop's MTU is %s bytes. ", mtu)
	}
	detail += "Small packets (ping, the TCP handshake) pass while anything " +
		"large hangs - a tunnel or VPN link with a lower MTU than the " +
		"interface advertises. If these ICMP messages are filtered anywhere " +
		"on the path, the sender never learns and the connection black-holes. " +
		"Lower the MTU on the tunnel interface or clamp TCP MSS."

	ia.fireKey("mtu_black_hole|"+p.SrcIP+"|"+p.DstIP, Insight{
		Kind:              "mtu_black_hole",
		Severity:          "error",
		Title:             "Path MTU too small (fragmentation needed)",
		Detail:            detail,
		FlowKey:           p.FlowKey,
		SrcIP:             p.DstIP, // the sender whose packets are too big
		DstIP:             p.SrcIP, // the router that reported it
		SuggestRouteCheck: false,   // an MTU fault, not an egress-interface pick
	})
	return true
}

// checkARPConflict spots two MAC addresses claiming the same IP - a static
// address duplicated onto a second host, or a rogue device. The symptom is
// traffic that works until the peer's ARP cache flips to the other MAC, so it
// presents as random, unexplained connection resets.
func (ia *InsightAnalyzer) checkARPConflict(p ParsedPacket) {
	m := reARPIsAt.FindStringSubmatch(strings.ToLower(p.Info + " " + p.Raw))
	if m == nil {
		return
	}
	ip, mac := m[1], m[2]
	if ip == "" || mac == "" {
		return
	}
	// A gratuitous ARP (host announcing its own address) is normal and must
	// not be read as a conflict: it is the same MAC re-claiming the same IP.
	if ia.arpOwners == nil {
		ia.arpOwners = map[string]arpOwner{}
	}
	prev, ok := ia.arpOwners[ip]
	if !ok {
		if len(ia.arpOwners) >= arpMaxOwners {
			ia.arpOwners = map[string]arpOwner{}
		}
		ia.arpOwners[ip] = arpOwner{mac: mac, seen: time.Now()}
		return
	}
	if prev.mac == mac {
		ia.arpOwners[ip] = arpOwner{mac: mac, seen: time.Now()}
		return
	}
	// Two different MACs for one IP.
	ia.fireKey("arp_conflict|"+ip, Insight{
		Kind:     "arp_conflict",
		Severity: "error",
		Title:    "Duplicate IP address",
		Detail: fmt.Sprintf(
			"%s is claimed by two MAC addresses: %s and %s. Two hosts hold the "+
				"same address - a static IP handed out twice, or a device that "+
				"kept an address the DHCP server has since reassigned. Traffic "+
				"works until a peer's ARP cache flips to the other MAC, so it "+
				"looks like random connection drops rather than a config error.",
			ip, prev.mac, mac),
		FlowKey:           p.FlowKey,
		SrcIP:             ip,
		DstIP:             "",
		SuggestRouteCheck: false,
	})
	// Keep the newest claimant so a third MAC is judged against it.
	ia.arpOwners[ip] = arpOwner{mac: mac, seen: time.Now()}
}

// checkDNS records outstanding queries and matches replies against them. The
// unanswered case is judged by sweepDNS, not here - "no reply yet" is only a
// fault once the grace window has passed.
func (ia *InsightAnalyzer) checkDNS(p ParsedPacket) {
	if p.Proto != "udp" {
		return
	}
	isQuery := p.DstPort == 53
	isReply := p.SrcPort == 53
	if !isQuery && !isReply {
		return
	}
	if ia.dnsPending == nil {
		ia.dnsPending = map[string]*dnsQuery{}
	}
	// Key on the conversation, not the transaction id: the id is not in the
	// header line tcpdump prints in brief mode, and a client re-asking the
	// same question on the same socket is the case we care about anyway.
	key := p.FlowKey
	if key == "" {
		key = p.SrcIP + "|" + p.DstIP
	}

	if isReply {
		// Any answer clears the pending query - including a SERVFAIL, which
		// is a different fault than silence and not ours to flag.
		delete(ia.dnsPending, key)
		return
	}

	if _, exists := ia.dnsPending[key]; exists {
		return // already tracking this conversation's outstanding query
	}
	if len(ia.dnsPending) >= dnsMaxPending {
		ia.trimDNS(dnsMaxPending * 9 / 10)
	}
	ia.dnsPending[key] = &dnsQuery{
		client:  p.SrcIP,
		server:  p.DstIP,
		flowKey: p.FlowKey,
		name:    dnsQueriedName(p),
		asked:   time.Now(),
	}
}

// sweepDNS fires for queries still unanswered after the grace window and drops
// them from the table. Called from Sweep, which already holds the lock.
func (ia *InsightAnalyzer) sweepDNS(now time.Time) {
	for key, q := range ia.dnsPending {
		if now.Sub(q.asked) < dnsGrace {
			continue
		}
		if !q.flared {
			q.flared = true
			what := "a query"
			if q.name != "" {
				what = "a query for " + q.name
			}
			ia.fireKey("dns_no_response|"+q.client+"|"+q.server, Insight{
				Kind:     "dns_no_response",
				Severity: "error",
				Title:    "DNS query with no answer",
				Detail: fmt.Sprintf(
					"%s sent %s to %s and got nothing back within %s. The "+
						"resolver is down or filtered, or its reply is taking a "+
						"path that never arrives. Every name lookup on this host "+
						"stalls until the client's own timeout expires, which "+
						"presents as general slowness rather than a DNS fault.",
					q.client, what, q.server, dnsGrace),
				FlowKey:           q.flowKey,
				SrcIP:             q.server, // the resolver that should have answered
				DstIP:             q.client,
				SuggestRouteCheck: true,
			})
		}
		delete(ia.dnsPending, key)
	}
}

// trimDNS drops the oldest outstanding queries until the table is at most
// `target` entries. Caller holds ia.mu.
func (ia *InsightAnalyzer) trimDNS(target int) {
	if len(ia.dnsPending) <= target {
		return
	}
	// The table is bounded and this only runs at the ceiling, so a single
	// pass dropping anything older than the grace window is enough; if that
	// is not sufficient, clear it outright rather than sorting.
	cutoff := time.Now().Add(-dnsGrace)
	for k, q := range ia.dnsPending {
		if len(ia.dnsPending) <= target {
			return
		}
		if q.asked.Before(cutoff) {
			delete(ia.dnsPending, k)
		}
	}
	if len(ia.dnsPending) > target {
		ia.dnsPending = map[string]*dnsQuery{}
	}
}

// reDNSName pulls the queried name out of the dissector's summary. tcpdump
// writes "A? example.com. (29)" and tshark "Standard query 0x1234 A
// example.com", so the name follows the record type in both.
var reDNSName = regexp.MustCompile(`\b(?:A|AAAA|CNAME|MX|NS|PTR|SOA|SRV|TXT)\??\s+([A-Za-z0-9_.-]+\.[A-Za-z0-9_.-]+)`)

// dnsQueriedName is best-effort: an empty result just makes the finding read
// "a query" instead of naming the record.
func dnsQueriedName(p ParsedPacket) string {
	for _, src := range []string{p.Info, p.Raw} {
		if m := reDNSName.FindStringSubmatch(src); m != nil {
			return strings.TrimSuffix(m[1], ".")
		}
	}
	return ""
}
