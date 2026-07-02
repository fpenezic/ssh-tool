// Package recorder writes terminal session recordings in asciicast v2
// format (https://docs.asciinema.org/manual/asciicast/v2/). One file
// per recording: a JSON header line followed by one JSON array per
// event. Output-only by design - keystrokes are never recorded, so a
// password typed at a sudo prompt can't leak into the file (it doesn't
// echo, and we never see the input side anyway).
package recorder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// header is the asciicast v2 first line.
type header struct {
	Version   int    `json:"version"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Timestamp int64  `json:"timestamp"`
	Title     string `json:"title,omitempty"`
}

// Recording is one live .cast file. Methods are safe for concurrent
// use; the PTY pump and the resize IPC race freely.
type Recording struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	path   string
	start  time.Time
	closed bool
}

func newRecording(path string, cols, rows uint16, title string) (*Recording, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	r := &Recording{
		f:     f,
		w:     bufio.NewWriter(f),
		path:  path,
		start: time.Now(),
	}
	h := header{
		Version:   2,
		Width:     int(cols),
		Height:    int(rows),
		Timestamp: r.start.Unix(),
		Title:     title,
	}
	line, err := json.Marshal(h)
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if _, err := r.w.Write(append(line, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return r, nil
}

// event appends one [elapsed, code, data] line. json.Marshal replaces
// invalid UTF-8 with U+FFFD, which matches how asciinema players treat
// raw PTY bytes - acceptable loss for binary garbage mid-stream.
func (r *Recording) event(code, data string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	elapsed := time.Since(r.start).Seconds()
	line, err := json.Marshal([]any{elapsed, code, data})
	if err != nil {
		return
	}
	r.w.Write(line)
	r.w.WriteByte('\n')
}

func (r *Recording) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.w.Flush()
	if cerr := r.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Manager tracks at most one active recording per session ID.
type Manager struct {
	mu   sync.RWMutex
	recs map[string]*Recording
}

func NewManager() *Manager {
	return &Manager{recs: map[string]*Recording{}}
}

// Start opens a new .cast file for the session. Errors if the session
// is already being recorded. Returns the final path.
func (m *Manager) Start(sessionID, path string, cols, rows uint16, title string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.recs[sessionID]; ok {
		return "", fmt.Errorf("session is already being recorded")
	}
	rec, err := newRecording(path, cols, rows, title)
	if err != nil {
		return "", err
	}
	m.recs[sessionID] = rec
	return rec.path, nil
}

// Stop finalises and removes the session's recording. ok=false when
// the session wasn't being recorded (idempotent - session-close
// cleanup calls this unconditionally).
func (m *Manager) Stop(sessionID string) (path string, ok bool) {
	m.mu.Lock()
	rec, ok := m.recs[sessionID]
	if ok {
		delete(m.recs, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return "", false
	}
	rec.close()
	return rec.path, true
}

// Write appends an output chunk to the session's recording, if any.
// Hot path for every PTY chunk - the RLock fast-exit keeps the cost
// near zero when nothing records.
func (m *Manager) Write(sessionID string, data []byte) {
	m.mu.RLock()
	rec := m.recs[sessionID]
	m.mu.RUnlock()
	if rec == nil {
		return
	}
	rec.event("o", string(data))
}

// Resize appends an asciicast "r" event so players reflow at the same
// point the live terminal did.
func (m *Manager) Resize(sessionID string, cols, rows uint16) {
	m.mu.RLock()
	rec := m.recs[sessionID]
	m.mu.RUnlock()
	if rec == nil {
		return
	}
	rec.event("r", fmt.Sprintf("%dx%d", cols, rows))
}

// Active reports whether the session is being recorded and where the
// file lives.
func (m *Manager) Active(sessionID string) (path string, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.recs[sessionID]
	if !ok {
		return "", false
	}
	return rec.path, true
}

// ActivePaths snapshots every live recording (sessionID -> path).
func (m *Manager) ActivePaths() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.recs))
	for id, rec := range m.recs {
		out[id] = rec.path
	}
	return out
}

// SuggestedFilename builds "<safe-name>-<timestamp>.cast". Name is
// reduced to letters / digits / dash / underscore so it can't escape
// the recordings dir or trip Windows filename rules.
func SuggestedFilename(name string, t time.Time) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	safe = strings.Trim(safe, "-")
	for strings.Contains(safe, "--") {
		safe = strings.ReplaceAll(safe, "--", "-")
	}
	if safe == "" {
		safe = "session"
	}
	return fmt.Sprintf("%s-%s.cast", safe, t.Format("20060102-150405"))
}
