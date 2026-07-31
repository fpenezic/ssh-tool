package ssh

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// LogTailSource selects what the tail streams.
type LogTailKind string

const (
	// LogTailJournal streams `journalctl -f` for a unit (or the whole
	// journal when Unit is empty).
	LogTailJournal LogTailKind = "journal"
	// LogTailFile streams `tail -F` on a path (follows across rotation).
	LogTailFile LogTailKind = "file"
	// LogTailContainer streams `<engine> logs -f` for one container.
	LogTailContainer LogTailKind = "container"
	// LogTailCompose streams `<engine> compose -p <project> logs -f` for a whole
	// compose stack (all services, each line prefixed with the service name).
	LogTailCompose LogTailKind = "compose"
)

// LogTailOptions describes a single tail. Sudo handling mirrors tcpdump: many
// logs (journal, /var/log/*) need root, so the same root/sudo probe drives the
// prefix. The command is composed on the backend from these fields - Unit /
// Path are shell-quoted, never interpolated raw.
type LogTailOptions struct {
	Kind LogTailKind
	Unit string // journal unit, e.g. "nginx" ("" = whole journal)
	Path string // file path for tail -F
	// Lines seeds the view with the last N lines before following (journal
	// -n, tail -n). 0 uses a sensible default.
	Lines int
	// Container-log fields (Kind == container / compose). Engine is the resolved
	// binary ("docker" | "podman"); empty defaults to "docker". Container is the
	// container id/name; Project is the compose project name.
	Engine    string
	Container string
	Project   string
}

// LogTailLine is one streamed log line with its 1-based sequence number (the
// ring watermark a re-attaching window dedupes against).
type LogTailLine struct {
	Text string `json:"text"`
	Seq  int64  `json:"seq"`
}

// LogTailLineHandler is invoked for each streamed line.
type LogTailLineHandler func(line LogTailLine)

// LogTailLifecycleHandler signals state changes: "needs_password" (sudo),
// "started", "reconnecting" (exec session dropped, client still alive),
// "ended" (gave up / client dead) with an optional message.
type LogTailLifecycleHandler func(event string, msg string)

// logTailRingCap bounds the server-side line history kept per tail so a window
// attaching after a detach recovers what it missed. Same idea + sizing as the
// tcpdump ring.
const logTailRingCap = 2000

// LogTailHandle is an active tail. Mirrors TcpdumpHandle: a UUID, a bounded
// server-side ring with a monotonic cum watermark, sudo password channel, and
// a cancel that stops the auto-reconnect loop.
type LogTailHandle struct {
	ID       string
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	awaitPwd chan string // buffered (1)

	ringMu sync.Mutex
	ring   []LogTailLine
	cum    int64

	// Opts records the tail parameters so a re-attaching window can show
	// what's running (unit / path).
	Opts LogTailOptions
}

func (h *LogTailHandle) appendRing(text string) LogTailLine {
	h.ringMu.Lock()
	defer h.ringMu.Unlock()
	h.cum++
	ln := LogTailLine{Text: text, Seq: h.cum}
	h.ring = append(h.ring, ln)
	if len(h.ring) > logTailRingCap {
		h.ring = h.ring[len(h.ring)-logTailRingCap:]
	}
	return ln
}

// Snapshot returns a copy of the retained line history and the current
// cumulative count. Safe from any goroutine.
func (h *LogTailHandle) Snapshot() ([]LogTailLine, int64) {
	h.ringMu.Lock()
	defer h.ringMu.Unlock()
	out := make([]LogTailLine, len(h.ring))
	copy(out, h.ring)
	return out, h.cum
}

// ProvidePassword feeds a sudo password to a tail waiting on one. Non-blocking.
func (h *LogTailHandle) ProvidePassword(pass string) {
	select {
	case h.awaitPwd <- pass:
	default:
	}
}

// Stop cancels the tail (and its reconnect loop) idempotently.
func (h *LogTailHandle) Stop() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
}

// buildLogTailCommand composes the shell command from the options. Unit / Path
// are shell-quoted. Sudo prefix is applied by the caller based on the probe.
func buildLogTailCommand(opts LogTailOptions) (string, error) {
	lines := opts.Lines
	if lines <= 0 {
		lines = 200
	}
	switch opts.Kind {
	case LogTailJournal:
		// -f follow, -n seed, --no-pager, -o short-iso for compact stamps.
		cmd := fmt.Sprintf("journalctl -f -n %d --no-pager -o short-iso", lines)
		if strings.TrimSpace(opts.Unit) != "" {
			cmd += " -u " + shellQuote(opts.Unit)
		}
		return cmd, nil
	case LogTailFile:
		if strings.TrimSpace(opts.Path) == "" {
			return "", fmt.Errorf("file path required")
		}
		// -F follows across truncation/rotation; -n seeds the tail.
		return fmt.Sprintf("tail -n %d -F %s", lines, shellQuote(opts.Path)), nil
	case LogTailContainer:
		if strings.TrimSpace(opts.Container) == "" {
			return "", fmt.Errorf("container required")
		}
		engine := containerEngine(opts.Engine)
		// -f follow, --tail seed. Container name/id is shell-quoted.
		return fmt.Sprintf("%s logs -f --tail %d %s", engine, lines, shellQuote(opts.Container)), nil
	case LogTailCompose:
		if strings.TrimSpace(opts.Project) == "" {
			return "", fmt.Errorf("compose project required")
		}
		engine := containerEngine(opts.Engine)
		// `<engine> compose -p <project> logs -f` fans in every service; each
		// line comes prefixed with the service name.
		return fmt.Sprintf("%s compose -p %s logs -f --tail %d", engine, shellQuote(opts.Project), lines), nil
	default:
		return "", fmt.Errorf("unknown log tail kind %q", opts.Kind)
	}
}

// containerEngine returns the engine binary to invoke, defaulting to docker and
// rejecting anything unexpected (defence against an injected engine string).
func containerEngine(engine string) string {
	switch strings.TrimSpace(engine) {
	case "podman":
		return "podman"
	default:
		return "docker"
	}
}

// StartLogTail launches a follow stream over the SSH client and keeps it alive
// across exec-session drops (as long as the client itself is up) by
// re-running the command. Lines flow to onLine; state to onLifecycle. Returns
// immediately with a handle; the stream runs on a background goroutine.
func StartLogTail(
	client *ssh.Client,
	rootUser, sudoNoPwd bool,
	opts LogTailOptions,
	onLine LogTailLineHandler,
	onLifecycle LogTailLifecycleHandler,
) (*LogTailHandle, error) {
	baseCmd, err := buildLogTailCommand(opts)
	if err != nil {
		return nil, err
	}
	switch {
	case rootUser:
		// direct
	case sudoNoPwd:
		baseCmd = "sudo -n " + baseCmd
	default:
		baseCmd = "sudo -S -p '' " + baseCmd
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &LogTailHandle{
		ID:       uuid.New().String(),
		cancel:   cancel,
		awaitPwd: make(chan string, 1),
		Opts:     opts,
	}

	needsPwd := !rootUser && !sudoNoPwd

	go func() {
		attempt := 0
		for {
			if ctx.Err() != nil {
				return
			}
			runErr := h.runOnce(ctx, client, baseCmd, needsPwd && attempt == 0, onLine, onLifecycle)
			if ctx.Err() != nil {
				return
			}
			// The exec session ended. If the SSH client is still usable,
			// reconnect and resume; otherwise give up.
			if !clientAlive(client) {
				onLifecycle("ended", "connection lost")
				return
			}
			attempt++
			onLifecycle("reconnecting", fmt.Sprintf("log stream dropped%s, reconnecting", tailErrSuffix(runErr)))
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()

	return h, nil
}

// runOnce opens one exec session, streams until it ends or ctx cancels, and
// returns the terminating error (nil on a clean EOF). promptPwd is true only
// on the first attempt when sudo needs a password.
func (h *LogTailHandle) runOnce(
	ctx context.Context,
	client *ssh.Client,
	cmd string,
	promptPwd bool,
	onLine LogTailLineHandler,
	onLifecycle LogTailLifecycleHandler,
) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	// journalctl/tail write nothing useful to stderr except sudo prompts and
	// the occasional warning; fold it into the stream so the user sees it.
	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}

	if err := sess.Start(cmd); err != nil {
		return err
	}

	if promptPwd {
		onLifecycle("needs_password", "sudo requires a password")
		go func() {
			select {
			case pass := <-h.awaitPwd:
				_, _ = stdin.Write([]byte(pass + "\n"))
			case <-ctx.Done():
			}
		}()
	} else {
		onLifecycle("started", "")
	}

	// Cancel-ctx closes the session out from under the scanners so a Stop()
	// unblocks the goroutine promptly.
	go func() {
		<-ctx.Done()
		_ = sess.Close()
	}()

	var wg sync.WaitGroup
	scan := func(r interface{ Read([]byte) (int, error) }) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			ln := h.appendRing(sc.Text())
			onLine(ln)
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()

	return sess.Wait()
}

// clientAlive reports whether the SSH client can still open a session. A cheap
// keepalive-style probe: opening + closing a session round-trips the transport.
func clientAlive(client *ssh.Client) bool {
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	_ = sess.Close()
	return true
}

func tailErrSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
