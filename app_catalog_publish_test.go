package main

import (
	"database/sql"
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
	"ssh-tool/internal/resolver"
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
	revision    int      // meta's protocol revision (default 1)
	admin       bool     // whoami admin
	patches     []string // "<path> <body>" of PATCH requests, in order
	order       []string // "patch" / "publish", in the order they arrived
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
			pa.mu.Lock()
			rev := pa.revision
			pa.mu.Unlock()
			if rev == 0 {
				rev = 1
			}
			fmt.Fprintf(w, `{"server":{"name":"Fake"},"protocol":{"major":1,"revision":%[2]d},"auth":{"authorization_endpoint":"http://%[1]s/a","token_endpoint":"http://%[1]s/oauth/token","client_id":"ssh-tool"}}`, r.Host, rev)
		case "/api/v1/whoami":
			pa.mu.Lock()
			adm := pa.admin
			pa.mu.Unlock()
			fmt.Fprintf(w, `{"subject":"s","name":"Bob","groups":[],"admin":%v,"via":"local"}`, adm)
		case "/oauth/token":
			fmt.Fprint(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"rt"}`)
		case "/api/v1/changes":
			fmt.Fprint(w, `{"reset":false,"cursor":"c","items":[]}`)
		case "/api/v1/publish":
			pa.mu.Lock()
			pa.order = append(pa.order, "publish")
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
			if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/slots/") {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				b, _ := json.Marshal(body)
				pa.mu.Lock()
				pa.patches = append(pa.patches, r.URL.Path+" "+string(b))
				pa.order = append(pa.order, "patch")
				pa.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"record": map[string]any{"uuid": strings.TrimPrefix(r.URL.Path, "/api/v1/slots/"), "rev": 2, "folder_uuid": nil, "type": "cred", "name": "opk", "kind": "slot", "backend": nil, "external_ref": nil, "doc_md": ""}, "warnings": []any{}})
				return
			}
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

// in is the dialog's input after a preview: it carries the reviewed
// plan's hash, as the dialog does.
func (pa *publishApp) in() CatalogPublishInput {
	in := CatalogPublishInput{CatalogID: pa.cat.ID, FolderID: pa.folder}
	if p, err := pa.a.CatalogPublishPreview(in); err == nil && p != nil {
		in.ReviewHash = p.ReviewHash
	}
	return in
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
	// A local vault credential is not sent: no slot, the publisher keeps it.
	if len(plan.Slots) != 0 || len(plan.KeptLocal) != 1 || plan.KeptLocal[0].Field != "Credential" {
		t.Fatalf("preview slots %+v, kept %+v", plan.Slots, plan.KeptLocal)
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
	if !strings.Contains(res.Message, "1 record(s)") || strings.Contains(res.Message, ".;") {
		t.Fatalf("message: %q", res.Message)
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

// A publish whose folder changed after the review is refused unsent.
func TestCatalogPublishRefusesAChangedReview(t *testing.T) {
	pa := newPublishApp(t)
	in := pa.in()
	in.ReviewHash = "stale"
	res, err := pa.a.CatalogPublish(in)
	if err != nil || res.Published || !res.Stale {
		t.Fatalf("result: %+v %v", res, err)
	}
}

// A jump chain names an inventory host as "dyn:<folderId>/<externalId>";
// publish read it as "dyn:<entryId>", found nothing, and kept every
// inventory bastion with the publisher instead of sending a record.
func TestPublishResolveDynJumpRef(t *testing.T) {
	a := newResolveTestApp(t)
	user := "deploy"
	port := uint16(2222)
	folder, err := a.db.CreateFolder(store.NewFolder{
		Name:     "proxmox",
		Settings: store.InheritableSettings{Username: &user, Port: &port},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.CreateDynamicFolder(store.DynamicFolder{FolderID: folder.ID, Provider: "proxmox", Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.ReplaceDynamicEntries(folder.ID, []store.DynamicEntry{{
		ID: "entry-1", FolderID: folder.ID, ExternalID: "vm/100", Name: "bastion-01",
		Hostname: "10.0.0.5", Kind: "guest_vm", Raw: json.RawMessage(`{}`),
	}}); err != nil {
		t.Fatal(err)
	}
	ref := resolver.DynRef(folder.ID, "vm/100")
	b, ok := a.publishResolveDyn(ref)
	if !ok {
		t.Fatalf("%s did not resolve", ref)
	}
	if b.ID != ref || b.Name != "bastion-01" || b.Hostname != "10.0.0.5" {
		t.Fatalf("resolved to %+v", b)
	}
	if b.Overrides.Username == nil || *b.Overrides.Username != "deploy" || b.Overrides.Port == nil || *b.Overrides.Port != 2222 {
		t.Fatalf("folder settings not carried: %+v", b.Overrides)
	}
	if _, ok := a.publishResolveDyn(resolver.DynRef(folder.ID, "vm/999")); ok {
		t.Fatal("a host gone from the inventory resolved")
	}
}

// Catalog-level slots at the app level: an admin publishing a new space on
// a revision-12 server reuses the opkssh slot of another space by moving
// it to the catalog level first (PATCH folder_uuid null, before the
// publish), and sends no second slot.
func TestCatalogPublishMovesSlotToCatalogLevel(t *testing.T) {
	pa := newPublishApp(t)
	db := pa.a.db
	pa.mu.Lock()
	pa.revision, pa.admin = catalog.CatalogSlotsRevision, true
	pa.mu.Unlock()
	opk, err := db.CreateCredential(store.NewCredential{Name: "my opkssh", Kind: store.CredOpkssh})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.auth_ref', ?) WHERE id = ?`, opk.ID, pa.conn); err != nil {
		t.Fatal(err)
	}
	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.UpsertCatalogSlotTx(tx, store.CatalogSlot{CatalogID: pa.cat.ID, UUID: "slot-opk", Rev: 1, FolderUUID: "space-a", Type: "cred", Name: "Bulb opkssh sso", Kind: "slot"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCatalogBinding(pa.cat.ID, "slot-opk", "cred", opk.ID); err != nil {
		t.Fatal(err)
	}
	in := pa.in()
	p, err := pa.a.CatalogPublishPreview(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Slots) != 1 || !p.Slots[0].MoveToCatalog || p.Slots[0].UUID != "slot-opk" {
		t.Fatalf("preview slots: %+v", p.Slots)
	}
	res, err := pa.a.CatalogPublish(in)
	if err != nil || !res.Published {
		t.Fatalf("publish: %+v %v", res, err)
	}
	pa.mu.Lock()
	defer pa.mu.Unlock()
	if len(pa.patches) != 1 || pa.patches[0] != `/api/v1/slots/slot-opk {"folder_uuid":null}` {
		t.Fatalf("patches: %v", pa.patches)
	}
	if strings.Join(pa.order, ",") != "patch,publish" {
		t.Fatalf("order: %v", pa.order)
	}
	if len(pa.lastRequest.Slots) != 0 {
		t.Fatalf("a new slot was sent: %+v", pa.lastRequest.Slots)
	}
	// The move is applied here at once (not left to the next sync).
	slots, _ := db.ListCatalogSlots(pa.cat.ID)
	for _, sl := range slots {
		if sl.UUID == "slot-opk" && (sl.FolderUUID != "" || sl.Rev != 2) {
			t.Fatalf("moved slot not applied locally: %+v", sl)
		}
	}
}

// A different slot picked for the move after the preview (bindings
// changed) is a different plan: the publish is refused as stale.
func TestCatalogPublishSlotMoveInReviewHash(t *testing.T) {
	pa := newPublishApp(t)
	db := pa.a.db
	pa.mu.Lock()
	pa.revision, pa.admin = catalog.CatalogSlotsRevision, true
	pa.mu.Unlock()
	opk, _ := db.CreateCredential(store.NewCredential{Name: "my opkssh", Kind: store.CredOpkssh})
	if _, err := db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.auth_ref', ?) WHERE id = ?`, opk.ID, pa.conn); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"slot-a", "slot-b"} {
		if err := db.WithTx(func(tx *sql.Tx) error {
			return db.UpsertCatalogSlotTx(tx, store.CatalogSlot{CatalogID: pa.cat.ID, UUID: u, Rev: 1, FolderUUID: "space-" + u, Type: "cred", Name: u, Kind: "slot"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetCatalogBinding(pa.cat.ID, "slot-a", "cred", opk.ID); err != nil {
		t.Fatal(err)
	}
	in := pa.in()
	if err := db.DeleteCatalogBinding(pa.cat.ID, "slot-a"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCatalogBinding(pa.cat.ID, "slot-b", "cred", opk.ID); err != nil {
		t.Fatal(err)
	}
	res, err := pa.a.CatalogPublish(in)
	if err != nil || !res.Stale {
		t.Fatalf("want a stale refusal, got %+v %v", res, err)
	}
	if pa.postCount() != 0 {
		t.Fatal("published anyway")
	}
}
