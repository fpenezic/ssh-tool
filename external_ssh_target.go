package main

import (
	"fmt"
	"regexp"
	"strings"

	"ssh-tool/internal/store"
)

// The external and system terminal launchers hand host and user names to an
// OpenSSH client on a command line that cmd.exe, PowerShell, wt.exe or a
// POSIX shell parses again. A name starting with "-" is read by ssh as an
// option (-oProxyCommand=... runs a local command), and & | ; ^ ` $ and
// friends are shell syntax there. Names come from the store, which an
// import, a sync or an MCP plan can fill, so they are checked here rather
// than trusted. The in-app client never parses a command line and does not
// need this.
var (
	extHostRe = regexp.MustCompile(`^[A-Za-z0-9_.:%\[\]-]+$`)
	extUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@\\-]+$`)
)

func checkExternalSSHTarget(s *store.ResolvedSettings) error {
	if err := checkExtHost(s.Hostname); err != nil {
		return err
	}
	if s.Username != nil {
		if err := checkExtUser(*s.Username); err != nil {
			return err
		}
	}
	for hop := s.JumpHost; hop != nil; hop = hop.Via {
		if err := checkExtHost(hop.Hostname); err != nil {
			return fmt.Errorf("jump host: %w", err)
		}
		if hop.Username != nil {
			if err := checkExtUser(*hop.Username); err != nil {
				return fmt.Errorf("jump host: %w", err)
			}
		}
	}
	return nil
}

func checkExtHost(h string) error {
	if h == "" || strings.HasPrefix(h, "-") || !extHostRe.MatchString(h) {
		return fmt.Errorf("host name %q cannot be passed to an external ssh safely", h)
	}
	return nil
}

func checkExtUser(u string) error {
	if u == "" {
		return nil
	}
	if strings.HasPrefix(u, "-") || !extUserRe.MatchString(u) {
		return fmt.Errorf("user name %q cannot be passed to an external ssh safely", u)
	}
	return nil
}
