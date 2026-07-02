package local

import "sync"

const scrollbackCap = 256 * 1024 // 256 KB - matches the SSH side

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
	if len(b.buf) > scrollbackCap {
		b.buf = b.buf[len(b.buf)-scrollbackCap:]
	}
	b.totalEmitted += uint64(len(data))
	return b.totalEmitted
}

func (b *scrollbackBuf) snapshot() ([]byte, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out, b.totalEmitted
}
