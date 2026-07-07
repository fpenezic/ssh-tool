package ssh

import (
	"fmt"

	"golang.org/x/crypto/ssh"
)

// runCaptureLimit caps how much combined output RunCapture returns, so a
// runaway command (an LLM ran `yes` or `cat` on a huge file) can't stream
// unbounded data back through the MCP bridge.
const runCaptureLimit = 256 * 1024 // 256 KB

// RunCapture runs a command on a fresh, non-PTY side session of client and
// returns the combined stdout+stderr, capped at runCaptureLimit. It never
// touches the interactive terminal - same pattern as FetchServerStats. Used by
// the MCP bridge's run tool. The command string is passed through verbatim;
// callers are responsible for the allowlist / approval gate.
func RunCapture(client *ssh.Client, command string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no live ssh client")
	}
	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("run session: %w", err)
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(command)
	if len(out) > runCaptureLimit {
		out = append(out[:runCaptureLimit], []byte("\n...[output truncated]")...)
	}
	if err != nil {
		// Return partial output alongside the error so the caller can surface a
		// non-zero exit's diagnostics.
		return string(out), fmt.Errorf("exit: %w", err)
	}
	return string(out), nil
}
