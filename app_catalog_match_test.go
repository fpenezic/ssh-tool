package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// A colleague's own copy of a catalog host: matching moves its credential
// (binding the catalog's unbound slot), user, notes, favourite, stored
// password, forwards, snippet, workspace and the jump reference of another
// local connection onto the catalog record, parks the copy with its
// settings on it, and Undo puts everything back.
func TestCatalogMatchApplyAndUndo(t *testing.T) {
	ca := newCatalogApp(t)
	a, db, catID := ca.a, ca.a.db, ca.cat.ID

	cred, err := db.CreateCredential(store.NewCredential{Name: "my key", Kind: store.CredAgent})
	if err != nil {
		t.Fatal(err)
	}
	me, port := "me", uint16(2222)
	old, err := db.CreateFolder(store.NewFolder{Name: "Old", Settings: store.InheritableSettings{AuthRef: &cred.ID, Username: &me}})
	if err != nil {
		t.Fatal(err)
	}
	l, err := db.CreateConnection(store.NewConnection{FolderID: &old.ID, Name: "web", Hostname: "WEB-1.example.com.",
		Notes: "my notes", Overrides: store.InheritableSettings{Port: &port}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`UPDATE connections SET favorite = 1, last_used_at = 1000 WHERE id = ?`, l.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.SetConnectionPassword(l.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}
	// Elsewhere: a host the catalog does not have.
	other, err := db.CreateConnection(store.NewConnection{FolderID: &old.ID, Name: "elsewhere", Hostname: "other.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	lp, rp, host := uint16(25432), uint16(5432), "127.0.0.1"
	dup, err := db.CreatePortForward(store.NewPortForward{ConnectionID: l.ID, Kind: "local", LocalPort: &lp, RemoteHost: &host, RemotePort: &rp})
	if err != nil {
		t.Fatal(err)
	}
	lp2, rp2, h2 := uint16(8080), uint16(80), "localhost"
	extra, err := db.CreatePortForward(store.NewPortForward{ConnectionID: l.ID, Kind: "local", LocalPort: &lp2, RemoteHost: &h2, RemotePort: &rp2, AutoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	sn, err := db.CreateSnippet(store.SnippetInput{ConnectionID: &l.ID, Name: "logs", Body: "journalctl -f"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := db.CreateWorkspace("daily", `{"tabs":[{"connectionId":"`+l.ID+`"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	lid := l.ID
	inner, err := db.CreateConnection(store.NewConnection{Name: "inner", Hostname: "10.0.0.9",
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &lid}}}})
	if err != nil {
		t.Fatal(err)
	}

	pv, err := a.CatalogMatchPreview(catID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Pairs) != 1 || pv.Pairs[0].Local.ID != l.ID || pv.Pairs[0].Catalog.ID != ca.connID || pv.Unmatched != 2 {
		t.Fatalf("preview: %+v", pv)
	}
	where := map[string]string{}
	for _, c := range pv.Pairs[0].Changes {
		where[c.Field] = c.Where
	}
	if where[`Slot "admin"`] != "slot" || where["User"] != "connection" || where["Notes"] != "record" || where["Saved password"] != "local" || where["Used in"] != "reference" {
		t.Fatalf("changes: %+v", pv.Pairs[0].Changes)
	}

	res, err := a.CatalogMatchApply(catID, map[string]string{l.ID: ca.connID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pairs != 1 || res.Bindings != 1 || res.Forwards != 2 || res.Passwords != 1 {
		t.Fatalf("result: %+v", res)
	}

	// The catalog connection now connects as the user did.
	rs, err := resolver.ResolveConnection(db, ca.connID)
	if err != nil {
		t.Fatal(err)
	}
	if rs.AuthRef == nil || *rs.AuthRef != cred.ID || rs.Username == nil || *rs.Username != "me" {
		t.Fatalf("resolved: auth %v user %v", rs.AuthRef, rs.Username)
	}
	cc, _ := db.GetConnection(ca.connID)
	if !cc.Favorite || cc.Notes != "my notes" || cc.PasswordVaultKey == nil || *cc.PasswordVaultKey != "conn_pass:"+ca.connID {
		t.Fatalf("catalog row: fav %v notes %q key %v", cc.Favorite, cc.Notes, cc.PasswordVaultKey)
	}
	if v, ok, _ := a.vault.Get("conn_pass:" + ca.connID); !ok || v != "s3cret" {
		t.Fatal("password not copied")
	}
	// Forwards: the catalog's one keeps the user's port; the other moved.
	cp, _ := db.GetPortForward(ca.fwdID)
	if cp.LocalPort == nil || *cp.LocalPort != 25432 {
		t.Fatalf("catalog forward port: %v", cp.LocalPort)
	}
	if _, err := db.GetPortForward(dup.ID); err == nil {
		t.Fatal("duplicate forward kept")
	}
	if f, _ := db.GetPortForward(extra.ID); f == nil || f.ConnectionID != ca.connID {
		t.Fatalf("extra forward: %+v", f)
	}
	if s, _ := db.GetSnippet(sn.ID); s.ConnectionID == nil || *s.ConnectionID != ca.connID {
		t.Fatal("snippet not moved")
	}
	if w, _ := db.GetWorkspace(ws.ID); !strings.Contains(w.LayoutJSON, ca.connID) {
		t.Fatalf("workspace: %s", w.LayoutJSON)
	}
	if in, _ := db.GetConnection(inner.ID); *in.Overrides.JumpHost.Chain.ConnectionID != ca.connID {
		t.Fatal("jump reference not moved")
	}
	// The copy is parked and still carries its settings.
	pl, _ := db.GetConnection(l.ID)
	park, _ := db.GetFolder(*pl.FolderID)
	if park.Name != "Matched to Team" || pl.Overrides.AuthRef == nil || *pl.Overrides.AuthRef != cred.ID {
		t.Fatalf("parked: folder %q overrides %+v", park.Name, pl.Overrides)
	}
	if pl.LastUsedAt != nil {
		t.Fatal("parked copy still in Recent")
	}
	// Used after the match: Undo keeps that.
	if _, err := db.Conn().Exec(`UPDATE connections SET last_used_at = 4242 WHERE id = ?`, ca.connID); err != nil {
		t.Fatal(err)
	}
	// Matched once: not offered again.
	if pv, _ := a.CatalogMatchPreview(catID, nil); len(pv.Pairs) != 0 || pv.LastRunPairs != 1 {
		t.Fatalf("after match: %+v", pv)
	}

	ur, err := a.CatalogMatchUndo(catID)
	if err != nil {
		t.Fatal(err)
	}
	if ur.Kept != 0 {
		t.Fatalf("undo kept %d", ur.Kept)
	}
	pl, _ = db.GetConnection(l.ID)
	if pl.FolderID == nil || *pl.FolderID != old.ID || pl.Overrides.AuthRef != nil {
		t.Fatalf("copy not back: %+v", pl)
	}
	if _, err := db.GetFolder(park.ID); err == nil {
		t.Fatal("park folder left behind")
	}
	if bs, _ := db.ListCatalogBindings(catID); len(bs) != 0 {
		t.Fatalf("bindings: %+v", bs)
	}
	if ovs, _ := db.ListCatalogOverrides(catID); len(ovs) != 0 {
		t.Fatalf("layers: %+v", ovs)
	}
	cc, _ = db.GetConnection(ca.connID)
	if cc.Favorite || cc.PasswordVaultKey != nil || cc.Notes != "" || cc.LastUsedAt == nil || *cc.LastUsedAt != 4242 {
		t.Fatalf("catalog row after undo: %+v", cc)
	}
	if pl.LastUsedAt == nil {
		t.Fatal("copy's last use not back")
	}
	if _, ok, _ := a.vault.Get("conn_pass:" + ca.connID); ok {
		t.Fatal("copied password left in the vault")
	}
	if f, err := db.GetPortForward(dup.ID); err != nil || f.ConnectionID != l.ID {
		t.Fatalf("duplicate forward not back: %v", err)
	}
	if f, _ := db.GetPortForward(extra.ID); f.ConnectionID != l.ID {
		t.Fatal("extra forward not back")
	}
	if cp, _ := db.GetPortForward(ca.fwdID); *cp.LocalPort != 15432 {
		t.Fatalf("catalog forward port after undo: %v", *cp.LocalPort)
	}
	if s, _ := db.GetSnippet(sn.ID); *s.ConnectionID != l.ID {
		t.Fatal("snippet not back")
	}
	if w, _ := db.GetWorkspace(ws.ID); !strings.Contains(w.LayoutJSON, l.ID) {
		t.Fatal("workspace not back")
	}
	if in, _ := db.GetConnection(inner.ID); *in.Overrides.JumpHost.Chain.ConnectionID != l.ID {
		t.Fatal("jump reference not back")
	}
	_ = other
}

// Several catalog records on the same host and port: the user name
// decides, and a pick settles the rest; a second local copy of a paired
// record is a duplicate.
func TestCatalogMatchAmbiguousAndDuplicates(t *testing.T) {
	ca := newCatalogApp(t)
	a, db, catID := ca.a, ca.a.db, ca.cat.ID
	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, overrides_json, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			VALUES ('cc2', 'cf', 'web-1 root', 'web-1.example.com', '{"port":2222,"username":"root"}', 1, 1, ?, 'c2-uuid', 1, '{"uuid":"c2-uuid","rev":1,"folder_uuid":"f-uuid","name":"web-1 root","hostname":"web-1.example.com","settings":{"port":2222,"username":"root"}}')`, catID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Materialise(db, catID, nil); err != nil {
		t.Fatal(err)
	}
	port := uint16(2222)
	root, deploy, other := "root", "deploy", "nobody"
	mk := func(name, user string) string {
		c, err := db.CreateConnection(store.NewConnection{Name: name, Hostname: "web-1.example.com", Overrides: store.InheritableSettings{Port: &port, Username: &user}})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	asRoot := mk("as root", root)
	asDeploy := mk("as deploy", deploy)
	again := mk("again", deploy)
	odd := mk("odd", other)

	pv, err := a.CatalogMatchPreview(catID, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range pv.Pairs {
		got[p.Local.ID] = p.Catalog.ID
	}
	if got[asRoot] != "cc2" || len(pv.Ambiguous) != 1 || pv.Ambiguous[0].Local.ID != odd || len(pv.Ambiguous[0].Options) != 2 {
		t.Fatalf("preview: pairs %v ambiguous %+v", got, pv.Ambiguous)
	}
	if !(got[asDeploy] == "cc" || got[again] == "cc") || len(pv.Duplicates) != 1 {
		t.Fatalf("deploy pair / duplicates: %v %+v", got, pv.Duplicates)
	}
	pv, _ = a.CatalogMatchPreview(catID, map[string]string{odd: "cc"})
	got = map[string]string{}
	for _, p := range pv.Pairs {
		got[p.Local.ID] = p.Catalog.ID
	}
	if got[odd] != "cc" || len(pv.Ambiguous) != 0 || len(pv.Duplicates) != 2 {
		t.Fatalf("with a pick: %v %+v", got, pv.Duplicates)
	}
	// A pair the preview would not make is refused.
	if _, err := a.CatalogMatchApply(catID, map[string]string{asRoot: "not-a-record"}); err == nil {
		t.Fatal("a wrong pair was applied")
	}
}

// The catalog's own database target keeps its definition and takes the
// user's login for the same database.
func TestCatalogMatchDbTargetLogins(t *testing.T) {
	ca := newCatalogApp(t)
	a, db, catID := ca.a, ca.a.db, ca.cat.ID
	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET catalog_json = ? WHERE id = 'cc'`,
			`{"uuid":"c-uuid","rev":2,"folder_uuid":"f-uuid","name":"web-1","hostname":"web-1.example.com","settings":{"port":2222,"db_targets":[{"id":"t1","name":"main","addr":"db:5432","database":"app"}]}}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Materialise(db, catID, nil); err != nil {
		t.Fatal(err)
	}
	cred, _ := db.CreateCredential(store.NewCredential{Name: "db login", Kind: store.CredAgent})
	port := uint16(2222)
	addr, dbn := "DB:5432", "app"
	l, err := db.CreateConnection(store.NewConnection{Name: "web", Hostname: "web-1.example.com", Overrides: store.InheritableSettings{Port: &port,
		DbTargets: []store.DbTarget{{ID: "mine", Name: "main", Addr: &addr, Database: &dbn, CredentialRef: &cred.ID}}, DbDefaultTarget: ptr("mine")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CatalogMatchApply(catID, map[string]string{l.ID: ca.connID}); err != nil {
		t.Fatal(err)
	}
	rs, err := resolver.ResolveConnection(db, ca.connID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.DbTargets) != 1 || rs.DbTargets[0].ID != "t1" || rs.DbTargets[0].CredentialRef == nil || *rs.DbTargets[0].CredentialRef != cred.ID {
		t.Fatalf("targets: %+v", rs.DbTargets)
	}
	if rs.DbDefaultTarget != "t1" {
		t.Fatalf("default target: %q", rs.DbDefaultTarget)
	}
}

// A shared value goes on the folder only when no catalog connection sits
// deeper in it: a folder layer would reach that one too.
func TestCatalogMatchNoLiftOverDeeperConnections(t *testing.T) {
	ca := newCatalogApp(t)
	a, db, catID := ca.a, ca.a.db, ca.cat.ID
	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			 VALUES ('cc2', 'cf', 'web-2', 'web-2.example.com', 1, 1, '` + catID + `', 'c2-uuid', 1, '{"uuid":"c2-uuid","rev":1,"folder_uuid":"f-uuid","name":"web-2","hostname":"web-2.example.com","settings":{}}')`,
			`INSERT INTO folders (id, parent_id, name, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			 VALUES ('cf2', 'cf', 'Deeper', 1, 1, '` + catID + `', 'f2-uuid', 1, '{"uuid":"f2-uuid","rev":1,"parent_uuid":"f-uuid","name":"Deeper","access":"read","settings":{}}')`,
			`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			 VALUES ('cc3', 'cf2', 'web-3', 'web-3.example.com', 1, 1, '` + catID + `', 'c3-uuid', 1, '{"uuid":"c3-uuid","rev":1,"folder_uuid":"f2-uuid","name":"web-3","hostname":"web-3.example.com","settings":{}}')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Materialise(db, catID, nil); err != nil {
		t.Fatal(err)
	}
	me, port := "me", uint16(2222)
	old, _ := db.CreateFolder(store.NewFolder{Name: "Old", Settings: store.InheritableSettings{Username: &me}})
	l1, _ := db.CreateConnection(store.NewConnection{FolderID: &old.ID, Name: "w1", Hostname: "web-1.example.com", Overrides: store.InheritableSettings{Port: &port}})
	l2, _ := db.CreateConnection(store.NewConnection{FolderID: &old.ID, Name: "w2", Hostname: "web-2.example.com"})
	if _, err := a.CatalogMatchApply(catID, map[string]string{l1.ID: ca.connID, l2.ID: "cc2"}); err != nil {
		t.Fatal(err)
	}
	ovs, _ := db.ListCatalogOverrides(catID)
	for _, o := range ovs {
		if o.Kind == "folders" {
			t.Fatalf("lifted onto the folder over web-3: %s", o.JSON)
		}
	}
	if rs, _ := resolver.ResolveConnection(db, "cc3"); rs.Username != nil && *rs.Username == "me" {
		t.Fatal("unmatched web-3 got the user")
	}
	if rs, _ := resolver.ResolveConnection(db, "cc2"); rs.Username == nil || *rs.Username != "me" {
		t.Fatal("web-2 lost its user")
	}
}

// Undo leaves what the user changed after the match: a password saved on
// the catalog connection, a forward's bookmarks.
func TestCatalogMatchUndoKeepsLaterEdits(t *testing.T) {
	ca := newCatalogApp(t)
	a, db, catID := ca.a, ca.a.db, ca.cat.ID
	port := uint16(2222)
	lp, rp, host := uint16(25432), uint16(5432), "127.0.0.1"
	l, _ := db.CreateConnection(store.NewConnection{Name: "web", Hostname: "web-1.example.com", Overrides: store.InheritableSettings{Port: &port}})
	if err := a.SetConnectionPassword(l.ID, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreatePortForward(store.NewPortForward{ConnectionID: l.ID, Kind: "local", LocalPort: &lp, RemoteHost: &host, RemotePort: &rp}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CatalogMatchApply(catID, map[string]string{l.ID: ca.connID}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetConnectionPassword(ca.connID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPortForwardBookmarks(ca.fwdID, []store.ProxyBookmark{{Name: "app", URL: "http://localhost:25432/"}}); err != nil {
		t.Fatal(err)
	}
	ur, err := a.CatalogMatchUndo(catID)
	if err != nil {
		t.Fatal(err)
	}
	if ur.Kept < 2 {
		t.Fatalf("kept %d", ur.Kept)
	}
	cc, _ := db.GetConnection(ca.connID)
	if cc.PasswordVaultKey == nil {
		t.Fatal("password saved after the match was dropped")
	}
	if v, ok, _ := a.vault.Get(*cc.PasswordVaultKey); !ok || v != "new" {
		t.Fatal("password saved after the match was deleted")
	}
	if f, _ := db.GetPortForward(ca.fwdID); len(f.Bookmarks) != 1 {
		t.Fatalf("bookmarks after undo: %+v", f.Bookmarks)
	}
}

// The user's own default database becomes the catalog target paired with
// it; pairing prefers address and database over a same-named target.
func TestCatalogMatchDbDefaultAndPairingOrder(t *testing.T) {
	cat := []store.DbTarget{{ID: "t1", Name: "main", Addr: ptr("db-b:5432"), Database: ptr("app")}}
	local := []store.DbTarget{
		{ID: "l1", Name: "main", Addr: ptr("db-a:5432"), Database: ptr("app"), CredentialRef: ptr("wrong")},
		{ID: "l2", Name: "other", Addr: ptr("DB-B:5432"), Database: ptr("app"), CredentialRef: ptr("right")},
	}
	got := dbTargetLogins(local, cat)
	if len(got) != 1 || *got[0].CredentialRef != "right" {
		t.Fatalf("logins: %+v", got)
	}
	if d := dbDefaultOnCatalog("l2", local, cat); d != "t1" {
		t.Fatalf("default: %q", d)
	}
	if d := dbDefaultOnCatalog("l1", local, cat); d != "" {
		t.Fatalf("unpaired default mapped to %q", d)
	}
}

func ptr(s string) *string { return &s }

// A password copy whose reference or journal entry fails is taken back:
// nothing is left in the vault that Undo would not know about.
func TestCatalogMatchPasswordCopyFailures(t *testing.T) {
	for _, mode := range []string{"reference", "journal"} {
		t.Run(mode, func(t *testing.T) {
			ca := newCatalogApp(t)
			a, db, catID := ca.a, ca.a.db, ca.cat.ID
			port := uint16(2222)
			l, _ := db.CreateConnection(store.NewConnection{Name: "web", Hostname: "web-1.example.com", Overrides: store.InheritableSettings{Port: &port}})
			if err := a.SetConnectionPassword(l.ID, "pw"); err != nil {
				t.Fatal(err)
			}
			if mode == "reference" {
				saved := matchSetKey
				matchSetKey = func(*store.DB, string, bool, string) error { return fmt.Errorf("boom") }
				t.Cleanup(func() { matchSetKey = saved })
			} else {
				saved := matchSaveJournal
				matchSaveJournal = func(*sql.Tx, string, []matchJournal) error { return fmt.Errorf("boom") }
				t.Cleanup(func() { matchSaveJournal = saved })
			}
			res, err := a.CatalogMatchApply(catID, map[string]string{l.ID: ca.connID})
			if err != nil {
				t.Fatal(err)
			}
			if res.Passwords != 0 || res.PassSkipped != 1 {
				t.Fatalf("result: %+v", res)
			}
			cc, _ := db.GetConnection(ca.connID)
			if cc.PasswordVaultKey != nil {
				t.Fatalf("reference left: %v", *cc.PasswordVaultKey)
			}
			if _, ok, _ := a.vault.Get("conn_pass:" + ca.connID); ok {
				t.Fatal("copy left in the vault")
			}
		})
	}
}

// Database names compare exactly (host names do not): "App" and "app" on
// one server are two databases and keep their own logins.
func TestCatalogMatchDbNamesCaseSensitive(t *testing.T) {
	cat := []store.DbTarget{
		{ID: "tA", Name: "upper", Addr: ptr("db:5432"), Database: ptr("App")},
		{ID: "ta", Name: "lower", Addr: ptr("db:5432"), Database: ptr("app")},
	}
	local := []store.DbTarget{
		{ID: "la", Name: "x", Addr: ptr("DB:5432"), Database: ptr("app"), CredentialRef: ptr("cred-lower")},
		{ID: "lA", Name: "y", Addr: ptr("DB:5432"), Database: ptr("App"), CredentialRef: ptr("cred-upper")},
	}
	got := map[string]string{}
	for _, o := range dbTargetLogins(local, cat) {
		got[o.ID] = *o.CredentialRef
	}
	if got["tA"] != "cred-upper" || got["ta"] != "cred-lower" {
		t.Fatalf("logins: %v", got)
	}
	if d := dbDefaultOnCatalog("la", local, cat); d != "ta" {
		t.Fatalf("default: %q", d)
	}
}

// A slot every connection using it covers with the user's own value no
// longer counts as one to bind.
func TestCatalogSlotCoveredByMySettings(t *testing.T) {
	ca := newCatalogApp(t)
	a, db, catID := ca.a, ca.a.db, ca.cat.ID
	count := func() (int, bool) {
		cats, err := a.CatalogList()
		if err != nil || len(cats) != 1 {
			t.Fatalf("list: %v %d", err, len(cats))
		}
		covered := false
		for _, s := range a.catalogSlotInfos(catID, mustSlots(t, db, catID)) {
			if s.UUID == "s1" {
				covered = s.Covered
			}
		}
		return cats[0].Unbound, covered
	}
	if n, cov := count(); n != 1 || cov {
		t.Fatalf("before: unbound %d covered %v", n, cov)
	}
	cred, _ := db.CreateCredential(store.NewCredential{Name: "mine", Kind: store.CredAgent})
	if err := a.CatalogSetLayer("connections", ca.connID, `{"auth_ref":"`+cred.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if n, cov := count(); n != 0 || !cov {
		t.Fatalf("after: unbound %d covered %v", n, cov)
	}
}

func mustSlots(t *testing.T, db *store.DB, catID string) []store.CatalogSlot {
	t.Helper()
	s, err := db.ListCatalogSlots(catID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
