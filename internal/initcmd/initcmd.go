// Package initcmd sends a connection's configured initial command into a
// freshly opened shell.
//
// It exists because the SSH and local-PTY paths need identical behaviour from
// two different packages: the same line splitting, the same blank-line
// skipping, and the same optional pause between lines. Only the line
// terminator differs (see Send).
package initcmd

import (
	"context"
	"strings"
	"time"
)

// Lines splits a possibly multi-line initial command into the lines that
// should actually be submitted. A textarea produces "\n", a Windows clipboard
// produces "\r\n", and a blank line is dropped so a trailing newline in the
// box does not fire a stray Enter.
func Lines(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(cmd, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Send writes each line of cmd through write, terminated by term, pausing
// delay between them.
//
// term differs per transport and is not interchangeable: the local PTY needs
// CR because ConPTY's line reader (PowerShell, cmd) acts on CR only, while the
// SSH path has always used LF and a remote Unix PTY maps CR to NL anyway.
//
// delay is what makes a line that replaces the shell work. "sudo su - user"
// hands the terminal to a new shell, and without a pause the follow-up lines
// are read by the shell on its way out instead of the one that just started.
// A zero delay sends the lines back to back, which is what this did before the
// setting existed - and importantly it also skips the timer entirely, so the
// no-delay path stays synchronous and unchanged.
//
// ctx cancellation stops the remaining lines; a session that dies mid-sequence
// must not keep typing into a closed PTY. A write error does the same.
func Send(ctx context.Context, cmd string, delay time.Duration, term string, write func([]byte) error) {
	lines := Lines(cmd)
	for i, line := range lines {
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
		if err := write([]byte(line + term)); err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}
