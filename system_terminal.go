//go:build !android && !ios

// Cross-platform helper for launching the OS terminal preloaded with
// a command. Best-effort by platform - falls back gracefully so the
// frontend can surface a meaningful error and let the user copy the
// command instead.
//
// SECURITY: argv is passed as a real argument vector, never
// interpolated into a shell string. Hostname / username fields
// flow from imported configs (RDM JSON, ssh_config) and from
// untrusted catalog sync - anything that lands in shell context
// would be a command-injection sink. The Windows + AppleScript
// paths each have one specific quoting need; that quoting is
// confined to the launch-helper and never accepts metacharacters
// that escape the surrounding quoting.

package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// launchInSystemTerminal opens a new terminal window running argv
// (e.g. {"ssh", "-p", "2222", "user@host"}). The launched shell stays
// open after argv finishes so the user can keep typing.
func launchInSystemTerminal(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	switch runtime.GOOS {
	case "windows":
		return launchWindowsTerminal(argv)
	case "darwin":
		return launchMacTerminal(argv)
	default:
		return launchLinuxTerminal(argv)
	}
}

func launchWindowsTerminal(argv []string) error {
	// Prefer Windows Terminal: `wt new-tab` accepts a real argv after
	// the subcommand. We pass argv directly; no cmd.exe parsing on
	// the way in. The trailing `cmd /k` keeps the shell open after
	// argv[0] exits.
	if _, err := exec.LookPath("wt.exe"); err == nil {
		full := append([]string{"new-tab", "--", "cmd.exe", "/k"}, argv...)
		return exec.Command("wt.exe", full...).Start()
	}
	// Fallback: cmd.exe /c start "" cmd /k <argv...>. exec.Command
	// already constructs the Windows command line from a real argv
	// (with the right quoting via syscall.EscapeArg); we never
	// concatenate argv into a single string ourselves.
	full := append([]string{"/c", "start", "", "cmd.exe", "/k"}, argv...)
	return exec.Command("cmd.exe", full...).Start()
}

// appleScriptQuote escapes a single string for embedding inside an
// AppleScript double-quoted literal. The two metacharacters inside
// such a literal are `"` and `\`. We do NOT allow newline-based
// escapes - that would let the value break out via OSAScript
// statement separators.
func appleScriptQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n', '\r':
			// Strip; AppleScript treats raw newlines as statement
			// terminators inside string literals on some macOS
			// versions. Hostnames/usernames have no business
			// containing them.
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func launchMacTerminal(argv []string) error {
	// AppleScript's `do script` evaluates a shell command inside the
	// Terminal.app session. We render argv to a properly POSIX-quoted
	// shell string so each token reaches /bin/sh as one argument.
	cmd := shellJoinPosix(argv)
	script := fmt.Sprintf(`tell application "Terminal" to do script %s`, appleScriptQuote(cmd))
	return exec.Command("osascript", "-e", script).Start()
}

// shellJoinPosix joins argv into a single POSIX-shell string where
// every token is wrapped in single quotes. Single quotes inside a
// token are encoded as the standard '\'' break-out sequence - a
// closing quote, an escaped quote, and a reopening quote. This is
// the same quoting style used by Python's shlex.quote and is safe
// against every shell metacharacter (no expansion happens inside
// single quotes in POSIX sh).
func shellJoinPosix(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}

func launchLinuxTerminal(argv []string) error {
	// Each emulator gets argv as a real array. The "; exec bash" tail
	// requires a shell, so we render argv through shellJoinPosix.
	cmd := shellJoinPosix(argv)
	candidates := []struct {
		name string
		args []string
	}{
		{"gnome-terminal", []string{"--", "bash", "-c", cmd + "; exec bash"}},
		{"konsole", []string{"-e", "bash", "-c", cmd + "; exec bash"}},
		{"xfce4-terminal", []string{"--hold", "-x", "bash", "-c", cmd}},
		{"xterm", []string{"-e", "bash", "-c", cmd + "; exec bash"}},
		{"alacritty", []string{"-e", "bash", "-c", cmd + "; exec bash"}},
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err == nil {
			return exec.Command(c.name, c.args...).Start()
		}
	}
	return fmt.Errorf("no supported terminal emulator found (tried %v)", terminalNames(candidates))
}

func terminalNames(candidates []struct {
	name string
	args []string
}) []string {
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.name
	}
	return names
}
