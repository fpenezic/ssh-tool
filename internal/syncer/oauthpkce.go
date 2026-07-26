package syncer

// OAuth 2.0 authorization-code flow with PKCE (RFC 7636) and a loopback
// redirect (RFC 8252), for cloud-storage sync backends whose auth is a public
// client - no client secret anywhere. Dropbox is the first user; OneDrive and
// Google Drive reuse this by supplying different OAuthEndpoints. The app_key
// (client ID) is user-supplied and public by design; PKCE is what makes that
// safe (a code_verifier the attacker never sees replaces the client secret).
//
// Loopback is desktop-only: Authorize stands up an ephemeral HTTP listener on
// 127.0.0.1:<random> to catch the redirect. Mobile would use a manual-code
// paste flow instead (not implemented here).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthEndpoints is the per-provider configuration the PKCE flow needs. Only
// these differ between Dropbox / OneDrive / Google Drive; the flow is shared.
type OAuthEndpoints struct {
	AuthURL  string   // authorization endpoint (browser lands here)
	TokenURL string   // token endpoint (code -> token, refresh -> token)
	Scopes   []string // requested scopes; joined with spaces
	// ExtraAuthParams are provider quirks added to the authorization URL.
	// Dropbox needs token_access_type=offline to get a refresh token.
	ExtraAuthParams map[string]string
	// RedirectPort is the fixed loopback port the redirect lands on. 0 means
	// defaultRedirectPort. Fixed (not random) because Dropbox/Azure require the
	// redirect_uri to be pre-registered exactly.
	RedirectPort int
}

// defaultRedirectPort is the loopback port used when a provider does not pin
// its own. Chosen in the high user range, unlikely to collide with a service.
const defaultRedirectPort = 53682

// RedirectURIForPort is the exact redirect URI the flow listens on for a given
// port. The provider console must have this registered verbatim. Exposed so the
// UI can show the user precisely what to paste.
func RedirectURIForPort(port int) string {
	if port == 0 {
		port = defaultRedirectPort
	}
	return fmt.Sprintf("http://127.0.0.1:%d/callback", port)
}

// DefaultRedirectURI is the redirect URI for the default port - what most
// providers should register.
func DefaultRedirectURI() string { return RedirectURIForPort(defaultRedirectPort) }

// Token is the result of an authorization or refresh. RefreshToken may be empty
// on a refresh (Dropbox does not re-issue one); callers carry the old one
// forward in that case.
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// authTimeout bounds the whole interactive flow - if the user never finishes in
// the browser, the loopback listener is torn down instead of hanging forever.
const authTimeout = 3 * time.Minute

// Authorize runs the interactive PKCE flow: it opens the browser at the
// provider's authorization URL, catches the redirect on a loopback listener,
// and exchanges the code (with the PKCE verifier) for a token. openBrowser is
// injected so the caller wires it to the Wails BrowserOpenURL shim.
func Authorize(ctx context.Context, ep OAuthEndpoints, clientID string, openBrowser func(string)) (Token, error) {
	if clientID == "" {
		return Token{}, fmt.Errorf("app key (client ID) is required")
	}

	verifier, challenge, err := pkcePair()
	if err != nil {
		return Token{}, err
	}
	state, err := randomToken(24)
	if err != nil {
		return Token{}, err
	}

	// Loopback listener on a FIXED port. Dropbox and Azure require the
	// redirect_uri to exactly match a pre-registered value (port included), so a
	// random port is unusable there - the user registers this exact URI in the
	// provider console. Google tolerates any loopback port, but a fixed one is
	// fine for it too, so all three share one URI.
	port := ep.RedirectPort
	if port == 0 {
		port = defaultRedirectPort
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return Token{}, fmt.Errorf("cannot open loopback listener on %s (port in use?): %w", addr, err)
	}
	defer ln.Close()
	redirectURI := RedirectURIForPort(port)

	authURL := buildAuthURL(ep, clientID, redirectURI, challenge, state)

	// One-shot server: the first valid /callback delivers the code (or an error)
	// on this channel, then the flow shuts the listener.
	type callback struct {
		code string
		err  error
	}
	result := make(chan callback, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if e := q.Get("error"); e != "" {
				writeClosePage(w, "Authorization failed. You can close this tab.")
				result <- callback{err: fmt.Errorf("authorization denied: %s", e)}
				return
			}
			if q.Get("state") != state {
				// Do not deliver a result: a mismatched state is a stray/hostile
				// request, not our redirect. The real one may still arrive.
				http.Error(w, "state mismatch", http.StatusBadRequest)
				return
			}
			code := q.Get("code")
			if code == "" {
				writeClosePage(w, "No authorization code returned. You can close this tab.")
				result <- callback{err: fmt.Errorf("no authorization code in redirect")}
				return
			}
			writeClosePage(w, "Connected. You can close this tab and return to ssh-tool.")
			result <- callback{code: code}
		}),
	}
	go srv.Serve(ln)
	defer srv.Close()

	openBrowser(authURL)

	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()

	var code string
	select {
	case cb := <-result:
		if cb.err != nil {
			return Token{}, cb.err
		}
		code = cb.code
	case <-ctx.Done():
		return Token{}, fmt.Errorf("timed out waiting for browser authorization")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}
	return exchange(ctx, ep.TokenURL, form)
}

// Refresh trades a refresh token for a fresh access token. No client secret -
// PKCE public clients refresh with client_id + refresh_token alone. A provider
// that omits a new refresh_token leaves Token.RefreshToken empty; the caller
// keeps the existing one.
func Refresh(ctx context.Context, ep OAuthEndpoints, clientID, refreshToken string) (Token, error) {
	if refreshToken == "" {
		return Token{}, fmt.Errorf("no refresh token - connect the account first")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	return exchange(ctx, ep.TokenURL, form)
}

// exchange POSTs a token request and parses the standard OAuth response.
func exchange(ctx context.Context, tokenURL string, form url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Token{}, fmt.Errorf("token response HTTP %d (unparseable)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || body.Error != "" {
		msg := body.Error
		if body.ErrorDesc != "" {
			msg = body.ErrorDesc
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return Token{}, fmt.Errorf("token endpoint rejected the request: %s", msg)
	}

	tok := Token{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken}
	if body.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return tok, nil
}

func buildAuthURL(ep OAuthEndpoints, clientID, redirectURI, challenge, state string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if len(ep.Scopes) > 0 {
		q.Set("scope", strings.Join(ep.Scopes, " "))
	}
	for k, v := range ep.ExtraAuthParams {
		q.Set(k, v)
	}
	sep := "?"
	if strings.Contains(ep.AuthURL, "?") {
		sep = "&"
	}
	return ep.AuthURL + sep + q.Encode()
}

// pkcePair returns a fresh code_verifier and its S256 code_challenge.
func pkcePair() (verifier, challenge string, err error) {
	verifier, err = randomToken(64) // 64 raw bytes -> 86 base64url chars (within 43-128)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// randomToken returns nBytes of CSPRNG randomness as a base64url string (no
// padding), suitable for a PKCE verifier or an anti-CSRF state value.
func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeClosePage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "<!doctype html><html><body style=\"font-family:sans-serif;padding:2rem\"><p>%s</p></body></html>", msg)
}
