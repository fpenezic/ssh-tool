package catalog

import (
	"database/sql"
	"encoding/json"
	"reflect"
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
	// Slots: only the Bitwarden credential, as external. Credential A (a
	// local vault credential) and the profile stay the publisher's; the
	// outside bastion goes as a copy inside the folder, the inside one as
	// a reference.
	if len(plan.Slots) != 1 {
		t.Fatalf("slots: %+v", plan.Slots)
	}
	if s := slotByKey(plan, "cred:"+x.credBW); s == nil || s.Kind != "external" || s.Backend != "bitwarden" {
		t.Fatalf("bitwarden slot: %+v", s)
	}
	if len(plan.OutsideJumps) != 1 || plan.OutsideJumps[0].Name != "bastion" || !plan.OutsideJumps[0].Include {
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
	// The four of the fixture plus the bastion copy published in the folder.
	if n := len(e.mustConns()); n != 5 {
		t.Fatalf("%d connections after the echo, want 5", n)
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
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
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
// same credential instead of creating a second one. A local credential
// gets no slot, now or later.
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
	if slotByKey(plan, "cred:"+x.credA) != nil {
		t.Fatal("a local credential became a slot")
	}
	credBW := slotByKey(plan, "cred:"+x.credBW)

	more, _ := e.db.CreateFolder(store.NewFolder{Name: "More"})
	cb2, _ := e.db.CreateCredential(store.NewCredential{Name: "bw-other-copy", Kind: store.CredPassword, StorageMode: store.StorageExternal,
		Config: map[string]any{"bitwarden_ref": map[string]any{"server_id": "srv2", "cipher_id": "cipher-1", "field": "password"}}})
	if _, err := e.db.CreateConnection(store.NewConnection{Name: "app-1", Hostname: "app-1.example.com", FolderID: &more.ID,
		Overrides: store.InheritableSettings{AuthRef: ptr(x.credBW), OTPRef: ptr(cb2.ID)}}); err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: more.ID, TargetUUID: plan.RootUUID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.Refusals) > 0 {
		t.Fatalf("refusals: %+v", plan2.Refusals)
	}
	s := slotByKey(plan2, "cred:"+x.credBW)
	if s == nil || s.New || s.UUID != credBW.UUID || s.ReusedFrom != "Mine" {
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

// "What to send": unticked categories stay out of the request, the review
// and the slots, and stay the publisher's own, so their tree is the same
// after publish and after the feed echoes it. An uploaded icon image is
// never sent and stays theirs too.
func TestPublishUnsentStaysLocal(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	img, err := e.db.PutImage([]byte("\x89PNG\r\n\x1a\nfake"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetConnectionIcon(x.db1, img); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetConnectionNamedIcon(x.web, "server", "blue"); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, id := range []string{x.web, x.db1, x.inner} {
		before[id] = resolvedJSON(t, e.db, id)
	}
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, AccountName: "bob",
		Unsent: map[string]bool{"notes": true, "database": true, "startup": true, "forwards": true, "icons": true}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(plan.Request)
	for _, gone := range []string{"ask ops", "db_targets", "initial_command", "cd /srv", "env_vars", `"server"`, img} {
		if strings.Contains(string(body), gone) {
			t.Fatalf("request carries unsent %q: %s", gone, body)
		}
	}
	if len(plan.Request.Forwards) != 0 {
		t.Fatalf("forwards sent: %+v", plan.Request.Forwards)
	}
	for _, f := range plan.FreeText {
		if f.Field == "notes" || f.Field == "initial_command" || f.Field == "env_vars" || strings.HasPrefix(f.Field, "db_targets") {
			t.Fatalf("review lists unsent %s", f.Field)
		}
	}
	if !strings.Contains(string(body), `"web"`) {
		t.Fatal("tags were sent unticked? they should go")
	}

	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		for id, want := range before {
			if got := resolvedJSON(t, e.db, id); got != want {
				t.Fatalf("%s: %s resolves differently:\n before %s\n after  %s", when, id, want, got)
			}
		}
		w, _ := e.db.GetConnection(x.web)
		if w.Notes != "ask ops" || w.IconName == nil || *w.IconName != "server" {
			t.Fatalf("%s: web-1 lost its notes or icon: %q %v", when, w.Notes, w.IconName)
		}
		d, _ := e.db.GetConnection(x.db1)
		if d.IconImageID == nil || *d.IconImageID != img {
			t.Fatalf("%s: db-1 lost its image: %v", when, d.IconImageID)
		}
		p, _ := e.db.GetPortForward(x.fwd)
		if p == nil || p.CatalogID != nil || p.ConnectionID != x.db1 {
			t.Fatalf("%s: the unsent forward is not the publisher's own on db-1: %+v", when, p)
		}
	}
	check("after publish")
	var items []Item
	for _, f := range plan.Request.Folders {
		items = append(items, upRec(KindFolders, f, map[string]any{"rev": 1, "access": "write"}))
	}
	for _, c := range plan.Request.Connections {
		items = append(items, upRec(KindConnections, c, map[string]any{"rev": 1}))
	}
	for _, s := range plan.Request.Slots {
		items = append(items, upRec(KindSlots, s, map[string]any{"rev": 1}))
	}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: items}})
	e.sync()
	check("after the feed echo")
}

// Uploaded icons: a valid image goes as icon_image (and the publisher's row
// keeps showing it, through its hash); one the catalog would refuse stays
// in the publisher's layer with the reason; one held back by the upload
// limit is marked for the background upload.
func TestPublishIconImages(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	good := pngOf(t, 16, 16)
	gid, err := e.db.PutImage(good, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	bid, err := e.db.PutImage([]byte("\x89PNG\r\n\x1a\nfake"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetConnectionIcon(x.db1, gid); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetConnectionIcon(x.web, bid); err != nil {
		t.Fatal(err)
	}
	build := func() *PublishPlan {
		t.Helper()
		lim := DefaultImageLimits
		plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, AccountName: "bob", Images: &lim})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	conn := func(plan *PublishPlan, name string) PublishConnection {
		for _, c := range plan.Request.Connections {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("no %s in request", name)
		return PublishConnection{}
	}
	uuidOf := func(plan *PublishPlan, local string) string {
		for _, r := range plan.Records {
			if r.LocalID == local {
				return r.UUID
			}
		}
		return ""
	}

	plan := build()
	if c := conn(plan, "db-1"); c.IconImage == nil || *c.IconImage != ImageRef(good) || c.IconName != nil {
		t.Fatalf("db-1 icon not sent as an image: %+v", c)
	}
	if c := conn(plan, "web-1"); c.IconImage != nil {
		t.Fatalf("web-1 sent an image the catalog refuses: %v", *c.IconImage)
	}
	if len(plan.Images) != 2 || len(plan.SentImages()) != 1 {
		t.Fatalf("plan images: %+v", plan.Images)
	}
	for _, pi := range plan.Images {
		if pi.ImageID == bid && (!pi.Kept || pi.Reason == "") {
			t.Fatalf("refused image without a reason: %+v", pi)
		}
	}
	if l := plan.Layers[uuidOf(plan, x.web)].JSON; !strings.Contains(l, bid) {
		t.Fatalf("web-1 layer does not keep its image: %s", l)
	}

	// Held back by the upload limit.
	held := build()
	held.KeepImage(ImageSHA256(good), "the catalog's upload limit - sent later", true)
	if c := conn(held, "db-1"); c.IconImage != nil {
		t.Fatalf("held image still in the request")
	}
	l := held.Layers[uuidOf(held, x.db1)].JSON
	if !strings.Contains(l, gid) || !strings.Contains(l, IconPendingKey) {
		t.Fatalf("db-1 layer lacks the pending image: %s", l)
	}

	// Sent: the converted row shows the user's own image row again.
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{x.db1: gid, x.web: bid} {
		c, _ := e.db.GetConnection(id)
		if c.IconImageID == nil || *c.IconImageID != want {
			t.Fatalf("%s icon after publish: %v, want %s", c.Name, c.IconImageID, want)
		}
	}
}

// By default only shared sources go as slots: the Bitwarden credential
// does, the vault credential and the network profile stay the
// publisher's own (listed, no slot), and the publisher's tree resolves
// the same. A reference to something that no longer exists, and a jump
// through an inventory host, are kept too instead of blocking.
func TestPublishKeepsLocalRefs(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	gone := "11111111-2222-3333-4444-555555555555"
	if _, err := e.db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.network_profile_id', ?) WHERE id = ?`, gone, x.inner); err != nil {
		t.Fatal(err)
	}
	dyn := "dyn:pve/101"
	if _, err := e.db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.jump_host', json(?)) WHERE id = ?`,
		`{"kind":"chain","chain":{"connection_id":"`+dyn+`"}}`, x.db1); err != nil {
		t.Fatal(err)
	}
	// db-1 jumps through an inventory host, which a test cannot resolve:
	// its layer is checked instead.
	before := map[string]string{}
	for _, id := range []string{x.web, x.inner} {
		before[id] = resolvedJSON(t, e.db, id)
	}
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, AccountName: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Refusals) > 0 {
		t.Fatalf("refusals: %+v", plan.Refusals)
	}
	if len(plan.Slots) != 1 || slotByKey(plan, "cred:"+x.credBW) == nil {
		t.Fatalf("slots: %+v", plan.Slots)
	}
	body, _ := json.Marshal(plan.Request)
	for _, local := range []string{x.credA, x.prof, gone, dyn} {
		if strings.Contains(string(body), local) {
			t.Fatalf("request carries %s: %s", local, body)
		}
	}
	reasons := map[string]bool{}
	for _, k := range plan.KeptLocal {
		reasons[k.Field] = true
	}
	for _, f := range []string{"Credential", "Network", "Jump host"} {
		if !reasons[f] {
			t.Fatalf("kept list lacks %s: %+v", f, plan.KeptLocal)
		}
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	for id, want := range before {
		if got := resolvedJSON(t, e.db, id); got != want {
			t.Fatalf("%s resolves differently:\n before %s\n after  %s", id, want, got)
		}
	}
	if c, _ := e.db.GetConnection(x.db1); c.Overrides.JumpHost == nil || !strings.Contains(mustJSONc(c.Overrides.JumpHost), dyn) {
		t.Fatalf("db-1 lost its own jump host")
	}
}

func mustJSONc(v any) string { b, _ := json.Marshal(v); return string(b) }

// A jump through an inventory host goes as a bastion record made from
// what the host resolves to now; the publisher keeps jumping through
// the inventory host. Skipped, it stays local.
func TestPublishSendsInventoryBastion(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	dyn := "dyn:pve/101"
	if _, err := e.db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.jump_host', json(?)) WHERE id = ?`,
		`{"kind":"chain","chain":{"connection_id":"`+dyn+`"}}`, x.db1); err != nil {
		t.Fatal(err)
	}
	resolve := func(id string) (store.Connection, bool) {
		return store.Connection{Name: "pve-bastion", Hostname: "10.0.0.5", Overrides: store.InheritableSettings{Username: ptr("jump"), Port: ptr(uint16(2222))}}, id == dyn
	}
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, AccountName: "bob", ResolveDyn: resolve})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	var bastion *PublishConnection
	for i, c := range plan.Request.Connections {
		if c.Hostname == "10.0.0.5" {
			bastion = &plan.Request.Connections[i]
		}
	}
	if bastion == nil || bastion.Name != "pve-bastion" {
		t.Fatalf("no bastion record: %+v", plan.Request.Connections)
	}
	body, _ := json.Marshal(plan.Request)
	if strings.Contains(string(body), dyn) || !strings.Contains(string(body), bastion.UUID) {
		t.Fatalf("db-1 does not jump through the record: %s", body)
	}
	if len(plan.OutsideJumps) != 1 || !plan.OutsideJumps[0].Dynamic || !plan.OutsideJumps[0].Include {
		t.Fatalf("outside jumps: %+v", plan.OutsideJumps)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.db.GetConnection(x.db1); c.Overrides.JumpHost == nil || !strings.Contains(mustJSONc(c.Overrides.JumpHost), dyn) {
		t.Fatalf("publisher lost the inventory jump")
	}

	// Skipped: nothing sent, kept local.
	e2 := newSyncEnv(t)
	x2 := publishFixture(t, e2.db)
	if _, err := e2.db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.jump_host', json(?)) WHERE id = ?`,
		`{"kind":"chain","chain":{"connection_id":"`+dyn+`"}}`, x2.db1); err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPublish(e2.db, PublishOptions{CatalogID: e2.cat.ID, RootID: x2.root, ResolveDyn: resolve, JumpSkip: map[string]bool{dyn: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan2.Request.Connections {
		if c.Hostname == "10.0.0.5" {
			t.Fatal("skipped bastion sent")
		}
	}
	if len(plan2.OutsideJumps) != 1 || plan2.OutsideJumps[0].Include {
		t.Fatalf("skipped outside jump: %+v", plan2.OutsideJumps)
	}
}

// A typed jump host (hostname) goes as it is; a vault credential on the
// hop is not sent, and the publisher keeps it.
func TestPublishManualJumpKeepsOwnCredential(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	if _, err := e.db.Conn().Exec(`UPDATE connections SET overrides_json = json_set(overrides_json, '$.jump_host', json(?)) WHERE id = ?`,
		`{"kind":"chain","chain":{"hostname":"pve-bastion.example.com","auth_ref":"`+x.credA+`"}}`, x.db1); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, AccountName: "bob"})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	body, _ := json.Marshal(plan.Request)
	if !strings.Contains(string(body), "pve-bastion.example.com") || strings.Contains(string(body), x.credA) {
		t.Fatalf("request: %s", body)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.db.GetConnection(x.db1); c.Overrides.JumpHost == nil || !strings.Contains(mustJSONc(c.Overrides.JumpHost), x.credA) {
		t.Fatalf("publisher lost the hop credential: %v", mustJSONc(c.Overrides.JumpHost))
	}
}

// Publishing under a catalog folder that sets what the published folder
// leaves unset lists it: the publisher's connections inherit it after.
func TestPublishListsTargetInherits(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	// Mine (now catalog) resolves port 2222 from its settings.
	if err := e.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE folders SET settings_json = json_set(settings_json, '$.port', 2222) WHERE id = ?`, x.root)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	more, _ := e.db.CreateFolder(store.NewFolder{Name: "More"})
	if _, err := e.db.CreateConnection(store.NewConnection{Name: "app-1", Hostname: "app-1.example.com", FolderID: &more.ID}); err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: more.ID, TargetUUID: plan.RootUUID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range plan2.TargetInherits {
		found = found || s == "port: 2222"
	}
	if !found {
		t.Fatalf("target inherits: %v", plan2.TargetInherits)
	}
}

// The user can keep a Bitwarden item local: all shared sources at once,
// or one credential; the review offers it back.
func TestPublishWithholdsChosenCredentials(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, SkipCreds: map[string]bool{x.credBW: true}})
	if err != nil {
		t.Fatal(err)
	}
	if slotByKey(plan, "cred:"+x.credBW) != nil {
		t.Fatalf("skipped credential still a slot: %+v", plan.Slots)
	}
	offered := false
	for _, k := range plan.KeptLocal {
		offered = offered || k.Cred == x.credBW
	}
	if !offered {
		t.Fatalf("kept list does not offer it back: %+v", plan.KeptLocal)
	}
	plan2, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, NoSharedCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan2.Slots {
		if s.Type == "cred" {
			t.Fatalf("a credential slot with shared sources off: %+v", s)
		}
	}
}

// An outside bastion the publisher chose not to send stays theirs: no
// copy, no slot, listed under what stays local.
func TestPublishOutsideBastionSkipped(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, JumpSkip: map[string]bool{x.bastion: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.OutsideJumps) != 1 || plan.OutsideJumps[0].Include {
		t.Fatalf("outside jumps: %+v", plan.OutsideJumps)
	}
	for _, s := range plan.Slots {
		if s.Type == slotJump {
			t.Fatalf("jump slot made: %+v", s)
		}
	}
	kept := false
	for _, k := range plan.KeptLocal {
		kept = kept || k.Field == "Jump host"
	}
	if !kept {
		t.Fatalf("skipped bastion not listed as kept: %+v", plan.KeptLocal)
	}
	if len(plan.Request.Connections) != 3 {
		t.Fatalf("connections sent: %d (a copy of the skipped bastion?)", len(plan.Request.Connections))
	}
}

// One connection goes straight into a catalog folder: no folder record,
// what it inherits locally folded onto it, its forward with it, the
// outside bastion as a copy, and the row converted in place under the
// target with the same resolution as before.
func TestPublishSingleConnection(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	mine, _ := e.db.CreateFolder(store.NewFolder{Name: "Mine2", Settings: store.InheritableSettings{Username: ptr("ops"), Port: ptr(uint16(2200))}})
	solo, err := e.db.CreateConnection(store.NewConnection{Name: "solo", Hostname: "solo.example.com", FolderID: &mine.ID,
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &x.bastion}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.CreatePortForward(store.NewPortForward{ConnectionID: solo.ID, Kind: "local", LocalPort: ptr(uint16(15432)),
		RemoteHost: ptr("127.0.0.1"), RemotePort: ptr(uint16(5432))}); err != nil {
		t.Fatal(err)
	}
	before := resolvedJSON(t, e.db, solo.ID)

	p2, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, ConnID: solo.ID, TargetUUID: plan.RootUUID})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Refusals) > 0 {
		t.Fatalf("refusals: %+v", p2.Refusals)
	}
	r := p2.Request
	if len(r.Folders) != 0 || len(r.Forwards) != 1 || len(r.Connections) != 2 {
		t.Fatalf("request: %d folders, %d connections, %d forwards", len(r.Folders), len(r.Connections), len(r.Forwards))
	}
	for _, c := range r.Connections {
		if c.FolderUUID != plan.RootUUID {
			t.Fatalf("%s not in the target: %s", c.Name, c.FolderUUID)
		}
	}
	body, _ := json.Marshal(r.Connections[0].Settings)
	if !strings.Contains(string(body), `"username":"ops"`) || !strings.Contains(string(body), `"port":2200`) {
		t.Fatalf("inherited settings not folded: %s", body)
	}
	if len(p2.FreeText) == 0 {
		t.Fatal("folded values not listed for review")
	}
	if err := CompletePublish(e.db, p2, accept(p2), "bob", nil); err != nil {
		t.Fatal(err)
	}
	c, _ := e.db.GetConnection(solo.ID)
	if c.CatalogID == nil || c.FolderID == nil || *c.FolderID != plan.Records[0].LocalID {
		t.Fatalf("not converted into the target: %+v", c)
	}
	// What it had resolves the same; what the target folder sets on top
	// was listed in the review (target_inherits), as for a folder publish.
	var was, now map[string]any
	_ = json.Unmarshal([]byte(before), &was)
	_ = json.Unmarshal([]byte(resolvedJSON(t, e.db, solo.ID)), &now)
	for _, k := range []string{"hostname", "username", "port", "jump_host"} {
		if !reflect.DeepEqual(was[k], now[k]) {
			t.Fatalf("%s changed: %v -> %v", k, was[k], now[k])
		}
	}
	listed := strings.Join(p2.TargetInherits, "; ")
	if !strings.Contains(listed, "initial_command") {
		t.Fatalf("target inherits not listed: %v", p2.TargetInherits)
	}

	// Without a target folder it is refused before anything is planned.
	if _, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, ConnID: x.bastion}); err == nil {
		t.Fatal("a single connection into a new space allowed")
	}
}

// User names unticked: no username in the request, the publisher keeps
// theirs in their layer and resolves as before.
func TestPublishWithoutUsernames(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	before := resolvedJSON(t, e.db, x.web)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root, Unsent: map[string]bool{"username": true}})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	body, _ := json.Marshal(plan.Request)
	if strings.Contains(string(body), `"username"`) {
		t.Fatalf("a username was sent: %s", body)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	if got := resolvedJSON(t, e.db, x.web); got != before {
		t.Fatalf("resolves differently:\n before %s\n after  %s", before, got)
	}
}

// Catalog-level slots (revision 12): a second space using the same shared
// credential reuses the first space's slot by moving it to the catalog
// level (admin only, and not when the user unticks the move); new slots
// can be made at the catalog level; a slot already there is in scope for
// every publish.
func TestPublishCatalogLevelSlots(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	plan, err := BuildPublish(e.db, PublishOptions{CatalogID: e.cat.ID, RootID: x.root})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompletePublish(e.db, plan, accept(plan), "bob", nil); err != nil {
		t.Fatal(err)
	}
	first := slotByKey(plan, "cred:"+x.credBW)
	if first == nil || !first.New {
		t.Fatalf("first space slot: %+v", first)
	}

	space2, _ := e.db.CreateFolder(store.NewFolder{Name: "Space two"})
	if _, err := e.db.CreateConnection(store.NewConnection{Name: "b-1", Hostname: "b-1.example.com", FolderID: &space2.ID,
		Overrides: store.InheritableSettings{AuthRef: ptr(x.credBW)}}); err != nil {
		t.Fatal(err)
	}
	build := func(o PublishOptions) *PublishPlan {
		t.Helper()
		o.CatalogID, o.RootID = e.cat.ID, space2.ID
		p, err := BuildPublish(e.db, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Refusals) > 0 {
			t.Fatalf("refusals: %+v", p.Refusals)
		}
		return p
	}
	// Not admin, or an older server: a new slot in the new space, as before.
	for _, o := range []PublishOptions{{Revision: CatalogSlotsRevision}, {Admin: true, Revision: CatalogSlotsRevision - 1}} {
		if s := slotByKey(build(o), "cred:"+x.credBW); s == nil || !s.New || s.MoveToCatalog {
			t.Fatalf("%+v: want a new slot, got %+v", o, s)
		}
	}
	// Admin on revision 12: the first space's slot moves and is reused.
	p := build(PublishOptions{Admin: true, Revision: CatalogSlotsRevision})
	s := slotByKey(p, "cred:"+x.credBW)
	if s == nil || s.New || !s.MoveToCatalog || s.UUID != first.UUID || len(p.Request.Slots) != 0 {
		t.Fatalf("move and reuse: %+v, new slots %+v", s, p.Request.Slots)
	}
	// Unticked: a new slot instead, here at the catalog level, sent with
	// folder_uuid null.
	p = build(PublishOptions{Admin: true, Revision: CatalogSlotsRevision, SlotsAtCatalog: true, NoSlotMove: map[string]bool{"cred:" + x.credBW: true}})
	s = slotByKey(p, "cred:"+x.credBW)
	if s == nil || !s.New || !s.CatalogLevel || len(p.Request.Slots) != 1 {
		t.Fatalf("catalog-level new slot: %+v", s)
	}
	raw, _ := json.Marshal(p.Request.Slots[0])
	if !strings.Contains(string(raw), `"folder_uuid":null`) {
		t.Fatalf("slot body: %s", raw)
	}
	// Once the slot is at the catalog level (as the feed brings it), every
	// publish has it in scope, admin or not.
	if _, err := e.db.Conn().Exec(`UPDATE catalog_slots SET folder_uuid = '' WHERE uuid = ?`, first.UUID); err != nil {
		t.Fatal(err)
	}
	s = slotByKey(build(PublishOptions{Revision: CatalogSlotsRevision}), "cred:"+x.credBW)
	if s == nil || s.New || s.MoveToCatalog || s.UUID != first.UUID || s.ReusedFrom != "the whole catalog" {
		t.Fatalf("catalog-level slot in scope: %+v", s)
	}
}
