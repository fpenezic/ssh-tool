// Package local manages in-app local-shell PTY sessions. Mirrors the
// shape of internal/ssh (Pool + Session + Scrollback) so the IPC layer
// and the frontend xterm consumer can treat both the same. No SSH
// machinery; spawns a subprocess (bash / zsh / powershell / wsl …)
// through github.com/aymanbagabas/go-pty which abstracts over the
// platform PTY (creack/pty on Unix, ConPTY on Windows).
package local

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	pty "github.com/aymanbagabas/go-pty"
)

// SessionID matches the shape used by the ssh package - opaque
// string assigned by the IPC layer.
type SessionID = string

// Session is one running local shell. The lifecycle is owned by the
// pool: caller creates via Spawn, the read goroutine pumps output
// into the OutputSink set on the session, and a single Close() (or
// the child exiting) tears everything down.
type Session struct {
	ID      string
	Kind    string // "bash" | "zsh" | "powershell" | "cmd" | "wsl" | "sh"
	Display string // human-readable label for the tab (e.g. "bash", "Ubuntu (WSL)")

	pty  pty.Pty
	cmd  *pty.Cmd
	done chan struct{}

	mu         sync.Mutex
	writeMu    sync.Mutex // serialises Write; see Write
	closedOnce sync.Once
	onClose    func(sessionID string)

	scrollback scrollbackBuf

	// outputSink is the App-supplied callback that forwards every PTY
	// chunk to the frontend (over the same `pty_output:<id>` event
	// the SSH layer uses). Set immediately after Spawn before the
	// pump goroutine starts.
	outputSink func(data []byte, cum uint64)
}

// SpawnRequest configures a new local session.
type SpawnRequest struct {
	// Kind picks the shell. Recognised values per-platform:
	//   linux:  "bash", "zsh", "sh", "" (auto -> $SHELL / bash / sh)
	//   darwin: "zsh", "bash", "sh", "" (auto -> $SHELL / zsh / bash)
	//   windows:"powershell", "cmd", "wsl", "" (auto -> wsl if present,
	//           else powershell, else cmd)
	Kind string

	// Cols / Rows are the initial PTY dimensions. Defaults 120x32.
	Cols, Rows uint16

	// Dir is the working directory the shell starts in. Empty keeps
	// the process default (the app's own cwd). On Windows this works
	// for the wsl kind too: wsl.exe inherits the Windows cwd and maps
	// it to the /mnt/... equivalent itself.
	Dir string
}

// Spawn creates a new Session and starts the child. The caller is
// expected to set the output sink + on-close callback before invoking
// Start() so no chunk is lost.
func Spawn(req SpawnRequest) (*Session, error) {
	if req.Cols == 0 {
		req.Cols = 120
	}
	if req.Rows == 0 {
		req.Rows = 32
	}

	kind, name, args, display, err := resolveShell(req.Kind)
	if err != nil {
		return nil, err
	}

	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := p.Resize(int(req.Cols), int(req.Rows)); err != nil {
		_ = p.Close()
		return nil, err
	}

	// With cmd.Dir set, a relative executable name is resolved against
	// that directory instead of PATH (documented os/exec semantics that
	// go-pty mirrors) - "wsl.exe" would be looked up as
	// <dir>\wsl.exe and fail. Pin the shell to its absolute path
	// before the Dir assignment below.
	if req.Dir != "" && !filepath.IsAbs(name) {
		if abs, err := exec.LookPath(name); err == nil {
			name = abs
		}
	}

	cmd := p.Command(name, args...)
	if req.Dir != "" {
		if st, err := os.Stat(req.Dir); err == nil && st.IsDir() {
			cmd.Dir = req.Dir
		}
		// A vanished/invalid dir silently falls back to the default
		// cwd - better a shell in the wrong place than no shell.
	}
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)

	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, err
	}

	sess := &Session{
		Kind:    kind,
		Display: display,
		pty:     p,
		cmd:     cmd,
		done:    make(chan struct{}),
	}
	return sess, nil
}

// Start kicks off the output pump goroutine. Call after SetOutputSink
// + SetOnClose so the first byte isn't dropped on the floor.
func (s *Session) Start() {
	go s.pumpOutput()
	go s.waitAndClose()
}

func (s *Session) pumpOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			cum := s.scrollback.append(chunk)
			// Read the sink under the lock: SetOutputSink writes it under
			// s.mu, and an unsynchronised read here is a data race (-race
			// flags it). Benign in practice today - the sink is installed
			// before the pump starts - but a correctness fix, and load-bearing
			// once a second consumer (session sharing) is involved. The lock
			// is dropped before calling the sink so a slow sink can't stall
			// SetOutputSink.
			s.mu.Lock()
			sink := s.outputSink
			s.mu.Unlock()
			if sink != nil {
				sink(chunk, cum)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("local session %s: pty read: %v", s.ID, err)
			}
			return
		}
	}
}

func (s *Session) waitAndClose() {
	_ = s.cmd.Wait()
	s.closeOnce()
}

func (s *Session) closeOnce() {
	s.closedOnce.Do(func() {
		_ = s.pty.Close()
		close(s.done)
		if fn := s.onClose; fn != nil {
			fn(s.ID)
		}
	})
}

// SetOutputSink registers the callback the pump uses for every PTY
// chunk. cum is the cumulative byte counter (same semantics as the
// SSH Session).
func (s *Session) SetOutputSink(fn func(data []byte, cum uint64)) {
	s.mu.Lock()
	s.outputSink = fn
	s.mu.Unlock()
}

// SetOnClose registers a callback fired when the session ends
// (whether the user closed it, the shell exited, or PTY failed).
func (s *Session) SetOnClose(fn func(string)) {
	s.mu.Lock()
	s.onClose = fn
	s.mu.Unlock()
}

// Write forwards keystrokes from the frontend into the shell. Serialised for
// the same reason as the SSH Session.Write: with session sharing a full-control
// guest is a second concurrent writer, and interleaved pty.Write calls would
// tear multi-byte input. Dedicated mutex, not s.mu (which is held across the
// sink call in pumpOutput).
func (s *Session) Write(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.pty.Write(data)
	return err
}

// Resize updates the PTY dimensions in response to xterm fit().
func (s *Session) Resize(cols, rows uint16) error {
	return s.pty.Resize(int(cols), int(rows))
}

// Disconnect closes the PTY and waits for the pump to exit.
func (s *Session) Disconnect() {
	s.closeOnce()
}

// Scrollback snapshots the buffered output + cumulative watermark
// for newly mounted terminals (UI reload, detach-redock).
func (s *Session) Scrollback() ([]byte, uint64) {
	return s.scrollback.snapshot()
}

// Done returns a channel that closes once the session is torn down.
func (s *Session) Done() <-chan struct{} { return s.done }

// resolveShell picks the actual binary + args per platform + kind.
// Returns (canonicalKind, name, args, displayLabel, err).
func resolveShell(kind string) (string, string, []string, string, error) {
	switch runtime.GOOS {
	case "linux":
		switch kind {
		case "", "auto":
			if s := os.Getenv("SHELL"); s != "" {
				return "shell", s, nil, baseName(s), nil
			}
			if _, err := exec.LookPath("bash"); err == nil {
				return "bash", "bash", nil, "bash", nil
			}
			return "sh", "sh", nil, "sh", nil
		case "bash", "zsh", "sh", "fish":
			path, err := exec.LookPath(kind)
			if err != nil {
				return "", "", nil, "", err
			}
			return kind, path, nil, kind, nil
		}
	case "darwin":
		switch kind {
		case "", "auto":
			if s := os.Getenv("SHELL"); s != "" {
				return "shell", s, nil, baseName(s), nil
			}
			if _, err := exec.LookPath("zsh"); err == nil {
				return "zsh", "zsh", nil, "zsh", nil
			}
			return "bash", "bash", nil, "bash", nil
		case "bash", "zsh", "sh", "fish":
			path, err := exec.LookPath(kind)
			if err != nil {
				return "", "", nil, "", err
			}
			return kind, path, nil, kind, nil
		}
	case "windows":
		switch kind {
		case "", "auto":
			if _, err := exec.LookPath("wsl.exe"); err == nil {
				return "wsl", "wsl.exe", nil, "WSL", nil
			}
			if _, err := exec.LookPath("powershell.exe"); err == nil {
				return "powershell", "powershell.exe", nil, "PowerShell", nil
			}
			return "cmd", "cmd.exe", nil, "Command Prompt", nil
		case "powershell":
			return "powershell", "powershell.exe", nil, "PowerShell", nil
		case "cmd":
			return "cmd", "cmd.exe", nil, "Command Prompt", nil
		case "wsl":
			return "wsl", "wsl.exe", nil, "WSL", nil
		}
	}
	return "", "", nil, "", errors.New("unsupported shell kind for this platform")
}

func baseName(p string) string {
	// trim path; keep just the executable name for the tab label
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
