// Ring-buffered log writer with event emit. Tees everything log.Printf
// produces both to stdout (as before) and to an in-memory ring so the
// frontend can render an in-app log viewer. Each appended line is also
// emitted as an "app_log" event for live tail.

package main

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

const logBufCap = 2000

type logBuffer struct {
	mu      sync.Mutex
	lines   []string
	cap     int
	stdout  io.Writer
	enabled atomic.Bool // false → bypass ring + event emit; stdout still gets data
}

func newLogBuffer(stdout io.Writer) *logBuffer {
	b := &logBuffer{lines: make([]string, 0, logBufCap), cap: logBufCap, stdout: stdout}
	b.enabled.Store(true)
	return b
}

// SetEnabled toggles the live-tail / ring-buffer collection. When off,
// Write is a passthrough to stdout - no ring growth, no events.
func (b *logBuffer) SetEnabled(on bool) { b.enabled.Store(on) }
func (b *logBuffer) Enabled() bool      { return b.enabled.Load() }

// Write implements io.Writer so we can plug it into log.SetOutput. The
// data we get is whatever log.Printf wrote, terminated by '\n'. We
// split on newlines so each logical log line lands as its own entry -
// some log lines (esp. multi-line errors) come in one Write.
func (b *logBuffer) Write(p []byte) (int, error) {
	// Forward to stdout untouched so dev-mode tail and CLI behaviour
	// don't change.
	n, err := b.stdout.Write(p)
	if !b.enabled.Load() {
		return n, err
	}
	// Strip the trailing newline log.Printf appends, then split.
	s := strings.TrimRight(string(p), "\n")
	if s == "" {
		return n, err
	}
	b.mu.Lock()
	for _, line := range strings.Split(s, "\n") {
		b.lines = append(b.lines, line)
		if len(b.lines) > b.cap {
			// drop oldest in chunks of 64 to keep the slice churn down
			drop := len(b.lines) - b.cap
			b.lines = b.lines[drop:]
		}
		// Emit live event so the log viewer can append without polling.
		// Safe even when rt is nil (initRuntime hasn't been called yet
		// during very early startup).
		EventsEmit("app_log", line)
	}
	b.mu.Unlock()
	return n, err
}

// Snapshot returns a copy of the current buffer (oldest first).
func (b *logBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Clear drops every line. The frontend has a clear button.
func (b *logBuffer) Clear() {
	b.mu.Lock()
	b.lines = b.lines[:0]
	b.mu.Unlock()
}
