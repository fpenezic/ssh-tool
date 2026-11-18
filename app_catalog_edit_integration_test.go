package main

// Record editing against a real catalog server in development mode
// (CATALOGTEST_URL, users as in internal/catalog/integration_test.go):
// bob attaches through an enrollment code, then creates, edits (a 412
// included), manages slots, moves and deletes through the app's IPC.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

var itReqField = regexp.MustCompile(`name="req" value="([^"]+)"`)

// itToken signs user in through the local form and returns a bearer token.
func itToken(t *testing.T, base, user string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := url.Values{"response_type": {"code"}, "client_id": {"ssh-tool"}, "redirect_uri": {"http://127.0.0.1:1/callback"},
		"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}, "state": {"s"}}
	resp, err := hc.Get(base + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := itReqField.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no sign-in form: %s", body)
	}
	resp, err = hc.PostForm(base+"/oauth/login/local", url.Values{"req": {string(m[1])}, "user": {user}, "password": {user}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")
	resp, err = hc.PostForm(base+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"http://127.0.0.1:1/callback"}, "client_id": {"ssh-tool"}, "code_verifier": {"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	if tok.AccessToken == "" {
		t.Fatal("no access token")
	}
	return tok.AccessToken
}

func itAPI(t *testing.T, base, tok, method, path, ifMatch string, body any) map[string]any {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		ct := "application/json"
		if method == http.MethodPatch {
			ct = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestIntegrationRecordEditing(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := creds.NewVault()
	v.SetPath(filepath.Join(dir, "vault.enc"))
	if err := v.Init("pass", false); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, vault: v}
	bobTok := itToken(t, base, "bob")
	code := itAPI(t, base, bobTok, "POST", "/api/v1/enrollments", "", nil)["code"].(string)
	info, err := a.CatalogEnrollConfirm(base, code, false, "")
	if err != nil {
		t.Fatal(err)
	}
	catID := info.ID
	cat, _ := db.GetCatalog(catID)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Create: a space (bob is admin), a connection in it.
	space, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "folders", ParentID: cat.RootFolderID, Form: catalog.EditForm{Name: "Edit space " + time.Now().Format("150405")}})
	must(err)
	connID, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "connections", ParentID: space, Form: catalog.EditForm{Name: "web-1", Hostname: "web-1.example.com"}})
	must(err)
	if _, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "connections", ParentID: space, Form: catalog.EditForm{Name: "sh", Hostname: "x", Protocol: "local"}}); err == nil {
		t.Fatal("a local connection was created")
	}

	// Edit hostname and notes.
	rec, err := a.CatalogRecord("connections", connID)
	must(err)
	if !rec.CanWrite || rec.Form == nil {
		t.Fatalf("bob cannot write: %+v", rec)
	}
	edited := *rec.Form
	edited.Hostname, edited.Notes = "web-1b.example.com", "mine"
	res, err := a.CatalogRecordSave(catalog.EditInput{Kind: "connections", LocalID: connID, Rev: rec.Rev, Loaded: *rec.Form, Edited: edited})
	must(err)
	if !res.Saved {
		t.Fatalf("save: %+v", res)
	}
	c, _ := db.GetConnection(connID)
	if c.Hostname != "web-1b.example.com" || c.Notes != "mine" {
		t.Fatalf("local row after save: %+v", c)
	}

	// 412: someone else changes notes; a save from the old form shows both.
	rec, _ = a.CatalogRecord("connections", connID)
	itAPI(t, base, bobTok, "PATCH", "/api/v1/connections/"+*c.RemoteUUID, fmt.Sprint(rec.Rev), map[string]any{"notes": "theirs"})
	edited = *rec.Form
	edited.Notes, edited.Tags = "mine again", []string{"web"}
	in := catalog.EditInput{Kind: "connections", LocalID: connID, Rev: rec.Rev, Loaded: *rec.Form, Edited: edited}
	res, err = a.CatalogRecordSave(in)
	must(err)
	if res.Saved || res.Plan == nil || len(res.Plan.Conflicts) != 2 {
		t.Fatalf("conflict: %+v", res)
	}
	t.Logf("conflict: %+v", res.Plan.Conflicts)
	in.AcceptRev, in.Resolve = res.Plan.ConflictRev, map[string]string{"notes": "mine"}
	res, err = a.CatalogRecordSave(in)
	must(err)
	if !res.Saved {
		t.Fatalf("re-apply: %+v", res)
	}
	c, _ = db.GetConnection(connID)
	if c.Notes != "mine again" || len(c.Tags) != 1 {
		t.Fatalf("after re-apply: %+v", c)
	}

	// A credential of bob's: a new slot, named first; then the slot is in
	// use and cannot be deleted.
	cr, _ := db.CreateCredential(store.NewCredential{Name: "deploy key", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	rec, _ = a.CatalogRecord("connections", connID)
	edited = *rec.Form
	edited.Settings.AuthRef = &cr.ID
	in = catalog.EditInput{Kind: "connections", LocalID: connID, Rev: rec.Rev, Loaded: *rec.Form, Edited: edited}
	res, _ = a.CatalogRecordSave(in)
	if len(res.Plan.NeedSlots) != 1 {
		t.Fatalf("need slot: %+v", res)
	}
	in.SlotNames = map[string]string{res.Plan.NeedSlots[0].Key: "deploy"}
	res, err = a.CatalogRecordSave(in)
	must(err)
	if !res.Saved {
		t.Fatalf("save with a new slot: %+v", res)
	}
	r, err := a.CatalogRecord("connections", connID)
	must(err)
	if r.Form.Settings.AuthRef == nil || *r.Form.Settings.AuthRef != cr.ID {
		t.Fatalf("bob does not resolve his credential through the new slot: %+v", r.Form.Settings.AuthRef)
	}
	slots, _ := db.ListCatalogSlots(catID)
	var slotUUID string
	for _, s := range slots {
		if s.Name == "deploy" {
			slotUUID = s.UUID
		}
	}
	if err := a.CatalogSlotDelete(catID, slotUUID); err == nil || !strings.Contains(err.Error(), `"web-1"`) {
		t.Fatalf("slot in use: %v", err)
	}
	must(a.CatalogSlotUpdate(CatalogSlotInput{CatalogID: catID, UUID: slotUUID, Name: "deploy login", DocMD: "Ask ops."}))

	// Move into a subfolder, then delete the space recursively.
	sub, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "folders", ParentID: space, Form: catalog.EditForm{Name: "Sub"}})
	must(err)
	must(a.CatalogMove("connections", connID, sub))
	c, _ = db.GetConnection(connID)
	if *c.FolderID != sub {
		t.Fatalf("not moved: %+v", c.FolderID)
	}
	must(a.CatalogDelete("folders", space, true))
	if _, err := db.GetConnection(connID); err == nil {
		t.Fatal("connection still here after the space was deleted")
	}
	if f, _ := db.GetFolder(space); f != nil {
		t.Fatal("space still here")
	}
}

// "Set it on the folder instead": a credential set on one connection goes
// on its folder; the sibling without one inherits it, the sibling that
// held the same one as its own loses the now redundant value.
func TestIntegrationSaveOnFolder(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := creds.NewVault()
	v.SetPath(filepath.Join(dir, "vault.enc"))
	if err := v.Init("pass", false); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, vault: v}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	bobTok := itToken(t, base, "bob")
	code := itAPI(t, base, bobTok, "POST", "/api/v1/enrollments", "", nil)["code"].(string)
	info, err := a.CatalogEnrollConfirm(base, code, false, "")
	must(err)
	catID := info.ID
	cat, _ := db.GetCatalog(catID)

	space, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "folders", ParentID: cat.RootFolderID, Form: catalog.EditForm{Name: "Folder space " + time.Now().Format("150405")}})
	must(err)
	defer func() { _ = a.CatalogDelete("folders", space, true) }()
	folder, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "folders", ParentID: space, Form: catalog.EditForm{Name: "Customer"}})
	must(err)
	ids := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		id, err := a.CatalogCreate(CatalogCreateInput{CatalogID: catID, Kind: "connections", ParentID: folder, Form: catalog.EditForm{Name: n, Hostname: n + ".example.com"}})
		must(err)
		ids[n] = id
	}
	cr, _ := db.CreateCredential(store.NewCredential{Name: "admin", Kind: store.CredPassword, StorageMode: store.StorageManaged})

	// c holds the credential as its own (a new slot on the folder).
	rec, _ := a.CatalogRecord("connections", ids["c"])
	edited := *rec.Form
	edited.Settings.AuthRef = &cr.ID
	in := catalog.EditInput{Kind: "connections", LocalID: ids["c"], Rev: rec.Rev, Loaded: *rec.Form, Edited: edited}
	res, _ := a.CatalogRecordSave(in)
	if len(res.Plan.NeedSlots) == 1 {
		in.SlotNames = map[string]string{res.Plan.NeedSlots[0].Key: "admin"}
		res, err = a.CatalogRecordSave(in)
		must(err)
	}
	if !res.Saved {
		t.Fatalf("c: %+v", res)
	}

	// a gets the same credential: offered on the folder.
	rec, _ = a.CatalogRecord("connections", ids["a"])
	edited = *rec.Form
	edited.Settings.AuthRef = &cr.ID
	edited.Notes = "set from a"
	in = catalog.EditInput{Kind: "connections", LocalID: ids["a"], Rev: rec.Rev, Loaded: *rec.Form, Edited: edited}
	offers, err := a.CatalogFolderOffers(in)
	must(err)
	if len(offers) != 1 || offers[0].Field != "auth_ref" || offers[0].FolderName != "Customer" ||
		strings.Join(offers[0].Gain, ",") != "b" || strings.Join(offers[0].Clear, ",") != "c" || offers[0].SelfOwn {
		t.Fatalf("offers: %+v", offers)
	}
	fres, err := a.CatalogRecordSaveOnFolder(in, []string{"auth_ref"})
	must(err)
	if !fres.Saved || len(fres.Left) > 0 {
		t.Fatalf("save on folder: %+v (folder %+v)", fres, fres.Folder)
	}
	fr, _ := a.CatalogRecord("folders", folder)
	if fr.Form.Settings.AuthRef == nil || *fr.Form.Settings.AuthRef != cr.ID {
		t.Fatalf("folder credential: %v", fr.Form.Settings.AuthRef)
	}
	for _, n := range []string{"a", "c"} {
		r, _ := a.CatalogRecord("connections", ids[n])
		if r.Form.Settings.AuthRef != nil {
			t.Fatalf("%s keeps its own credential: %v", n, *r.Form.Settings.AuthRef)
		}
	}
	if r, _ := a.CatalogRecord("connections", ids["a"]); r.Form.Notes != "set from a" {
		t.Fatalf("a's other change lost: %q", r.Form.Notes)
	}
	// Now the folder says it: nothing more to offer.
	rec, _ = a.CatalogRecord("connections", ids["b"])
	edited = *rec.Form
	edited.Settings.AuthRef = &cr.ID
	if offers, _ = a.CatalogFolderOffers(catalog.EditInput{Kind: "connections", LocalID: ids["b"], Rev: rec.Rev, Loaded: *rec.Form, Edited: edited}); len(offers) != 0 {
		t.Fatalf("offered again: %+v", offers)
	}
}
