package inventory

import (
	"strings"
	"testing"
)

func TestValidateProxmoxBaseURLAcceptsNormalForms(t *testing.T) {
	cases := []struct {
		raw      string
		wantHost string
		wantPort string
	}{
		{"https://pve.example.com:8006", "pve.example.com", "8006"},
		{"https://pve.example.com:8006/", "pve.example.com", "8006"},
		{"https://10.0.0.5:8006", "10.0.0.5", "8006"},
		// No port: Proxmox's own default is reported for display.
		{"https://pve.example.com", "pve.example.com", "8006"},
		// Behind a reverse proxy on 443 with a subpath.
		{"https://proxy.example.com:443/pve", "proxy.example.com", "443"},
	}
	for _, c := range cases {
		info, err := ValidateProxmoxBaseURL(c.raw)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.raw, err)
			continue
		}
		if info.Host != c.wantHost {
			t.Errorf("%s: host = %q, want %q", c.raw, info.Host, c.wantHost)
		}
		if info.Port != c.wantPort {
			t.Errorf("%s: port = %q, want %q", c.raw, info.Port, c.wantPort)
		}
	}
}

// A Proxmox cluster normally lives on a LAN or behind a VPN. Rejecting
// private addresses would break the ordinary case, so this validation must
// never do that.
func TestValidateProxmoxBaseURLAllowsPrivateAndLocalAddresses(t *testing.T) {
	for _, raw := range []string{
		"https://192.168.1.10:8006",
		"https://10.0.0.5:8006",
		"https://172.16.4.4:8006",
		"https://localhost:8006",
		"https://127.0.0.1:8006",
		"https://pve.internal:8006",
	} {
		if _, err := ValidateProxmoxBaseURL(raw); err != nil {
			t.Errorf("%s must be accepted - Proxmox is normally on a private network: %v", raw, err)
		}
	}
}

// Plain http means the API token crosses the network in clear text. It is
// still allowed (isolated lab networks exist) but must be reported so the UI
// can warn.
func TestValidateProxmoxBaseURLFlagsPlainHTTP(t *testing.T) {
	info, err := ValidateProxmoxBaseURL("http://pve.example.com:8006")
	if err != nil {
		t.Fatalf("http should be allowed, got %v", err)
	}
	if !info.Insecure {
		t.Error("a plain http base_url must be reported as insecure - the API token is sent in clear text")
	}

	secure, err := ValidateProxmoxBaseURL("https://pve.example.com:8006")
	if err != nil {
		t.Fatal(err)
	}
	if secure.Insecure {
		t.Error("https must not be flagged insecure")
	}
}

// A bare host:port parses as scheme "pve" with an opaque body, so without an
// explicit check it would pass validation and fail later with an unhelpful
// message.
func TestValidateProxmoxBaseURLRejectsSchemelessHost(t *testing.T) {
	_, err := ValidateProxmoxBaseURL("pve.example.com:8006")
	if err == nil {
		t.Fatal("a base_url with no scheme must be rejected")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "https://pve.example.com:8006") {
		t.Errorf("the error should say what to write instead, got %q", got)
	}
}

func TestValidateProxmoxBaseURLRejectsBadSchemes(t *testing.T) {
	for _, raw := range []string{
		"ftp://pve.example.com",
		"file:///etc/passwd",
		"gopher://pve.example.com:8006",
	} {
		if _, err := ValidateProxmoxBaseURL(raw); err == nil {
			t.Errorf("%s must be rejected", raw)
		}
	}
}

// Userinfo is easy to miss when reading the field and would be sent to the
// same host as the token.
func TestValidateProxmoxBaseURLRejectsEmbeddedCredentials(t *testing.T) {
	for _, raw := range []string{
		"https://user@pve.example.com:8006",
		"https://user:pass@pve.example.com:8006",
	} {
		if _, err := ValidateProxmoxBaseURL(raw); err == nil {
			t.Errorf("%s must be rejected - credentials do not belong in base_url", raw)
		}
	}
}

func TestValidateProxmoxBaseURLRejectsQueryAndFragment(t *testing.T) {
	for _, raw := range []string{
		"https://pve.example.com:8006/?redirect=http://evil.example.com",
		"https://pve.example.com:8006/#frag",
	} {
		if _, err := ValidateProxmoxBaseURL(raw); err == nil {
			t.Errorf("%s must be rejected", raw)
		}
	}
}

func TestValidateProxmoxBaseURLRejectsEmptyAndHostless(t *testing.T) {
	for _, raw := range []string{"", "   ", "https://", "https:///path"} {
		if _, err := ValidateProxmoxBaseURL(raw); err == nil {
			t.Errorf("%q must be rejected", raw)
		}
	}
}

func TestValidateProxmoxBaseURLNormalizesTrailingSlash(t *testing.T) {
	info, err := ValidateProxmoxBaseURL("https://pve.example.com:8006/")
	if err != nil {
		t.Fatal(err)
	}
	if info.Normalized != "https://pve.example.com:8006" {
		t.Errorf("normalized = %q, want the trailing slash trimmed", info.Normalized)
	}
}

// Fetch now validates before sending the token, so any base_url an EXISTING
// install could already have stored must still be accepted - a validation
// that breaks working configs on upgrade is worse than the risk it addresses.
func TestValidateAcceptsShapesExistingConfigsMayHold(t *testing.T) {
	for _, raw := range []string{
		"https://pve.example.com:8006",
		"https://pve.example.com:8006/",
		"https://192.168.1.10:8006",
		"http://192.168.1.10:8006", // allowed, only flagged
		"https://pve",              // short internal hostname
		"https://pve.example.com",  // no port
		"https://[2001:db8::1]:8006",
	} {
		if _, err := ValidateProxmoxBaseURL(raw); err != nil {
			t.Errorf("%s is a shape an existing install may hold and must keep working: %v", raw, err)
		}
	}
}
