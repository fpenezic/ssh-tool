package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

func ptr[T any](v T) *T { return &v }

type pubFixture struct {
	outer, root, sub string // folders: outer is local, above the published root
	web, db1, inner  string // connections inside
	bastion          string // connection outside the subtree
	fwd              string
	credA, credBW    string
	prof             string
}

// publishFixture: Outer (username, env, credential A) > Mine (network
// profile, tunnel autostart) > Sub; web-1 with a Bitwarden-backed
// credential jumping through inner-bastion (inside), db-1 jumping through
// an outside bastion, a broadcast group, a forward with local data.
func publishFixture(t *testing.T, db *store.DB) pubFixture {
	t.Helper()
	var x pubFixture
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ca, err := db.CreateCredential(store.NewCredential{Name: "team-admin", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	must(err)
	cb, err := db.CreateCredential(store.NewCredential{Name: "bw-deploy", Kind: store.CredPassword, StorageMode: store.StorageExternal,
		Config: map[string]any{"bitwarden_ref": map[string]any{"server_id": "srv1", "cipher_id": "cipher-1", "field": "password"}}})
	must(err)
	np, err := db.CreateNetworkProfile("office-wg", `{"kind":"wireguard"}`)
	must(err)
	x.credA, x.credBW, x.prof = ca.ID, cb.ID, np.ID

	outer, err := db.CreateFolder(store.NewFolder{Name: "Outer", Settings: store.InheritableSettings{
		Username: ptr("deploy"), AuthRef: ptr(ca.ID), EnvVars: map[string]string{"TEAM": "ops"}}})
	must(err)
	root, err := db.CreateFolder(store.NewFolder{Name: "Mine", ParentID: &outer.ID, Settings: store.InheritableSettings{
		NetworkProfileID: ptr(np.ID), TunnelAutostart: ptr(true), InitialCommand: ptr("cd /srv")}})
	must(err)
	sub, err := db.CreateFolder(store.NewFolder{Name: "Sub", ParentID: &root.ID})
	must(err)
	x.outer, x.root, x.sub = outer.ID, root.ID, sub.ID

	bastion, err := db.CreateConnection(store.NewConnection{Name: "bastion", Hostname: "bastion.example.com",
		Overrides: store.InheritableSettings{Port: ptr(uint16(2200))}})
	must(err)
	inner, err := db.CreateConnection(store.NewConnection{Name: "inner-bastion", Hostname: "inner.example.com", FolderID: &root.ID})
	must(err)
	web, err := db.CreateConnection(store.NewConnection{Name: "web-1", Hostname: "web-1.example.com", FolderID: &sub.ID,
		Notes: "ask ops", Tags: []string{"web"},
		Overrides: store.InheritableSettings{AuthRef: ptr(cb.ID), BroadcastGroupID: ptr("grp-1"),
			JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &inner.ID}}}})
	must(err)
	db1, err := db.CreateConnection(store.NewConnection{Name: "db-1", Hostname: "db-1.example.com", FolderID: &sub.ID,
		Overrides: store.InheritableSettings{
			JumpHost:  &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}},
			DbTargets: []store.DbTarget{{ID: "t1", Name: "prod", CredentialRef: ptr(ca.ID)}}}})
	must(err)
	x.bastion, x.inner, x.web, x.db1 = bastion.ID, inner.ID, web.ID, db1.ID
	rh, rp := "127.0.0.1", uint16(5432)
	p, err := db.CreatePortForward(store.NewPortForward{ConnectionID: db1.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp,
		AutoStart: true, Description: "postgres"})
	must(err)
	must(db.SetPortForwardBookmarks(p.ID, []store.ProxyBookmark{{Name: "ui", URL: "http://localhost:5432"}}))
	x.fwd = p.ID
	return x
}

func resolvedJSON(t *testing.T, db *store.DB, id string) string {
	t.Helper()
	r, err := resolver.ResolveConnection(db, id)
	if err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func accept(plan *PublishPlan) []Accepted {
	var out []Accepted
	for _, r := range plan.Records {
		out = append(out, Accepted{Kind: r.Kind, UUID: r.UUID, Rev: 1})
	}
	for _, s := range plan.Request.Slots {
		out = append(out, Accepted{Kind: KindSlots, UUID: s.UUID, Rev: 1})
	}
	return out
}

func slotByKey(plan *PublishPlan, key string) *PlanSlot {
	for i := range plan.Slots {
		if plan.Slots[i].Key == key {
			return &plan.Slots[i]
		}
	}
	return nil
}

// The core promise: after publish and conversion, every connection of the
// publisher resolves exactly as before, with the same local ids.
func TestPublishRoundTripResolvesTheSame(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	before := map[string]string{}
	for _, id := range []string{x.web, x.db1, x.inner} {
		before[id] = resolvedJSON(t, e.db, id)
	}
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, AccountName: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Refusals) > 0 {
		t.Fatalf("refusals: %+v", plan.Refusals)
	}
	// Slots: credential A (folded from Outer, and the db target), the
	// Bitwarden one as external, the profile, the outside bastion as a
	// jump slot. The inside bastion is a reference, not a slot.
	if len(plan.Slots) != 4 {
		t.Fatalf("slots: %+v", plan.Slots)
	}
	if s := slotByKey(plan, "cred:"+x.credBW); s == nil || s.Kind != "external" || s.Backend != "bitwarden" {
		t.Fatalf("bitwarden slot: %+v", s)
	}
	if s := slotByKey(plan, "jump:"+x.bastion); s == nil || s.BindName != "bastion" {
		t.Fatalf("jump slot: %+v", s)
	}
	if len(plan.OutsideJumps) != 1 || plan.OutsideJumps[0].Name != "bastion" || plan.OutsideJumps[0].Include {
		t.Fatalf("outside jumps: %+v", plan.OutsideJumps)
	}
	// Nothing local leaves: no local ids, no broadcast group.
	body, _ := json.Marshal(plan.Request)
	for _, local := range []string{x.credA, x.credBW, x.prof, x.bastion, x.web, "grp-1", "srv1"} {
		if strings.Contains(string(body), local) {
			t.Fatalf("request carries local value %q: %s", local, body)
		}
	}
	if !strings.Contains(string(body), `"cipher-1"`) {
		t.Fatal("external slot does not name the item")
	}

	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	for id, want := range before {
		if got := resolvedJSON(t, e.db, id); got != want {
			t.Fatalf("connection %s resolves differently after publish:\n before %s\n after  %s", id, want, got)
		}
	}
	// Same rows, now catalog rows, under the catalog root.
	root, _ := e.db.GetFolder(x.root)
	if root.CatalogID == nil || *root.ParentID != e.cat.RootFolderID {
		t.Fatalf("root not converted or not moved: %+v", root)
	}
	p, _ := e.db.GetPortForward(x.fwd)
	if p.CatalogID == nil || !p.AutoStart || len(p.Bookmarks) != 1 {
		t.Fatalf("forward lost local data: %+v", p)
	}
	// The local-only bits are the publisher's layer.
	ovs, _ := e.db.ListCatalogOverrides(e.cat.ID)
	var layers []string
	for _, o := range ovs {
		layers = append(layers, o.JSON)
	}
	all := strings.Join(layers, " ")
	if !strings.Contains(all, "grp-1") || !strings.Contains(all, "tunnel_autostart") {
		t.Fatalf("layers: %v", layers)
	}
	// Nothing to bind for the publisher.
	inputs := map[string]bool{}
	bs, _ := e.db.ListCatalogBindings(e.cat.ID)
	for _, b := range bs {
		inputs[b.SlotUUID] = true
	}
	for _, s := range plan.Slots {
		if !inputs[s.UUID] {
			t.Fatalf("slot %s left unbound", s.Name)
		}
	}
	// The feed bringing the same records back at rev 1 changes nothing.
	var items []Item
	for _, f := range plan.Request.Folders {
		items = append(items, upRec(KindFolders, f, map[string]any{"rev": 1, "access": "write"}))
	}
	for _, c := range plan.Request.Connections {
		items = append(items, upRec(KindConnections, c, map[string]any{"rev": 1}))
	}
	for _, f := range plan.Request.Forwards {
		items = append(items, upRec(KindForwards, f, map[string]any{"rev": 1}))
	}
	for _, s := range plan.Request.Slots {
		items = append(items, upRec(KindSlots, s, map[string]any{"rev": 1}))
	}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: items}})
	e.sync()
	for id, want := range before {
		if got := resolvedJSON(t, e.db, id); got != want {
			t.Fatalf("connection %s changed after the feed echoed the publish", id)
		}
	}
	if n := len(e.mustConns()); n != 4 {
		t.Fatalf("%d connections after the echo, want 4", n)
	}
}

func upRec(kind string, rec any, extra map[string]any) Item {
	b, _ := json.Marshal(rec)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	for k, v := range extra {
		m[k] = v
	}
	return up(kind, m)
}

// What is folded onto the root is listed with its origin, folder level.
func TestPublishListsFreeTextAndInherited(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
	if err != nil {
		t.Fatal(err)
	}
	find := func(field string) *FreeText {
		for i := range plan.FreeText {
			if plan.FreeText[i].Field == field {
				return &plan.FreeText[i]
			}
		}
		return nil
	}
	seen := map[string]bool{}
	for _, f := range plan.FreeText {
		k := f.UUID + "/" + f.Field
		if seen[k] {
			t.Fatalf("%s listed twice", k)
		}
		seen[k] = true
	}
	if f := find("env_vars"); f == nil || !f.FolderLevel || f.Value != "TEAM=ops" {
		t.Fatalf("env vars: %+v", f)
	}
	if f := find("username"); f == nil || f.InheritedFrom != "Outer" {
		t.Fatalf("inherited username not listed with its origin: %+v", f)
	}
	if f := find("initial_command"); f == nil || !f.FolderLevel {
		t.Fatalf("initial command: %+v", f)
	}
	if f := find("notes"); f == nil || f.FolderLevel || f.Value != "ask ops" {
		t.Fatalf("notes: %+v", f)
	}
	if f := find("db_targets.0"); f == nil || f.Value != "name=prod" {
		t.Fatalf("db target: %+v", f)
	}
	names := 0
	for _, f := range plan.FreeText {
		if f.Kind == KindSlots && f.Field == "name" {
			names++
		}
	}
	if names != len(plan.Slots) {
		t.Fatalf("slot names listed %d, slots %d", names, len(plan.Slots))
	}
}

// "Include" publishes a copy of the outside bastion inside the root; the
// publisher's own bastion stays local and untouched.
func TestPublishIncludesABastionCopy(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root,
		JumpInclude: map[string]bool{x.bastion: true}})
	if err != nil {
		t.Fatal(err)
	}
	if slotByKey(plan, "jump:"+x.bastion) != nil {
		t.Fatal("jump slot created although the bastion is included")
	}
	var copyUUID string
	for _, r := range plan.Records {
		if r.CopyOf == x.bastion {
			copyUUID = r.UUID
			if r.LocalID != "" {
				t.Fatal("the copy claims the local bastion row")
			}
		}
	}
	if copyUUID == "" {
		t.Fatal("no bastion copy")
	}
	var db1 PublishConnection
	var copyRec PublishConnection
	for _, c := range plan.Request.Connections {
		switch c.Name {
		case "db-1":
			db1 = c
		case "bastion":
			copyRec = c
		}
	}
	if !strings.Contains(string(db1.Settings), copyUUID) {
		t.Fatalf("db-1 does not jump through the copy: %s", db1.Settings)
	}
	if copyRec.FolderUUID != plan.RootUUID || !strings.Contains(string(copyRec.Settings), `"port":2200`) {
		t.Fatalf("copy not in the root or lost its settings: %+v", copyRec)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := e.db.GetConnection(x.bastion)
	if b.CatalogID != nil || b.FolderID != nil {
		t.Fatalf("the publisher's bastion changed: %+v", b)
	}
}

// Publishing again into the published folder reuses the slot bound to the
// same credential instead of creating a second one.
func TestPublishReusesSlotsInScope(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	credA := slotByKey(plan, "cred:"+x.credA)

	more, _ := e.db.CreateFolder(store.NewFolder{Name: "More"})
	cb2, _ := e.db.CreateCredential(store.NewCredential{Name: "bw-other-copy", Kind: store.CredPassword, StorageMode: store.StorageExternal,
		Config: map[string]any{"bitwarden_ref": map[string]any{"server_id": "srv2", "cipher_id": "cipher-1", "field": "password"}}})
	if _, err := e.db.CreateConnection(store.NewConnection{Name: "app-1", Hostname: "app-1.example.com", FolderID: &more.ID,
		Overrides: store.InheritableSettings{AuthRef: ptr(x.credA), OTPRef: ptr(cb2.ID)}}); err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: more.ID, TargetUUID: plan.RootUUID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.Refusals) > 0 {
		t.Fatalf("refusals: %+v", plan2.Refusals)
	}
	s := slotByKey(plan2, "cred:"+x.credA)
	if s == nil || s.New || s.UUID != credA.UUID || s.ReusedFrom != "Mine" {
		t.Fatalf("credential slot not reused: %+v", s)
	}
	bw := slotByKey(plan2, "cred:"+cb2.ID)
	if bw == nil || bw.New {
		t.Fatalf("external slot with the same item not reused: %+v", bw)
	}
	if len(plan2.Request.Slots) != 0 {
		t.Fatalf("new slots sent: %+v", plan2.Request.Slots)
	}
	if plan2.Request.ParentUUID == nil || *plan2.Request.ParentUUID != plan.RootUUID {
		t.Fatal("parent uuid not the target")
	}
}

func TestPublishRefusals(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	if _, err := e.db.CreateConnection(store.NewConnection{Name: "shell", Protocol: "local", FolderID: &x.sub}); err != nil {
		t.Fatal(err)
	}
	dyn, _ := e.db.CreateFolder(store.NewFolder{Name: "Proxmox", ParentID: &x.root})
	if err := e.db.CreateDynamicFolder(store.DynamicFolder{FolderID: dyn.ID, Provider: "proxmox", Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.CreateConnection(store.NewConnection{Name: "detached", Hostname: "d.example.com", FolderID: &x.sub,
		Overrides: store.InheritableSettings{AuthRef: ptr(store.CatalogUnboundRef("gone", "slot-1"))}}); err != nil {
		t.Fatal(err)
	}
	empty, _ := e.db.CreateFolder(store.NewFolder{Name: "Empty"})
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range plan.Refusals {
		got[r.Name] = true
	}
	for _, want := range []string{"shell", "Proxmox", "detached"} {
		if !got[want] {
			t.Fatalf("no refusal for %s: %+v", want, plan.Refusals)
		}
	}
	plan, _ = BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: empty.ID})
	if len(plan.Refusals) != 1 || !strings.Contains(plan.Refusals[0].Reason, "empty") {
		t.Fatalf("empty folder: %+v", plan.Refusals)
	}
	// A pending publish blocks a second one of the same rows.
	_ = e.db.RecordPublishPending(e.cat.ID, []store.PublishPending{{Kind: "connections", RemoteUUID: "u1", LocalID: x.web}})
	plan, _ = BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.sub})
	found := false
	for _, r := range plan.Refusals {
		found = found || strings.Contains(r.Reason, "not settled")
	}
	if !found {
		t.Fatalf("pending publish not refused: %+v", plan.Refusals)
	}
}

// A target the caller can only read is refused before anything is built.
func TestPublishNeedsWriteOnTarget(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{up(KindFolders, folderRec("ro", 1, nil, "Read only"))}}})
	e.sync()
	if _, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, TargetUUID: "ro"}); err == nil ||
		!strings.Contains(err.Error(), "cannot add") {
		t.Fatalf("err = %v", err)
	}
}
