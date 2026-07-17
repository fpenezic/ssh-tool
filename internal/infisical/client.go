package infisical

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

// Client talks to an Infisical server: Universal Auth login, project listing,
// and raw secret reads. It performs NO decryption - Infisical decrypts
// server-side and returns plaintext values over TLS. This is the whole model,
// and the reason the Infisical backend is much simpler than Bitwarden.
type Client struct {
	server string // base URL, no trailing slash
	http   *http.Client
}

// NewClient builds a client for a server base URL (e.g. https://infisical.example.com).
func NewClient(server string) *Client {
	return NewClientWithDialer(server, nil)
}

// NewClientWithDialer is NewClient with an optional dialer, so a server
// reachable only through a WireGuard profile can be routed via that tunnel. A
// nil dialer uses the default direct transport.
func NewClientWithDialer(server string, dial DialContext) *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	if dial != nil {
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

// Login exchanges a machine-identity API key for an access token via Universal
// Auth. It returns the token and its TTL in seconds so the caller can cache it.
func (c *Client) Login(creds Credentials) (token string, expiresIn int, err error) {
	body, _ := json.Marshal(map[string]string{
		"clientId":     creds.ClientID,
		"clientSecret": creds.ClientSecret,
	})
	req, err := http.NewRequest(http.MethodPost, c.server+"/api/v1/auth/universal-auth/login", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	var tr tokenResp
	if err := c.do(req, &tr); err != nil {
		return "", 0, fmt.Errorf("infisical: login: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("infisical: login returned no access token")
	}
	return tr.AccessToken, tr.ExpiresIn, nil
}

// ListProjects returns the workspaces the machine identity can see, each with
// its environments. Source for the browse tree.
func (c *Client) ListProjects(token string) ([]project, error) {
	req, err := http.NewRequest(http.MethodGet, c.server+"/api/v1/workspace", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var wr workspacesResp
	if err := c.do(req, &wr); err != nil {
		return nil, fmt.Errorf("infisical: list projects: %w", err)
	}
	if len(wr.Workspaces) > 0 {
		return wr.Workspaces, nil
	}
	return wr.Projects, nil
}

// ListSecrets returns the secrets at one project+environment+path. Values are
// plaintext. Used by the picker to enumerate keys. When recursive is true,
// nested-folder secrets are included and each carries its SecretPath (subject to
// server-version quirks - some Infisical builds ignore recursive on subpaths, in
// which case only the queried path is returned and the tree degrades to it).
func (c *Client) ListSecrets(token, projectID, environment, secretPath string, recursive bool) ([]rawSecret, error) {
	if secretPath == "" {
		secretPath = "/"
	}
	q := url.Values{}
	q.Set("workspaceId", projectID)
	q.Set("environment", environment)
	q.Set("secretPath", secretPath)
	if recursive {
		q.Set("recursive", "true")
	}
	req, err := http.NewRequest(http.MethodGet, c.server+"/api/v3/secrets/raw?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var sr secretsResp
	if err := c.do(req, &sr); err != nil {
		return nil, fmt.Errorf("infisical: list secrets: %w", err)
	}
	return sr.Secrets, nil
}

// ReadSecret returns the plaintext value of one secret. This is the Resolve
// leg: the single-key raw endpoint, addressed by project+environment+path+key.
func (c *Client) ReadSecret(token, projectID, environment, secretPath, key string) (string, error) {
	if secretPath == "" {
		secretPath = "/"
	}
	q := url.Values{}
	q.Set("workspaceId", projectID)
	q.Set("environment", environment)
	q.Set("secretPath", secretPath)
	u := c.server + "/api/v3/secrets/raw/" + url.PathEscape(key) + "?" + q.Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var sr singleSecretResp
	if err := c.do(req, &sr); err != nil {
		return "", fmt.Errorf("infisical: read secret %q: %w", key, err)
	}
	return sr.Secret.SecretValue, nil
}

// do executes req and decodes a JSON response into out. Non-2xx is an error
// carrying a body snippet. errUnauthorized is returned on 401 so the manager can
// re-login.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
