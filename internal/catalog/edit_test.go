package catalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

func TestMergePatch(t *testing.T) {
	old := map[string]any{"a": 1.0, "b": "x", "s": map[string]any{"k": "v", "gone": true, "keep": 1.0}, "arr": []any{1.0}}
	nw := map[string]any{"a": 1.0, "b": "y", "s": map[string]any{"k": "w", "keep": 1.0, "new": "n"}, "arr": []any{1.0, 2.0}}
	got := MergePatch(old, nw)
	want := map[string]any{"b": "y", "s": map[string]any{"k": "w", "gone": nil, "new": "n"}, "arr": []any{1.0, 2.0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch %v, want %v", got, want)
	}
	if p := MergePatch(old, old); len(p) != 0 {
		t.Fatalf("equal inputs: %v", p)
	}
}

// editEnv: a catalog folder the user writes to, with slot s1 bound to the
// user's credential A, a jump slot j1 nobody bound, connection c1 using s1
// and a settings key this app does not know.
type editEnv struct {
	*syncEnv
	credA, credB string
	conn         *store.Connection
	scope        RecordScope
	form         EditForm
	raw          json.RawMessage
}

func newEditEnv(t *testing.T) *editEnv {
	e := &editEnv{syncEnv: newSyncEnv(t)}
	a, _ := e.db.CreateCredential(store.NewCredential{Name: "admin key", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	b, _ := e.db.CreateCredential(store.NewCredential{Name: "deploy key", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	e.credA, e.credB = a.ID, b.ID
	f := folderRec("f1", 1, nil, "Team")
	f["access"] = "write"
	other := folderRec("f2", 1, nil, "Other")
	c := connRec("c1", 3, "f1", "web-1")
	c["settings"] = map[string]any{"auth_ref": "s1", "port": 2222, "x_future": map[string]any{"a": 1}}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, f), up(KindFolders, other),
		up(KindSlots, map[string]any{"uuid": "s1", "rev": 1, "folder_uuid": "f1", "type": "cred", "name": "admin", "kind": "slot"}),
		up(KindSlots, map[string]any{"uuid": "j1", "rev": 1, "folder_uuid": "f1", "type": "jump", "name": "bastion"}),
		up(KindSlots, map[string]any{"uuid": "s9", "rev": 1, "folder_uuid": "f2", "type": "cred", "name": "elsewhere", "kind": "slot"}),
		up(KindConnections, c),
	}}})
	if err := e.db.SetCatalogBinding(e.cat.ID, "s1", "cred", e.credA); err != nil {
		t.Fatal(err)
	}
	e.sync()
	e.conn = e.syncEnv.conn("c1")
	fl := e.folder("f1")
	e.scope = RecordScope{FolderUUID: "f1", FolderLocalID: fl.ID}
	var raw string
	_ = e.db.Conn().QueryRow(`SELECT catalog_json FROM connections WHERE id = ?`, e.conn.ID).Scan(&raw)
	e.raw = json.RawMessage(raw)
	form, err := FormOf(e.db, e.cat.ID, KindConnections, e.raw)
	if err != nil {
		t.Fatal(err)
	}
	e.form = form
	return e
}

func (e *editEnv) plan(t *testing.T, edit func(*EditForm), mod func(*EditInput)) *EditPlan {
	t.Helper()
	edited := e.form
	b, _ := json.Marshal(e.form)
	_ = json.Unmarshal(b, &edited) // deep copy
	edit(&edited)
	in := EditInput{Kind: KindConnections, LocalID: e.conn.ID, Rev: 3, Loaded: e.form, Edited: edited}
	if mod != nil {
		mod(&in)
	}
	p, err := PlanEdit(e.db, e.cat, e.scope, in, e.raw, 3)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanEditOnlyChangedFields(t *testing.T) {
	e := newEditEnv(t)
	if e.form.Settings.AuthRef == nil || *e.form.Settings.AuthRef != e.credA {
		t.Fatalf("form shows the slot as the user's credential: %+v", e.form.Settings.AuthRef)
	}
	p := e.plan(t, func(f *EditForm) {}, nil)
	if len(p.Changed) != 0 || p.Patch != nil {
		t.Fatalf("no edit: %+v", p)
	}
	p = e.plan(t, func(f *EditForm) {
		f.Notes = "new notes"
		port := uint16(2200)
		f.Settings.Port = &port
	}, nil)
	b, _ := json.Marshal(p.Patch)
	if string(b) != `{"notes":"new notes","settings":{"port":2200}}` {
		t.Fatalf("patch %s", b)
	}
}

func TestPlanEditReferences(t *testing.T) {
	e := newEditEnv(t)
	// Picking the user's own credential A again: the slot bound to it.
	p := e.plan(t, func(f *EditForm) { f.Settings.OTPRef = &e.credA }, nil)
	if got := p.Patch["settings"].(map[string]any)["otp_ref"]; got != "s1" {
		t.Fatalf("otp_ref = %v, want the bound slot s1", got)
	}
	// Credential B: no slot stands for it - a new one to name first.
	p = e.plan(t, func(f *EditForm) { f.Settings.AuthRef = &e.credB }, nil)
	if len(p.NeedSlots) != 1 || p.NeedSlots[0].Name != "deploy key" {
		t.Fatalf("need slots: %+v", p.NeedSlots)
	}
	key := p.NeedSlots[0].Key
	p = e.plan(t, func(f *EditForm) { f.Settings.AuthRef = &e.credB }, func(in *EditInput) { in.SlotNames = map[string]string{key: "deploy"} })
	if len(p.NeedSlots) != 0 || len(p.NewSlots) != 1 || p.NewSlots[0].Name != "deploy" || p.NewSlots[0].FolderUUID != "f1" {
		t.Fatalf("named slot: %+v", p.NewSlots)
	}
	if p.Patch["settings"].(map[string]any)["auth_ref"] != p.NewSlots[0].UUID {
		t.Fatal("auth_ref does not name the new slot")
	}
	// The unbound jump slot's marker goes back as the slot.
	p = e.plan(t, func(f *EditForm) {
		m := store.CatalogUnboundRef(e.cat.ID, "j1")
		f.Settings.JumpHost = &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &m}}
	}, nil)
	b, _ := json.Marshal(p.Patch["settings"])
	if !strings.Contains(string(b), `"jump_slot":"j1"`) {
		t.Fatalf("jump: %s", b)
	}
	// A slot of another folder is refused before sending.
	p = e.plan(t, func(f *EditForm) {
		m := store.CatalogUnboundRef(e.cat.ID, "s9")
		f.Settings.AuthRef = &m
	}, nil)
	if len(p.Refusals) == 0 {
		t.Fatal("out-of-scope slot not refused")
	}
	// Protocol local is refused.
	p = e.plan(t, func(f *EditForm) { f.Protocol = "local" }, nil)
	if len(p.Refusals) == 0 {
		t.Fatal("protocol local not refused")
	}
	// Removing a key sends null, and the unknown key is never touched.
	p = e.plan(t, func(f *EditForm) { f.Settings.Port = nil }, nil)
	b, _ = json.Marshal(p.Patch)
	if string(b) != `{"settings":{"port":null}}` {
		t.Fatalf("patch %s", b)
	}
}

func TestPlanEditConflicts(t *testing.T) {
	e := newEditEnv(t)
	// Someone else changed notes and the port meanwhile (rev 4).
	var cur map[string]any
	_ = json.Unmarshal(e.raw, &cur)
	cur["rev"] = 4
	cur["notes"] = "theirs"
	cur["settings"].(map[string]any)["port"] = 2300
	curRaw, _ := json.Marshal(cur)
	edit := func(f *EditForm) {
		f.Notes = "mine"
		f.Hostname = "web-1b.example.com"
	}
	in := func(mod func(*EditInput)) *EditPlan {
		edited := e.form
		edit(&edited)
		x := EditInput{Kind: KindConnections, LocalID: e.conn.ID, Rev: 3, Loaded: e.form, Edited: edited}
		if mod != nil {
			mod(&x)
		}
		p, err := PlanEdit(e.db, e.cat, e.scope, x, curRaw, 4)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := in(nil)
	if p.ConflictRev != 4 || len(p.Conflicts) != 2 {
		t.Fatalf("conflicts: %+v", p)
	}
	for _, c := range p.Conflicts {
		switch c.Field {
		case "notes":
			if !c.Both || c.Mine != "mine" || c.Theirs != "theirs" {
				t.Fatalf("notes: %+v", c)
			}
		case "hostname":
			if c.Both {
				t.Fatalf("hostname only mine: %+v", c)
			}
		}
	}
	// Re-apply keeping theirs for notes: only the hostname goes.
	p = in(func(x *EditInput) { x.AcceptRev = 4; x.Resolve = map[string]string{"notes": "theirs"} })
	b, _ := json.Marshal(p.Patch)
	if string(b) != `{"hostname":"web-1b.example.com"}` {
		t.Fatalf("re-apply theirs: %s", b)
	}
	p = in(func(x *EditInput) { x.AcceptRev = 4; x.Resolve = map[string]string{"notes": "mine"} })
	b, _ = json.Marshal(p.Patch)
	if string(b) != `{"hostname":"web-1b.example.com","notes":"mine"}` {
		t.Fatalf("re-apply mine: %s", b)
	}
}
