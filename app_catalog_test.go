package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/exporter"
	"ssh-tool/internal/store"
)

type catalogApp struct {
	a                          *App
	cat                        *store.Catalog
	folderID, connID, fwdID    string
	tokenRefused, browserCalls int
}

// newCatalogApp: an App with a store, an unlocked vault and one catalog
// holding a folder (with a credential slot), a connection and a forward,
// materialised. The catalog's server is a fake that refuses every refresh
// token (a session the server ended).
func newCatalogApp(t *testing.T) *catalogApp {
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
	ca := &catalogApp{a: &App{db: db, vault: v}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/meta":
			fmt.Fprintf(w, `{"server":{"name":"Fake"},"protocol":{"major":1,"revision":1},"auth":{"authorization_endpoint":"http://%[1]s/a","token_endpoint":"http://%[1]s/oauth/token","client_id":"ssh-tool"}}`, r.Host)
		case "/oauth/token":
			ca.tokenRefused++
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant"}`))
		default:
			w.WriteHeader(401)
		}
	}))
	t.Cleanup(srv.Close)
	cat, err := db.CreateCatalog(store.NewCatalog{Name: "Team", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ca.cat = cat
	ca.folderID, ca.connID, ca.fwdID = "cf", "cc", "cp"
	err = db.WithCatalogWriter(func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO folders (id, parent_id, name, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			 VALUES ('cf', '` + cat.RootFolderID + `', 'Customer A', 1, 1, '` + cat.ID + `', 'f-uuid', 1,
			 '{"uuid":"f-uuid","rev":1,"parent_uuid":null,"name":"Customer A","access":"read","doc_md":"Ask ops.","settings":{"auth_ref":"s1","username":"deploy"}}')`,
			`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			 VALUES ('cc', 'cf', 'web-1', 'web-1.example.com', 1, 1, '` + cat.ID + `', 'c-uuid', 1,
			 '{"uuid":"c-uuid","rev":1,"folder_uuid":"f-uuid","name":"web-1","hostname":"web-1.example.com","settings":{"port":2222,"tunnel_autostart":true}}')`,
			`INSERT INTO port_forwards (id, connection_id, kind, local_port, remote_host, remote_port, description, catalog_id, remote_uuid, remote_rev, catalog_json)
			 VALUES ('cp', 'cc', 'local', 15432, '127.0.0.1', 5432, 'pg', '` + cat.ID + `', 'p-uuid', 1,
			 '{"uuid":"p-uuid","rev":1,"connection_uuid":"c-uuid","kind":"local","local_port":15432,"remote_host":"127.0.0.1","remote_port":5432,"auto_start":true}')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return db.UpsertCatalogSlotTx(tx, store.CatalogSlot{CatalogID: cat.ID, UUID: "s1", Rev: 1, Type: "cred", Name: "admin", Kind: "slot", DocMD: "Ask ops."})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Materialise(db, cat.ID, nil); err != nil {
		t.Fatal(err)
	}
	old := catalogOpenBrowser
	catalogOpenBrowser = func(string) { ca.browserCalls++ }
	t.Cleanup(func() { catalogOpenBrowser = old })
	return ca
}

func TestCatalogEditsGoToTheLocalLayer(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	fav := true
	if _, err := a.updateConnection(store.UpdateConnection{ID: ca.connID, Favorite: &fav}); err != nil {
		t.Fatalf("favourite: %v", err)
	}
	name := "renamed"
	if _, err := a.updateConnection(store.UpdateConnection{ID: ca.connID, Name: &name}); !errors.Is(err, errCatalogOwned) {
		t.Fatalf("rename: %v", err)
	}
	c, _ := a.db.GetConnection(ca.connID)
	edited := c.Overrides
	port := uint16(2200)
	user := "me"
	edited.Port, edited.Username = &port, &user
	if _, err := a.updateConnection(store.UpdateConnection{ID: ca.connID, Overrides: &edited}); err != nil {
		t.Fatalf("settings edit: %v", err)
	}
	c, _ = a.db.GetConnection(ca.connID)
	if *c.Overrides.Port != 2200 || *c.Overrides.Username != "me" || !c.Favorite {
		t.Fatalf("materialised after edit: %+v", c)
	}
	rec, err := a.CatalogRecord("connections", ca.connID)
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.Layer) != `{"username":"me","port":2200}` {
		t.Fatalf("layer holds more than the changed fields: %s", rec.Layer)
	}
	if rec.Catalog.Port == nil || *rec.Catalog.Port != 2222 || !rec.SuggestedTunnelAutostart {
		t.Fatalf("catalog value for the editor: %+v", rec)
	}
	// Reset to catalog.
	if err := a.CatalogSetLayer("connections", ca.connID, "{}"); err != nil {
		t.Fatal(err)
	}
	if c, _ = a.db.GetConnection(ca.connID); *c.Overrides.Port != 2222 || c.Overrides.Username != nil {
		t.Fatalf("after reset: %+v", c.Overrides)
	}
	if fr, _ := a.CatalogRecord("folders", ca.folderID); fr.DocMD != "Ask ops." {
		t.Errorf("folder doc: %+v", fr)
	}
}

func TestCatalogWritePathsRefuse(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	for name, err := range map[string]error{
		"folder in catalog folder": func() error { _, e := a.FoldersCreate(FoldersCreateInput{Name: "x", ParentID: &ca.folderID}); return e }(),
		"folder in catalog root": func() error {
			_, e := a.FoldersCreate(FoldersCreateInput{Name: "x", ParentID: &ca.cat.RootFolderID})
			return e
		}(),
		"connection in catalog folder": func() error {
			_, e := a.ConnectionsCreate(ConnectionsCreateInput{Name: "x", Hostname: "h", FolderID: &ca.folderID})
			return e
		}(),
		"import into catalog folder": func() error { _, e := a.SshConfigImport("Host x\n  HostName h\n", ca.folderID); return e }(),
		"archive into catalog folder": func() error {
			_, e := a.ImportArchive(ImportArchiveRequest{Text: "{}", TargetFolderID: ca.folderID})
			return e
		}(),
	} {
		if !errors.Is(err, errCatalogFolder) {
			t.Errorf("%s: %v", name, err)
		}
	}
	n := "x"
	for name, err := range map[string]error{
		"rename catalog folder": func() error { _, e := a.FoldersUpdate(FoldersUpdateInput{ID: ca.folderID, Name: &n}); return e }(),
		"move catalog folder": func() error {
			_, e := a.FoldersUpdate(FoldersUpdateInput{ID: ca.folderID, ClearParent: true})
			return e
		}(),
		"edit catalog root": func() error {
			_, e := a.FoldersUpdate(FoldersUpdateInput{ID: ca.cat.RootFolderID, Settings: &store.InheritableSettings{}})
			return e
		}(),
		"delete catalog folder": a.FoldersDelete(ca.folderID),
		"delete connection":     a.ConnectionsDelete(ca.connID),
		"forward description": func() error {
			_, e := a.ForwardsUpdate(ForwardUpdateInput{ID: ca.fwdID, Description: &n})
			return e
		}(),
	} {
		if !errors.Is(err, errCatalogOwned) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := a.ForwardsDelete(ca.fwdID); err == nil {
		t.Error("catalog forward deleted")
	}
	// A batch edit leaves catalog rows out.
	res, err := a.ConnectionsBatchUpdate(ConnectionsBatchUpdateInput{IDs: []string{ca.connID}, AddTags: []string{"t"}})
	if err != nil || res.Updated != 0 {
		t.Errorf("batch: %+v %v", res, err)
	}
	// A clone is the user's own, at the top, with what it inherited.
	cl, err := a.ConnectionsClone(ca.connID)
	if err != nil || cl.CatalogID != nil || cl.FolderID != nil || cl.Overrides.Username == nil || *cl.Overrides.Username != "deploy" {
		t.Fatalf("clone: %+v %v", cl, err)
	}
}

func TestCatalogForwardLocalColumnsAndLayer(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	if f, _ := a.db.GetPortForward(ca.fwdID); f.AutoStart {
		t.Fatal("catalog auto_start suggestion started the forward")
	}
	port := uint16(25432)
	on := true
	f, err := a.ForwardsUpdate(ForwardUpdateInput{ID: ca.fwdID, LocalPort: &port, AutoStart: &on})
	if err != nil {
		t.Fatal(err)
	}
	if *f.LocalPort != 25432 || !f.AutoStart {
		t.Fatalf("forward: %+v", f)
	}
	if err := a.ForwardsSetDisabled(ca.fwdID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ForwardsStart(ca.fwdID, "no-session"); !errors.Is(err, errForwardDisabled) {
		t.Fatalf("disabled forward start: %v", err)
	}
	if _, err := a.ForwardsUpdate(ForwardUpdateInput{ID: ca.fwdID, ClearLocalPort: true}); err != nil {
		t.Fatal(err)
	}
	if f, _ = a.db.GetPortForward(ca.fwdID); *f.LocalPort != 15432 {
		t.Fatalf("reset local port: %v", *f.LocalPort)
	}
}

func TestCatalogBindingAndUsage(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	cred, err := a.db.CreateCredential(store.NewCredential{Name: "my key", Kind: store.CredAgent})
	if err != nil {
		t.Fatal(err)
	}
	slots, _ := a.CatalogSlots(ca.cat.ID)
	if len(slots) != 1 || slots[0].State != "unbound" || slots[0].UsedBy != 1 {
		t.Fatalf("slots: %+v", slots)
	}
	if err := a.CatalogBind(ca.cat.ID, "s1", cred.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := a.db.GetFolder(ca.folderID); *f.Settings.AuthRef != cred.ID {
		t.Fatalf("bound: %v", *f.Settings.AuthRef)
	}
	if slots, _ = a.CatalogSlots(ca.cat.ID); slots[0].State != "bound" || slots[0].TargetName != "my key" {
		t.Fatalf("slot after bind: %+v", slots[0])
	}
	use, _ := a.db.CredentialUsage(cred.ID)
	found := false
	for _, u := range use {
		found = found || u.Kind == "catalog_slot"
	}
	if !found {
		t.Fatalf("usage does not mention the catalog slot: %+v", use)
	}
	if err := a.CatalogBind(ca.cat.ID, "s1", "not-a-credential"); err == nil {
		t.Error("bound to nothing")
	}
}

// Nothing but an explicit sign-in opens a browser. A background pass that
// finds no token, a dead session or a locked vault sets the state and stops.
func TestCatalogBackgroundNeverSignsIn(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	state := func() string {
		list, err := a.CatalogList()
		if err != nil || len(list) != 1 {
			t.Fatalf("list: %+v %v", list, err)
		}
		return list[0].State
	}
	// Never signed in on this machine (rows came through profile sync).
	if err := a.catalogSyncNow(ca.cat.ID, true); !errors.Is(err, catalog.ErrSignedOut) {
		t.Fatalf("no token: %v", err)
	}
	if s := state(); s != catStateNoLogin {
		t.Fatalf("no token state: %s", s)
	}
	// A token the server refuses.
	if err := (vaultTokens{a: a, id: ca.cat.ID}).Save("stale"); err != nil {
		t.Fatal(err)
	}
	if err := a.catalogSyncNow(ca.cat.ID, true); !errors.Is(err, catalog.ErrSignedOut) {
		t.Fatalf("refused token: %v", err)
	}
	if s := state(); s != catStateSignedOU {
		t.Fatalf("refused token state: %s", s)
	}
	if ca.tokenRefused != 1 {
		t.Errorf("%d refresh attempts, want 1", ca.tokenRefused)
	}
	// It survives a restart (the in-memory state is gone).
	delete(a.catalogs.state, ca.cat.ID)
	if s := state(); s != catStateSignedOU {
		t.Fatalf("signed out after a restart: %s", s)
	}
	// The scheduler leaves a signed-out catalog alone.
	a.catalogSchedulerPass()
	if ca.tokenRefused != 1 {
		t.Errorf("the scheduler retried a signed-out catalog")
	}
	// Locked vault.
	a.vault.Lock(false)
	delete(a.catalogs.state, ca.cat.ID)
	if s := state(); s != catStateLocked {
		t.Fatalf("locked state: %s", s)
	}
	if ca.browserCalls != 0 {
		t.Fatalf("a browser was opened %d time(s) without the user asking", ca.browserCalls)
	}
}

func TestExportLeavesCatalogOut(t *testing.T) {
	ca := newCatalogApp(t)
	if _, err := ca.a.db.CreateConnection(store.NewConnection{Name: "own", Hostname: "h"}); err != nil {
		t.Fatal(err)
	}
	arc, err := exporter.Build(ca.a.db, nil, nil, exporter.Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(arc.Connections) != 1 || arc.Connections[0].Name != "own" {
		t.Fatalf("export: %+v", arc.Connections)
	}
	for _, f := range arc.Folders {
		if f.Name == "Customer A" || f.Name == "Team" {
			t.Fatalf("catalog folder exported: %+v", f)
		}
	}
}

func TestCatalogAddRefusesTheSameServerTwice(t *testing.T) {
	ca := newCatalogApp(t)
	_, err := ca.a.CatalogAdd(ca.cat.BaseURL + "/")
	if err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("second attachment: %v", err)
	}
}

func TestCatalogShortError(t *testing.T) {
	long := &url.Error{Op: "Get", URL: "http://127.0.0.1:18080/api/v1/changes?since=CURSOR", Err: errors.New("connection refused")}
	if got := catalogShortError(long); got != "cannot reach the catalog server" || strings.Contains(got, "CURSOR") {
		t.Fatalf("short error: %q", got)
	}
	if got := catalogShortError(&catalog.APIError{Status: 500, Message: "boom"}); !strings.Contains(got, "boom") {
		t.Fatalf("api error: %q", got)
	}
}

func TestCatalogFolderEditGoesToTheLayer(t *testing.T) {
	ca := newCatalogApp(t)
	f, _ := ca.a.db.GetFolder(ca.folderID)
	edited := f.Settings
	user := "me"
	edited.Username = &user
	if _, err := ca.a.FoldersUpdate(FoldersUpdateInput{ID: ca.folderID, Name: &f.Name, Settings: &edited}); err != nil {
		t.Fatal(err)
	}
	f, _ = ca.a.db.GetFolder(ca.folderID)
	if *f.Settings.Username != "me" {
		t.Fatalf("folder layer not applied: %+v", f.Settings)
	}
	rec, _ := ca.a.CatalogRecord("folders", ca.folderID)
	if string(rec.Layer) != `{"username":"me"}` {
		t.Fatalf("folder layer: %s", rec.Layer)
	}
}
