package syncer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestDropbox points a Dropbox transport at a test server for both RPC and
// content hosts.
func newTestDropbox(t *testing.T, srvURL string, refresh func() (string, error)) *Dropbox {
	t.Helper()
	d := NewDropbox("access-token-0", "/ssh-tool", refresh)
	d.rpcBase = srvURL
	d.contentBase = srvURL
	return d
}

func TestDropboxGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error_summary":"path/not_found/...","error":{".tag":"path","path":{".tag":"not_found"}}}`)
	}))
	defer srv.Close()

	d := newTestDropbox(t, srv.URL, nil)
	_, err := d.Get("meta.json")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDropboxPutGetRoundTrip(t *testing.T) {
	store := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/files/upload":
			var arg struct {
				Path string `json:"path"`
			}
			json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg)
			body, _ := io.ReadAll(r.Body)
			store[arg.Path] = body
			io.WriteString(w, `{"name":"x"}`)
		case "/2/files/download":
			var arg struct {
				Path string `json:"path"`
			}
			json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg)
			data, ok := store[arg.Path]
			if !ok {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error_summary":"path/not_found/"}`)
				return
			}
			w.Write(data)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := newTestDropbox(t, srv.URL, nil)
	want := []byte("sealed-snapshot-bytes")
	if err := d.Put("snapshot.stb", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := d.Get("snapshot.stb")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, want)
	}
}

func TestDropboxMoveRoundTrip(t *testing.T) {
	store := map[string][]byte{"/ssh-tool/tmp": []byte("payload")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2/files/move_v2" {
			var arg struct {
				From string `json:"from_path"`
				To   string `json:"to_path"`
			}
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &arg)
			data, ok := store[arg.From]
			if !ok {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error_summary":"from_lookup/not_found/"}`)
				return
			}
			store[arg.To] = data
			delete(store, arg.From)
			io.WriteString(w, `{"metadata":{}}`)
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
	}))
	defer srv.Close()

	d := newTestDropbox(t, srv.URL, nil)
	if err := d.Move("tmp", "snapshot.stb"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, ok := store["/ssh-tool/snapshot.stb"]; !ok {
		t.Fatal("destination not written")
	}
	if _, ok := store["/ssh-tool/tmp"]; ok {
		t.Fatal("source not removed")
	}
}

// TestDropboxRefreshOn401 verifies an expired access token self-heals: the
// first request 401s, do() calls refresh, the retry carries the new bearer.
func TestDropboxRefreshOn401(t *testing.T) {
	calls := 0
	refreshed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		auth := r.Header.Get("Authorization")
		if auth != "Bearer access-token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error_summary":"expired_access_token/"}`)
			return
		}
		io.WriteString(w, `{"name":"ok"}`)
	}))
	defer srv.Close()

	d := newTestDropbox(t, srv.URL, func() (string, error) {
		refreshed = true
		return "access-token-1", nil
	})
	if err := d.Put("snapshot.stb", []byte("x")); err != nil {
		t.Fatalf("Put after refresh: %v", err)
	}
	if !refreshed {
		t.Fatal("refresh was not invoked on 401")
	}
	if calls != 2 {
		t.Fatalf("want 2 upstream calls (401 + retry), got %d", calls)
	}
}

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

// TestRefreshOmitsSecret asserts the refresh token POST carries client_id +
// refresh_token and NO client_secret (PKCE public client).
func TestRefreshOmitsSecret(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		io.WriteString(w, `{"access_token":"a","expires_in":14400}`)
	}))
	defer srv.Close()

	ep := OAuthEndpoints{TokenURL: srv.URL}
	if _, err := Refresh(t.Context(), ep, "app-key", "", "refresh-tok"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if form.Get("client_secret") != "" {
		t.Fatal("client_secret present in PKCE refresh - must be absent")
	}
	if form.Get("client_id") != "app-key" || form.Get("refresh_token") != "refresh-tok" {
		t.Fatalf("refresh form missing client_id/refresh_token: %v", form)
	}
	if form.Get("grant_type") != "refresh_token" {
		t.Fatalf("wrong grant_type: %q", form.Get("grant_type"))
	}
}

// TestRefreshIncludesSecretWhenSet verifies the Google path: a non-empty client
// secret IS sent in the refresh (Google requires it even for a Desktop app).
func TestRefreshIncludesSecretWhenSet(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		io.WriteString(w, `{"access_token":"a","expires_in":3600}`)
	}))
	defer srv.Close()

	ep := OAuthEndpoints{TokenURL: srv.URL}
	if _, err := Refresh(t.Context(), ep, "client-id", "goog-secret", "refresh-tok"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if form.Get("client_secret") != "goog-secret" {
		t.Fatalf("client_secret not forwarded: %q", form.Get("client_secret"))
	}
}

// TestBuildAuthURL sanity-checks the authorization URL carries the PKCE params
// and Dropbox's offline quirk.
func TestBuildAuthURL(t *testing.T) {
	raw := buildAuthURL(DropboxEndpoints, "app-key", "http://127.0.0.1:5555/callback", "chal", "st4te")
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
	} {
		if q.Get(k) != want {
			t.Errorf("auth URL %s=%q want %q", k, q.Get(k), want)
		}
	}
	if !strings.Contains(q.Get("scope"), "files.content.write") {
		t.Errorf("scope missing write: %q", q.Get("scope"))
	}
}
