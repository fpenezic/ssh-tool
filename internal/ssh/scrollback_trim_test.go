package ssh

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Cutting the ring at an exact byte offset left the first replayed line
// chopped mid-word - "om systemd[1114806]: Stopped ..." where the line
// actually began "Aug 23 11:08:41 auth...". That reads as corruption rather
// than as history scrolling off the top.
func TestTrimToLineStart(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "drops a leading partial line",
			in:   "om systemd[1]: Stopped\nAug 23 full line\nanother\n",
			want: "Aug 23 full line\nanother\n",
		},
		{
			name: "already at a boundary keeps everything after it",
			in:   "\nfirst\nsecond\n",
			want: "first\nsecond\n",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "single complete line with no leading fragment is left as is",
			in:   "only line without newline",
			want: "only line without newline",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(trimToLineStart([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("trimToLineStart(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A full-screen TUI (vim, htop) can fill the entire ring using carriage
// returns and cursor moves without ever emitting a newline. Scanning the
// whole buffer for one would throw away everything we have, so the search is
// bounded: past that point a chopped first line beats an empty scrollback.
func TestTrimToLineStartGivesUpOnALongUnbrokenRun(t *testing.T) {
	noNewlines := bytes.Repeat([]byte("x"), 8*1024)
	got := trimToLineStart(noNewlines)
	if len(got) != len(noNewlines) {
		t.Fatalf("dropped %d bytes of a newline-free buffer; it should be left intact",
			len(noNewlines)-len(got))
	}

	// A newline just past the scan window is also out of reach, by design.
	late := append(bytes.Repeat([]byte("y"), 5*1024), '\n')
	late = append(late, []byte("after")...)
	if got := trimToLineStart(late); len(got) != len(late) {
		t.Fatalf("trimmed using a newline beyond the scan limit")
	}

	// One inside the window is found.
	early := append(bytes.Repeat([]byte("z"), 100), '\n')
	early = append(early, []byte("after")...)
	if got := string(trimToLineStart(early)); got != "after" {
		t.Fatalf("got %q, want %q", got, "after")
	}
}

// The trim must only ever run on an over-cap buffer, and must leave the
// caller with something that still starts a line.
func TestAppendTrimsAtALineBoundary(t *testing.T) {
	var b scrollbackBuf
	sink := &countingSink{}

	// Fill past the cap with uniform, newline-terminated lines.
	line := strings.Repeat("a", 99) + "\n"
	for len(b.buf) <= scrollbackCap {
		b.appendAndEmit([]byte(line), sink, "s1")
	}

	// The trim is batched, so the buffer is allowed to sit up to slack over
	// the cap between trims.
	if len(b.buf) > scrollbackCap+scrollbackSlack {
		t.Fatalf("buffer %d exceeds cap+slack %d", len(b.buf), scrollbackCap+scrollbackSlack)
	}
	snap, _ := b.snapshot()
	if len(snap) == 0 {
		t.Fatal("snapshot is empty after trimming")
	}
	// Every line here is the same length, so a boundary-aligned buffer
	// divides evenly and its first line is a whole one.
	if first, _, ok := bytes.Cut(snap, []byte("\n")); !ok {
		t.Fatal("no newline in the snapshot")
	} else if len(first) != 99 {
		t.Fatalf("first line is %d bytes, want a whole 99-byte line (partial line survived the trim)", len(first))
	}
}

// totalEmitted counts bytes ever written and must not be affected by
// trimming - the frontend uses it as a dedupe watermark, so losing count
// would replay or drop live output.
func TestTrimDoesNotDisturbTheWatermark(t *testing.T) {
	var b scrollbackBuf
	sink := &countingSink{}

	line := strings.Repeat("b", 199) + "\n"
	var written uint64
	for len(b.buf) <= scrollbackCap {
		b.appendAndEmit([]byte(line), sink, "s1")
		written += uint64(len(line))
	}

	_, cum := b.snapshot()
	if cum != written {
		t.Fatalf("totalEmitted = %d, want %d (every byte ever written)", cum, written)
	}
}

type countingSink struct{ n int }

func (c *countingSink) EmitOutput(string, []byte, uint64) { c.n++ }
func (c *countingSink) EmitState(string, SessionState)    {}
func (c *countingSink) EmitDebug(string, string)          {}
func (c *countingSink) EmitExitStatus(string, uint32)     {}

// The trim must stay off the per-write path. The first version cut back to
// exactly the cap, so the next append was over it again and every single
// chunk paid a full 1 MB copy: 14000 writes took 2.15s instead of 8ms. A
// busy session would have stalled visibly.
func TestAppendStaysFastPastTheCap(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	var b scrollbackBuf
	sink := &countingSink{}
	line := strings.Repeat("a", 99) + "\n"

	// Enough writes to cross the cap and keep going well past it.
	writes := (scrollbackCap/len(line))*2 + 1000
	start := time.Now()
	for i := 0; i < writes; i++ {
		b.appendAndEmit([]byte(line), sink, "s1")
	}
	elapsed := time.Since(start)

	// Generous bound - the regression was ~250x over budget, so this catches
	// it without being flaky on a loaded machine.
	if elapsed > 2*time.Second {
		t.Fatalf("%d writes took %s; the trim is back on the per-write path", writes, elapsed)
	}
}
