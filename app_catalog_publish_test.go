package main

import (
	"encoding/json"
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

type publishApp struct {
	a           *App
	cat         *store.Catalog
	folder      string
	conn        string
	cred        string
	mu          sync.Mutex
	mode        string // ok | refuse | drop | unauth
	posts       int
	lastRequest catalog.PublishRequest
}

func newPublishApp(t *testing.T) *publishApp {
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
	pa := &publishApp{a: &App{db: db, vault: v}, mode: "ok"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/meta":
			fmt.Fprintf(w, `{"server":{"name":"Fake"},"protocol":{"major":1,"revision":1},"auth":{"authorization_endpoint":"http://%[1]s/a","token_endpoint":"http://%[1]s/oauth/token","client_id":"ssh-tool"}}`, r.Host)
		case "/oauth/token":
			fmt.Fprint(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"rt"}`)
		case "/api/v1/changes":
			fmt.Fprint(w, `{"reset":false,"cursor":"c","items":[]}`)
		case "/api/v1/publish":
			pa.mu.Lock()
			pa.posts++
			mode := pa.mode
			var req catalog.PublishRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			pa.lastRequest = req
			pa.mu.Unlock()
			switch mode {
			case "unauth":
				w.WriteHeader(401)
			case "drop":
				hj, _ := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				c.Close()
			case "refuse":
				w.WriteHeader(422)
				fmt.Fprintf(w, `{"error":{"code":"validation","message":"publish refused","details":{"records":[{"kind":"connections","uuid":%q,"code":"validation","path":"hostname","message":"too long"}]}}}`,
					req.Connections[0].UUID)
			default:
				var recs []map[string]any
				for _, f := range req.Folders {
					recs = append(recs, map[string]any{"kind": "folders", "uuid": f.UUID, "rev": 1})
				}
				for _, c := range req.Connections {
					recs = append(recs, map[string]any{"kind": "connections", "uuid": c.UUID, "rev": 1})
				}
				for _, s := range req.Slots {
					recs = append(recs, map[string]any{"kind": "slots", "uuid": s.UUID, "rev": 1})
				}
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(map[string]any{"records": recs, "warnings": []any{}})
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	cat, err := db.CreateCatalog(store.NewCatalog{Name: "Team", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	pa.cat = cat
	if err := (vaultTokens{a: pa.a, id: cat.ID}).Save("rt"); err != nil {
		t.Fatal(err)
	}
	cr, err := db.CreateCredential(store.NewCredential{Name: "admin", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	if err != nil {
		t.Fatal(err)
	}
	f, err := db.CreateFolder(store.NewFolder{Name: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CreateConnection(store.NewConnection{Name: "web-1", Hostname: "web-1.example.com", FolderID: &f.ID,
		Overrides: store.InheritableSettings{AuthRef: &cr.ID}})
	if err != nil {
		t.Fatal(err)
	}
	pa.folder, pa.conn, pa.cred = f.ID, c.ID, cr.ID
	return pa
}

func (pa *publishApp) postCount() int {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	return pa.posts
}

func (pa *publishApp) setMode(m string) {
	pa.mu.Lock()
	pa.mode = m
	pa.mu.Unlock()
}

func (pa *publishApp) in() CatalogPublishInput {
	return CatalogPublishInput{CatalogID: pa.cat.ID, FolderID: pa.folder}
}

func (pa *publishApp) pending(t *testing.T) bool {
	t.Helper()
	busy, err := pa.a.db.PendingPublishForLocal([]string{pa.folder, pa.conn})
	if err != nil {
		t.Fatal(err)
	}
	return busy
}

func TestCatalogPublishConvertsInPlace(t *testing.T) {
	pa := newPublishApp(t)
	plan, err := pa.a.CatalogPublishPreview(pa.in())
	if err != nil {
		t.Fatal(err)
	}
	if pa.postCount() != 0 {
		t.Fatal("the preview sent something")
	}
	if len(plan.Slots) != 1 || plan.Slots[0].BindName != "admin" {
		t.Fatalf("preview slots: %+v", plan.Slots)
	}
	res, err := pa.a.CatalogPublish(pa.in())
	if err != nil || !res.Published {
		t.Fatalf("publish: %+v %v", res, err)
	}
	c, _ := pa.a.db.GetConnection(pa.conn)
	if c.CatalogID == nil || c.RemoteUUID == nil {
		t.Fatalf("connection not converted: %+v", c)
	}
	if c.Overrides.AuthRef == nil || *c.Overrides.AuthRef != pa.cred {
		t.Fatalf("the publisher's credential is not what the connection uses now: %+v", c.Overrides.AuthRef)
	}
	if pa.pending(t) {
		t.Fatal("pending rows left")
	}
	pa.mu.Lock()
	sent := fmt.Sprint(pa.lastRequest)
	pa.mu.Unlock()
	if strings.Contains(sent, pa.cred) {
		t.Fatal("the request carried the local credential id")
	}
	// A converted folder cannot be published again.
	if _, err := pa.a.CatalogPublishPreview(pa.in()); err == nil {
		t.Fatal("preview of a catalog folder allowed")
	}
}

func TestCatalogPublishRefusedMapsToRows(t *testing.T) {
	pa := newPublishApp(t)
	pa.setMode("refuse")
	res, err := pa.a.CatalogPublish(pa.in())
	if err != nil {
		t.Fatal(err)
	}
	if res.Published || res.Unknown || len(res.Problems) != 1 {
		t.Fatalf("result: %+v", res)
	}
	p := res.Problems[0]
	if p.LocalID != pa.conn || p.Name != "web-1" || p.Field != "hostname" || p.Path != "Mine/web-1" {
		t.Fatalf("problem not mapped to the row: %+v", p)
	}
	if c, _ := pa.a.db.GetConnection(pa.conn); c.CatalogID != nil {
		t.Fatal("converted although refused")
	}
	if pa.pending(t) {
		t.Fatal("pending rows kept after a refusal")
	}
}

// No answer: the outcome is open, the rows stay local and pending until a
// sync shows whether the publish arrived.
func TestCatalogPublishWithoutAnswerStaysPending(t *testing.T) {
	pa := newPublishApp(t)
	pa.setMode("drop")
	res, err := pa.a.CatalogPublish(pa.in())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unknown || res.Published {
		t.Fatalf("result: %+v", res)
	}
	if c, _ := pa.a.db.GetConnection(pa.conn); c.CatalogID != nil {
		t.Fatal("converted without an answer")
	}
	if !pa.pending(t) {
		t.Fatal("pending rows dropped although the outcome is open")
	}
	// A second publish of the same rows is refused while it is open.
	pa.setMode("ok")
	res, err = pa.a.CatalogPublish(pa.in())
	if err != nil || len(res.Refusals) == 0 || pa.postCount() != 1 {
		t.Fatalf("second publish: %+v %v (posts %d)", res, err, pa.postCount())
	}
}

func TestCatalogPublishSignedOut(t *testing.T) {
	pa := newPublishApp(t)
	pa.setMode("unauth")
	res, err := pa.a.CatalogPublish(pa.in())
	if err != nil {
		t.Fatal(err)
	}
	if res.Published || res.Unknown {
		t.Fatalf("result: %+v", res)
	}
	if pa.pending(t) {
		t.Fatal("pending rows kept although nothing was created")
	}
	if c, _ := pa.a.db.GetConnection(pa.conn); c.CatalogID != nil {
		t.Fatal("converted although signed out")
	}
}

func TestCatalogPublishLocalRefusalSendsNothing(t *testing.T) {
	pa := newPublishApp(t)
	if _, err := pa.a.db.CreateConnection(store.NewConnection{Name: "shell", Protocol: "local", FolderID: &pa.folder}); err != nil {
		t.Fatal(err)
	}
	res, err := pa.a.CatalogPublish(pa.in())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != 1 || res.Refusals[0].Name != "shell" || pa.postCount() != 0 {
		t.Fatalf("result: %+v, posts %d", res, pa.postCount())
	}
}

// Publish is desktop only, needs the vault and a sign-in.
func TestCatalogPublishGuards(t *testing.T) {
	pa := newPublishApp(t)
	_ = (vaultTokens{a: pa.a, id: pa.cat.ID}).Save("")
	if _, err := pa.a.CatalogPublish(pa.in()); err == nil || !strings.Contains(err.Error(), "sign in") {
		t.Fatalf("without a sign-in: %v", err)
	}
	pa.a.vault.Lock(false)
	if _, err := pa.a.CatalogPublishPreview(pa.in()); err == nil || !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("locked vault: %v", err)
	}
}
