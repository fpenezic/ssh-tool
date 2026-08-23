package ssh

import (
	"strings"
	"testing"
	"time"
)

// collectInsights runs an analyzer over a set of packets and returns
// everything it emitted.
func collectInsights(localCIDRs []string, packets ...ParsedPacket) []Insight {
	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, localCIDRs)
	for _, p := range packets {
		ia.Observe(p)
	}
	return got
}

func findInsight(list []Insight, kind string) *Insight {
	for i := range list {
		if list[i].Kind == kind {
			return &list[i]
		}
	}
	return nil
}

// --- MTU black hole -------------------------------------------------------

func TestMTUBlackHoleFromICMPFragNeeded(t *testing.T) {
	got := collectInsights(nil, ParsedPacket{
		Proto: "icmp", SrcIP: "10.0.0.1", DstIP: "10.0.0.9",
		Info: "unreachable - need to frag (mtu 1400)",
		Raw:  "01:00:00.1 IP 10.0.0.1 > 10.0.0.9: ICMP 10.0.0.9 unreachable - need to frag (mtu 1400), length 36",
	})
	in := findInsight(got, "mtu_black_hole")
	if in == nil {
		t.Fatalf("expected an mtu_black_hole insight, got %+v", got)
	}
	if in.Severity != "error" {
		t.Errorf("severity = %q, want error", in.Severity)
	}
	if !strings.Contains(in.Detail, "1400") {
		t.Errorf("the next-hop MTU is what makes this actionable, missing from: %s", in.Detail)
	}
	// The route-check button reproduces an egress-interface decision, which
	// is not the fault here.
	if in.SuggestRouteCheck {
		t.Error("an MTU fault should not offer the route check")
	}
}

func TestMTUBlackHoleIPv6PacketTooBig(t *testing.T) {
	got := collectInsights(nil, ParsedPacket{
		Proto: "icmp6", SrcIP: "2001:db8::1", DstIP: "2001:db8::9",
		Info: "ICMP6, packet too big, mtu 1280",
	})
	if findInsight(got, "mtu_black_hole") == nil {
		t.Errorf("IPv6 'packet too big' should trip the MTU check, got %+v", got)
	}
}

// "fragmentation needed" is a SUBTYPE of ICMP unreachable. If the generic
// unreachable branch runs first it reports a routing fault for what is an MTU
// problem, and the user chases the wrong thing.
func TestMTUTakesPrecedenceOverGenericUnreachable(t *testing.T) {
	got := collectInsights(nil, ParsedPacket{
		Proto: "icmp", SrcIP: "10.0.0.1", DstIP: "10.0.0.9",
		Info: "unreachable - need to frag (mtu 1400)",
	})
	if findInsight(got, "mtu_black_hole") == nil {
		t.Fatal("expected the MTU insight")
	}
	if findInsight(got, "icmp_unreachable") != nil {
		t.Error("frag-needed must not also report as a generic unreachable")
	}
}

func TestPlainUnreachableStillReportsAsUnreachable(t *testing.T) {
	got := collectInsights(nil, ParsedPacket{
		Proto: "icmp", SrcIP: "10.0.0.1", DstIP: "10.0.0.9",
		Info: "host 10.0.0.50 unreachable",
	})
	if findInsight(got, "icmp_unreachable") == nil {
		t.Errorf("a plain unreachable must still be reported, got %+v", got)
	}
	if findInsight(got, "mtu_black_hole") != nil {
		t.Error("a plain unreachable is not an MTU fault")
	}
}

func TestMTUWithoutAdvertisedValueStillFires(t *testing.T) {
	got := collectInsights(nil, ParsedPacket{
		Proto: "icmp", SrcIP: "10.0.0.1", DstIP: "10.0.0.9",
		Info: "unreachable - fragmentation needed and DF set",
	})
	if findInsight(got, "mtu_black_hole") == nil {
		t.Errorf("a router that omits the MTU should still trip the check, got %+v", got)
	}
}

// --- Duplicate IP / ARP conflict -----------------------------------------

func TestARPConflictTwoMACsOneIP(t *testing.T) {
	got := collectInsights(nil,
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:02"},
	)
	in := findInsight(got, "arp_conflict")
	if in == nil {
		t.Fatalf("expected an arp_conflict insight, got %+v", got)
	}
	if in.Severity != "error" {
		t.Errorf("severity = %q, want error", in.Severity)
	}
	for _, want := range []string{"10.0.0.5", "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"} {
		if !strings.Contains(in.Detail, want) {
			t.Errorf("detail must name %q to be actionable: %s", want, in.Detail)
		}
	}
}

// A host re-announcing its own address is normal traffic, not a conflict.
func TestARPGratuitousRepeatIsNotAConflict(t *testing.T) {
	got := collectInsights(nil,
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
	)
	if in := findInsight(got, "arp_conflict"); in != nil {
		t.Errorf("the same MAC re-claiming its own IP is gratuitous ARP, not a conflict: %+v", in)
	}
}

func TestARPDifferentIPsDoNotConflict(t *testing.T) {
	got := collectInsights(nil,
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.6 is-at aa:bb:cc:dd:ee:02"},
	)
	if findInsight(got, "arp_conflict") != nil {
		t.Error("two different IPs on two different MACs is normal")
	}
}

func TestARPConflictFiresOncePerIP(t *testing.T) {
	got := collectInsights(nil,
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:02"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:02"},
	)
	n := 0
	for _, in := range got {
		if in.Kind == "arp_conflict" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a flapping address should report once, got %d insights", n)
	}
}

// An ARP request carries no is-at, so it must never be read as an ownership
// claim.
func TestARPRequestIsNotAnOwnershipClaim(t *testing.T) {
	got := collectInsights(nil,
		ParsedPacket{Proto: "arp", Info: "Request who-has 10.0.0.5 tell 10.0.0.1"},
		ParsedPacket{Proto: "arp", Info: "Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:01"},
	)
	if findInsight(got, "arp_conflict") != nil {
		t.Error("a who-has request must not count as a MAC claiming the address")
	}
}

// --- DNS no response ------------------------------------------------------

// dnsQueryPacket builds a client -> resolver query.
func dnsQueryPacket(client, server, name string) ParsedPacket {
	p := ParsedPacket{
		Proto: "udp", SrcIP: client, SrcPort: 51000, DstIP: server, DstPort: 53,
		Info: "Standard query 0x1234 A " + name,
	}
	p.FlowKey = flowKey("udp", p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
	return p
}

func dnsReplyPacket(client, server string) ParsedPacket {
	p := ParsedPacket{
		Proto: "udp", SrcIP: server, SrcPort: 53, DstIP: client, DstPort: 51000,
		Info: "Standard query response 0x1234 A 93.184.216.34",
	}
	p.FlowKey = flowKey("udp", p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
	return p
}

func TestDNSNoResponseFiresAfterGrace(t *testing.T) {
	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, nil)
	ia.Observe(dnsQueryPacket("10.0.0.9", "10.0.0.1", "example.com"))

	// Before the grace window nothing should fire - a query in flight is not
	// a fault.
	ia.Sweep()
	if findInsight(got, "dns_no_response") != nil {
		t.Fatal("a query still inside the grace window must not be reported")
	}

	// Age the query past the window.
	ia.mu.Lock()
	for _, q := range ia.dnsPending {
		q.asked = time.Now().Add(-2 * dnsGrace)
	}
	ia.mu.Unlock()
	ia.Sweep()

	in := findInsight(got, "dns_no_response")
	if in == nil {
		t.Fatalf("expected a dns_no_response insight, got %+v", got)
	}
	if !strings.Contains(in.Detail, "example.com") {
		t.Errorf("the queried name should be named in the detail: %s", in.Detail)
	}
	if in.SrcIP != "10.0.0.1" || in.DstIP != "10.0.0.9" {
		t.Errorf("route check should run from the resolver back to the client, got %s -> %s", in.SrcIP, in.DstIP)
	}
}

func TestDNSAnsweredQueryNeverFires(t *testing.T) {
	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, nil)
	ia.Observe(dnsQueryPacket("10.0.0.9", "10.0.0.1", "example.com"))
	ia.Observe(dnsReplyPacket("10.0.0.9", "10.0.0.1"))

	ia.mu.Lock()
	for _, q := range ia.dnsPending {
		q.asked = time.Now().Add(-2 * dnsGrace)
	}
	ia.mu.Unlock()
	ia.Sweep()

	if findInsight(got, "dns_no_response") != nil {
		t.Error("an answered query must never be reported as unanswered")
	}
}

// A SERVFAIL/NXDOMAIN is an answer. It is a different fault and not this
// detector's to claim.
func TestDNSErrorResponseCountsAsAnswered(t *testing.T) {
	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, nil)
	ia.Observe(dnsQueryPacket("10.0.0.9", "10.0.0.1", "nope.example.com"))
	reply := dnsReplyPacket("10.0.0.9", "10.0.0.1")
	reply.Info = "Standard query response 0x1234 Server failure"
	ia.Observe(reply)

	ia.mu.Lock()
	for _, q := range ia.dnsPending {
		q.asked = time.Now().Add(-2 * dnsGrace)
	}
	ia.mu.Unlock()
	ia.Sweep()

	if findInsight(got, "dns_no_response") != nil {
		t.Error("a SERVFAIL is an answer - silence is what this detector reports")
	}
}

func TestDNSPendingClearedAfterFiring(t *testing.T) {
	ia := NewInsightAnalyzer(func(Insight) {}, nil)
	ia.Observe(dnsQueryPacket("10.0.0.9", "10.0.0.1", "example.com"))
	ia.mu.Lock()
	for _, q := range ia.dnsPending {
		q.asked = time.Now().Add(-2 * dnsGrace)
	}
	ia.mu.Unlock()
	ia.Sweep()

	ia.mu.Lock()
	n := len(ia.dnsPending)
	ia.mu.Unlock()
	if n != 0 {
		t.Errorf("a fired query must leave the pending table, %d left", n)
	}
}

// Non-DNS UDP must not enter the pending table at all, or a busy host fills
// it with traffic this detector has no opinion about.
func TestNonDNSUDPIsNotTracked(t *testing.T) {
	ia := NewInsightAnalyzer(func(Insight) {}, nil)
	p := ParsedPacket{Proto: "udp", SrcIP: "10.0.0.9", SrcPort: 5000, DstIP: "10.0.0.1", DstPort: 5001}
	p.FlowKey = flowKey("udp", p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
	ia.Observe(p)

	ia.mu.Lock()
	n := len(ia.dnsPending)
	ia.mu.Unlock()
	if n != 0 {
		t.Errorf("non-DNS UDP should not be tracked, %d entries", n)
	}
}

func TestDNSQueriedNameParsing(t *testing.T) {
	cases := []struct{ info, want string }{
		{"Standard query 0x1234 A example.com", "example.com"},
		{"Standard query 0xabcd AAAA host.sub.example.com", "host.sub.example.com"},
		{"A? example.com. (29)", "example.com"},
		{"Standard query response", ""},
	}
	for _, c := range cases {
		got := dnsQueriedName(ParsedPacket{Info: c.info})
		if got != c.want {
			t.Errorf("dnsQueriedName(%q) = %q, want %q", c.info, got, c.want)
		}
	}
}

func TestDNSPendingTableIsBounded(t *testing.T) {
	ia := NewInsightAnalyzer(func(Insight) {}, nil)
	for i := 0; i < dnsMaxPending+500; i++ {
		p := dnsQueryPacket("10.0.0.9", "10.0.0.1", "example.com")
		// A distinct client port per query = a distinct conversation.
		p.SrcPort = 10000 + i
		p.FlowKey = flowKey("udp", p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
		ia.Observe(p)
	}
	ia.mu.Lock()
	n := len(ia.dnsPending)
	ia.mu.Unlock()
	if n > dnsMaxPending {
		t.Errorf("pending table grew past its cap: %d > %d", n, dnsMaxPending)
	}
}
