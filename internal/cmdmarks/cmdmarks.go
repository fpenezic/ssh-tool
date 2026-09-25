// Package cmdmarks records when the user ran a command in a terminal
// session, as a byte position in the session's output stream plus a time.
//
// The terminal's own markers live in xterm and die whenever its buffer is
// rebuilt from the backend ring (a background tab releasing its scrollback,
// detach, redock, reload). Positions in the output stream are what that
// replay is made of, so a mark kept here can be put back on the same line.
package cmdmarks

import "sync"

// Mark is one command: Cum is the session's output byte count when Enter
// was sent (the command line is the line the cursor is on at that point),
// At is Unix milliseconds.
type Mark struct {
	Cum uint64 `json:"cum"`
	At  int64  `json:"at"`
}

// maxMarks bounds the log for a session that runs forever. The output ring
// is 1 MB, so this is far more commands than can still be on screen.
const maxMarks = 10000

type Log struct {
	mu    sync.Mutex
	marks []Mark
}

func (l *Log) Add(cum uint64, at int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.marks = append(l.marks, Mark{Cum: cum, At: at})
	if len(l.marks) > maxMarks {
		l.marks = append([]Mark(nil), l.marks[len(l.marks)-maxMarks:]...)
	}
}

// Between returns the marks with from <= Cum <= to, oldest first, and
// forgets the ones before from: output that old has left the ring and can
// never be replayed again.
func (l *Log) Between(from, to uint64) []Mark {
	l.mu.Lock()
	defer l.mu.Unlock()
	drop := 0
	for drop < len(l.marks) && l.marks[drop].Cum < from {
		drop++
	}
	if drop > 0 {
		l.marks = append([]Mark(nil), l.marks[drop:]...)
	}
	var out []Mark
	for _, m := range l.marks {
		if m.Cum > to {
			break
		}
		out = append(out, m)
	}
	return out
}
