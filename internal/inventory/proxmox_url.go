package inventory

// Validation for the Proxmox base_url. Unlike every other provider, Proxmox
// takes a URL the user pastes in, and the API token rides on every request to
// it in an Authorization header. That makes the field worth checking before
// the first refresh rather than after: whatever host it names receives a
// credential.
//
// What this does NOT do is block private addresses. A Proxmox cluster is
// normally on a LAN or behind a VPN, so an SSRF-style private-IP rejection -
// correct for the archive fetcher, which only ever talks to a public catalog -
// would break the ordinary case here. The goal is a URL that cannot silently
// mean something other than what the user read, plus a resolved host the UI
// can show them.

import (
	"fmt"
	neturl "net/url"
	"strings"
)

// ProxmoxURLInfo describes a base_url after validation, for display before the
// first refresh. Host is what the token will actually be sent to.
type ProxmoxURLInfo struct {
	// Host is the hostname or IP, without the port.
	Host string `json:"host"`
	// Port is the explicit port, or the scheme default (8006 is Proxmox's
	// own, but only when the user wrote no port and no scheme default fits).
	Port string `json:"port"`
	// Insecure is true when the URL is plain http, meaning the API token
	// crosses the network in clear text.
	Insecure bool `json:"insecure"`
	// Normalized is the URL with the trailing slash trimmed, as the
	// provider will use it.
	Normalized string `json:"normalized"`
}

// ValidateProxmoxBaseURL parses and checks a user-entered base_url. It returns
// a description of where requests will go, or an error explaining what is
// wrong with the URL.
func ValidateProxmoxBaseURL(raw string) (*ProxmoxURLInfo, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("base_url is required")
	}

	// A bare "pve.example.com:8006" parses as scheme "pve" with an opaque
	// body, which would otherwise sail through as a valid URL and then fail
	// at request time with something unhelpful. Catch it here and say what
	// to write instead.
	if !strings.Contains(trimmed, "://") {
		return nil, fmt.Errorf(
			"base_url needs a scheme - write https://%s", trimmed)
	}

	u, err := neturl.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("base_url is not a valid URL: %w", err)
	}
	switch u.Scheme {
	case "https", "http":
	default:
		return nil, fmt.Errorf(
			"base_url scheme must be https (or http), got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("base_url has no host")
	}
	// Credentials in the URL are never right here - the token is the
	// credential, and a userinfo section would be sent to the same host
	// while being easy to miss when reading the field.
	if u.User != nil {
		return nil, fmt.Errorf("base_url must not embed a username or password")
	}
	// A path is accepted (some deployments sit behind a reverse proxy on a
	// subpath) but a query or fragment is always a mistake, and a query
	// string is how a crafted value would try to reshape the request URL.
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("base_url must not contain a query string or fragment")
	}

	port := u.Port()
	if port == "" {
		// Proxmox's own default. Reported for display only - the provider
		// leaves the URL alone and lets net/http apply the scheme default,
		// so a deployment behind a 443 reverse proxy keeps working.
		port = "8006"
	}

	return &ProxmoxURLInfo{
		Host:       u.Hostname(),
		Port:       port,
		Insecure:   u.Scheme == "http",
		Normalized: strings.TrimRight(trimmed, "/"),
	}, nil
}
