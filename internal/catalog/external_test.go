package catalog

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

func externalEnv(t *testing.T, servers int) (*syncEnv, []string) {
	e := newSyncEnv(t)
	var ids []string
	for i := 0; i < servers; i++ {
		s, err := e.db.CreateBitwardenServer(store.BitwardenServer{Name: "vw" + string(rune('a'+i)), ServerURL: "https://vault.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, map[string]any{"uuid": "space", "rev": 1, "parent_uuid": nil, "name": "Space", "access": "read",
			"settings": map[string]any{"auth_ref": "ext", "otp_ref": "other-backend"}}),
		up(KindSlots, map[string]any{"uuid": "ext", "rev": 1, "folder_uuid": "space", "type": "cred", "name": "web admin",
			"kind": "external", "backend": "bitwarden", "external_ref": map[string]any{"cipher_id": "c-1", "field": "privatekey"}}),
		up(KindSlots, map[string]any{"uuid": "other-backend", "rev": 1, "folder_uuid": "space", "type": "cred", "name": "x",
			"kind": "external", "backend": "keepass", "external_ref": map[string]any{"entry": "e"}}),
		up(KindConnections, connRec("web", 1, "space", "web-1")),
	}}})
	e.sync()
	return e, ids
}

func (e *syncEnv) materialiseWith(probe ItemProbe) {
	e.t.Helper()
	cat, _ := e.db.GetCatalog(e.cat.ID)
	ext, err := PrepareExternal(e.db, cat, probe)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := Materialise(e.db, e.cat.ID, ext); err != nil {
		e.t.Fatal(err)
	}
}

func TestExternalOneServer(t *testing.T) {
	e, servers := externalEnv(t, 1)
	e.materialiseWith(nil)
	sp := e.folder("space")
	want := store.CatalogCredentialID(e.cat.ID, "ext")
	if sp.Settings.AuthRef == nil || *sp.Settings.AuthRef != want {
		t.Fatalf("external slot not resolved: %v", sp.Settings.AuthRef)
	}
	// An unknown backend is an ordinary slot to bind by hand.
	if *sp.Settings.OTPRef != store.CatalogUnboundRef(e.cat.ID, "other-backend") {
		t.Fatalf("unknown backend: %v", *sp.Settings.OTPRef)
	}
	c, err := e.db.GetCredential(want)
	if err != nil {
		t.Fatal(err)
	}
	ref := store.ParseBitwardenRef(c.Config)
	if ref == nil || ref.ServerID != servers[0] || ref.CipherID != "c-1" || ref.Field != "privatekey" || c.Kind != store.CredKey {
		t.Fatalf("credential: %+v %+v", c, ref)
	}
	if c.FolderID == nil || *c.FolderID != store.CatalogCredentialFolderID(e.cat.ID) {
		t.Fatalf("credential folder: %v", c.FolderID)
	}
	// Making it does not dirty the profile (derived, like catalog rows).
	before := e.db.ContentFingerprint()
	e.materialiseWith(nil)
	if e.db.ContentFingerprint() != before {
		t.Error("re-preparing changed the fingerprint")
	}
}

func TestExternalServerChoice(t *testing.T) {
	// None registered: unbound.
	e, _ := externalEnv(t, 0)
	e.materialiseWith(nil)
	if sp := e.folder("space"); *sp.Settings.AuthRef != store.CatalogUnboundRef(e.cat.ID, "ext") {
		t.Fatalf("no server: %v", *sp.Settings.AuthRef)
	}

	// Several: the one that holds the item.
	e, servers := externalEnv(t, 3)
	e.materialiseWith(func(server, cipher string) (bool, error) {
		if server == servers[0] {
			return false, errors.New("vault unreachable")
		}
		return server == servers[2] && cipher == "c-1", nil
	})
	c, err := e.db.GetCredential(store.CatalogCredentialID(e.cat.ID, "ext"))
	if err != nil || store.ParseBitwardenRef(c.Config).ServerID != servers[2] {
		t.Fatalf("picked: %+v %v", c, err)
	}

	// Several holding it: ambiguous, unbound until the user picks.
	e, _ = externalEnv(t, 2)
	e.materialiseWith(func(string, string) (bool, error) { return true, nil })
	if sp := e.folder("space"); *sp.Settings.AuthRef != store.CatalogUnboundRef(e.cat.ID, "ext") {
		t.Fatalf("ambiguous: %v", *sp.Settings.AuthRef)
	}
}

func TestExternalBindingWinsAndSlotRemoval(t *testing.T) {
	e, _ := externalEnv(t, 1)
	own, err := e.db.CreateCredential(store.NewCredential{Name: "own", Kind: store.CredAgent})
	if err != nil {
		t.Fatal(err)
	}
	e.materialiseWith(nil)
	if err := e.db.SetCatalogBinding(e.cat.ID, "ext", "cred", own.ID); err != nil {
		t.Fatal(err)
	}
	e.materialiseWith(nil)
	if sp := e.folder("space"); *sp.Settings.AuthRef != own.ID {
		t.Fatalf("manual binding did not win: %v", *sp.Settings.AuthRef)
	}
	if _, err := e.db.GetCredential(store.CatalogCredentialID(e.cat.ID, "ext")); err == nil {
		t.Error("made credential kept after the user bound the slot")
	}
	// The slot leaves the catalog: the made credential and folder go.
	_ = e.db.DeleteCatalogBinding(e.cat.ID, "ext")
	e.materialiseWith(nil)
	e.fs.serve(map[string]Feed{"c1": {Cursor: "c2", Items: []Item{del(KindSlots, "ext", 2)}}})
	e.sync()
	e.materialiseWith(nil)
	if _, err := e.db.GetCredential(store.CatalogCredentialID(e.cat.ID, "ext")); err == nil {
		t.Error("credential of a removed slot kept")
	}
	if fs, _ := e.db.ListCredentialFolders(); len(fs) != 0 {
		t.Errorf("empty catalog credential folder kept: %+v", fs)
	}
}

// Made credentials never reach an older app and survive its snapshot.
func TestExternalCredentialsShadowed(t *testing.T) {
	e, _ := externalEnv(t, 1)
	e.materialiseWith(nil)
	id := store.CatalogCredentialID(e.cat.ID, "ext")
	snap := filepath.Join(t.TempDir(), "snap.db")
	if _, err := e.db.Conn().Exec(`VACUUM INTO ?`, snap); err != nil {
		t.Fatal(err)
	}
	if err := store.ShadowSnapshot(snap, "n"); err != nil {
		t.Fatal(err)
	}
	raw, _ := sql.Open("sqlite", snap)
	var n int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM credential_refs WHERE id LIKE 'catcred-%'`).Scan(&n)
	var f int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM credential_folders WHERE id LIKE 'catcred-%'`).Scan(&f)
	raw.Close()
	if n != 0 || f != 0 {
		t.Fatalf("made credentials left in the snapshot: %d creds, %d folders", n, f)
	}
	// Another current machine takes them back.
	other, err := store.Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.MirrorFromWith(snap, store.ShadowTrust{FromSync: true, Nonce: "n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.GetCredential(id); err != nil {
		t.Fatalf("made credential not taken back: %v", err)
	}
	// A snapshot from an older app (no shadow, no made credentials) keeps them.
	legacy := filepath.Join(t.TempDir(), "legacy.db")
	l, _ := sql.Open("sqlite", legacy)
	_, _ = l.Exec(`CREATE TABLE credential_refs (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
		storage_mode TEXT NOT NULL DEFAULT 'managed', created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0)`)
	_, _ = l.Exec(`INSERT INTO credential_refs (id, name, kind) VALUES ('mine', 'mine', 'agent')`)
	l.Close()
	if _, err := e.db.MirrorFromWith(legacy, store.ShadowTrust{FromSync: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.GetCredential(id); err != nil {
		t.Fatalf("made credential lost to an older snapshot: %v", err)
	}
	if _, err := e.db.GetCredential("mine"); err != nil {
		t.Fatalf("the older snapshot's own credential missing: %v", err)
	}
}
