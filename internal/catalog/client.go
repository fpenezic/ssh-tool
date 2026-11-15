package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/oauthpkce"
)

// APIError is an error response of the catalog API (PROTOCOL 11). Code is
// stable and machine-readable; a client handles a code it does not know by
// its HTTP status.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details json.RawMessage
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("catalog: %s (%s)", msg, e.Code)
	}
	return "catalog: " + msg
}

// RetryAfterError: the server asked to come back later (429 / 503 with
// Retry-After). The sync loop waits at least Wait before the next try.
type RetryAfterError struct {
	Status int
	Wait   time.Duration
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("catalog: server busy (HTTP %d), retry in %s", e.Status, e.Wait)
}

// ErrSignedOut: there is no usable refresh token (never signed in on this
// machine, or the server ended the session: rotation reuse, expiry, groups
// gone). The user has to sign in again.
var ErrSignedOut = errors.New("catalog: not signed in - sign in to the catalog again")

// TokenStore keeps the refresh token between runs (the vault, under a key
// scoped to this machine). Load returns "" when there is none.
type TokenStore interface {
	Load() (string, error)
	Save(refreshToken string) error
}

// SameServer reports whether two base URLs name the same catalog server:
// scheme and host compared case-insensitively, a default port equal to
// none, localhost equal to 127.0.0.1, trailing slashes ignored.
func SameServer(a, b string) bool { return normaliseBase(a) == normaliseBase(b) }

func normaliseBase(raw string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil {
		return strings.ToLower(raw)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "::1" {
		host = "127.0.0.1"
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

// ValidateBaseURL checks a catalog base URL: https, or plain http only on
// loopback (PROTOCOL 1). Returns it without a trailing slash.
func ValidateBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("catalog: %q is not a URL", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
			return "", fmt.Errorf("catalog: plain http is only allowed on loopback; use https")
		}
	default:
		return "", fmt.Errorf("catalog: unsupported URL scheme %q", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("catalog: the base URL must not carry a query or fragment")
	}
	return raw, nil
}

// Client talks to one catalog server. The access token lives in memory
// only; the refresh token in Tokens. Safe for concurrent use: a refresh
// happens once even when several requests meet an expired token together,
// since presenting a refresh token twice ends the session.
type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
	Tokens    TokenStore

	mu     sync.Mutex
	meta   *Meta
	access string
	expiry time.Time
	gen    int // bumped on every token change
}

// NewClient builds a client for a validated base URL.
func NewClient(baseURL, userAgent string, tokens TokenStore) (*Client, error) {
	u, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: u, HTTP: &http.Client{Timeout: 60 * time.Second}, UserAgent: userAgent, Tokens: tokens}, nil
}

// SetAccessToken installs a fresh access token (after a sign-in).
func (c *Client) SetAccessToken(token string, expiry time.Time) {
	c.mu.Lock()
	c.access, c.expiry = token, expiry
	c.gen++
	c.mu.Unlock()
}

// Meta fetches (once) and returns GET /api/v1/meta. It is anonymous.
func (c *Client) Meta(ctx context.Context) (*Meta, error) {
	c.mu.Lock()
	if c.meta != nil {
		m := c.meta
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()
	var m Meta
	if err := c.do(ctx, http.MethodGet, "/api/v1/meta", nil, &m, false); err != nil {
		return nil, err
	}
	if m.Protocol.Major != ProtocolMajor {
		return nil, fmt.Errorf("catalog: the server speaks protocol %d, this app speaks %d", m.Protocol.Major, ProtocolMajor)
	}
	if m.Auth.TokenEndpoint == "" || m.Auth.AuthorizationEndpoint == "" || m.Auth.ClientID == "" {
		return nil, fmt.Errorf("catalog: the server's meta lacks its sign-in endpoints")
	}
	c.mu.Lock()
	c.meta = &m
	c.mu.Unlock()
	return &m, nil
}

// WhoAmI is GET /api/v1/whoami.
func (c *Client) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	var w WhoAmI
	if err := c.do(ctx, http.MethodGet, "/api/v1/whoami", nil, &w, true); err != nil {
		return nil, err
	}
	return &w, nil
}

// Changes is one page of GET /api/v1/changes. An empty since asks for a
// full snapshot.
func (c *Client) Changes(ctx context.Context, since string, limit int) (*Feed, error) {
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v1/changes"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var f Feed
	if err := c.do(ctx, http.MethodGet, path, nil, &f, true); err != nil {
		return nil, err
	}
	return &f, nil
}

// Revoke ends the session at the server (sign out, remove catalog). A
// failure is not fatal for the caller: the local token is dropped anyway.
func (c *Client) Revoke(ctx context.Context) error {
	m, err := c.Meta(ctx)
	if err != nil {
		return err
	}
	rt, err := c.Tokens.Load()
	if err != nil || rt == "" || m.Auth.RevocationEndpoint == "" {
		return err
	}
	form := url.Values{"token": {rt}, "client_id": {m.Auth.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.Auth.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setUA(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	c.SetAccessToken("", time.Time{})
	return nil
}

func (c *Client) endpoints(m *Meta) oauthpkce.Endpoints {
	return oauthpkce.Endpoints{
		AuthURL:    m.Auth.AuthorizationEndpoint,
		TokenURL:   m.Auth.TokenEndpoint,
		AnyPort:    true,
		Timeout:    10 * time.Minute,
		HTTPClient: c.HTTP,
	}
}

// SignIn runs the browser sign-in (authorization code + PKCE on a loopback
// redirect, PROTOCOL 10.1), stores the refresh token and keeps the access
// token. Cancel ctx to give up (the sign-in page may never redirect, for
// example when it rate-limits failed attempts).
func (c *Client) SignIn(ctx context.Context, openBrowser func(string)) error {
	m, err := c.Meta(ctx)
	if err != nil {
		return err
	}
	tok, err := oauthpkce.Authorize(ctx, c.endpoints(m), m.Auth.ClientID, "", openBrowser)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return fmt.Errorf("catalog: the server issued no refresh token")
	}
	if err := c.Tokens.Save(tok.RefreshToken); err != nil {
		return fmt.Errorf("catalog: store refresh token: %w", err)
	}
	c.SetAccessToken(tok.AccessToken, tok.Expiry)
	return nil
}

// token returns a usable access token, refreshing when there is none or it
// expired. stale names a token the server just refused: it is replaced
// even if not yet expired, unless another request already did.
func (c *Client) token(ctx context.Context, staleGen int) (string, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := c.access != "" && (c.expiry.IsZero() || time.Now().Add(30*time.Second).Before(c.expiry))
	if fresh && c.gen != staleGen {
		return c.access, c.gen, nil
	}
	m := c.meta
	if m == nil {
		c.mu.Unlock()
		mm, err := c.Meta(ctx)
		c.mu.Lock()
		if err != nil {
			return "", 0, err
		}
		m = mm
		// Someone else may have refreshed meanwhile.
		if c.access != "" && c.gen != staleGen && (c.expiry.IsZero() || time.Now().Add(30*time.Second).Before(c.expiry)) {
			return c.access, c.gen, nil
		}
	}
	rt, err := c.Tokens.Load()
	if err != nil {
		return "", 0, err
	}
	if rt == "" {
		return "", 0, ErrSignedOut
	}
	// The refresh holds the lock: a second refresh with the same token
	// would end the session at the server.
	tok, err := oauthpkce.Refresh(ctx, c.endpoints(m), m.Auth.ClientID, "", rt)
	if err != nil {
		if oauthpkce.IsInvalidGrant(err) {
			_ = c.Tokens.Save("")
			c.access = ""
			c.gen++
			return "", 0, ErrSignedOut
		}
		return "", 0, err
	}
	if tok.RefreshToken != "" {
		if err := c.Tokens.Save(tok.RefreshToken); err != nil {
			return "", 0, fmt.Errorf("catalog: store refresh token: %w", err)
		}
	}
	c.access, c.expiry = tok.AccessToken, tok.Expiry
	c.gen++
	return c.access, c.gen, nil
}

func (c *Client) setUA(req *http.Request) {
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
}

// do sends one request and decodes a JSON answer into out. With auth, a
// 401 refreshes the access token once and retries (PROTOCOL 10.3).
func (c *Client) do(ctx context.Context, method, path string, body any, out any, auth bool) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	staleGen := -1
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		c.setUA(req)
		var gen int
		if auth {
			tok, g, err := c.token(ctx, staleGen)
			if err != nil {
				return err
			}
			gen = g
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("catalog: %w", err)
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			return fmt.Errorf("catalog: read response: %w", rerr)
		}
		if auth && resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			staleGen = gen
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			return &RetryAfterError{Status: resp.StatusCode, Wait: retryAfter(resp.Header.Get("Retry-After"))}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return parseAPIError(resp.StatusCode, raw)
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("catalog: unreadable response from %s: %w", path, err)
		}
		return nil
	}
	return ErrSignedOut
}

func parseAPIError(status int, raw []byte) error {
	var body struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	e := &APIError{Status: status}
	if json.Unmarshal(raw, &body) == nil {
		e.Code, e.Message, e.Details = body.Error.Code, body.Error.Message, body.Error.Details
	}
	return e
}

// retryAfter reads a Retry-After header (seconds or an HTTP date); a
// missing or odd value means one minute.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
		return 0
	}
	return time.Minute
}

// PublishResponse is a 201 from POST /api/v1/publish.
type PublishResponse struct {
	Records  []Accepted     `json:"records"`
	Warnings []RecordReport `json:"warnings"`
}

// RecordReport is a per-record problem of a refused publish
// (details.records) or a warning of an accepted one.
type RecordReport struct {
	Kind    string `json:"kind"`
	UUID    string `json:"uuid"`
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Publish sends a publish request (PROTOCOL 8.2). All or nothing on the
// server: see PublishNotCreated for what an error means.
func (c *Client) Publish(ctx context.Context, req *PublishRequest) (*PublishResponse, error) {
	var out PublishResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/publish", req, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// PublishNotCreated reports whether a failed publish certainly created
// nothing: the server answered with a client error (it checks everything
// before it writes, in one transaction), or the request never went out
// with a valid token. Anything else (no answer, a cut connection, a
// server error) leaves the outcome open; the next sync settles it.
func PublishNotCreated(err error) bool {
	if errors.Is(err, ErrSignedOut) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 400 && ae.Status < 500
	}
	var ra *RetryAfterError
	if errors.As(err, &ra) {
		return ra.Status == http.StatusTooManyRequests
	}
	return false
}

// PublishRecordErrors is the per-record list of a refused publish.
func PublishRecordErrors(err error) []RecordReport {
	var ae *APIError
	if !errors.As(err, &ae) || len(ae.Details) == 0 {
		return nil
	}
	var d struct {
		Records []RecordReport `json:"records"`
	}
	if json.Unmarshal(ae.Details, &d) != nil {
		return nil
	}
	return d.Records
}
