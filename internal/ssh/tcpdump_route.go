package ssh

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// RouteQuery asks "how would the kernel reach Dst, optionally sourced
// from From?". From is the address a service answered with (e.g. the IP
// a request arrived on); leaving it empty asks the plain forward route.
type RouteQuery struct {
	Dst  string `json:"dst"`  // destination IP to resolve a route for
	From string `json:"from"` // optional source IP to pin (the address a 0.0.0.0 service should have answered from)
}

// RouteResult is the parsed output of `ip route get`. Dev is the egress
// interface the kernel picked; Src is the source address it would stamp
// on the packet; Via is the gateway (empty for on-link). Mismatch is
// set by the caller's interpretation, not parsed here. Raw keeps the
// full line for display.
type RouteResult struct {
	Dst   string `json:"dst"`
	From  string `json:"from"`
	Dev   string `json:"dev"`
	Src   string `json:"src"`
	Via   string `json:"via"`
	Raw   string `json:"raw"`
	Error string `json:"error,omitempty"`
}

var (
	reRouteDev = regexp.MustCompile(`\bdev\s+(\S+)`)
	reRouteSrc = regexp.MustCompile(`\bsrc\s+(\S+)`)
	reRouteVia = regexp.MustCompile(`\bvia\s+(\S+)`)
)

// CheckRoutes runs `ip route get` for each query in a single SSH session
// and parses the egress interface + source address. This is the active
// confirmation step the UI triggers per-flow: it shows what interface
// and source IP the kernel would actually use to reach a peer, which is
// the ground truth for "is traffic leaving the wrong interface / with
// the wrong source IP".
//
// One session, one shell loop - keeps it to a single round trip even
// for several flows. ip route get needs no sudo.
func CheckRoutes(client *ssh.Client, queries []RouteQuery) ([]RouteResult, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	// Build one script that runs every query, fencing each result with a
	// marker line so we can split the combined output back apart. Using
	// `2>&1` folds errors (e.g. "RTNETLINK answers: Network is
	// unreachable") into the same stream so they land against the right
	// query.
	var b strings.Builder
	for i, q := range queries {
		dst := shellQuote(q.Dst)
		cmd := "ip route get " + dst
		if q.From != "" {
			cmd += " from " + shellQuote(q.From)
		}
		fmt.Fprintf(&b, "echo '<<<ROUTE %d>>>'; %s 2>&1; ", i, cmd)
	}
	out, _ := sess.CombinedOutput(b.String())
	// CombinedOutput returns an error when the *last* command exits
	// non-zero (an unreachable route does). We don't treat that as fatal
	// - the per-query error text is captured in the section body below.

	sections := splitRouteSections(string(out), len(queries))
	results := make([]RouteResult, len(queries))
	for i, q := range queries {
		r := RouteResult{Dst: q.Dst, From: q.From, Raw: strings.TrimSpace(sections[i])}
		body := sections[i]
		low := strings.ToLower(body)
		if strings.Contains(low, "unreachable") ||
			strings.Contains(low, "rtnetlink") ||
			strings.Contains(low, "not found") ||
			strings.Contains(low, "invalid") {
			r.Error = strings.TrimSpace(body)
		} else {
			if m := reRouteDev.FindStringSubmatch(body); m != nil {
				r.Dev = m[1]
			}
			if m := reRouteSrc.FindStringSubmatch(body); m != nil {
				r.Src = m[1]
			}
			if m := reRouteVia.FindStringSubmatch(body); m != nil {
				r.Via = m[1]
			}
		}
		results[i] = r
	}
	return results, nil
}

// ListLocalCIDRs returns the host's IPv4/IPv6 interface subnets in CIDR
// form (e.g. "10.0.0.5/24"), used to seed the ARP off-subnet insight.
// Parses `ip -o addr show` - one address per line. No sudo. Best-effort:
// returns whatever it can parse, ignoring loopback and link-local.
func ListLocalCIDRs(client *ssh.Client) ([]string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	out, err := sess.Output("ip -o addr show 2>/dev/null")
	if err != nil {
		return nil, err
	}
	var cidrs []string
	reInet := regexp.MustCompile(`\binet6?\s+(\S+)`)
	for _, line := range strings.Split(string(out), "\n") {
		m := reInet.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cidr := m[1]
		// Drop loopback and link-local - never legitimate ARP targets to
		// reason about.
		if strings.HasPrefix(cidr, "127.") || strings.HasPrefix(cidr, "::1") ||
			strings.HasPrefix(cidr, "169.254.") || strings.HasPrefix(strings.ToLower(cidr), "fe80:") {
			continue
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs, nil
}

// splitRouteSections cuts the combined output on the "<<<ROUTE n>>>"
// markers into n bodies, indexed by query number.
func splitRouteSections(out string, n int) []string {
	secs := make([]string, n)
	cur := -1
	var sb strings.Builder
	flush := func() {
		if cur >= 0 && cur < n {
			secs[cur] = sb.String()
		}
		sb.Reset()
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "<<<ROUTE ") {
			flush()
			// parse the index out of "<<<ROUTE 3>>>"
			idxStr := strings.TrimSuffix(strings.TrimPrefix(line, "<<<ROUTE "), ">>>")
			cur = -1
			fmt.Sscanf(idxStr, "%d", &cur)
			continue
		}
		if cur >= 0 {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	flush()
	return secs
}
