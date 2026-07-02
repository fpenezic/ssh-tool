package ssh

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
//   - else uses `sudo -S -p ''` reading from stdin. needsPassword=true
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
	// The exclusion is assembled in-shell from $SSH_CONNECTION so it uses
	// the real client IP + port of THIS session (any SSH port). Fields:
	// "$1=client_ip $2=client_port $3=server_ip $4=server_port".
	userBPF := opts.BPFFilter
	if opts.ExcludeSSH {
		// e.g. not (host 203.0.113.7 and port 51234)
		sshExcl := `not \( host $(echo $SSH_CONNECTION | awk '{print $1}') and port $(echo $SSH_CONNECTION | awk '{print $2}') \)`
		if userBPF != "" {
			// Wrap the user filter so precedence is unambiguous.
			cmd += " " + shellQuote(userBPF) + " and " + sshExcl
		} else {
			cmd += " " + sshExcl
		}
	} else if userBPF != "" {
		cmd += " " + shellQuote(userBPF)
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
			if strings.Contains(low, "listening on") || strings.Contains(low, "verbose output") {
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
				onLifecycle("error", "tcpdump not installed on the remote host (or not on PATH)")
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
				onLifecycle("error", "tcpdump not installed on the remote host (or not on PATH)")
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
