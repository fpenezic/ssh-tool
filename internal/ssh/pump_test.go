package ssh

import (
	"bytes"
	"sync"
	"testing"
)

// orderSink records (cum, len) of every emit in arrival order so a test can
// assert the cum sequence is monotonically increasing - i.e. chunks reach
// the frontend in the same order their cum was assigned.
type orderSink struct {
	mu   sync.Mutex
	cums []uint64
}

func (s *orderSink) EmitOutput(_ string, _ []byte, cum uint64) {
	s.mu.Lock()
	s.cums = append(s.cums, cum)
	s.mu.Unlock()
}
func (s *orderSink) EmitState(string, SessionState) {}
func (s *orderSink) EmitExitStatus(string, uint32)   {}
func (s *orderSink) EmitDebug(string, string)        {}

// TestAppendAndEmit_OrderedUnderConcurrency is the regression test for the
// "prompt rendered mid-listing" garble on a big `ll`: two pump goroutines
// (stdout + stderr) both call appendAndEmit. If cum assignment and the emit
// aren't atomic, a chunk with a higher cum can be emitted before a lower one;
// the frontend concatenates live chunks in arrival order, so out-of-order
// emits land content on the wrong rows. Emitting inside the scrollback lock
// guarantees emit order == cum order regardless of goroutine scheduling.
func TestAppendAndEmit_OrderedUnderConcurrency(t *testing.T) {
	const goroutines = 4
	const perG = 500
	var b scrollbackBuf
	sink := &orderSink{}
	chunk := []byte("xxxxxxxxxx") // 10 bytes

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				b.appendAndEmit(chunk, sink, "sid")
			}
		}()
	}
	wg.Wait()

	if got := len(sink.cums); got != goroutines*perG {
		t.Fatalf("emit count = %d, want %d", got, goroutines*perG)
	}
	// Because the emit happens under the same lock that bumps totalEmitted,
	// the recorded cum sequence must be strictly increasing.
	for i := 1; i < len(sink.cums); i++ {
		if sink.cums[i] <= sink.cums[i-1] {
			t.Fatalf("cum out of order at %d: %d after %d (emit order != cum order)",
				i, sink.cums[i], sink.cums[i-1])
		}
	}
	// And the final cum must equal total bytes appended.
	want := uint64(goroutines * perG * len(chunk))
	if last := sink.cums[len(sink.cums)-1]; last != want {
		t.Fatalf("final cum = %d, want %d", last, want)
	}
}

// TestSplitAtSafeBoundary covers the PTY-chunk boundary fix: a fixed-size
// read must not emit a chunk that ends mid-escape-sequence or mid-UTF-8
// rune, or xterm renders garbage / desyncs the cursor when the halves
// arrive as separate events (observed on `ls -l /var/log` where an 8 KiB
// read split "ESC[01;31m" in two).
func TestSplitAtSafeBoundary(t *testing.T) {
	esc := byte(0x1b)
	cases := []struct {
		name     string
		in       []byte
		wantEmit []byte
		wantHold []byte
	}{
		{
			name:     "plain ascii - nothing held",
			in:       []byte("total 108484\r\ndrwxr-xr-x root\r\n"),
			wantEmit: []byte("total 108484\r\ndrwxr-xr-x root\r\n"),
			wantHold: nil,
		},
		{
			name:     "complete CSI at end - nothing held",
			in:       append([]byte("file"), esc, '[', '0', 'm'),
			wantEmit: append([]byte("file"), esc, '[', '0', 'm'),
			wantHold: nil,
		},
		{
			// The real bug: chunk ends "...ESC[0" with the final 'm'
			// not yet arrived. Hold the ESC..[..0 tail.
			name:     "torn CSI - hold the incomplete tail",
			in:       append([]byte("alternatives.log.6.gz"), esc, '[', '0'),
			wantEmit: []byte("alternatives.log.6.gz"),
			wantHold: []byte{esc, '[', '0'},
		},
		{
			name:     "lone trailing ESC - held",
			in:       append([]byte("data"), esc),
			wantEmit: []byte("data"),
			wantHold: []byte{esc},
		},
		{
			// ESC[01;31m split right after the semicolon (from the cast:
			// "...2025 ESC[01;" | "31malternati...").
			name:     "torn SGR color - hold from ESC",
			in:       append([]byte(" 2025 "), esc, '[', '0', '1', ';'),
			wantEmit: []byte(" 2025 "),
			wantHold: []byte{esc, '[', '0', '1', ';'},
		},
		{
			name:     "partial UTF-8 rune at end - held",
			in:       []byte{'a', 'b', 0xe2, 0x82}, // euro sign U+20AC is e2 82 ac, ac missing
			wantEmit: []byte{'a', 'b'},
			wantHold: []byte{0xe2, 0x82},
		},
		{
			name:     "complete UTF-8 rune at end - nothing held",
			in:       []byte{'a', 0xe2, 0x82, 0xac},
			wantEmit: []byte{'a', 0xe2, 0x82, 0xac},
			wantHold: nil,
		},
		{
			name:     "complete OSC (title) terminated by BEL - nothing held",
			in:       append(append([]byte{esc, ']', '0', ';'}, []byte("root@host")...), 0x07),
			wantEmit: append(append([]byte{esc, ']', '0', ';'}, []byte("root@host")...), 0x07),
			wantHold: nil,
		},
		{
			name:     "empty input",
			in:       nil,
			wantEmit: nil,
			wantHold: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			emit, hold := splitAtSafeBoundary(c.in)
			if !bytes.Equal(emit, c.wantEmit) {
				t.Errorf("emit = %q, want %q", emit, c.wantEmit)
			}
			if !bytes.Equal(hold, c.wantHold) {
				t.Errorf("hold = %q, want %q", hold, c.wantHold)
			}
			// Invariant: emit + hold must equal the input exactly (no
			// byte lost or duplicated).
			if c.in != nil {
				rejoined := append(append([]byte{}, emit...), hold...)
				if !bytes.Equal(rejoined, c.in) {
					t.Errorf("emit+hold = %q, want round-trip %q", rejoined, c.in)
				}
			}
		})
	}
}

// TestSplitAtSafeBoundary_NeverStalls verifies an over-long unterminated
// sequence is eventually emitted rather than held forever (a malformed or
// adversarial stream must not stall output).
func TestSplitAtSafeBoundary_NeverStalls(t *testing.T) {
	esc := byte(0x1b)
	// A CSI that never terminates, longer than the hold bound.
	long := append([]byte{esc, '['}, bytes.Repeat([]byte("0;"), 200)...)
	emit, hold := splitAtSafeBoundary(long)
	if len(hold) != 0 {
		t.Errorf("over-long CSI should not be held, got hold len %d", len(hold))
	}
	if !bytes.Equal(emit, long) {
		t.Errorf("over-long CSI should be emitted whole")
	}
}
