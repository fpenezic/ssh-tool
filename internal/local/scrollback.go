package local

import (
	"bytes"
	"sync"
)

const scrollbackCap = 1024 * 1024 // 1 MB - matches the SSH side
const scrollbackSlack = 128 * 1024

// scrollbackBuf mirrors the SSH-side buffer so newly mounted
// terminals (UI reload, detach-redock) can replay history through
// the same snapshot-then-subscribe protocol the frontend uses.
type scrollbackBuf struct {
	mu           sync.Mutex
	buf          []byte
	totalEmitted uint64
}

// append writes the chunk and returns the new cumulative byte count
// under the same lock, so the caller emits the pty_output event with
// a consistent watermark.
func (b *scrollbackBuf) append(data []byte) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, data...)
	// Batched like the SSH side so the trim stays off the per-write path.
	if len(b.buf) > scrollbackCap+scrollbackSlack {
		b.buf = trimToLineStart(b.buf[len(b.buf)-scrollbackCap:])
	}
	b.totalEmitted += uint64(len(data))
	return b.totalEmitted
}

// trimToLineStart mirrors the SSH side: drop a leading partial line so the
// replayed snapshot starts at a line boundary instead of mid-word. Bounded
// so a TUI that fills the ring without a newline is not wiped out.
// Copies rather than reslices - see the SSH-side note: a reslice keeps the
// old array alive and makes the append path quadratic.
func trimToLineStart(buf []byte) []byte {
	const maxScan = 4 * 1024
	limit := min(len(buf), maxScan)
	start := 0
	if i := bytes.IndexByte(buf[:limit], '\n'); i >= 0 {
		start = i + 1
	}
	trimmed := len(buf) - start
	// Headroom matters as much as the copy does. A right-sized slice leaves
	// cap == len, so EVERY subsequent append reallocates and copies the whole
	// megabyte: measured at ~1ms per write against ~0.5us before the cap was
	// reached, which is enough to stall a busy session. Give the new buffer
	// room to grow back to the cap.
	out := make([]byte, trimmed, scrollbackCap+scrollbackCap/8)
	copy(out, buf[start:])
	return out
}

func (b *scrollbackBuf) snapshot() ([]byte, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out, b.totalEmitted
}
