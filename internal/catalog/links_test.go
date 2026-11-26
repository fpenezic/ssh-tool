package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExternalLink(t *testing.T) {
	links := map[string]string{
		"bitwarden": "https://vault.example.com/#/vault?itemId={cipher_id}",
		"plain":     "http://vault.example.com/{id}",
		"js":        "{id}",
		"host":      "https://{id}",
		"nohost":    "https:///items/{id}",
		"user":      "https://{id}@vault.example.com/",
		"two":       "https://x.example.com/p/{project_id}/s?k={key}",
	}
	cases := []struct {
		name, backend, ref, want string
	}{
		{"filled", "bitwarden", `{"cipher_id":"c-1","field":"password"}`, "https://vault.example.com/#/vault?itemId=c-1"},
		{"backend case", "Bitwarden", `{"cipher_id":"c-1"}`, "https://vault.example.com/#/vault?itemId=c-1"},
		{"encoded", "bitwarden", `{"cipher_id":"a b&c=d#e/f"}`, "https://vault.example.com/#/vault?itemId=a%20b%26c%3Dd%23e%2Ff"},
		{"missing field", "bitwarden", `{"field":"password"}`, ""},
		{"not a string", "bitwarden", `{"cipher_id":7}`, ""},
		{"no template", "infisical", `{"key":"k"}`, ""},
		{"http refused", "plain", `{"id":"x"}`, ""},
		{"scheme from the ref refused", "js", `{"id":"javascript:alert(1)"}`, ""},
		{"no host", "nohost", `{"id":"x"}`, ""},
		{"host filled", "host", `{"id":"vault.example.com"}`, "https://vault.example.com"},
		{"userinfo refused", "user", `{"id":"me"}`, ""},
		{"two fields", "two", `{"project_id":"p1","key":"K"}`, "https://x.example.com/p/p1/s?k=K"},
		{"bad json", "bitwarden", `{`, ""},
		{"empty ref", "bitwarden", ``, ""},
	}
	for _, c := range cases {
		if got := ExternalLink(links, c.backend, c.ref); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := ExternalLink(nil, "bitwarden", `{"cipher_id":"c"}`); got != "" {
		t.Errorf("nil links: got %q", got)
	}
}

func TestClientConfig(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"external_links":{"bitwarden":"https://v.example.com/#/vault?itemId={cipher_id}"}}`))
		} else {
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no"}}`))
		}
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "ssh-tool/test", &memTokens{})
	if err != nil {
		t.Fatal(err)
	}
	c.SetAccessToken("tok", time.Now().Add(time.Hour))
	cfg, err := c.Config(context.Background())
	if err != nil || cfg.ExternalLinks["bitwarden"] == "" {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	status = http.StatusNotFound
	cfg, err = c.Config(context.Background())
	if err != nil || len(cfg.ExternalLinks) != 0 {
		t.Fatalf("404 config: %+v %v", cfg, err)
	}
}
