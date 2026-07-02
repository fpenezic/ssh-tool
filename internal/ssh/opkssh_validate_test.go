package ssh

import (
	"strings"
	"testing"

	opkconfig "github.com/openpubkey/opkssh/commands/config"
)

func TestValidateOpksshIssuer(t *testing.T) {
	cases := []struct {
		name    string
		issuer  string
		wantErr string // substring; empty = expect success
	}{
		{"https google", "https://accounts.google.com", ""},
		{"https gitlab", "https://gitlab.com", ""},
		{"https microsoft", "https://login.microsoftonline.com/common/v2.0", ""},
		{"http localhost dev", "http://localhost:8080/realms/dev", ""},
		{"http 127.0.0.1 dev", "http://127.0.0.1:8080", ""},
		{"http attacker", "http://attacker.example.com", "loopback"},
		{"plain attacker host", "http://evil.com/oidc", "loopback"},
		{"ftp scheme", "ftp://example.com", "scheme must be https"},
		{"data scheme", "data:text/plain,foo", "scheme must be https"},
		{"empty", "", "scheme must be"}, // empty issuer falls through to scheme check; provider-level guard catches it explicitly
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateOpksshIssuer(c.issuer)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected err containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestValidateOpksshRedirect(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		wantErr string
	}{
		{"localhost http", "http://localhost:11110/login-callback", ""},
		{"127.0.0.1 http", "http://127.0.0.1:11110/login-callback", ""},
		{"127.1.2.3 http", "http://127.1.2.3:11110/cb", ""},
		{"ipv6 loopback brackets", "http://[::1]:11110/cb", ""},
		{"localhost https", "https://localhost:11110/cb", ""},
		{"localhost no port", "http://localhost/cb", ""},
		{"attacker", "http://attacker.example.com/callback", "loopback"},
		{"plain ip non-loopback", "http://10.0.0.5:11110/cb", "loopback"},
		{"ftp", "ftp://localhost/cb", "scheme must be"},
		{"javascript", "javascript:alert(1)", "scheme must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateOpksshRedirect(c.uri)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected err containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestValidateOpksshYAML(t *testing.T) {
	// Empty is fine - editor uses empty to mean "not configured yet"
	// and runtime path rejects it explicitly with a clearer error.
	if err := ValidateOpksshYAML(""); err != nil {
		t.Fatalf("empty YAML should pass: %v", err)
	}
	if err := ValidateOpksshYAML("   \n\t  "); err != nil {
		t.Fatalf("whitespace-only YAML should pass: %v", err)
	}

	goodYAML := `
providers:
  - alias: google
    issuer: https://accounts.google.com
    client_id: test-client
    redirect_uris:
      - http://localhost:11110/login-callback
`
	if err := ValidateOpksshYAML(goodYAML); err != nil {
		t.Fatalf("legit YAML rejected: %v", err)
	}

	hostileRedirect := `
providers:
  - alias: google
    issuer: https://accounts.google.com
    client_id: test-client
    redirect_uris:
      - http://attacker.example.com/cb
`
	if err := ValidateOpksshYAML(hostileRedirect); err == nil {
		t.Fatal("hostile redirect_uri accepted")
	}

	hostileIssuer := `
providers:
  - alias: bad
    issuer: http://attacker.example.com
    client_id: test-client
    redirect_uris:
      - http://localhost:11110/cb
`
	if err := ValidateOpksshYAML(hostileIssuer); err == nil {
		t.Fatal("hostile issuer accepted")
	}

	// Mixed list of providers: even one bad provider fails the
	// whole YAML - at runtime selectProvider could pick it via
	// hint / default_provider, so we can't tell which will run.
	mixed := `
providers:
  - alias: good
    issuer: https://accounts.google.com
    client_id: c1
    redirect_uris:
      - http://localhost:11110/cb
  - alias: bad
    issuer: http://attacker.example.com
    client_id: c2
    redirect_uris:
      - http://localhost:11110/cb
`
	if err := ValidateOpksshYAML(mixed); err == nil {
		t.Fatal("mixed providers with one hostile entry accepted")
	}

	if err := ValidateOpksshYAML("not yaml: [unclosed"); err == nil {
		t.Fatal("garbage YAML accepted")
	}
}

func TestValidateOpksshProvider(t *testing.T) {
	ok := &opkconfig.ProviderConfig{
		Issuer:       "https://accounts.google.com",
		RedirectURIs: []string{"http://localhost:11110/login-callback"},
	}
	if err := validateOpksshProvider(ok); err != nil {
		t.Fatalf("legit config rejected: %v", err)
	}

	// Hostile redirect uri.
	bad := &opkconfig.ProviderConfig{
		Issuer:       "https://accounts.google.com",
		RedirectURIs: []string{"http://attacker.example.com/cb"},
	}
	if err := validateOpksshProvider(bad); err == nil {
		t.Fatal("hostile redirect accepted")
	}

	// Hostile issuer.
	bad2 := &opkconfig.ProviderConfig{
		Issuer:       "http://attacker.example.com",
		RedirectURIs: []string{"http://localhost:11110/cb"},
	}
	if err := validateOpksshProvider(bad2); err == nil {
		t.Fatal("hostile issuer accepted")
	}

	// Empty redirect list.
	bad3 := &opkconfig.ProviderConfig{
		Issuer:       "https://accounts.google.com",
		RedirectURIs: []string{},
	}
	if err := validateOpksshProvider(bad3); err == nil {
		t.Fatal("empty redirect list accepted")
	}

	// Mixed list - even one hostile entry fails the whole provider
	// because we don't know which one the library will end up
	// using at runtime.
	bad4 := &opkconfig.ProviderConfig{
		Issuer:       "https://accounts.google.com",
		RedirectURIs: []string{"http://localhost:11110/cb", "http://attacker.example.com/cb"},
	}
	if err := validateOpksshProvider(bad4); err == nil {
		t.Fatal("mixed list with one hostile entry accepted")
	}

	// Hostile remote_redirect_uri.
	bad5 := &opkconfig.ProviderConfig{
		Issuer:            "https://accounts.google.com",
		RedirectURIs:      []string{"http://localhost:11110/cb"},
		RemoteRedirectURI: "http://attacker.example.com/cb",
	}
	if err := validateOpksshProvider(bad5); err == nil {
		t.Fatal("hostile remote_redirect_uri accepted")
	}
}
