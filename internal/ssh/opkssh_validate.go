package ssh

import (
	"fmt"
	"net/url"
	"strings"

	opkconfig "github.com/openpubkey/opkssh/commands/config"
)

// ValidateOpksshYAML parses a raw provider YAML and runs the same
// safety checks every provider will face at OIDC login time. Used at
// credential save time so the user is told immediately that a pasted
// config will be rejected - without it, the failure only surfaces
// when opkssh next tries to mint a cert, which can be hours or days
// after the broken paste.
//
// Empty input is treated as "no config yet" and returns nil - the
// editor uses an empty field to mean "user hasn't filled this in
// yet" and the runtime path already rejects that explicitly.
func ValidateOpksshYAML(yaml string) error {
	if strings.TrimSpace(yaml) == "" {
		return nil
	}
	cc, err := opkconfig.NewClientConfig([]byte(yaml))
	if err != nil {
		return fmt.Errorf("parse provider YAML: %w", err)
	}
	if len(cc.Providers) == 0 {
		return fmt.Errorf("no providers defined in YAML")
	}
	for i := range cc.Providers {
		p := cc.Providers[i]
		if err := validateOpksshProvider(&p); err != nil {
			return err
		}
	}
	return nil
}

// validateOpksshProvider guards against hostile YAML pasted into the
// opkssh credential editor. Two attack vectors we mitigate:
//
//  1. Attacker IdP via "issuer": a non-https issuer (or one pointing
//     at a non-TLS attacker host) hands the user's identity over in
//     plaintext + lets the attacker mint id_tokens claiming to be the
//     legitimate provider. Real IdPs are https-only. The narrow
//     exception is OIDC dev work against http://localhost - same
//     trust boundary as the redirect uri below.
//  2. Token exfiltration via "redirect_uris": the OIDC browser flow
//     posts the auth code to the redirect uri. If we let the user
//     point that at attacker.example, the issued code (and any
//     access_token if implicit flow) leaks. opkssh runs a local
//     callback listener; the only safe redirect targets are loopback
//     addresses (RFC 8252 §7.3 native-app guidance).
//
// Returns the first error encountered. Caller refuses to proceed
// with the OIDC flow on non-nil.
func validateOpksshProvider(p *opkconfig.ProviderConfig) error {
	if p == nil {
		return fmt.Errorf("nil provider config")
	}
	if strings.TrimSpace(p.Issuer) == "" {
		return fmt.Errorf("provider has empty issuer")
	}
	if err := validateOpksshIssuer(p.Issuer); err != nil {
		return fmt.Errorf("provider %q: %w", p.Issuer, err)
	}
	if len(p.RedirectURIs) == 0 {
		return fmt.Errorf("provider %q has no redirect_uris configured", p.Issuer)
	}
	for _, raw := range p.RedirectURIs {
		if err := validateOpksshRedirect(raw); err != nil {
			return fmt.Errorf("provider %q redirect %q: %w", p.Issuer, raw, err)
		}
	}
	if p.RemoteRedirectURI != "" {
		if err := validateOpksshRedirect(p.RemoteRedirectURI); err != nil {
			return fmt.Errorf("provider %q remote_redirect_uri %q: %w", p.Issuer, p.RemoteRedirectURI, err)
		}
	}
	return nil
}

func validateOpksshIssuer(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("http:// issuer only allowed for loopback hosts (got %q)", u.Hostname())
	default:
		return fmt.Errorf("scheme must be https (got %q)", u.Scheme)
	}
}

func validateOpksshRedirect(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	// Per RFC 8252 native apps redirect to a loopback URI. https
	// loopback is fine too but http is the common form. We refuse
	// anything that resolves outside loopback.
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https (got %q)", u.Scheme)
	}
	if !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("host must be loopback (localhost, 127.0.0.1, ::1); got %q", u.Hostname())
	}
	return nil
}

// isLoopbackHost matches the hostnames the OIDC native-app guidance
// (RFC 8252 §7.3) considers safe redirect targets. We accept literal
// IPv4 / IPv6 loopback plus "localhost" - Windows resolves it via
// hosts file, Linux via NSS, macOS via DNS; in every case it goes to
// 127.0.0.1 / ::1. Anything else is potentially attacker-controlled.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	// 127.0.0.0/8 - accept any 127.x.y.z literal, matches Go stdlib's
	// net.IP.IsLoopback for IPv4.
	if strings.HasPrefix(host, "127.") {
		return true
	}
	return false
}
