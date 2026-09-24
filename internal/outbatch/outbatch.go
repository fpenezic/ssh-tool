// Package outbatch coalesces terminal output into fewer, larger chunks
// before it is emitted to the WebView.
//
// Every emitted chunk becomes one Wails event, and delivery costs about a
// millisecond per event whatever its size (measured on Windows/WebView2:
// 7.9 MB of `seq` output left the pump in 366 ms as 1407 events and took
// 1726 ms to arrive). Output only ever comes in two shapes: a lone chunk
// after a quiet spell (a keystroke echo, a prompt), which must go out at
// once, and a burst, where nobody can see anything faster than a frame.
// So the first chunk after a quiet spell is emitted immediately and the
// rest of a burst is held for at most Window and emitted together, or as
// soon as MaxBytes have piled up.
package outbatch

import (
	"sync"
	"time"
)

const (
	// Window is the longest a chunk waits during a burst. Below one
	// 60 Hz frame, so a burst paints no later than it would otherwise.
	Window = 10 * time.Millisecond
	// MaxBytes caps a single emitted chunk so a flood does not build one
	// huge event (and one huge xterm write).
	MaxBytes = 256 * 1024
)

// Coalescer batches chunks passed to Push and hands them to emit in the
// same order. emit is always called with the Coalescer's lock held, so
// it is never called concurrently and never out of order; it must not
// call back into the Coalescer.
type Coalescer struct {
	mu       sync.Mutex
	emit     func([]byte)
	buf      []byte
	lastEmit time.Time
	timer    *time.Timer
	closed   bool
	now      func() time.Time
}

func New(emit func([]byte)) *Coalescer {
	return &Coalescer{emit: emit, now: time.Now}
}

// Push queues data for emission. The caller may reuse data after Push
// returns: it is copied unless it is emitted on the spot.
func (c *Coalescer) Push(data []byte) {
	if len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		c.emit(data)
		return
	}
	now := c.now()
	if len(c.buf) == 0 && now.Sub(c.lastEmit) >= Window {
		// Quiet before this chunk: nothing to wait for.
		c.emit(data)
		c.lastEmit = now
		return
	}
	c.buf = append(c.buf, data...)
	if len(c.buf) >= MaxBytes {
		c.flushLocked()
		return
	}
	if c.timer == nil {
		wait := Window - now.Sub(c.lastEmit)
		if wait < 0 {
			wait = 0
		}
		c.timer = time.AfterFunc(wait, c.Flush)
	}
}

// Flush emits whatever is held.
func (c *Coalescer) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

// Close flushes what is held; later Pushes are emitted immediately.
func (c *Coalescer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
	c.closed = true
}

func (c *Coalescer) flushLocked() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if len(c.buf) == 0 {
		return
	}
	out := c.buf
	// A fresh buffer, not buf[:0]: emit may keep the slice (the scrollback
	// and the event payload both hold on to what they are given).
	c.buf = nil
	c.emit(out)
	c.lastEmit = c.now()
}
