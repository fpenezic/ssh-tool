package initcmd

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \n\t\n", nil},
		{"single line", "tmux new -A -s main", []string{"tmux new -A -s main"}},
		{"trailing newline does not add a stray Enter", "cd /var/www\n", []string{"cd /var/www"}},
		{"blank line between commands is dropped", "sudo su - deploy\n\nwhoami", []string{"sudo su - deploy", "whoami"}},
		{"windows clipboard crlf", "one\r\ntwo", []string{"one", "two"}},
		{"indentation inside a line is kept", "cd /srv\n    ls -la", []string{"cd /srv", "    ls -la"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Lines(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Lines(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// A zero delay must not introduce a timer at all: the no-delay path is what
// every existing connection uses and it has to stay exactly as fast as before.
func TestSendWithoutDelayIsImmediate(t *testing.T) {
	var got []string
	start := time.Now()
	Send(context.Background(), "one\ntwo\nthree", 0, "\n", func(b []byte) error {
		got = append(got, string(b))
		return nil
	})
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("zero delay still waited %s", elapsed)
	}
	want := []string{"one\n", "two\n", "three\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// The pause goes BETWEEN lines, not before the first one - the first line must
// reach the shell as promptly as it always did.
func TestSendDelaysBetweenLinesOnly(t *testing.T) {
	var at []time.Duration
	start := time.Now()
	Send(context.Background(), "a\nb\nc", 40*time.Millisecond, "\r", func(b []byte) error {
		at = append(at, time.Since(start))
		return nil
	})
	if len(at) != 3 {
		t.Fatalf("wrote %d lines, want 3", len(at))
	}
	if at[0] > 30*time.Millisecond {
		t.Fatalf("first line waited %s; the delay should only apply between lines", at[0])
	}
	if at[1] < 35*time.Millisecond {
		t.Fatalf("second line came after %s, want at least one 40ms gap", at[1])
	}
	if at[2] < 75*time.Millisecond {
		t.Fatalf("third line came after %s, want two 40ms gaps", at[2])
	}
}

// The terminator is transport-specific and must be passed through untouched:
// ConPTY only acts on CR, so an LF here would leave the command rendered but
// not submitted.
func TestSendUsesTheGivenTerminator(t *testing.T) {
	var got []string
	Send(context.Background(), "x\ny", 0, "\r", func(b []byte) error {
		got = append(got, string(b))
		return nil
	})
	want := []string{"x\r", "y\r"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// A session that dies mid-sequence must not keep typing into a dead PTY.
func TestSendStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n int
	Send(ctx, "a\nb\nc\nd", 20*time.Millisecond, "\n", func(b []byte) error {
		n++
		if n == 2 {
			cancel()
		}
		return nil
	})
	if n > 2 {
		t.Fatalf("wrote %d lines after cancel, want to stop at 2", n)
	}
}

// A write failure means the shell is gone; the rest of the sequence is
// pointless and could block.
func TestSendStopsOnWriteError(t *testing.T) {
	var n int
	Send(context.Background(), "a\nb\nc", 0, "\n", func(b []byte) error {
		n++
		return errors.New("pty closed")
	})
	if n != 1 {
		t.Fatalf("kept writing after an error: %d lines", n)
	}
}

// An already-cancelled context must not write anything at all.
func TestSendRefusesACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var n int
	Send(ctx, "a\nb", 20*time.Millisecond, "\n", func(b []byte) error {
		n++
		return nil
	})
	if n > 1 {
		t.Fatalf("wrote %d lines with a cancelled context", n)
	}
}
