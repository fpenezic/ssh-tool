package oauthpkce

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestPKCEChallengeIsS256 checks the verifier/challenge relationship the auth
// server will enforce: challenge == base64url(sha256(verifier)), no padding.
func TestPKCEChallengeIsS256(t *testing.T) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		t.Fatalf("pkcePair: %v", err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("verifier length %d out of RFC 7636 range 43-128", len(verifier))
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Fatalf("challenge %q != S256(verifier) %q", challenge, want)
	}
}

// TestBuildAuthURL sanity-checks the authorization URL carries the PKCE params
// and a provider's extra parameters and scopes.
func TestBuildAuthURL(t *testing.T) {
	ep := Endpoints{
		AuthURL:         "https://auth.example.com/oauth2/authorize",
		Scopes:          []string{"files.content.read", "files.content.write"},
		ExtraAuthParams: map[string]string{"token_access_type": "offline"},
	}
	raw := buildAuthURL(ep, "app-key", "http://127.0.0.1:5555/callback", "chal", "st4te")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "app-key",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
		"state":                 "st4te",
		"token_access_type":     "offline",
		"redirect_uri":          "http://127.0.0.1:5555/callback",
	} {
		if q.Get(k) != want {
			t.Errorf("auth URL %s=%q want %q", k, q.Get(k), want)
		}
	}
	if !strings.Contains(q.Get("scope"), "files.content.write") {
		t.Errorf("scope missing write: %q", q.Get("scope"))
	}
}

// browserFollowing plays the browser and the authorization server's
// redirect: it reads the authorization URL and calls the loopback redirect
// with either a code or the given error parameters.
func browserFollowing(t *testing.T, seen *url.Values, redirect func(q url.Values) url.Values) func(string) {
	return func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Errorf("auth url: %v", err)
			return
		}
		*seen = u.Query()
		back := redirect(u.Query())
		back.Set("state", u.Query().Get("state"))
		go func() {
			resp, err := http.Get(u.Query().Get("redirect_uri") + "?" + back.Encode())
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
}

func TestAuthorizeAnyPortFullFlow(t *testing.T) {
	var form url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		io.WriteString(w, `{"access_token":"at","refresh_token":"rt","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokenSrv.Close()

	var seen url.Values
	ep := Endpoints{AuthURL: "https://catalog.example.com/oauth/authorize", TokenURL: tokenSrv.URL, AnyPort: true}
	tok, err := Authorize(t.Context(), ep, "ssh-tool", "", browserFollowing(t, &seen, func(url.Values) url.Values {
		return url.Values{"code": {"the-code"}}
	}))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" || tok.Expiry.IsZero() {
		t.Fatalf("token: %+v", tok)
	}
	redirect := seen.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || strings.HasSuffix(redirect, ":53682/callback") {
		t.Fatalf("AnyPort did not pick its own port: %q", redirect)
	}
	if form.Get("redirect_uri") != redirect {
		t.Errorf("token exchange redirect_uri %q, authorize used %q", form.Get("redirect_uri"), redirect)
	}
	if form.Get("code") != "the-code" || form.Get("grant_type") != "authorization_code" || form.Get("client_secret") != "" {
		t.Errorf("token form: %v", form)
	}
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != seen.Get("code_challenge") {
		t.Error("verifier does not match the challenge sent to authorize")
	}
}

func TestAuthorizeReturnsProviderError(t *testing.T) {
	var seen url.Values
	ep := Endpoints{AuthURL: "https://catalog.example.com/oauth/authorize", TokenURL: "http://127.0.0.1:1/unused", AnyPort: true}
	_, err := Authorize(t.Context(), ep, "ssh-tool", "", browserFollowing(t, &seen, func(url.Values) url.Values {
		return url.Values{"error": {"access_denied"}, "error_description": {"The sign-in carried no groups."}}
	}))
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != "access_denied" || ae.Description != "The sign-in carried no groups." {
		t.Fatalf("want AuthError with the description, got %v", err)
	}
}

func TestAuthorizeCancelAndTimeout(t *testing.T) {
	ep := Endpoints{AuthURL: "https://catalog.example.com/oauth/authorize", TokenURL: "http://127.0.0.1:1/unused", AnyPort: true}

	ctx, cancel := context.WithCancel(t.Context())
	_, err := Authorize(ctx, ep, "ssh-tool", "", func(string) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: want context.Canceled, got %v", err)
	}

	ep.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err = Authorize(t.Context(), ep, "ssh-tool", "", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Timeout not honoured")
	}
}

// A wrong-state request (any page can hit the loopback port) delivers
// nothing; the real redirect still completes the flow.
func TestAuthorizeIgnoresWrongState(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"at","refresh_token":"rt"}`)
	}))
	defer tokenSrv.Close()
	ep := Endpoints{AuthURL: "https://catalog.example.com/oauth/authorize", TokenURL: tokenSrv.URL, AnyPort: true}
	tok, err := Authorize(t.Context(), ep, "ssh-tool", "", func(authURL string) {
		u, _ := url.Parse(authURL)
		redirect := u.Query().Get("redirect_uri")
		go func() {
			if resp, err := http.Get(redirect + "?state=forged&error=access_denied"); err == nil {
				resp.Body.Close()
			}
			if resp, err := http.Get(redirect + "?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=c"); err == nil {
				resp.Body.Close()
			}
		}()
	})
	if err != nil || tok.AccessToken != "at" {
		t.Fatalf("forged callback disturbed the flow: %v %+v", err, tok)
	}
}

func TestRefreshInvalidGrantIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"Refresh token already used."}`)
	}))
	defer srv.Close()
	_, err := Refresh(t.Context(), Endpoints{TokenURL: srv.URL}, "ssh-tool", "", "rt-old")
	if !IsInvalidGrant(err) {
		t.Fatalf("want invalid_grant, got %v", err)
	}
	// The message is what the sync backends showed before the type existed.
	if err.Error() != "token endpoint rejected the request: Refresh token already used." {
		t.Errorf("message changed: %q", err.Error())
	}
	if IsInvalidGrant(errors.New("token endpoint rejected the request: invalid_grant")) {
		t.Error("a plain error matched")
	}
}

func TestRefreshUsesGivenClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"a"}`)
	}))
	defer srv.Close()
	used := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := Refresh(t.Context(), Endpoints{TokenURL: srv.URL, HTTPClient: client}, "c", "", "rt"); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("HTTPClient ignored")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
