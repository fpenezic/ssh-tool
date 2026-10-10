package main

import (
	"net/url"
	"strings"
)

// openableURL: the schemes OpenURL hands to the OS. Everything it opens is a
// web or mail link (notes, terminal links, release pages); on Windows the
// OS open is ShellExecute, which would also run file:, ms-*: or a custom
// protocol handler, so anything else is refused here rather than trusted
// to every caller's own filtering.
func openableURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != ""
	}
	return false
}
