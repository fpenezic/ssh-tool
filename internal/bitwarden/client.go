package bitwarden

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DialContext dials a host:port, optionally through a tunnel. Matches
// http.Transport.DialContext and ssh.ContextDialer so the app can route a
// server behind a WireGuard profile.
type DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

// Client talks to a Vaultwarden / Bitwarden server: API-key login and the vault
// sync. It performs no decryption - it returns the raw sync JSON, which OpenVault
// turns into a decrypted Vault. The master password never reaches this layer.
type Client struct {
	server string // base URL, no trailing slash
	http   *http.Client
}

// Credentials are the API-key login inputs (Settings -> Security -> Keys).
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// NewClient builds a client for a server base URL (e.g. https://vault.example.com).
func NewClient(server string) *Client {
	return NewClientWithDialer(server, nil)
}

// NewClientWithDialer is NewClient with an optional dialer, so a server reachable
// only through a WireGuard profile can be routed via that tunnel. A nil dialer
// uses the default direct transport.
func NewClientWithDialer(server string, dial DialContext) *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	if dial != nil {
		// Clone the default transport so TLS defaults, proxy handling, and
		// timeouts stay intact; only swap the dial step.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		}
		hc.Transport = tr
	}
	return &Client{
		server: strings.TrimRight(server, "/"),
		http:   hc,
	}
}

// Login exchanges an API key for an access token via client_credentials.
func (c *Client) Login(creds Credentials) (string, error) {
	form := url.Values{
		"grant_type":       {"client_credentials"},
		"client_id":        {creds.ClientID},
		"client_secret":    {creds.ClientSecret},
		"scope":            {"api"},
		"deviceType":       {"21"},
		"deviceIdentifier": {deviceIdentifier},
		"deviceName":       {"ssh-tool"},
	}
	req, err := http.NewRequest(http.MethodPost, c.server+"/identity/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("bitwarden: login: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bitwarden: login failed (%d): %s", resp.StatusCode, snippet(body))
	}
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("bitwarden: decode token: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("bitwarden: login returned no access token")
	}
	return tr.AccessToken, nil
}

// Sync fetches the vault. Returns the raw JSON body (fed to OpenVault) so the
// caller can hash/cache it without re-marshalling.
func (c *Client) Sync(token string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.server+"/api/sync?excludeDomains=true", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bitwarden: sync: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitwarden: sync failed (%d): %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

// LoginAndSync is the common path: log in, then sync.
func (c *Client) LoginAndSync(creds Credentials) ([]byte, error) {
	token, err := c.Login(creds)
	if err != nil {
		return nil, err
	}
	return c.Sync(token)
}

const deviceIdentifier = "ssh-tool-0000-0000-0000-000000000000"

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
