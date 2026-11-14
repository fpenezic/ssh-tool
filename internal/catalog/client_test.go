package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memTokens struct {
	mu sync.Mutex
	rt string
}

func (m *memTokens) Load() (string, error) { m.mu.Lock(); defer m.mu.Unlock(); return m.rt, nil }
func (m *memTokens) Save(s string) error   { m.mu.Lock(); m.rt = s; m.mu.Unlock(); return nil }

// fakeServer is the slice of the catalog protocol the client uses: meta,
// token (refresh with rotation and reuse detection), whoami, changes.
type fakeServer struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	access   string
	refresh  string
	used     map[string]bool
	refreshN int32
	pages    []Feed
	busy     int // answer this many requests with 429 first
	sinceLog []string
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, access: "at-1", refresh: "rt-1", used: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"server":{"name":"Example catalog","version":"2.0.0"},"protocol":{"major":1,"revision":1},
			"auth":{"authorization_endpoint":"%[1]s/oauth/authorize","token_endpoint":"%[1]s/oauth/token",
			"revocation_endpoint":"%[1]s/oauth/revoke","client_id":"ssh-tool","methods":["local"]},"future":{"x":1}}`, f.srv.URL)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		atomic.AddInt32(&f.refreshN, 1)
		f.mu.Lock()
		defer f.mu.Unlock()
		rt := r.PostForm.Get("refresh_token")
		if f.used[rt] || rt != f.refresh {
			// Reuse ends the session (PROTOCOL 10.2).
			f.refresh = ""
			w.WriteHeader(400)
			io := `{"error":"invalid_grant","error_description":"refresh token already used"}`
			w.Write([]byte(io))
			return
		}
		f.used[rt] = true
		n := len(f.used) + 1
		f.refresh = fmt.Sprintf("rt-%d", n)
		f.access = fmt.Sprintf("at-%d", n)
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"refresh_token":%q}`, f.access, f.refresh)
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			ok := r.Header.Get("Authorization") == "Bearer "+f.access
			f.mu.Unlock()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				w.WriteHeader(401)
				w.Write([]byte(`{"error":{"code":"unauthenticated","message":"Token expired."}}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/whoami", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"subject":"s-1","name":"Alice","email":null,"groups":["ops"],"admin":false,"via":"local","new_field":true}`))
	}))
	mux.HandleFunc("GET /api/v1/changes", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.busy > 0 {
			f.busy--
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			return
		}
		since := r.URL.Query().Get("since")
		f.sinceLog = append(f.sinceLog, since)
		idx := 0
		if since != "" {
			fmt.Sscanf(since, "c%d", &idx)
		}
		if idx >= len(f.pages) {
			json.NewEncoder(w).Encode(Feed{Cursor: since, Items: []Item{}})
			return
		}
		json.NewEncoder(w).Encode(f.pages[idx])
	}))
	mux.HandleFunc("GET /api/v1/folders/{uuid}", auth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"code":"not_found","message":"No such record."}}`))
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) expire() { f.mu.Lock(); f.access = "rotated-away"; f.mu.Unlock() }

func newTestClient(t *testing.T, f *fakeServer, rt string) (*Client, *memTokens) {
	toks := &memTokens{rt: rt}
	c, err := NewClient(f.srv.URL, "ssh-tool/test", toks)
	if err != nil {
		t.Fatal(err)
	}
	return c, toks
}

func TestValidateBaseURL(t *testing.T) {
	for in, ok := range map[string]bool{
		"https://catalog.example.com":  true,
		"https://catalog.example.com/": true,
		"http://127.0.0.1:18080":       true,
		"http://[::1]:18080":           true,
		"http://localhost:18080":       true,
		"http://catalog.example.com":   false,
		"ftp://catalog.example.com":    false,
		"catalog.example.com":          false,
		"https://x.example.com/?a=1":   false,
	} {
		got, err := ValidateBaseURL(in)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v", in, err)
		}
		if err == nil && strings.HasSuffix(got, "/") {
			t.Errorf("%s: trailing slash kept: %s", in, got)
		}
	}
}

func TestMetaChecksProtocolAndIgnoresUnknown(t *testing.T) {
	f := newFakeServer(t)
	c, _ := newTestClient(t, f, "")
	m, err := c.Meta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Server.Name != "Example catalog" || m.Auth.ClientID != "ssh-tool" {
		t.Fatalf("meta: %+v", m)
	}
}

// No access token yet: the first call refreshes with the stored token,
// rotates it, and the request goes through.
func TestRefreshOnFirstUseRotatesToken(t *testing.T) {
	f := newFakeServer(t)
	c, toks := newTestClient(t, f, "rt-1")
	w, err := c.WhoAmI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.Subject != "s-1" {
		t.Fatalf("whoami: %+v", w)
	}
	if rt, _ := toks.Load(); rt != "rt-2" {
		t.Fatalf("rotated refresh token not stored: %q", rt)
	}
}

// A 401 on a live token refreshes once and retries (PROTOCOL 10.3).
func TestUnauthorizedRefreshesOnceAndRetries(t *testing.T) {
	f := newFakeServer(t)
	c, _ := newTestClient(t, f, "rt-1")
	c.SetAccessToken("at-1", time.Now().Add(time.Hour))
	f.expire()
	if _, err := c.WhoAmI(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&f.refreshN); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
}

// Many requests meeting an expired token together refresh exactly once:
// presenting the same refresh token twice would end the session.
func TestConcurrentRequestsRefreshOnce(t *testing.T) {
	f := newFakeServer(t)
	c, _ := newTestClient(t, f, "rt-1")
	c.SetAccessToken("at-1", time.Now().Add(time.Hour))
	f.expire()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.WhoAmI(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
	}
	if n := atomic.LoadInt32(&f.refreshN); n != 1 {
		t.Fatalf("%d refreshes, want exactly 1", n)
	}
}

func TestInvalidGrantSignsOut(t *testing.T) {
	f := newFakeServer(t)
	c, toks := newTestClient(t, f, "rt-stale")
	_, err := c.WhoAmI(context.Background())
	if !errors.Is(err, ErrSignedOut) {
		t.Fatalf("want ErrSignedOut, got %v", err)
	}
	if rt, _ := toks.Load(); rt != "" {
		t.Errorf("dead refresh token kept: %q", rt)
	}
	// Never signed in at all.
	c2, _ := newTestClient(t, f, "")
	if _, err := c2.WhoAmI(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("no token: %v", err)
	}
}

func TestRetryAfterAndAPIErrors(t *testing.T) {
	f := newFakeServer(t)
	f.busy = 1
	c, _ := newTestClient(t, f, "rt-1")
	_, err := c.Changes(context.Background(), "", 0)
	var ra *RetryAfterError
	if !errors.As(err, &ra) || ra.Wait != 7*time.Second || ra.Status != 429 {
		t.Fatalf("want RetryAfterError 7s, got %v", err)
	}
	err = c.do(context.Background(), http.MethodGet, "/api/v1/folders/x", nil, nil, true)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" {
		t.Fatalf("want not_found APIError, got %v", err)
	}
	if retryAfter("") != time.Minute || retryAfter("junk") != time.Minute || retryAfter("0") != 0 {
		t.Error("retryAfter defaults")
	}
}

func TestChangesPassesCursorAndDecodesItems(t *testing.T) {
	f := newFakeServer(t)
	f.pages = []Feed{
		{Reset: true, More: true, Cursor: "c1", Items: []Item{
			{Kind: "folders", Op: "upsert", Record: json.RawMessage(`{"uuid":"f1","rev":1,"parent_uuid":null,"name":"Space","access":"read","doc_md":"x"}`)},
			{Kind: "gadgets", Op: "upsert", Record: json.RawMessage(`{"uuid":"g1"}`)},
		}},
		{Reset: true, More: false, Cursor: "c2", Items: []Item{
			{Kind: "forwards", Op: "delete", UUID: "p1", Rev: 3, Reason: "deleted"},
		}},
	}
	c, _ := newTestClient(t, f, "rt-1")
	p1, err := c.Changes(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if !p1.Reset || !p1.More || p1.Cursor != "c1" || len(p1.Items) != 2 {
		t.Fatalf("page 1: %+v", p1)
	}
	var fo Folder
	if err := json.Unmarshal(p1.Items[0].Record, &fo); err != nil || fo.Name != "Space" || fo.ParentUUID != nil {
		t.Fatalf("folder: %+v %v", fo, err)
	}
	if KnownKind(p1.Items[1].Kind) {
		t.Error("unknown kind reported as known")
	}
	p2, err := c.Changes(context.Background(), p1.Cursor, 500)
	if err != nil || p2.More || p2.Items[0].Op != "delete" || p2.Items[0].Rev != 3 {
		t.Fatalf("page 2: %+v %v", p2, err)
	}
	if got := f.sinceLog; len(got) != 2 || got[0] != "" || got[1] != "c1" {
		t.Fatalf("cursors sent: %v", got)
	}
}

func TestSkeletonAndProtocolHelpers(t *testing.T) {
	var f Folder
	_ = json.Unmarshal([]byte(`{"uuid":"a","access":"skeleton","doc_md":null}`), &f)
	if !f.Skeleton() || f.DocMD != nil {
		t.Fatalf("skeleton: %+v", f)
	}
	for in, want := range map[string]string{"": "ssh", " SSH ": "ssh", "Local": "local", "web": "web"} {
		c := Connection{Protocol: in}
		if c.ProtocolOrDefault() != want {
			t.Errorf("%q -> %q", in, c.ProtocolOrDefault())
		}
	}
}

// SignIn drives the browser flow against the fake server's token endpoint.
func TestSignInStoresTokens(t *testing.T) {
	f := newFakeServer(t)
	// The authorize step: redirect straight back with a code. The fake
	// token endpoint here only speaks refresh, so accept any code too.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code_verifier") == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"access_token":"at-x","expires_in":3600,"refresh_token":"rt-x"}`))
	})
	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		f.srv.Config.Handler.ServeHTTP(w, r)
	})
	authSrv := httptest.NewServer(mux)
	defer authSrv.Close()
	toks := &memTokens{}
	c, err := NewClient(authSrv.URL, "ssh-tool/test", toks)
	if err != nil {
		t.Fatal(err)
	}
	// Point meta at the auth server's endpoints.
	c.meta = &Meta{}
	c.meta.Protocol.Major = 1
	c.meta.Auth.AuthorizationEndpoint = authSrv.URL + "/oauth/authorize"
	c.meta.Auth.TokenEndpoint = authSrv.URL + "/oauth/token"
	c.meta.Auth.ClientID = "ssh-tool"
	err = c.SignIn(context.Background(), func(u string) {
		go func() {
			if resp, err := http.Get(u); err == nil {
				resp.Body.Close()
			}
		}()
	})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if rt, _ := toks.Load(); rt != "rt-x" {
		t.Fatalf("refresh token %q", rt)
	}
	if c.access != "at-x" {
		t.Fatalf("access token %q", c.access)
	}
}
