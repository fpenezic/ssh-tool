package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

func TestParseCatalogLink(t *testing.T) {
	l := parseCatalogLink([]string{"--x", "ssh-tool://catalog/add?url=https%3A%2F%2Fcatalog.example.com&code=SDKGM-F02SJ"})
	if l == nil || l.URL != "https://catalog.example.com" || l.Code != "SDKGM-F02SJ" {
		t.Fatalf("%+v", l)
	}
	for _, bad := range []string{"ssh-tool://import?source=x", "ssh-tool://catalog/add?url=x", "https://catalog/add?url=a&code=b"} {
		if parseCatalogLink([]string{bad}) != nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

// enrollServer is a fake catalog with one valid code.
type enrollServer struct {
	mu        sync.Mutex
	major     int
	redeemed  bool
	limited   bool
	inspects  int
	redeems   int
	revoked   []string
	devices   []string
	refreshes int
}

func newEnrollApp(t *testing.T) (*App, *enrollServer, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v := creds.NewVault()
	v.SetPath(filepath.Join(dir, "vault.enc"))
	if err := v.Init("pass", false); err != nil {
		t.Fatal(err)
	}
	es := &enrollServer{major: 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		es.mu.Lock()
		defer es.mu.Unlock()
		_ = r.ParseForm()
		if es.limited && (r.URL.Path == "/oauth/enrollment/inspect" || r.URL.Path == "/oauth/token") {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":"rate_limited"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/meta":
			fmt.Fprintf(w, `{"server":{"name":"Team catalog"},"protocol":{"major":%d,"revision":1},"auth":{"authorization_endpoint":"http://%[2]s/a","token_endpoint":"http://%[2]s/oauth/token","revocation_endpoint":"http://%[2]s/oauth/revoke","client_id":"ssh-tool"}}`, es.major, r.Host)
		case "/oauth/enrollment/inspect":
			es.inspects++
			if es.redeemed || catalog.NormaliseCode(r.Form.Get("code")) != catalog.NormaliseCode("GOODC0DE") {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			fmt.Fprint(w, `{"user":{"name":"Alice","email":"alice@example.com"},"expires_in":241}`)
		case "/oauth/token":
			switch r.Form.Get("grant_type") {
			case "urn:ssh-tool:params:oauth:grant-type:enrollment":
				es.redeems++
				if es.redeemed || catalog.NormaliseCode(r.Form.Get("code")) != catalog.NormaliseCode("GOODC0DE") {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":"invalid_grant"}`)
					return
				}
				es.redeemed = true
				es.devices = append(es.devices, r.Form.Get("device_name"))
				fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-%d"}`, es.redeems)
			case "refresh_token":
				es.refreshes++
				fmt.Fprint(w, `{"access_token":"at2","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-r"}`)
			}
		case "/oauth/revoke":
			es.revoked = append(es.revoked, r.Form.Get("token"))
		case "/api/v1/whoami":
			fmt.Fprint(w, `{"subject":"dev:alice","name":"Alice","groups":["ops"],"admin":false,"via":"local"}`)
		case "/api/v1/changes":
			fmt.Fprint(w, `{"reset":true,"cursor":"c","items":[]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &App{db: db, vault: v}, es, srv.URL
}

func TestCatalogEnrollFlow(t *testing.T) {
	a, es, base := newEnrollApp(t)
	// Inspect: nothing created, nothing used up; the URL as received.
	p, err := a.CatalogEnrollInspect(base+"/", "goodc-0de", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ServerName != "Team catalog" || p.UserName != "Alice" || p.UserEmail != "alice@example.com" || p.BaseURL != base+"/" || p.ExistingID != "" {
		t.Fatalf("preview: %+v", p)
	}
	if p.NormalisedURL != base {
		t.Fatalf("normalised url: %q", p.NormalisedURL)
	}
	if cats, _ := a.db.ListCatalogs(); len(cats) != 0 || es.redeems != 0 {
		t.Fatalf("inspect created or redeemed something: %d catalogs, %d redeems", len(cats), es.redeems)
	}
	// Confirm: redeemed once, attached, signed in, device name sent.
	info, err := a.CatalogEnrollConfirm(base, "goodc-0de", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Team catalog" || es.redeems != 1 || len(es.devices) != 1 || es.devices[0] == "" {
		t.Fatalf("confirm: %+v redeems %d devices %v", info, es.redeems, es.devices)
	}
	if has, _ := a.hasCatalogToken(info.ID); !has {
		t.Fatal("no token stored")
	}
	cat, _ := a.db.GetCatalog(info.ID)
	if cat.AccountName != "Alice" {
		t.Fatalf("account not recorded: %+v", cat)
	}
	// A used code: refused at inspect, with what to do.
	if _, err := a.CatalogEnrollInspect(base, "goodc-0de", ""); !errors.Is(err, catalog.ErrEnrollmentInvalid) {
		t.Fatalf("used code: %v", err)
	}
	// Already attached: inspect says so; confirm without replace refuses.
	es.mu.Lock()
	es.redeemed = false
	es.mu.Unlock()
	p, err = a.CatalogEnrollInspect(base, "GOODC0DE", "")
	if err != nil || p.ExistingID != info.ID || p.ExistingAccount != "Alice" {
		t.Fatalf("existing: %+v %v", p, err)
	}
	if _, err := a.CatalogEnrollConfirm(base, "GOODC0DE", false, ""); err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("no replace: %v", err)
	}
	if es.redeems != 1 {
		t.Fatal("the code was redeemed without replace")
	}
	// Replace: the old session is revoked, the new token kept, same row.
	if _, err := a.CatalogEnrollConfirm(base, "GOODC0DE", true, ""); err != nil {
		t.Fatal(err)
	}
	if len(es.revoked) == 0 {
		t.Fatal("the replaced session was not revoked")
	}
	if cats, _ := a.db.ListCatalogs(); len(cats) != 1 {
		t.Fatalf("%d catalogs after replace", len(cats))
	}
}

func TestCatalogEnrollErrors(t *testing.T) {
	a, es, base := newEnrollApp(t)
	if _, err := a.CatalogEnrollInspect(base, "WRONG", ""); !errors.Is(err, catalog.ErrEnrollmentInvalid) {
		t.Fatalf("invalid code: %v", err)
	}
	es.limited = true
	if _, err := a.CatalogEnrollInspect(base, "GOODC0DE", ""); err == nil || !strings.Contains(err.Error(), "42 s") {
		t.Fatalf("rate limited inspect: %v", err)
	}
	if _, err := a.CatalogEnrollConfirm(base, "GOODC0DE", false, ""); err == nil || !strings.Contains(err.Error(), "42 s") {
		t.Fatalf("rate limited redeem: %v", err)
	}
	es.limited = false
	es.major = 2
	if _, err := a.CatalogEnrollInspect(base, "GOODC0DE", ""); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("protocol major: %v", err)
	}
	if es.inspects != 1 {
		t.Fatalf("inspected %d times; a wrong protocol must stop before the code is sent", es.inspects)
	}
	if cats, _ := a.db.ListCatalogs(); len(cats) != 0 {
		t.Fatal("a failed enrollment created a catalog")
	}
	// Locked vault: nothing redeemed.
	es.major = 1
	a.vault.Lock(false)
	if _, err := a.CatalogEnrollConfirm(base, "GOODC0DE", false, ""); err == nil || es.redeems != 0 {
		t.Fatalf("locked vault: %v, redeems %d", err, es.redeems)
	}
}
