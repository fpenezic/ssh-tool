package ssh

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Insight is a single network-health finding the analyzer derives from
// the live packet stream. It is emitted at most once per distinct
// (Kind, Key) so the UI list doesn't churn. Severity drives colour;
// Title is a short label; Detail is the human explanation; FlowKey ties
// it back to a conversation so the UI can offer a route-check button.
type Insight struct {
	Kind     string `json:"kind"`     // "udp_src_mismatch" | "half_open" | "icmp_unreachable" | "icmp_redirect" | "ttl_exceeded" | "arp_off_subnet" | "rst_storm"
	Severity string `json:"severity"` // "error" | "warn" | "info"
	Title    string `json:"title"`    // short label for the row
	Detail   string `json:"detail"`   // full explanation
	FlowKey  string `json:"flow_key"` // conversation this concerns ("" if n/a)
	// SrcIP / DstIP carry the endpoints the route-check needs. For a
	// reply-path problem these are the *server* address that answered
	// and the *client* it answered to, so "ip route get <client> from
	// <server>" reproduces the kernel's egress decision.
	SrcIP string `json:"src_ip"`
	DstIP string `json:"dst_ip"`
	// SuggestRouteCheck flags findings where an `ip route get` on the
	// host would confirm or refute the hypothesis (wrong egress
	// interface / source IP). The UI shows a per-flow button only then.
	SuggestRouteCheck bool `json:"suggest_route_check"`
}

// flowState tracks the minimum we need per conversation to spot the
// anomalies. Kept tiny - this runs for every packet.
type flowState struct {
	proto string
	// For half-open detection: did we see a SYN, and a SYN-ACK back?
	sawSyn     bool
	sawSynAck  bool
	synAt      time.Time
	halfFlared bool // already emitted the half-open insight

	// For UDP src-IP mismatch: the destination IP a client first sent
	// to (the address it *thinks* it's talking to). A later reply whose
	// SOURCE IP differs is the wrong-interface / 0.0.0.0-bind smell.
	udpReqDst   string // server IP the client addressed
	udpReqSrc   string // client IP
	udpFlared   bool

	rstCount int
	rstFlared bool

	// Wall-clock of the last packet on this flow. Drives eviction so a
	// continuous capture on a busy host doesn't accumulate one flowState
	// per ephemeral-port connection forever (that was a multi-GB leak).
	lastSeen time.Time
}

// Bounds on the rolling state so a continuous capture can't grow
// unbounded. On a busy host, distinct 4-tuples (ephemeral source
// ports) arrive faster than connections resolve, so both maps need a
// ceiling plus age-based eviction.
const (
	// Flows untouched for this long are evicted on the next Sweep. A
	// finished/idle conversation can't trip any new insight, so dropping
	// it costs nothing but the (rare) re-detection if it resumes.
	flowIdleTTL = 30 * time.Second
	// Hard caps: if eviction can't keep up (sweep interval vs. arrival
	// rate), Observe trims the oldest entries so memory stays bounded
	// even between sweeps.
	maxFlows = 20000
	maxSeen  = 20000
)

// InsightAnalyzer holds rolling per-flow state and de-dupes findings.
// One per capture session. All methods are safe for the single stream
// goroutine; a mutex guards the sweep timer that fires from another
// goroutine.
type InsightAnalyzer struct {
	mu     sync.Mutex
	flows  map[string]*flowState
	seen   map[string]bool // de-dupe key = kind|flowkey
	emit   func(Insight)
	// localNets are the host's own subnets, used to judge whether an ARP
	// "who-has" is for an off-subnet target (no route). Populated lazily
	// from observed traffic when not provided.
	localNets []*net.IPNet
}

// NewInsightAnalyzer builds an analyzer. emit is called once per new
// distinct finding. localCIDRs may be nil - pass the host's interface
// subnets when known to enable the ARP off-subnet check; otherwise that
// check stays disabled (no false positives from an unknown topology).
func NewInsightAnalyzer(emit func(Insight), localCIDRs []string) *InsightAnalyzer {
	ia := &InsightAnalyzer{
		flows: map[string]*flowState{},
		seen:  map[string]bool{},
		emit:  emit,
	}
	for _, c := range localCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			ia.localNets = append(ia.localNets, n)
		}
	}
	return ia
}

// Observe feeds one parsed packet into the analyzer. It updates rolling
// state and emits any newly-tripped insight. Cheap and lock-held only
// briefly.
func (ia *InsightAnalyzer) Observe(p ParsedPacket) {
	ia.mu.Lock()
	defer ia.mu.Unlock()

	// ICMP control messages are an immediate, per-packet signal - no
	// flow state needed.
	if p.Proto == "icmp" || p.Proto == "icmpv6" {
		ia.checkICMP(p)
		return
	}
	if p.Proto == "arp" {
		ia.checkARP(p)
		return
	}

	if p.FlowKey == "" {
		return
	}
	fs := ia.flows[p.FlowKey]
	if fs == nil {
		// Hard backstop: if the flow table is already at the ceiling,
		// evict the oldest entries before inserting so a burst between
		// sweeps can't blow past the cap.
		if len(ia.flows) >= maxFlows {
			ia.trimFlows(maxFlows * 9 / 10)
		}
		fs = &flowState{proto: p.Proto}
		ia.flows[p.FlowKey] = fs
	}
	fs.lastSeen = time.Now()

	switch p.Proto {
	case "tcp":
		ia.checkTCP(p, fs)
	case "udp":
		ia.checkUDP(p, fs)
	}
}

// trimFlows evicts the oldest flows (by lastSeen) until the table is at
// most `target` entries. Caller holds ia.mu. O(n log n) but only runs
// when we hit the ceiling, which under normal traffic never happens.
func (ia *InsightAnalyzer) trimFlows(target int) {
	if len(ia.flows) <= target {
		return
	}
	type kv struct {
		key string
		t   time.Time
	}
	all := make([]kv, 0, len(ia.flows))
	for k, fs := range ia.flows {
		all = append(all, kv{k, fs.lastSeen})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for i := 0; i < len(all)-target; i++ {
		delete(ia.flows, all[i].key)
	}
}

// checkUDP spots the wrong-interface reply: a client sends to server IP
// A, but the reply's SOURCE address is B (B != A). That's exactly what a
// service bound to 0.0.0.0 does when the kernel's route back to the
// client egresses a different interface than the one the request
// arrived on - the answer leaves with the wrong source IP and the
// client drops it.
func (ia *InsightAnalyzer) checkUDP(p ParsedPacket, fs *flowState) {
	if p.SrcIP == "" || p.DstIP == "" {
		return
	}
	// First packet on the flow establishes the "client → server" view.
	// We can't always tell direction, so use a heuristic: the side with
	// the lower (ephemeral-looking) source port talking to a well-known
	// or service port is the client. If we can't tell, record the first
	// seen direction as the request.
	if fs.udpReqDst == "" {
		fs.udpReqDst = p.DstIP
		fs.udpReqSrc = p.SrcIP
		return
	}
	// A reply is a packet flowing the other way (its src is the prior
	// dst's host or a *different* host claiming to be the server). The
	// tell: the reply's SOURCE IP is neither the original client nor the
	// server the client addressed.
	if p.DstIP == fs.udpReqSrc && p.SrcIP != fs.udpReqDst && !fs.udpFlared {
		fs.udpFlared = true
		ia.fire(Insight{
			Kind:     "udp_src_mismatch",
			Severity: "error",
			Title:    "UDP reply from a different source IP",
			Detail: fmt.Sprintf(
				"Client %s sent to %s but the reply came back from %s. "+
					"A service bound to 0.0.0.0 answers from whatever source "+
					"address the kernel's return route picks, not the address "+
					"the request arrived on - so the reply leaves the wrong "+
					"interface and the client discards it. Check the route "+
					"back to the client and bind the service to the specific IP.",
				fs.udpReqSrc, fs.udpReqDst, p.SrcIP),
			FlowKey:           p.FlowKey,
			SrcIP:             fs.udpReqDst, // the IP the service *should* answer from
			DstIP:             fs.udpReqSrc, // the client
			SuggestRouteCheck: true,
		})
	}
}

// checkTCP tracks the SYN / SYN-ACK handshake for half-open detection
// and counts RSTs for storm detection. Half-open is only judged stale
// by the periodic Sweep - here we just record the SYN time.
func (ia *InsightAnalyzer) checkTCP(p ParsedPacket, fs *flowState) {
	low := strings.ToLower(p.Raw)
	isSyn := strings.Contains(p.Raw, "[S]") || strings.Contains(low, "flags [s]")
	isSynAck := strings.Contains(p.Raw, "[S.]") || strings.Contains(low, "flags [s.]")
	isRst := strings.Contains(p.Raw, "[R]") || strings.Contains(p.Raw, "[R.]") ||
		strings.Contains(low, "flags [r")

	switch {
	case isSynAck:
		fs.sawSynAck = true
	case isSyn:
		if !fs.sawSyn {
			fs.sawSyn = true
			fs.synAt = time.Now()
		}
	}
	if isRst {
		fs.rstCount++
		if fs.rstCount >= 5 && !fs.rstFlared {
			fs.rstFlared = true
			ia.fire(Insight{
				Kind:     "rst_storm",
				Severity: "warn",
				Title:    "Repeated TCP resets on one flow",
				Detail: fmt.Sprintf(
					"%d RSTs seen on %s↔%s:%d. A burst of resets usually means "+
						"a firewall or middlebox is tearing the connection down, "+
						"or the return path is broken so the peer never sees the "+
						"ACKs and gives up.",
					fs.rstCount, p.SrcIP, p.DstIP, p.DstPort),
				FlowKey:           p.FlowKey,
				SrcIP:             p.SrcIP,
				DstIP:             p.DstIP,
				SuggestRouteCheck: true,
			})
		}
	}
}

// checkICMP turns control messages into insights. Unreachable /
// redirect / time-exceeded are direct routing signals tcpdump hands us
// for free.
func (ia *InsightAnalyzer) checkICMP(p ParsedPacket) {
	info := strings.ToLower(p.Info + " " + p.Raw)
	switch {
	case strings.Contains(info, "unreachable"):
		kind, sev := "icmp_unreachable", "warn"
		ia.fireKey(kind+"|"+p.SrcIP+"|"+p.DstIP, Insight{
			Kind:     kind,
			Severity: sev,
			Title:    "ICMP unreachable",
			Detail: fmt.Sprintf(
				"%s reported destination unreachable to %s (%s). The router "+
					"has no path to the target, or a filter is dropping it. "+
					"Check the route to the destination on the host.",
				p.SrcIP, p.DstIP, strings.TrimSpace(p.Info)),
			FlowKey:           p.FlowKey,
			SrcIP:             p.DstIP, // the host that tried to reach something
			DstIP:             p.SrcIP, // the unreachable next-hop reporter
			SuggestRouteCheck: true,
		})
	case strings.Contains(info, "redirect"):
		kind := "icmp_redirect"
		ia.fireKey(kind+"|"+p.SrcIP+"|"+p.DstIP, Insight{
			Kind:     kind,
			Severity: "warn",
			Title:    "ICMP redirect",
			Detail: fmt.Sprintf(
				"%s sent an ICMP redirect to %s. The host is using a gateway "+
					"that isn't the best next hop for that destination - a sign "+
					"the routing table points at the wrong router for this "+
					"traffic.",
				p.SrcIP, p.DstIP),
			FlowKey:           p.FlowKey,
			SrcIP:             p.DstIP,
			DstIP:             p.SrcIP,
			SuggestRouteCheck: true,
		})
	case strings.Contains(info, "time exceeded") || strings.Contains(info, "ttl exceeded"):
		kind := "ttl_exceeded"
		ia.fireKey(kind+"|"+p.SrcIP+"|"+p.DstIP, Insight{
			Kind:     kind,
			Severity: "warn",
			Title:    "TTL exceeded in transit",
			Detail: fmt.Sprintf(
				"%s reported TTL exceeded for traffic to %s. The packet's hop "+
					"count ran out before reaching the destination - usually a "+
					"routing loop or a path far longer than expected.",
				p.SrcIP, p.DstIP),
			FlowKey:           p.FlowKey,
			SrcIP:             p.DstIP,
			DstIP:             p.SrcIP,
			SuggestRouteCheck: true,
		})
	}
}

// checkARP flags an ARP who-has for a target outside every known local
// subnet - a host shouldn't ARP for an off-link address; it should send
// to its gateway. When it does, the route/netmask is wrong. Disabled
// unless localNets is populated (no topology = no judgement).
func (ia *InsightAnalyzer) checkARP(p ParsedPacket) {
	if len(ia.localNets) == 0 || p.DstIP == "" {
		return
	}
	target := net.ParseIP(p.DstIP)
	if target == nil {
		return
	}
	for _, n := range ia.localNets {
		if n.Contains(target) {
			return // on-link, fine
		}
	}
	kind := "arp_off_subnet"
	ia.fireKey(kind+"|"+p.DstIP, Insight{
		Kind:     kind,
		Severity: "warn",
		Title:    "ARP for an off-subnet address",
		Detail: fmt.Sprintf(
			"A host is ARPing for %s, which isn't in any local subnet. "+
				"Off-link destinations should go via the gateway, not a direct "+
				"ARP - this points at a wrong netmask or a missing route.",
			p.DstIP),
		FlowKey:           p.FlowKey,
		SrcIP:             p.SrcIP,
		DstIP:             p.DstIP,
		SuggestRouteCheck: false, // host-local config issue, not an egress pick
	})
}

// Sweep judges time-based conditions: a SYN with no SYN-ACK after the
// grace window is a half-open connection (server unreachable, return
// path dead, or a firewall swallowing the handshake). Call periodically.
func (ia *InsightAnalyzer) Sweep() {
	ia.mu.Lock()
	defer ia.mu.Unlock()
	const grace = 3 * time.Second
	now := time.Now()
	for key, fs := range ia.flows {
		// Evict idle conversations first - a finished flow can't trip a
		// new insight, so keeping it only wastes memory. This is the
		// primary defence against the continuous-capture leak.
		if !fs.lastSeen.IsZero() && now.Sub(fs.lastSeen) > flowIdleTTL {
			delete(ia.flows, key)
			continue
		}
		if fs.proto != "tcp" || !fs.sawSyn || fs.sawSynAck || fs.halfFlared {
			continue
		}
		if now.Sub(fs.synAt) < grace {
			continue
		}
		fs.halfFlared = true
		// Recover endpoints from the flow key: "tcp|ip:port|ip:port".
		a, b := flowEndpoints(key)
		ia.fire(Insight{
			Kind:     "half_open",
			Severity: "error",
			Title:    "TCP SYN with no reply",
			Detail: fmt.Sprintf(
				"%s → %s: a SYN went out but no SYN-ACK came back within %s. "+
					"The server is down or filtered, or the reply is taking a "+
					"different path and never arrives. Check the route back to "+
					"the initiator.",
				a, b, grace),
			FlowKey:           key,
			SrcIP:             hostOf(a),
			DstIP:             hostOf(b),
			SuggestRouteCheck: true,
		})
	}
}

// fire emits an insight de-duped on kind|flowkey.
func (ia *InsightAnalyzer) fire(in Insight) {
	ia.fireKey(in.Kind+"|"+in.FlowKey, in)
}

// fireKey emits with an explicit de-dupe key (for ICMP/ARP where the
// flow key may be empty but src/dst still identify the finding).
func (ia *InsightAnalyzer) fireKey(dedupe string, in Insight) {
	if ia.seen[dedupe] {
		return
	}
	// Bound the de-dupe set. Reaching this cap means tens of thousands
	// of *distinct* findings, which only happens under pathological
	// traffic; resetting risks re-emitting a handful of still-live
	// findings, an acceptable trade for not leaking unbounded memory.
	if len(ia.seen) >= maxSeen {
		ia.seen = map[string]bool{}
	}
	ia.seen[dedupe] = true
	if ia.emit != nil {
		ia.emit(in)
	}
}

// flowEndpoints splits "proto|a:port|b:port" back into "a:port",
// "b:port". Returns the raw key on a malformed input.
func flowEndpoints(key string) (string, string) {
	parts := strings.Split(key, "|")
	if len(parts) != 3 {
		return key, ""
	}
	return parts[1], parts[2]
}

// hostOf strips the ":port" suffix from an "ip:port" string, IPv6-safe
// (splits on the last colon only).
func hostOf(hp string) string {
	if hp == "" {
		return ""
	}
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		return hp[:i]
	}
	return hp
}
