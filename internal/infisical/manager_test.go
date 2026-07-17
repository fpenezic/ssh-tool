package infisical

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ssh-tool/internal/store"
)

// fakeSecrets implements SecretReader; unlocked togglable.
type fakeSecrets struct {
	unlocked bool
}

func (s *fakeSecrets) Get(string) (string, bool, error) { return "", false, nil }
func (s *fakeSecrets) Unlocked() bool                    { return s.unlocked }

// plainSealer is an identity sealer (vault crypto is exercised in creds tests;
// here we only need the cache round-trip).
type plainSealer struct{}

func (plainSealer) Seal(b []byte) ([]byte, error) { return append([]byte{}, b...), nil }
func (plainSealer) Open(b []byte) ([]byte, error) { return append([]byte{}, b...), nil }

// fakeInfisical is an httptest handler standing in for a real Infisical server.
// It counts logins (to prove token caching + re-login) and can be flipped
// offline (every request 500s) or made to reject the first token (401 once).
type fakeInfisical struct {
	logins     int32
	reads      int32
	offline    atomic.Bool
	expiresIn  int
	reject401  atomic.Bool // when set, the NEXT read returns 401 (then re-arms off)
	secretVal  string
	knownToken atomic.Value // string: the token issued on the last login
}

func (f *fakeInfisical) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/universal-auth/login", func(w http.ResponseWriter, r *http.Request) {
		if f.offline.Load() {
			w.WriteHeader(500)
			return
		}
		n := atomic.AddInt32(&f.logins, 1)
		tok := "tok-" + itoa(int(n))
		f.knownToken.Store(tok)
		exp := f.expiresIn
		if exp == 0 {
			exp = 2592000
		}
		_ = json.NewEncoder(w).Encode(tokenResp{AccessToken: tok, ExpiresIn: exp, TokenType: "Bearer"})
	})
	mux.HandleFunc("/api/v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		if !f.authOK(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(workspacesResp{Workspaces: []project{
			{ID: "proj1", Name: "Infra", Environments: []projectEnv{{Name: "Production", Slug: "prod", ID: "e1"}}},
		}})
	})
	mux.HandleFunc("/api/v3/secrets/raw/", func(w http.ResponseWriter, r *http.Request) {
		if f.offline.Load() {
			w.WriteHeader(500)
			return
		}
		if f.reject401.CompareAndSwap(true, false) {
			w.WriteHeader(401)
			return
		}
		if !f.authOK(w, r) {
			return
		}
		atomic.AddInt32(&f.reads, 1)
		key := strings.TrimPrefix(r.URL.Path, "/api/v3/secrets/raw/")
		_ = json.NewEncoder(w).Encode(singleSecretResp{Secret: rawSecret{SecretKey: key, SecretValue: f.secretVal}})
	})
	mux.HandleFunc("/api/v3/secrets/raw", func(w http.ResponseWriter, r *http.Request) {
		if f.offline.Load() {
			w.WriteHeader(500)
			return
		}
		if !f.authOK(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(secretsResp{Secrets: []rawSecret{
			{SecretKey: "API_KEY", SecretValue: "v1", SecretPath: "/"},
			{SecretKey: "password", SecretValue: f.secretVal, SecretPath: "/cloudflare"},
		}})
	})
	return mux
}

func (f *fakeInfisical) authOK(w http.ResponseWriter, r *http.Request) bool {
	want, _ := f.knownToken.Load().(string)
	if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); want == "" || got != want {
		w.WriteHeader(401)
		return false
	}
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func newTestManager(t *testing.T, fake *fakeInfisical) (*Manager, store.InfisicalServer) {
	t.Helper()
	ts := httptest.NewServer(fake.handler())
	t.Cleanup(ts.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv, err := db.CreateInfisicalServer(store.InfisicalServer{
		Name:      "inf",
		ServerURL: ts.URL,
		APIKeyRef: "apikey-cred",
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecrets{unlocked: true}
	lookup := func(string) (Credentials, error) {
		return Credentials{ClientID: "id", ClientSecret: "sec"}, nil
	}
	m := NewManagerWithClient(db, secrets, plainSealer{}, lookup, func(s store.InfisicalServer) *Client {
		return NewClient(s.ServerURL)
	}, t.TempDir())
	return m, *srv
}

func TestResolveReadsSecret(t *testing.T) {
	fake := &fakeInfisical{secretVal: "hunter2"}
	m, srv := newTestManager(t, fake)

	ref := store.InfisicalRef{ServerID: srv.ID, ProjectID: "proj1", Environment: "prod", SecretPath: "/cloudflare", Key: "password"}
	val, fresh, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "hunter2" {
		t.Fatalf("value: got %q", val)
	}
	if fresh != FreshFetched {
		t.Fatalf("freshness: got %q, want fetched", fresh)
	}
	// A second resolve reuses the cached token (no re-login).
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got := atomic.LoadInt32(&fake.logins); got != 1 {
		t.Fatalf("logins: got %d, want 1 (token should be cached)", got)
	}
}

func TestResolveVaultLocked(t *testing.T) {
	fake := &fakeInfisical{secretVal: "x"}
	m, srv := newTestManager(t, fake)
	m.secrets.(*fakeSecrets).unlocked = false
	_, _, err := m.Resolve(store.InfisicalRef{ServerID: srv.ID, ProjectID: "p", Environment: "prod", Key: "k"})
	if err != ErrVaultLocked {
		t.Fatalf("want ErrVaultLocked, got %v", err)
	}
}

func TestResolveReloginOn401(t *testing.T) {
	fake := &fakeInfisical{secretVal: "v"}
	m, srv := newTestManager(t, fake)
	ref := store.InfisicalRef{ServerID: srv.ID, ProjectID: "proj1", Environment: "prod", SecretPath: "/", Key: "API_KEY"}

	// Prime a token.
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatal(err)
	}
	// Force the next read to 401; Resolve should re-login and retry.
	fake.reject401.Store(true)
	val, _, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve after 401: %v", err)
	}
	if val != "v" {
		t.Fatalf("value after re-login: got %q", val)
	}
	if got := atomic.LoadInt32(&fake.logins); got != 2 {
		t.Fatalf("logins: got %d, want 2 (one initial + one re-login)", got)
	}
}

func TestResolveOfflineServesSealedCache(t *testing.T) {
	fake := &fakeInfisical{secretVal: "cached-value"}
	m, srv := newTestManager(t, fake)
	ref := store.InfisicalRef{ServerID: srv.ID, ProjectID: "proj1", Environment: "prod", SecretPath: "/", Key: "API_KEY"}

	// First resolve seals the value.
	if _, fresh, err := m.Resolve(ref); err != nil || fresh != FreshFetched {
		t.Fatalf("prime: fresh=%v err=%v", fresh, err)
	}
	// Server goes offline; the sealed cache is served, marked stale.
	fake.offline.Store(true)
	m.Forget() // drop the token so we hit login (which also fails offline)
	val, fresh, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("offline Resolve should serve cache, got err: %v", err)
	}
	if val != "cached-value" {
		t.Fatalf("cached value: got %q", val)
	}
	if fresh != FreshStale {
		t.Fatalf("freshness: got %q, want stale", fresh)
	}
}

func TestResolveOfflineNoCacheFails(t *testing.T) {
	fake := &fakeInfisical{secretVal: "x"}
	fake.offline.Store(true)
	m, srv := newTestManager(t, fake)
	_, _, err := m.Resolve(store.InfisicalRef{ServerID: srv.ID, ProjectID: "p", Environment: "prod", Key: "k"})
	if err == nil {
		t.Fatal("offline with no cache should error")
	}
}

func TestBrowseTree(t *testing.T) {
	fake := &fakeInfisical{secretVal: "s"}
	m, srv := newTestManager(t, fake)
	groups, err := m.Browse(srv.ID)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "Infra" || groups[0].ProjectID != "proj1" {
		t.Fatalf("groups: %+v", groups)
	}
	if len(groups[0].Environments) != 1 || groups[0].Environments[0].Slug != "prod" {
		t.Fatalf("environments: %+v", groups[0].Environments)
	}
	entries := groups[0].Environments[0].Entries
	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}
	// Entries sort by path then key: "/API_KEY" then "/cloudflare/password".
	if entries[0].Key != "API_KEY" || entries[0].Path != "/" {
		t.Fatalf("entry0: %+v", entries[0])
	}
	if entries[1].Key != "password" || entries[1].Path != "/cloudflare" {
		t.Fatalf("entry1: %+v", entries[1])
	}
}

func TestTestLoginForcesFreshLogin(t *testing.T) {
	fake := &fakeInfisical{secretVal: "s"}
	m, srv := newTestManager(t, fake)
	// Prime a token via a resolve.
	_, _, _ = m.Resolve(store.InfisicalRef{ServerID: srv.ID, ProjectID: "proj1", Environment: "prod", SecretPath: "/", Key: "API_KEY"})
	if err := m.TestLogin(srv.ID); err != nil {
		t.Fatalf("TestLogin: %v", err)
	}
	// TestLogin drops the cached token and logs in again.
	if got := atomic.LoadInt32(&fake.logins); got != 2 {
		t.Fatalf("logins: got %d, want 2", got)
	}
}

func TestForgetClearsTokens(t *testing.T) {
	fake := &fakeInfisical{secretVal: "s"}
	m, srv := newTestManager(t, fake)
	ref := store.InfisicalRef{ServerID: srv.ID, ProjectID: "proj1", Environment: "prod", SecretPath: "/", Key: "API_KEY"}
	_, _, _ = m.Resolve(ref)
	m.Forget()
	// After Forget the next resolve must log in again (2 total).
	_, _, _ = m.Resolve(ref)
	if got := atomic.LoadInt32(&fake.logins); got != 2 {
		t.Fatalf("logins after Forget+Resolve: got %d, want 2", got)
	}
}
