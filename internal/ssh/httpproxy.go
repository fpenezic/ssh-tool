package ssh

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// isInternalDestHost reports whether host (a bare host or IP, no port) resolves
// to - or literally is - an address the give-internet proxy must refuse by
// default: loopback, RFC1918 / unique-local private ranges, link-local
// (incl. cloud metadata 169.254.169.254), or unspecified. A hostile process on
// the borrowing server would otherwise use the proxy to reach the operator's
// own localhost and LAN, since ssh-tool dials from ITS network, not the
// server's. Names are resolved here (the whole feature resolves DNS app-side)
// so a name pointing at an internal IP is caught too.
//
// Returns (true, reason) when the destination is internal and should be
// blocked. Unresolvable names are treated as blocked - fail closed - with the
// resolve error as the reason.
func isInternalDestHost(host string) (bool, string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return true, "empty host"
	}
	// Strip IPv6 brackets if a literal slipped through without a port.
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := net.LookupIP(host)
		if err != nil || len(resolved) == 0 {
			return true, fmt.Sprintf("cannot resolve %q", host)
		}
		ips = resolved
	}
	for _, ip := range ips {
		if isInternalIP(ip) {
			return true, fmt.Sprintf("%s is an internal/private address", ip)
		}
	}
	return false, ""
}

// isInternalIP is the range check behind isInternalDestHost.
func isInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() {
		return true
	}
	// ip.IsPrivate covers 10/8, 172.16/12, 192.168/16 and fc00::/7. Add the
	// carrier-grade NAT range 100.64/10 (RFC 6598), which IsPrivate omits but
	// which routes to internal infra on many networks.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// Minimal forward HTTP proxy for the "give internet" reverse tunnel. A
// server with no outbound net reaches this over a reverse forward and points
// its http_proxy/https_proxy at it; ssh-tool dials the real destination from
// its own network. DNS is therefore resolved on the ssh-tool side - the whole
// point when the server itself can't resolve names.
//
// Two request shapes are handled:
//
//   - CONNECT host:port HTTP/1.1   (HTTPS and anything tunnelled) - we reply
//     200 and then byte-tunnel to host:port.
//   - GET/POST/... http://host/... HTTP/1.1   (plain-HTTP proxying, used by
//     apt / curl without --proxy-tunnel) - we dial the origin and replay the
//     buffered request bytes, rewriting the request-line target to the
//     origin-relative path the origin expects.
//
// Anything malformed gets a 400 and the connection is closed.

const (
	// maxProxyHeaderBytes caps the request-line + headers we buffer before the
	// blank line, so a hostile server-side process can't balloon memory by
	// never terminating the header block.
	maxProxyHeaderBytes = 8 * 1024
)

// httpProxyTarget is the result of parsing a proxied request: the address to
// dial plus, for non-CONNECT requests, the raw bytes to replay to the origin
// (the rewritten request line + original headers). For CONNECT, replay is nil
// and the caller must write the 200 reply itself.
type httpProxyTarget struct {
	addr    string // host:port to dial (ssh-tool side)
	connect bool   // true for CONNECT (tunnel), false for plain-HTTP proxying
	replay  []byte // request bytes to send to the origin (non-CONNECT only)
}

// handleHTTPProxy reads and parses one proxied request off conn. On success it
// returns the dial target; the caller dials it, and for CONNECT writes the 200
// reply before tunnelling, or for plain HTTP writes target.replay to the
// origin first. On any parse error it writes a 400 to conn and returns the
// error (caller just closes).
func handleHTTPProxy(conn net.Conn, br *bufio.Reader) (*httpProxyTarget, error) {
	// Read the request line, bounded.
	line, err := readLimitedLine(br)
	if err != nil {
		writeProxyError(conn, "400 Bad Request")
		return nil, fmt.Errorf("http proxy: read request line: %w", err)
	}
	parts := strings.Fields(line)
	if len(parts) < 3 {
		writeProxyError(conn, "400 Bad Request")
		return nil, fmt.Errorf("http proxy: malformed request line %q", line)
	}
	method, target := parts[0], parts[1]

	if strings.EqualFold(method, "CONNECT") {
		host := normalizeHostPort(target, "443")
		if host == "" {
			writeProxyError(conn, "400 Bad Request")
			return nil, fmt.Errorf("http proxy: bad CONNECT target %q", target)
		}
		// Drain the rest of the header block (bounded) so the client's write
		// buffer doesn't stall; we don't need the headers for CONNECT.
		if err := drainHeaders(br, len(line)); err != nil {
			writeProxyError(conn, "400 Bad Request")
			return nil, err
		}
		return &httpProxyTarget{addr: host, connect: true}, nil
	}

	// Plain-HTTP proxying: the target is an absolute URI (http://host/path).
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "http") {
		writeProxyError(conn, "400 Bad Request")
		return nil, fmt.Errorf("http proxy: bad absolute-URI target %q", target)
	}
	addr := normalizeHostPort(u.Host, "80")
	if addr == "" {
		writeProxyError(conn, "400 Bad Request")
		return nil, fmt.Errorf("http proxy: bad host in %q", target)
	}

	// Rewrite the request line to the origin-relative form and buffer the rest
	// of the headers to replay. The origin expects "GET /path HTTP/1.1", not
	// the absolute URI a proxy receives.
	reqPath := u.RequestURI()
	rewritten := fmt.Sprintf("%s %s %s\r\n", method, reqPath, parts[2])

	rest, err := readHeadersRaw(br, len(line))
	if err != nil {
		writeProxyError(conn, "400 Bad Request")
		return nil, err
	}
	replay := append([]byte(rewritten), rest...)
	return &httpProxyTarget{addr: addr, connect: false, replay: replay}, nil
}

// normalizeHostPort ensures host has an explicit port, defaulting to
// defaultPort when absent. Returns "" if the input is unusable.
func normalizeHostPort(host, defaultPort string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	// No port present (SplitHostPort errored). Guard against a bare "[::1]"
	// style bracketed IPv6 without a port by letting JoinHostPort handle it.
	return net.JoinHostPort(host, defaultPort)
}

// readLimitedLine reads a single CRLF/LF-terminated line, capped at
// maxProxyHeaderBytes, returned without the trailing newline/CR.
func readLimitedLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		if sb.Len() > maxProxyHeaderBytes {
			return "", fmt.Errorf("http proxy: request line too long")
		}
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			s := sb.String()
			return strings.TrimSuffix(s, "\r"), nil
		}
		sb.WriteByte(b)
	}
}

// drainHeaders consumes header lines up to and including the terminating blank
// line, enforcing the byte cap (already includes the request-line length).
func drainHeaders(br *bufio.Reader, consumed int) error {
	total := consumed
	for {
		line, err := readLimitedLine(br)
		if err != nil {
			return fmt.Errorf("http proxy: read headers: %w", err)
		}
		total += len(line) + 2
		if total > maxProxyHeaderBytes {
			return fmt.Errorf("http proxy: headers too long")
		}
		if line == "" {
			return nil
		}
	}
}

// readHeadersRaw reads header lines up to and including the blank line and
// returns them as raw bytes (with CRLF terminators) so they can be replayed to
// the origin verbatim. Enforces the byte cap.
func readHeadersRaw(br *bufio.Reader, consumed int) ([]byte, error) {
	var out []byte
	total := consumed
	for {
		line, err := readLimitedLine(br)
		if err != nil {
			return nil, fmt.Errorf("http proxy: read headers: %w", err)
		}
		total += len(line) + 2
		if total > maxProxyHeaderBytes {
			return nil, fmt.Errorf("http proxy: headers too long")
		}
		out = append(out, []byte(line)...)
		out = append(out, '\r', '\n')
		if line == "" {
			return out, nil
		}
	}
}

// writeProxyError sends a minimal HTTP error response. Best-effort; caller
// closes the connection regardless.
func writeProxyError(conn net.Conn, status string) {
	_, _ = conn.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
}
