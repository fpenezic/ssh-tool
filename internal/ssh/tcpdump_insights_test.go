package ssh

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

// collect runs the analyzer over a set of packets and returns every
// insight emitted.
func collect(localCIDRs []string, pkts ...ParsedPacket) []Insight {
	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, localCIDRs)
	for _, p := range pkts {
		ia.Observe(p)
	}
	return got
}

func mkUDP(src string, sp int, dst string, dp int) ParsedPacket {
	p := ParsedPacket{Proto: "udp", SrcIP: src, SrcPort: sp, DstIP: dst, DstPort: dp}
	p.FlowKey = flowKey("udp", src, sp, dst, dp)
	return p
}

func TestUDPSrcMismatch(t *testing.T) {
	// Client 10.0.0.9 asks server 10.0.0.1:53; reply comes back from a
	// DIFFERENT source 10.0.0.2 - the 0.0.0.0-bind wrong-interface smell.
	req := mkUDP("10.0.0.9", 40000, "10.0.0.1", 53)
	reply := mkUDP("10.0.0.2", 53, "10.0.0.9", 40000)
	// Same flow key requires same endpoint set; force them onto one flow
	// by using the request's canonical key for both so the analyzer sees
	// them as one conversation.
	reply.FlowKey = req.FlowKey

	got := collect(nil, req, reply)
	if len(got) != 1 {
		t.Fatalf("want 1 insight, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "udp_src_mismatch" {
		t.Fatalf("want udp_src_mismatch, got %q", got[0].Kind)
	}
	if got[0].SrcIP != "10.0.0.1" || got[0].DstIP != "10.0.0.9" {
		t.Fatalf("route-check endpoints wrong: src=%s dst=%s", got[0].SrcIP, got[0].DstIP)
	}
	if !got[0].SuggestRouteCheck {
		t.Fatalf("expected route-check suggestion")
	}
}

func TestUDPNoMismatchWhenSrcMatches(t *testing.T) {
	req := mkUDP("10.0.0.9", 40000, "10.0.0.1", 53)
	reply := mkUDP("10.0.0.1", 53, "10.0.0.9", 40000)
	reply.FlowKey = req.FlowKey
	got := collect(nil, req, reply)
	if len(got) != 0 {
		t.Fatalf("want no insight, got %+v", got)
	}
}

func TestHalfOpenAfterSweep(t *testing.T) {
	syn := ParsedPacket{Proto: "tcp", SrcIP: "10.0.0.9", SrcPort: 50000,
		DstIP: "10.0.0.1", DstPort: 443, Raw: "IP 10.0.0.9.50000 > 10.0.0.1.443: Flags [S], seq 1"}
	syn.FlowKey = flowKey("tcp", "10.0.0.9", 50000, "10.0.0.1", 443)

	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, nil)
	ia.Observe(syn)

	// Before the grace window: no finding.
	ia.Sweep()
	if len(got) != 0 {
		t.Fatalf("premature half-open: %+v", got)
	}
	// Force the SYN to look stale and sweep again.
	ia.flows[syn.FlowKey].synAt = time.Now().Add(-5 * time.Second)
	ia.Sweep()
	if len(got) != 1 || got[0].Kind != "half_open" {
		t.Fatalf("want half_open, got %+v", got)
	}
}

func TestHalfOpenSuppressedBySynAck(t *testing.T) {
	syn := ParsedPacket{Proto: "tcp", SrcIP: "10.0.0.9", SrcPort: 50000,
		DstIP: "10.0.0.1", DstPort: 443, Raw: "IP 10.0.0.9.50000 > 10.0.0.1.443: Flags [S]"}
	syn.FlowKey = flowKey("tcp", "10.0.0.9", 50000, "10.0.0.1", 443)
	synack := ParsedPacket{Proto: "tcp", SrcIP: "10.0.0.1", SrcPort: 443,
		DstIP: "10.0.0.9", DstPort: 50000, Raw: "IP 10.0.0.1.443 > 10.0.0.9.50000: Flags [S.]"}
	synack.FlowKey = syn.FlowKey

	var got []Insight
	ia := NewInsightAnalyzer(func(in Insight) { got = append(got, in) }, nil)
	ia.Observe(syn)
	ia.Observe(synack)
	ia.flows[syn.FlowKey].synAt = time.Now().Add(-5 * time.Second)
	ia.Sweep()
	if len(got) != 0 {
		t.Fatalf("handshake completed, should be no half-open: %+v", got)
	}
}

func TestICMPUnreachable(t *testing.T) {
	p := ParsedPacket{Proto: "icmp", SrcIP: "10.0.0.254", DstIP: "10.0.0.9",
		Info: "host 8.8.8.8 unreachable", Raw: "IP 10.0.0.254 > 10.0.0.9: ICMP host 8.8.8.8 unreachable"}
	got := collect(nil, p)
	if len(got) != 1 || got[0].Kind != "icmp_unreachable" {
		t.Fatalf("want icmp_unreachable, got %+v", got)
	}
}

func TestICMPDedupe(t *testing.T) {
	p := ParsedPacket{Proto: "icmp", SrcIP: "10.0.0.254", DstIP: "10.0.0.9",
		Info: "host 8.8.8.8 unreachable", Raw: "ICMP host unreachable"}
	got := collect(nil, p, p, p)
	if len(got) != 1 {
		t.Fatalf("expected dedupe to 1, got %d", len(got))
	}
}

func TestARPOffSubnet(t *testing.T) {
	// Host ARPing for 192.168.5.5 while its only subnet is 10.0.0.0/24.
	p := ParsedPacket{Proto: "arp", DstIP: "192.168.5.5", SrcIP: "10.0.0.9",
		Info: "Request who-has 192.168.5.5 tell 10.0.0.9"}
	got := collect([]string{"10.0.0.9/24"}, p)
	if len(got) != 1 || got[0].Kind != "arp_off_subnet" {
		t.Fatalf("want arp_off_subnet, got %+v", got)
	}
}

func TestARPOnSubnetNoFinding(t *testing.T) {
	p := ParsedPacket{Proto: "arp", DstIP: "10.0.0.50", SrcIP: "10.0.0.9",
		Info: "Request who-has 10.0.0.50 tell 10.0.0.9"}
	got := collect([]string{"10.0.0.9/24"}, p)
	if len(got) != 0 {
		t.Fatalf("on-subnet ARP should not flag: %+v", got)
	}
}

func TestARPDisabledWithoutTopology(t *testing.T) {
	p := ParsedPacket{Proto: "arp", DstIP: "192.168.5.5", SrcIP: "10.0.0.9",
		Info: "Request who-has 192.168.5.5 tell 10.0.0.9"}
	got := collect(nil, p)
	if len(got) != 0 {
		t.Fatalf("ARP check must be off without local CIDRs: %+v", got)
	}
}

func TestRSTStorm(t *testing.T) {
	base := ParsedPacket{Proto: "tcp", SrcIP: "10.0.0.1", SrcPort: 443,
		DstIP: "10.0.0.9", DstPort: 50000, Raw: "IP 10.0.0.1.443 > 10.0.0.9.50000: Flags [R]"}
	base.FlowKey = flowKey("tcp", "10.0.0.1", 443, "10.0.0.9", 50000)
	pkts := []ParsedPacket{base, base, base, base, base}
	got := collect(nil, pkts...)
	if len(got) != 1 || got[0].Kind != "rst_storm" {
		t.Fatalf("want one rst_storm, got %+v", got)
	}
}

func TestSplitRouteSections(t *testing.T) {
	out := "<<<ROUTE 0>>>\n10.0.0.9 dev eth0 src 10.0.0.1\n<<<ROUTE 1>>>\nRTNETLINK answers: Network is unreachable\n"
	secs := splitRouteSections(out, 2)
	if len(secs) != 2 {
		t.Fatalf("want 2 sections, got %d", len(secs))
	}
	if !contains(secs[0], "eth0") {
		t.Fatalf("section 0 missing dev: %q", secs[0])
	}
	if !contains(secs[1], "unreachable") {
		t.Fatalf("section 1 missing error: %q", secs[1])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRingBufferSnapshotAndSeq(t *testing.T) {
	h := &TcpdumpHandle{}
	// Append 3, seq is 1-based and monotonic.
	for i := 1; i <= 3; i++ {
		seq := h.appendRing(ParsedPacket{Raw: "p"})
		if seq != int64(i) {
			t.Fatalf("append %d: seq=%d want %d", i, seq, i)
		}
	}
	snap, cum := h.Snapshot()
	if cum != 3 || len(snap) != 3 {
		t.Fatalf("snapshot cum=%d len=%d want 3/3", cum, len(snap))
	}
}

func TestRingBufferEviction(t *testing.T) {
	h := &TcpdumpHandle{}
	total := tcpdumpRingCap + 50
	for i := 0; i < total; i++ {
		h.appendRing(ParsedPacket{Raw: "p"})
	}
	snap, cum := h.Snapshot()
	if cum != int64(total) {
		t.Fatalf("cum=%d want %d (cum must keep counting past the cap)", cum, total)
	}
	if len(snap) != tcpdumpRingCap {
		t.Fatalf("ring len=%d want capped at %d", len(snap), tcpdumpRingCap)
	}
}

func TestIsNotFound(t *testing.T) {
	for _, s := range []string{
		"tcpdump: command not found",
		"sh: 1: tcpdump: not found",
		"bash: tcpdump: No such file or directory",
	} {
		if !isNotFound(s) {
			t.Errorf("isNotFound(%q) = false, want true", s)
		}
	}
	// A missing interface is NOT a missing-binary error.
	if isNotFound("tcpdump: en9: no such device exists") {
		t.Error("missing interface should not be treated as missing binary")
	}
}

func TestIsExit127(t *testing.T) {
	if isExit127(nil) {
		t.Error("nil err should be false")
	}
	if !isExit127(errors.New("Process exited with status 127")) {
		t.Error("status 127 string should match")
	}
	if isExit127(errors.New("Process exited with status 1")) {
		t.Error("status 1 should not match")
	}
}

// TestFlowTableBoundedUnderManyFlows is the regression guard for the
// continuous-capture memory leak: distinct ephemeral-port flows used to
// accumulate one flowState (and one seen entry) forever. Feeding far
// more than the hard cap must leave the maps bounded.
func TestFlowTableBoundedUnderManyFlows(t *testing.T) {
	ia := NewInsightAnalyzer(func(Insight) {}, nil)
	for i := 0; i < maxFlows*3; i++ {
		sp := 1024 + (i % 60000)
		// Vary the destination too so keys stay distinct past 60k ports.
		dst := "10.0.0." + itoa(i%256)
		p := mkUDP("192.168.1.5", sp, dst, 53)
		// Make every key unique regardless of the i%... wraps.
		p.FlowKey = p.FlowKey + ":" + itoa(i)
		ia.Observe(p)
	}
	if len(ia.flows) > maxFlows {
		t.Fatalf("flows table unbounded: %d > cap %d", len(ia.flows), maxFlows)
	}
}

// TestIdleFlowsEvictedOnSweep proves Sweep drops conversations that
// have gone quiet past the idle TTL.
func TestIdleFlowsEvictedOnSweep(t *testing.T) {
	ia := NewInsightAnalyzer(func(Insight) {}, nil)
	p := mkUDP("192.168.1.5", 5000, "10.0.0.1", 53)
	ia.Observe(p)
	if len(ia.flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(ia.flows))
	}
	// Backdate lastSeen past the TTL, then sweep.
	ia.mu.Lock()
	for _, fs := range ia.flows {
		fs.lastSeen = time.Now().Add(-2 * flowIdleTTL)
	}
	ia.mu.Unlock()
	ia.Sweep()
	if len(ia.flows) != 0 {
		t.Fatalf("idle flow not evicted: %d remain", len(ia.flows))
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
