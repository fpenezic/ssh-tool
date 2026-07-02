// Simple rotating log file. One file per logical "current" log,
// rotated by size. Not concurrent across processes - fine for a
// desktop app where only one instance writes.
//
// On each launch we OPEN-OR-CREATE the same file (append mode) and
// roll it when it crosses a soft cap. Old files are .1, .2, .3 with
// .3 dropped - same pattern as logrotate.

package main

import (
	"io"
	"os"
	"sync"
)

const (
	logSoftCap   = 5 * 1024 * 1024 // 5 MiB
	logKeepCount = 3
)

type rotatingFile struct {
	mu    sync.Mutex
	path  string
	f     *os.File
	bytes int64
}

func openLogFile(path string) io.Writer {
	r := &rotatingFile{path: path}
	if err := r.open(); err != nil {
		// Failed to open - fall back to a no-op writer; stderr still
		// has everything.
		return io.Discard
	}
	return r
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil {
		r.bytes = st.Size()
	}
	r.f = f
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(p), nil
	}
	n, err := r.f.Write(p)
	r.bytes += int64(n)
	if r.bytes >= logSoftCap {
		r.rotateLocked()
	}
	return n, err
}

// rotateLocked closes the current file, shuffles app.log -> app.log.1,
// app.log.1 -> app.log.2, … dropping the oldest. Then opens a fresh
// file. Best-effort: if any step fails we just keep using whatever
// state we ended up in.
func (r *rotatingFile) rotateLocked() {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	// Drop the oldest.
	oldest := r.path + "." + itoa(logKeepCount)
	_ = os.Remove(oldest)
	// Shift the rest up by one.
	for i := logKeepCount - 1; i >= 1; i-- {
		from := r.path + "." + itoa(i)
		to := r.path + "." + itoa(i+1)
		_ = os.Rename(from, to)
	}
	// Move current -> .1.
	_ = os.Rename(r.path, r.path+".1")
	// Reopen.
	r.bytes = 0
	_ = r.open()
}

func itoa(i int) string {
	switch i {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	case 4:
		return "4"
	}
	// Anything past 4 we don't bother formatting nicely - used only
	// for the "drop the oldest" step.
	return "x"
}
