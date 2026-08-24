package ssh

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// TcpdumpOptions controls a single capture session.
type TcpdumpOptions struct {
	Iface     string // network interface name, e.g. "eth0"
	BPFFilter string // BPF filter expression, e.g. "host 1.2.3.4 and port 443"
	MaxCount  int    // packet count cap (passed via -c). 0 = no cap (we still cap at 5000 for safety).
	// Verbose toggles `-v -nn` (with payload decode) instead of `-q`
	// (brief, header only). Each captured packet then carries a
	// PacketDecode for known protos (DHCP, DNS, ARP).
	Verbose bool
	// PortOverrides maps a non-standard port to a protocol name so the
	// decoder will treat traffic on that port as that protocol. Useful
	// for HTTP on 9000, MQTT bridge on 1885, etc. Lower-cased proto
	// names: "http", "tls", "dns", "ntp", "snmp", "ldap", "smb",
	// "mqtt", "ssh", "dhcp". Empty / unknown values are ignored.
	PortOverrides map[int]string
	// Insights enables the live network-health analyzer (half-open TCP,
	// UDP source-IP mismatch, ICMP unreachable/redirect/TTL-exceeded,
	// ARP off-subnet, RST storms). Independent of Verbose - works off
	// the parsed header stream. Findings arrive via onInsight.
	Insights bool
	// LocalCIDRs are the capture host's own interface subnets. When
	// supplied they enable the ARP off-subnet check; absent, that one
	// check stays off (no topology = no false positives).
	LocalCIDRs []string
	// ExcludeSSH, when true, appends a BPF clause that drops the SSH
	// control connection this capture rides on. Capturing tcpdump output
	// over the same SSH session is a feedback loop: each captured packet
	// is streamed back over SSH, generating more SSH packets that tcpdump
	// then captures - tens of Mbit/s and massive kernel drops. We exclude
	// it using the remote's own $SSH_CONNECTION (client_ip client_port
	// server_ip server_port), so it works regardless of the SSH port.
	// Default-on from the app.
	ExcludeSSH bool
	// Engine selects the capture backend: "" / "tcpdump" (default) or
	// "tshark". tshark is offered only where DetectTshark found the binary;
	// it dissects application protocols tcpdump cannot name, and streams
	// tab-separated fields instead of tcpdump's text grammar, so the stdout
	// path parses it with parseTsharkLine rather than ParseTcpdumpLine.
	Engine string
}

// useTshark reports whether this capture runs under tshark.
func (o TcpdumpOptions) useTshark() bool {
	return strings.TrimSpace(o.Engine) == "tshark"
}

// TcpdumpLineHandler is invoked for each parsed line from the capture.
// Receives both the raw line and a best-effort structured parse -
// the parse is empty/zero-valued when the line doesn't match any
// known tcpdump pattern.
type TcpdumpLineHandler func(packet ParsedPacket)

// TcpdumpLifecycleHandler signals state changes - "needs_password" when
// sudo prompts, "started" once data flows, "ended" when the process exits
// (with optional error message).
type TcpdumpLifecycleHandler func(event string, msg string)

// TcpdumpInsightHandler is invoked once per distinct network-health
// finding the analyzer derives from the stream. Nil disables insights.
type TcpdumpInsightHandler func(in Insight)

// tcpdumpRingCap bounds the server-side packet history kept per capture
// so a window that attaches after the fact (post-detach) can recover
// what it missed. Matches the frontend's non-verbose RENDER_CAP so the
// snapshot can't exceed what the UI would show anyway.
const tcpdumpRingCap = 2000

// TcpdumpHandle represents an active capture. The owning code keeps it
// to send Stop() / ProvidePassword() / Cancel().
type TcpdumpHandle struct {
	ID       string
	sess     *ssh.Session
	stdin    io.WriteCloser
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	awaitPwd chan string // buffered (1) - frontend posts the password here

	// Server-side packet history. ring holds the last tcpdumpRingCap
	// packets; cum is the total ever appended (a monotonic watermark the
	// frontend dedupes against, same idea as the PTY snapshot/cum race
	// fix). A window attaching mid-capture pulls Snapshot() then dedupes
	// live chunks whose seq <= the snapshot's cum.
	ringMu sync.Mutex
	ring   []ParsedPacket
	cum    int64

	// Opts records the capture parameters so a window attaching after a
	// detach can show what's running (interface, BPF, verbose,
	// continuous, insights) instead of losing that context.
	Opts TcpdumpOptions
}

// appendRing records one packet into the bounded history and returns its
// 1-based sequence number (== cum after the append). The live emit path
// stamps each ParsedPacket with this so the frontend can dedupe a
// snapshot against the live stream.
func (h *TcpdumpHandle) appendRing(p ParsedPacket) int64 {
	h.ringMu.Lock()
	defer h.ringMu.Unlock()
	h.cum++
	h.ring = append(h.ring, p)
	if len(h.ring) > tcpdumpRingCap {
		h.ring = h.ring[len(h.ring)-tcpdumpRingCap:]
	}
	return h.cum
}

// Snapshot returns a copy of the retained packet history and the current
// cumulative count. Safe to call from any goroutine.
func (h *TcpdumpHandle) Snapshot() ([]ParsedPacket, int64) {
	h.ringMu.Lock()
	defer h.ringMu.Unlock()
	out := make([]ParsedPacket, len(h.ring))
	copy(out, h.ring)
	return out, h.cum
}

// ListInterfaces runs `ls /sys/class/net` on the target host and
// returns the names. Cheap, no sudo. Used by the frontend to populate
// the interface dropdown.
func ListInterfaces(client *ssh.Client) ([]string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	out, err := sess.Output("ls /sys/class/net 2>/dev/null")
	if err != nil {
		return nil, err
	}
	// "any" is a Linux-kernel pseudo-interface tcpdump exposes for
	// capturing across every device at once. It doesn't appear in
	// /sys/class/net (kernel doesn't expose it there), so we add it
	// up front - most operators reach for it first when they don't
	// yet know which interface the traffic rides on.
	ifs := []string{"any"}
	for _, name := range strings.Fields(string(out)) {
		// Filter out the always-uninteresting ones; keep lo for completeness.
		if name == "bonding_masters" {
			continue
		}
		ifs = append(ifs, name)
	}
	return ifs, nil
}

// CheckRootOrSudo returns (rootUser, sudoNoPasswordWorks, err). If
// rootUser is true the caller can skip sudo. If sudoNoPasswordWorks is
// true a cached/NOPASSWD ticket is good for the next call. Otherwise
// the frontend has to prompt the user for a password.
func CheckRootOrSudo(client *ssh.Client) (bool, bool, error) {
	// Whoami first - cheap.
	wo, err := client.NewSession()
	if err != nil {
		return false, false, err
	}
	out, err := wo.Output("whoami")
	wo.Close()
	if err != nil {
		return false, false, err
	}
	if strings.TrimSpace(string(out)) == "root" {
		return true, true, nil
	}
	// Probe sudo without prompting. -n bails out with exit code 1 if
	// sudo would have asked for a password.
	sess, err := client.NewSession()
	if err != nil {
		return false, false, err
	}
	defer sess.Close()
	if err := sess.Run("sudo -n true"); err != nil {
		// Exit non-zero → would need a password. Not an error from
		// our perspective.
		return false, false, nil
	}
	return false, true, nil
}

// StartTcpdump launches tcpdump (under sudo if needed) on the target
// host and streams parsed lines through onLine. The handle is returned
// immediately so the caller can stop or provide a password later.
//
// Auth model:
//   - if rootUser is true, runs tcpdump directly.
//   - else uses `sudo -S -p ”` reading from stdin. needsPassword=true
//     means a prompt is required; otherwise sudo -n is used.
func StartTcpdump(
	client *ssh.Client,
	rootUser, sudoNoPwd bool,
	opts TcpdumpOptions,
	onLine TcpdumpLineHandler,
	onLifecycle TcpdumpLifecycleHandler,
	onInsight TcpdumpInsightHandler,
) (*TcpdumpHandle, error) {
	if opts.Iface == "" {
		return nil, fmt.Errorf("interface required")
	}
	// MaxCount semantics:
	//   > 0  -> cap at that many packets (clamped to 5000 ceiling)
	//   == 0 -> default 5000 cap
	//   < 0  -> continuous: no -c, runs until explicitly stopped. Used
	//           for a long-lived capture that should survive a tab
	//           detach and keep streaming. The frontend RENDER_CAP still
	//           trims what's kept in the DOM, so memory stays bounded.
	continuous := opts.MaxCount < 0
	maxCount := opts.MaxCount
	if !continuous && (maxCount == 0 || maxCount > 5000) {
		maxCount = 5000
	}

	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}

	// Compose the capture command for the selected engine. Both branches
	// take the same SSH-exclusion clause and apply the same sudo prefix
	// below, so switching engines changes only the command text.
	cmd, err := buildCaptureCommand(opts, sshExclusionBPF(client), maxCount, continuous)
	if err != nil {
		sess.Close()
		return nil, err
	}
	switch {
	case rootUser:
		// direct run
	case sudoNoPwd:
		cmd = "sudo -n " + cmd
	default:
		// We pipe the password via stdin (sudo -S reads until newline).
		cmd = "sudo -S -p '' " + cmd
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &TcpdumpHandle{
		ID:       uuid.New().String(),
		sess:     sess,
		stdin:    stdin,
		cancel:   cancel,
		awaitPwd: make(chan string, 1),
		Opts:     opts,
	}

	if err := sess.Start(cmd); err != nil {
		sess.Close()
		cancel()
		return nil, err
	}

	// Live network-health analyzer. nil-safe: when insights are off the
	// analyzer is left nil and the stream goroutine skips Observe. The
	// emit closure routes findings out through onInsight; the sweep
	// ticker drives the time-based half-open check until ctx cancels.
	var analyzer *InsightAnalyzer
	if opts.Insights && onInsight != nil {
		analyzer = NewInsightAnalyzer(onInsight, opts.LocalCIDRs)
		go func() {
			t := time.NewTicker(1 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					analyzer.Sweep()
				}
			}
		}()
	}

	// If sudo needs a password, send it as soon as the frontend provides
	// it. Drain in the background.
	if !rootUser && !sudoNoPwd {
		onLifecycle("needs_password", "sudo requires a password")
		go func() {
			select {
			case pass := <-h.awaitPwd:
				_, _ = io.WriteString(stdin, pass+"\n")
			case <-ctx.Done():
			}
		}()
	} else {
		onLifecycle("started", "")
	}

	// stdout → parsed line handler. Brief mode is one packet per line.
	// Verbose mode emits a header line per packet followed by indented
	// continuation lines (BOOTP/DHCP options, DNS records, etc). We
	// collect those into a single packet emit so the Decode tab sees
	// the full payload at once.
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		// tshark's -T fields output is strictly one record per line - there
		// are no continuation lines to reassemble, so it takes a much simpler
		// loop than tcpdump's multi-line verbose format below.
		if opts.useTshark() {
			for sc.Scan() {
				line := sc.Text()
				pkt, ok := parseTsharkLine(line)
				if !ok {
					// Banner / status noise. Surface it unparsed rather than
					// dropping it, same as the tcpdump path does.
					if strings.TrimSpace(line) == "" {
						continue
					}
					pkt = ParsedPacket{Raw: line}
					pkt.Seq = h.appendRing(pkt)
					onLine(pkt)
					continue
				}
				if analyzer != nil {
					analyzer.Observe(pkt)
				}
				pkt.Seq = h.appendRing(pkt)
				onLine(pkt)
			}
			return
		}

		var header string
		var payload []string

		flush := func() {
			if header == "" {
				return
			}
			// Verbose tcpdump splits a packet across the timestamp+IP
			// preamble line and a follow-up "  src.port > dst.port: ..."
			// line. Join the header with as many leading payload lines
			// as needed so the parser sees the full "header sentence".
			parseInput := header
			payloadStart := 0
			for i, pl := range payload {
				if strings.Contains(pl, " > ") &&
					(strings.Contains(pl, "IP") ||
						strings.Contains(pl, ".") /* "10.0.0.1.443" */) {
					parseInput = header + " " + strings.TrimSpace(pl)
					payloadStart = i + 1
					break
				}
				// Cap the look-ahead - never consume more than 3 lines
				// before giving up.
				if i >= 2 {
					break
				}
			}
			pkt, _ := ParseTcpdumpLine(parseInput)
			pkt.Raw = header
			if opts.Verbose {
				// Hand the decoder the full multi-line packet content
				// in p.Raw - header + payload joined - so regexes that
				// look for fields like xid (which live on the BOOTP
				// header line, not the IP preamble) actually find them.
				if len(payload) > 0 {
					pkt.Raw = header + "\n" + strings.Join(payload, "\n")
				}
				if d := DecodeWithOverrides(&pkt, payload[payloadStart:], opts.PortOverrides); d != nil {
					pkt.Decoded = d
				}
			}
			if analyzer != nil {
				analyzer.Observe(pkt)
			}
			pkt.Seq = h.appendRing(pkt)
			onLine(pkt)
			header = ""
			payload = payload[:0]
		}

		// Hard cap on accumulated continuation lines for a single
		// packet so a giant verbose hex-dump or a malformed stream
		// where no header ever lands can't grow the payload slice
		// without bound. 256 lines covers the deepest legit decode
		// (TLS handshake with full cipher list); past that we drop
		// further continuations silently rather than buffer them.
		const maxPayloadLines = 256
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			// Header lines start with a digit (the timestamp). Anything
			// else is a continuation of the previous packet.
			isHeader := len(line) > 0 && line[0] >= '0' && line[0] <= '9'
			if isHeader {
				flush()
				header = line
			} else if header != "" {
				if len(payload) < maxPayloadLines {
					payload = append(payload, line)
				}
			} else {
				// No header yet (startup banner like "listening on …") -
				// emit as a raw, unparsed packet so the user sees it.
				pkt := ParsedPacket{Raw: line}
				pkt.Seq = h.appendRing(pkt)
				onLine(pkt)
			}
		}
		flush()
	}()

	// notFoundCh carries whether the stderr scan saw a "binary missing"
	// signal, so the exit handler can suppress a duplicate/confusing
	// "ended" message (exit 127) and not double-report.
	notFoundCh := make(chan bool, 1)

	// The engine name for user-facing errors - "tshark not installed" must
	// not say tcpdump, or the user installs the wrong package.
	engineName := "tcpdump"
	if opts.useTshark() {
		engineName = "tshark"
	}
	notInstalledMsg := engineName + " not installed on the remote host (or not on PATH)"

	// stderr → lifecycle / error surface. Distinguish a wrong-password
	// from a missing-tcpdump from a generic failure by sniffing common
	// substrings.
	go func() {
		sc := bufio.NewScanner(stderr)
		var firstErr string
		notFound := false
		for sc.Scan() {
			line := sc.Text()
			// Suppress tcpdump's startup banner - it goes to stderr.
			low := strings.ToLower(line)
			// Startup banners go to stderr on both engines: tcpdump's
			// "listening on eth0..." and tshark's "Capturing on 'eth0'".
			if strings.Contains(low, "listening on") ||
				strings.Contains(low, "verbose output") ||
				strings.HasPrefix(low, "capturing on") {
				continue
			}
			if firstErr == "" {
				firstErr = line
			}
			switch {
			case strings.Contains(low, "incorrect password") || strings.Contains(low, "sorry, try again"):
				onLifecycle("password_rejected", line)
			case isNotFound(low):
				// "tcpdump: command not found" (bash), "sh: 1: tcpdump:
				// not found" (dash/Debian), "tcpdump: No such file or
				// directory", etc. All mean the binary isn't on PATH.
				notFound = true
				onLifecycle("error", notInstalledMsg)
			}
		}
		notFoundCh <- notFound
	}()

	// Wait for the process to exit in the background - emit "ended".
	go func() {
		err := sess.Wait()
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		cancel()
		_ = sess.Close()
		// Drain the stderr verdict (stderr pipe is closed by Wait, so the
		// scan loop has finished). Exit 127 from a shell means the binary
		// wasn't found; if stderr already reported that, don't re-emit a
		// confusing "ended: Process exited with status 127".
		notFound := <-notFoundCh
		if notFound || isExit127(err) {
			if !notFound {
				onLifecycle("error", notInstalledMsg)
			}
			onLifecycle("ended", "")
			return
		}
		if err != nil {
			onLifecycle("ended", err.Error())
		} else {
			onLifecycle("ended", "")
		}
	}()

	return h, nil
}

// isNotFound matches the various shells' "binary not on PATH" stderr.
// Case-insensitive so callers can pass a raw line.
func isNotFound(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "command not found") ||
		strings.Contains(low, "not found") ||
		strings.Contains(low, "no such file or directory")
}

// isExit127 reports whether an ssh session exit was code 127 (the POSIX
// shell "command not found" status), even when no stderr line explained
// it.
func isExit127(err error) bool {
	if err == nil {
		return false
	}
	if ee, ok := err.(*ssh.ExitError); ok {
		return ee.ExitStatus() == 127
	}
	return strings.Contains(err.Error(), "status 127")
}

// ProvidePassword feeds the cached sudo prompt with the user's input.
// Safe to call once; subsequent calls are no-ops.
func (h *TcpdumpHandle) ProvidePassword(pass string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	select {
	case h.awaitPwd <- pass:
	default:
	}
}

// Stop sends SIGINT to the remote tcpdump so the kernel flushes any
// in-flight buffer before the process exits, then closes the SSH
// session. Idempotent.
func (h *TcpdumpHandle) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	// Best-effort SIGINT - sess.Signal may not work on every server,
	// in which case sess.Close() still tears the channel down.
	_ = h.sess.Signal(ssh.SIGINT)
	_ = h.stdin.Close()
	_ = h.sess.Close()
	h.cancel()
	h.closed = true
}

// sshExclusionBPF builds the BPF clause that drops the SSH control connection
// this capture rides on, so tcpdump output streamed back over SSH doesn't feed
// itself. It derives the client IP + port from the live SSH connection's local
// address (what the server sees as the peer), which is correct whether the hop
// went out a physical NIC or through the userspace WireGuard netstack - unlike
// the old $SSH_CONNECTION shell form, which came back empty over WireGuard (and
// under sudo env_reset), silently disabling the filter and flooding port 22.
//
// When the Go-side address is usable we emit a literal `not ( host H and port P )`
// using our own peer IP:port as the server sees it; otherwise we fall back to
// the $SSH_CONNECTION shell form. We deliberately do NOT exclude by the target's
// SSH listen port alone - that would also hide any other SSH traffic the user
// wants to see. Only this exact control connection is dropped.
func sshExclusionBPF(client *ssh.Client) string {
	// Ask the SERVER what it sees first. LocalAddr() is our socket's address,
	// which is only the same thing when nothing rewrites it in between - and
	// behind NAT it is not: the client holds a private address (192.168.x.x)
	// while the server sees the public one. The filter then names a host that
	// never appears on the wire there, so it excludes nothing and the capture
	// feeds on its own SSH traffic.
	if client != nil {
		if peer, ok := remoteSSHPeer(client); ok {
			if bpf, ok := sshExclusionFromAddr(peer); ok {
				return bpf
			}
		}
		// No answer from the server (unusual shell, no ss/who): the local
		// address is still right for a direct or WireGuard hop.
		if la := client.LocalAddr(); la != nil {
			if bpf, ok := sshExclusionFromAddr(la.String()); ok {
				return bpf
			}
		}
	}
	return sshExclusionFallback
}

// remoteSSHPeer asks the server which address:port this SSH connection comes
// from - the authoritative answer, since that is what its own tcpdump will
// see on the wire.
//
// $SSH_CONNECTION alone is not enough (it is empty under sudo's env_reset and
// over a userspace WireGuard hop - see e9dd3b8), so this tries it first and
// falls back to reading the socket table for our own sshd process. Both run in
// one command so this costs a single round trip.
func remoteSSHPeer(client *ssh.Client) (string, bool) {
	sess, err := client.NewSession()
	if err != nil {
		return "", false
	}
	defer sess.Close()

	// 1. $SSH_CONNECTION is "client_ip client_port server_ip server_port" -
	//    the direct answer when the variable survives.
	// 2. Failing that, walk up from this shell to its sshd parent and read
	//    that process's peer from the socket table. Keyed on OUR process,
	//    not on port 22, so a server listening on a non-standard port still
	//    resolves - and so a host with many SSH sessions gives back THIS
	//    one rather than whichever matched first.
	// Output is one line: "IP PORT".
	const probe = `
if [ -n "$SSH_CONNECTION" ]; then
  echo "$SSH_CONNECTION" | awk '{print $1, $2}'
else
  p=$$
  while [ "$p" != "1" ] && [ -n "$p" ]; do
    if ss -tnp 2>/dev/null | grep -q "pid=$p,"; then
      ss -tnp 2>/dev/null | awk -v pid="pid=$p," '$0 ~ pid && $1=="ESTAB" {
        n=split($5,a,":"); port=a[n];
        host=substr($5, 1, length($5)-length(port)-1);
        print host, port; exit
      }'
      break
    fi
    p=$(awk '{print $4}' /proc/$p/stat 2>/dev/null)
  done
fi
`
	out, err := sess.Output(probe)
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", false
	}
	host, port := fields[0], fields[1]
	if host == "" || port == "" || !isNumericPort(port) {
		return "", false
	}
	// An IPv6 address has to be bracketed for SplitHostPort in the caller.
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port, true
	}
	return host + ":" + port, true
}

// sshExclusionFallback is the runtime shell form used when we can't derive the
// peer address in Go. Unreliable over WireGuard / under sudo (see caller), but
// better than no exclusion.
// Unescaped, like sshExclusionFromAddr - shellEscapeBPF adds what the
// unquoted tcpdump context needs. This form still has to run through a shell
// (the $(...) substitutions are the point), so it is only usable where the
// clause is NOT quoted.
const sshExclusionFallback = `not ( host $(echo $SSH_CONNECTION | awk '{print $1}') and port $(echo $SSH_CONNECTION | awk '{print $2}') )`

// shellEscapeBPF escapes the shell-significant characters in a BPF clause that
// is passed as BARE (unquoted) words - which is how the tcpdump command line
// is assembled. Only the grouping parens matter: everything else in a BPF
// expression we generate is alphanumeric, dots and spaces.
//
// The tshark path must NOT use this: it puts the clause inside a quoted -f
// argument, where a backslash is a literal character and would corrupt the
// filter. A rejected capture filter means no filter at all, which is how the
// SSH control connection got back into the capture.
func shellEscapeBPF(bpf string) string {
	bpf = strings.ReplaceAll(bpf, "(", `\(`)
	return strings.ReplaceAll(bpf, ")", `\)`)
}

// sshExclusionFromAddr builds the literal `not ( host H and port P )` clause
// from a "host:port" address string, returning ok=false when the address is
// unusable or contains characters that could corrupt the BPF.
func sshExclusionFromAddr(addr string) (string, bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return "", false
	}
	if strings.ContainsAny(host, " ()\\'\"`$") || !isNumericPort(port) {
		return "", false
	}
	// Plain BPF, NOT shell-escaped. Callers decide the escaping, because the
	// two engines put this clause in different places: tcpdump takes it as
	// bare (unquoted) shell words, tshark takes it inside a quoted -f
	// argument. Baking `\(` in here produced a filter that reached tshark
	// with literal backslashes, which it rejects - and a rejected capture
	// filter means NO filter, so the SSH control connection flooded back in.
	return fmt.Sprintf("not ( host %s and port %s )", host, port), true
}

func isNumericPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// shellQuote wraps a single argument in '...' with embedded quotes
// escaped. Good enough for the small set of values we shove through -
// interface names + BPF filters that legitimately need quoting.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexAny(s, " \t\"'$`\\") < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildCaptureCommand composes the capture command line for the configured
// engine. Split out of StartTcpdump so both engines are covered by table tests
// without opening an SSH session.
func buildCaptureCommand(opts TcpdumpOptions, sshExcl string, maxCount int, continuous bool) (string, error) {
	if opts.useTshark() {
		o := opts
		o.MaxCount = maxCount
		if continuous {
			o.MaxCount = -1
		}
		return buildTsharkCommand(o, sshExcl)
	}
	return buildTcpdumpCommand(opts, sshExcl, maxCount, continuous)
}

// buildTcpdumpCommand composes the tcpdump invocation. This is the original
// in-line command construction from StartTcpdump, extracted verbatim.
func buildTcpdumpCommand(opts TcpdumpOptions, sshExcl string, maxCount int, continuous bool) (string, error) {
	if strings.TrimSpace(opts.Iface) == "" {
		return "", fmt.Errorf("interface required")
	}
	// -l line-buffered, -nn no DNS / no port name resolution,
	// -tttt human timestamps. Verbose mode swaps -q for -v -X so
	// tcpdump emits the per-protocol payload decode (BOOTP/DHCP
	// options, DNS answer records, ARP who-has/is-at) AND hex+ASCII
	// dumps that let us pull TLS SNI out of ClientHello packets.
	verboseFlags := "-q"
	if opts.Verbose {
		verboseFlags = "-v -X"
	}
	// Snaplen (-s): cap bytes captured per packet. tcpdump's default is
	// 262144, so on a busy link it ships the FULL payload of every packet
	// over the SSH channel - tens of Mbit/s of stdout that we then throw
	// away (the UI shows headers + a short decode). 160 bytes covers
	// Ethernet+IP+TCP/UDP headers for the brief view; verbose/decode mode
	// needs payload (DHCP options, DNS records, TLS SNI in ClientHello),
	// but those live near the start, so 1024 keeps them while still
	// cutting the wire volume ~256x versus the default.
	snaplen := 160
	if opts.Verbose {
		snaplen = 1024
	}
	cmd := fmt.Sprintf("tcpdump -l -nn -tttt -s %d %s -i %s", snaplen, verboseFlags, shellQuote(opts.Iface))
	if !continuous {
		cmd += fmt.Sprintf(" -c %d", maxCount)
	}
	// Build the BPF: the user filter AND-ed with the SSH-exclusion clause.
	//
	// Capturing tcpdump output over the same SSH session is a feedback loop -
	// every captured packet is streamed back over SSH, generating more SSH
	// packets to capture. We must drop the control connection.
	//
	// We used to assemble this in-shell from $SSH_CONNECTION, but that is
	// unreliable: under sudo (env_reset) or a non-standard login shell it can
	// be empty, and the filter silently degrades to `not ( host and port )`,
	// which lets the SSH traffic flood through (thousands of port-22 packets).
	// Instead we derive the exclusion in Go from the live SSH connection and
	// bake it in as a literal, with the $SSH_CONNECTION form kept only as a
	// runtime fallback when the Go-side addresses aren't available.
	userBPF := opts.BPFFilter
	if opts.ExcludeSSH {
		// The clause arrives as plain BPF; tcpdump takes it as bare shell
		// words, so the parens need escaping here (and only here).
		escaped := shellEscapeBPF(sshExcl)
		if userBPF != "" {
			// Wrap the user filter so precedence is unambiguous.
			cmd += " " + shellQuote(userBPF) + " and " + escaped
		} else {
			cmd += " " + escaped
		}
	} else if userBPF != "" {
		cmd += " " + shellQuote(userBPF)
	}
	return cmd, nil
}
