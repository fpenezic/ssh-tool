//go:build windows

package ssh

// validateAgentSocket is a no-op on Windows. The Windows SSH agent is
// served over a named pipe (e.g. \\.\pipe\openssh-ssh-agent) whose
// ACL is managed by the operating system; the UNIX-style uid /
// parent-dir-perm checks don't translate. If the user has pointed
// socket_path at an unusual pipe we let the dial fail naturally.
func validateAgentSocket(path string) error { return nil }
