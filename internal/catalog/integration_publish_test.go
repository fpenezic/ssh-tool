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
	// The outside bastion went as a copy inside the folder.
	if n, _ := bdb.ListConnections(nil); len(n) != connsBefore+1 {
		t.Fatalf("bob holds %d connections, want %d (the bastion copy)", len(n), connsBefore+1)
	}

	// alice: syncs; bob's own credential stayed his, the bastion is the
	// copy, only the Bitwarden item is a slot. She sets her credential
	// under My settings on the folder, as for any setting.
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
	var slots []store.CatalogSlot
	every, _ := adb.ListCatalogSlots(acat.ID)
	for _, s := range every {
		if s.FolderUUID == plan.RootUUID {
			slots = append(slots, s)
		}
	}
	if len(slots) != 1 || slots[0].Kind != "external" {
		t.Fatalf("alice sees slots %+v, want the Bitwarden one", slots)
	}
	r, err := resolver.ResolveConnection(adb, aw.ID)
	if err != nil {
		t.Fatalf("alice resolve: %v", err)
	}
	if r.AuthRef != nil && *r.AuthRef != "" {
		t.Fatalf("bob's credential reached alice: %v", *r.AuthRef)
	}
	if r.JumpHost == nil || r.JumpHost.Hostname != "bastion.example.com" || *r.Username != "deploy" {
		t.Fatalf("alice's web-1: %+v (jump %+v)", r, r.JumpHost)
	}
	mycred, _ := adb.CreateCredential(store.NewCredential{Name: "alice-key", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	if err := adb.SetCatalogOverride(acat.ID, plan.RootUUID, "folders", `{"auth_ref":"`+mycred.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if err := Materialise(adb, acat.ID, nil); err != nil {
		t.Fatal(err)
	}
	if r, err = resolver.ResolveConnection(adb, aw.ID); err != nil || r.AuthRef == nil || *r.AuthRef != mycred.ID {
		t.Fatalf("alice's own credential under My settings: %+v %v", r.AuthRef, err)
	}
	// The outside bastion included as a copy, in a second folder.
	more, _ := bdb.CreateFolder(store.NewFolder{Name: "Customer C"})
	app1, _ := bdb.CreateConnection(store.NewConnection{Name: "app-1", Hostname: "app-1.example.com", FolderID: &more.ID,
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}}}})
	plan3, err := BuildPublish(bdb, PublishOptions{CatalogID: bcat.ID, RootID: more.ID, TargetUUID: space,
		AccountName: "bob"})
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

// One connection published straight into an existing space (protocol
// revision 3): no folder record, the bastion it jumps through as a copy,
// its forward along, and bob's row converted in place.
func TestIntegrationPublishSingleConnection(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	bob, _ := signedIn(t, base, "bob")
	ctx := context.Background()
	if m, err := bob.Meta(ctx); err != nil || m.Protocol.Revision < 3 {
		t.Skipf("server below revision 3: %+v %v", m, err)
	}
	space := newUUID(t)
	api(t, bob, "POST", "/api/v1/folders", "", map[string]any{"uuid": space, "parent_uuid": nil, "name": "Solo space " + space[:8], "settings": map[string]any{}})

	bdb, err := store.Open(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	bcat, _ := bdb.CreateCatalog(store.NewCatalog{Name: "Dev", BaseURL: base})
	benv := &syncEnv{t: t, db: bdb, cat: bcat, c: bob}
	benv.sync()

	bastion, _ := bdb.CreateConnection(store.NewConnection{Name: "solo-bastion", Hostname: "bastion.example.com"})
	mine, _ := bdb.CreateFolder(store.NewFolder{Name: "Mine", Settings: store.InheritableSettings{Username: ptr("deploy")}})
	solo, _ := bdb.CreateConnection(store.NewConnection{Name: "solo", Hostname: "solo.example.com", FolderID: &mine.ID,
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}}}})
	rh, rp := "127.0.0.1", uint16(5432)
	if _, err := bdb.CreatePortForward(store.NewPortForward{ConnectionID: solo.ID, Kind: "local", RemoteHost: &rh, RemotePort: &rp}); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPublish(bdb, PublishOptions{CatalogID: bcat.ID, ConnID: solo.ID, TargetUUID: space, AccountName: "bob"})
	if err != nil || len(plan.Refusals) > 0 {
		t.Fatalf("plan: %v %+v", err, plan.Refusals)
	}
	resp, err := bob.Publish(ctx, &plan.Request)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(resp.Records) != 3 {
		t.Fatalf("published %d records, want connection, bastion copy and forward", len(resp.Records))
	}
	if err := CompletePublish(bdb, plan, resp.Records, "bob", nil); err != nil {
		t.Fatal(err)
	}
	benv.sync()
	c, _ := bdb.GetConnection(solo.ID)
	if c.CatalogID == nil || c.RemoteUUID == nil {
		t.Fatalf("not converted: %+v", c)
	}
	r, err := resolver.ResolveConnection(bdb, solo.ID)
	if err != nil || r.Username == nil || *r.Username != "deploy" {
		t.Fatalf("resolved after publish: %+v %v", r, err)
	}
}
