package outbatch

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (r *recorder) emit(b []byte) {
	r.mu.Lock()
	r.chunks = append(r.chunks, append([]byte(nil), b...))
	r.mu.Unlock()
}

func (r *recorder) snapshot() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.chunks...)
}

// A keystroke echo after a quiet spell must not wait for the window.
func TestLoneChunkIsEmittedAtOnce(t *testing.T) {
	r := &recorder{}
	c := New(r.emit)
	c.Push([]byte("a"))
	if got := r.snapshot(); len(got) != 1 || string(got[0]) != "a" {
		t.Fatalf("emitted %q, want one chunk \"a\" before Push returned", got)
	}
}

// The rest of a burst is held and goes out as one chunk, in order.
func TestBurstIsCoalescedInOrder(t *testing.T) {
	r := &recorder{}
	c := New(r.emit)
	var want bytes.Buffer
	for i := 0; i < 200; i++ {
		p := []byte{byte('a' + i%26)}
		want.Write(p)
		c.Push(p)
	}
	time.Sleep(3 * Window)
	got := r.snapshot()
	if len(got) > 3 {
		t.Fatalf("200 pushes in one burst became %d chunks, want the first alone and the rest together", len(got))
	}
	if joined := bytes.Join(got, nil); !bytes.Equal(joined, want.Bytes()) {
		t.Fatalf("output reordered or lost: got %q", joined)
	}
}

func TestMaxBytesFlushesWithoutWaiting(t *testing.T) {
	r := &recorder{}
	c := New(r.emit)
	c.Push([]byte("x"))            // emitted at once
	c.Push(make([]byte, MaxBytes)) // over the cap: flushed now
	if got := r.snapshot(); len(got) != 2 || len(got[1]) != MaxBytes {
		t.Fatalf("got %d chunks, want the cap-sized one flushed inside Push", len(got))
	}
}

func TestCloseFlushesAndLaterPushesPassThrough(t *testing.T) {
	r := &recorder{}
	c := New(r.emit)
	c.Push([]byte("1"))
	c.Push([]byte("2")) // held
	c.Close()
	c.Push([]byte("3"))
	got := r.snapshot()
	if joined := string(bytes.Join(got, nil)); joined != "123" || len(got) != 3 {
		t.Fatalf("got %q in %d chunks, want \"123\" in 3", joined, len(got))
	}
}

// Pushes from several goroutines (the pump plus the flush timer) never
// reorder or drop bytes.
func TestConcurrentPushKeepsEveryByte(t *testing.T) {
	r := &recorder{}
	c := New(r.emit)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				c.Push([]byte("0123456789"))
			}
		}()
	}
	wg.Wait()
	c.Close()
	total := 0
	for _, b := range r.snapshot() {
		total += len(b)
	}
	if total != 4*1000*10 {
		t.Fatalf("emitted %d bytes, want %d", total, 4*1000*10)
	}
}
