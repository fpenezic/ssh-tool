package local

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// overlapPty is a fake pty.Pty whose Write asserts it is never called
// concurrently with itself - the exact property Session.writeMu must give.
// Only Write is exercised; the rest satisfy the interface.
type overlapPty struct {
	inWrite atomic.Int32
	overlap atomic.Bool
}

func (p *overlapPty) Write(b []byte) (int, error) {
	if p.inWrite.Add(1) != 1 {
		p.overlap.Store(true)
	}
	// Hold the "critical section" open long enough that an unserialised
	// second writer would land inside it.
	time.Sleep(time.Microsecond)
	p.inWrite.Add(-1)
	return len(b), nil
}

func (p *overlapPty) Read(b []byte) (int, error)              { return 0, nil }
func (p *overlapPty) Close() error                            { return nil }
func (p *overlapPty) Name() string                            { return "fake" }
func (p *overlapPty) Command(string, ...string) *pty.Cmd      { return nil }
func (p *overlapPty) CommandContext(context.Context, string, ...string) *pty.Cmd {
	return nil
}
func (p *overlapPty) Resize(int, int) error { return nil }
func (p *overlapPty) Fd() uintptr           { return 0 }

// TestWriteSerialised proves Session.Write serialises concurrent writers, so a
// full-control share guest writing at the same time as the host can't tear a
// payload. Run with -race; without the mutex both the overlap flag and the
// race detector fire.
func TestWriteSerialised(t *testing.T) {
	fake := &overlapPty{}
	s := &Session{ID: "t", pty: fake}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if err := s.Write([]byte("hello")); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if fake.overlap.Load() {
		t.Fatal("concurrent Session.Write calls overlapped inside pty.Write")
	}
}

// TestSetOutputSinkRaceFree proves the sink can be swapped while pumpOutput's
// read path runs, with no data race. Mirrors the exact access pattern
// pumpOutput uses (read s.outputSink under s.mu) against SetOutputSink's write.
func TestSetOutputSinkRaceFree(t *testing.T) {
	s := &Session{ID: "t"}
	stop := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// The same guarded read pumpOutput now performs.
			s.mu.Lock()
			sink := s.outputSink
			s.mu.Unlock()
			if sink != nil {
				sink([]byte("x"), 1)
			}
		}
	}()

	for i := 0; i < 1000; i++ {
		s.SetOutputSink(func([]byte, uint64) {})
	}
	close(stop)
	wg.Wait()
}
