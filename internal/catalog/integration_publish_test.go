package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// Publish against the dev server (see integration_test.go): bob publishes
// a local folder into a space alice's group reads; alice syncs it, finds
// the slots unbound, binds her own credential and resolves.
func TestIntegrationPublish(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	bob, _ := signedIn(t, base, "bob")
	alice, _ := signedIn(t, base, "alice")
	ctx := context.Background()

	space := newUUID(t)
	api(t, bob, "POST", "/api/v1/folders", "", map[string]any{"uuid": space, "parent_uuid": nil, "name": "Publish space " + space[:8], "settings": map[string]any{}})
	api(t, bob, "PUT", "/api/v1/folders/"+space+"/acl", "0", map[string]any{"entries": []any{map[string]any{"group": "ops", "role": "read"}}})

	// bob's machine: the catalog attached and synced, a local folder.
	bdb, err := store.Open(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	bcat, _ := bdb.CreateCatalog(store.NewCatalog{Name: "Dev", BaseURL: base})
	benv := &syncEnv{t: t, db: bdb, cat: bcat, c: bob}
	benv.sync()

	cred, _ := bdb.CreateCredential(store.NewCredential{Name: "deploy-key", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	bw, _ := bdb.CreateCredential(store.NewCredential{Name: "bw-db", Kind: store.CredPassword, StorageMode: store.StorageExternal,
		Config: map[string]any{"bitwarden_ref": map[string]any{"server_id": "srv", "cipher_id": "cipher-it", "field": "password"}}})
	bastion, _ := bdb.CreateConnection(store.NewConnection{Name: "bob-bastion", Hostname: "bastion.example.com"})
	mine, _ := bdb.CreateFolder(store.NewFolder{Name: "Customer B", Settings: store.InheritableSettings{Username: ptr("deploy"), AuthRef: &cred.ID}})
	web, _ := bdb.CreateConnection(store.NewConnection{Name: "web-1", Hostname: "web-1.example.com", FolderID: &mine.ID,
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}}}})
	dbc, _ := bdb.CreateConnection(store.NewConnection{Name: "db-1", Hostname: "db-1.example.com", FolderID: &mine.ID,
		Overrides: store.InheritableSettings{DbCredentialRef: &bw.ID}})
	rh, rp := "127.0.0.1", uint16(5432)
	if _, err := bdb.CreatePortForward(store.NewPortForward{ConnectionID: dbc.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp, AutoStart: true, Description: "pg"}); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, id := range []string{web.ID, dbc.ID} {
		r, _ := resolver.ResolveConnection(bdb, id)
		b, _ := json.Marshal(r)
		before[id] = string(b)
	}

	// A refused publish: a hostname over the limit, mapped to its record.
	bad, _ := bdb.CreateConnection(store.NewConnection{Name: "too-long", Hostname: strings.Repeat("h", 300), FolderID: &mine.ID})
	plan, err := BuildPublish(bdb, PublishOptions{CatalogID: bcat.ID, RootID: mine.ID, TargetUUID: space, AccountName: "bob"})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	_, err = bob.Publish(ctx, &plan.Request)
	if err == nil || !PublishNotCreated(err) {
		t.Fatalf("over-long hostname: %v", err)
	}
	reps := PublishRecordErrors(err)
	badUUID := ""
	for _, r := range plan.Records {
		if r.LocalID == bad.ID {
			badUUID = r.UUID
		}
	}
	if len(reps) == 0 || reps[0].UUID != badUUID {
		t.Fatalf("per-record errors: %+v (want %s)", reps, badUUID)
	}
	t.Logf("refused as expected: %+v", reps)
	if err := bdb.DeleteConnection(bad.ID); err != nil {
		t.Fatal(err)
	}

	// The real publish.
	plan, err = BuildPublish(bdb, PublishOptions{CatalogID: bcat.ID, RootID: mine.ID, TargetUUID: space, AccountName: "bob"})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	all, _ := bdb.ListConnections(nil)
	connsBefore := len(all)
	resp, err := bob.Publish(ctx, &plan.Request)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Logf("published %d records, warnings %+v", len(resp.Records), resp.Warnings)
	if err := CompletePublish(bdb, plan, resp.Records, "bob", nil); err != nil {
		t.Fatal(err)
	}
	res := benv.sync()
	t.Logf("bob's sync after publish: %+v", res)
	for id, want := range before {
		r, _ := resolver.ResolveConnection(bdb, id)
		b, _ := json.Marshal(r)
		if string(b) != want {
			t.Fatalf("bob's %s resolves differently:\n%s\n%s", id, want, b)
		}
	}
	if n, _ := bdb.ListConnections(nil); len(n) != connsBefore {
		t.Fatalf("bob holds %d connections, want %d (no copies)", len(n), connsBefore)
	}

	// alice: syncs, slots unbound, binds her own, resolves.
	adb, err := store.Open(filepath.Join(t.TempDir(), "alice.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer adb.Close()
	acat, _ := adb.CreateCatalog(store.NewCatalog{Name: "Dev", BaseURL: base})
	aenv := &syncEnv{t: t, db: adb, cat: acat, c: alice}
	aenv.sync()
	var webUUID string
	for _, r := range plan.Records {
		if r.LocalID == web.ID {
			webUUID = r.UUID
		}
	}
	aw := aenv.conn(webUUID)
	if aw == nil {
		t.Fatal("alice does not see web-1")
	}
	if _, err := resolver.ResolveConnection(adb, aw.ID); err == nil {
		t.Fatal("alice resolved web-1 without binding its slots")
	}
	var slots []store.CatalogSlot
	every, _ := adb.ListCatalogSlots(acat.ID)
	for _, s := range every {
		if s.FolderUUID == plan.RootUUID {
			slots = append(slots, s)
		}
	}
	if len(slots) != 3 {
		t.Fatalf("alice sees %d slots, want 3: %+v", len(slots), slots)
	}
	mycred, _ := adb.CreateCredential(store.NewCredential{Name: "alice-key", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	mybastion, _ := adb.CreateConnection(store.NewConnection{Name: "alice-bastion", Hostname: "jump.example.com"})
	for _, s := range slots {
		switch {
		case s.Type == "cred" && s.Kind == "slot":
			_ = adb.SetCatalogBinding(acat.ID, s.UUID, "cred", mycred.ID)
		case s.Type == "jump":
			_ = adb.SetCatalogBinding(acat.ID, s.UUID, "jump", mybastion.ID)
		case s.Kind == "external":
			var ref map[string]string
			_ = json.Unmarshal([]byte(s.ExternalRefJSON), &ref)
			if ref["cipher_id"] != "cipher-it" {
				t.Fatalf("external slot: %+v", s)
			}
		}
	}
	if err := Materialise(adb, acat.ID, nil); err != nil {
		t.Fatal(err)
	}
	r, err := resolver.ResolveConnection(adb, aw.ID)
	if err != nil {
		t.Fatalf("alice resolve: %v", err)
	}
	if r.AuthRef == nil || *r.AuthRef != mycred.ID || r.JumpHost == nil || r.JumpHost.Hostname != "jump.example.com" || *r.Username != "deploy" {
		t.Fatalf("alice's web-1: %+v (jump %+v)", r, r.JumpHost)
	}
	// The outside bastion included as a copy, in a second folder.
	more, _ := bdb.CreateFolder(store.NewFolder{Name: "Customer C"})
	app1, _ := bdb.CreateConnection(store.NewConnection{Name: "app-1", Hostname: "app-1.example.com", FolderID: &more.ID,
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}}}})
	plan3, err := BuildPublish(bdb, PublishOptions{CatalogID: bcat.ID, RootID: more.ID, TargetUUID: space,
		JumpInclude: map[string]bool{bastion.ID: true}, AccountName: "bob"})
	if err != nil || len(plan3.Refusals) > 0 {
		t.Fatalf("plan3: %v %+v", err, plan3.Refusals)
	}
	resp3, err := bob.Publish(ctx, &plan3.Request)
	if err != nil {
		t.Fatalf("publish with a bastion copy: %v", err)
	}
	if err := CompletePublish(bdb, plan3, resp3.Records, "bob", nil); err != nil {
		t.Fatal(err)
	}
	benv.sync()
	br, err := resolver.ResolveConnection(bdb, app1.ID)
	if err != nil || br.JumpHost == nil || br.JumpHost.Hostname != "bastion.example.com" {
		t.Fatalf("bob's app-1 after the copy arrived: %+v %v", br, err)
	}
	if b, _ := bdb.GetConnection(bastion.ID); b.CatalogID != nil {
		t.Fatal("bob's own bastion was converted")
	}
	aenv.sync()
	var app1UUID string
	for _, r := range plan3.Records {
		if r.LocalID == app1.ID {
			app1UUID = r.UUID
		}
	}
	ac := aenv.conn(app1UUID)
	if ac == nil {
		t.Fatal("alice does not see app-1")
	}
	ar, err := resolver.ResolveConnection(adb, ac.ID)
	if err != nil || ar.JumpHost == nil || ar.JumpHost.Hostname != "bastion.example.com" {
		t.Fatalf("alice's app-1 does not jump through the published copy: %+v %v", ar, err)
	}

	// alice cannot create a space.
	plan2, _ := BuildPublish(adb, PublishOptions{CatalogID: acat.ID, RootID: func() string {
		f, _ := adb.CreateFolder(store.NewFolder{Name: "Alice's"})
		_, _ = adb.CreateConnection(store.NewConnection{Name: "x", Hostname: "x.example.com", FolderID: &f.ID})
		return f.ID
	}()})
	if _, err := alice.Publish(ctx, &plan2.Request); err == nil || !PublishNotCreated(err) {
		t.Fatalf("a non-admin created a space: %v", err)
	}
}
