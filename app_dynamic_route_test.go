package main

import (
	"encoding/json"
	"testing"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// doRaw is a droplet as the DigitalOcean provider stores it in Raw.
func doRaw(pub, priv string) json.RawMessage {
	var nets []map[string]string
	if pub != "" {
		nets = append(nets, map[string]string{"ip_address": pub, "type": "public"})
	}
	if priv != "" {
		nets = append(nets, map[string]string{"ip_address": priv, "type": "private"})
	}
	b, _ := json.Marshal(map[string]any{"networks": map[string]any{"v4": nets}})
	return b
}

// A DigitalOcean folder with a bastion (public + private), a private-only
// monitoring box and a public web server. hostname_source "auto" gave each
// entry the address the provider picked.
func newBastionFolder(t *testing.T, a *App, cfg map[string]any) (string, map[string]*store.DynamicEntry) {
	t.Helper()
	user := "ops"
	folder, err := a.db.CreateFolder(store.NewFolder{Name: "do", Settings: store.InheritableSettings{Username: &user}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.CreateDynamicFolder(store.DynamicFolder{FolderID: folder.ID, Provider: "digitalocean", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	rows := []store.DynamicEntry{
		{ID: "e-bastion", FolderID: folder.ID, ExternalID: "droplet:1", Name: "bastion", Hostname: "203.0.113.10", Raw: doRaw("203.0.113.10", "10.10.0.2")},
		{ID: "e-mon", FolderID: folder.ID, ExternalID: "droplet:2", Name: "monitoring", Hostname: "10.10.0.3", Raw: doRaw("", "10.10.0.3")},
		{ID: "e-web", FolderID: folder.ID, ExternalID: "droplet:3", Name: "web", Hostname: "203.0.113.11", Raw: doRaw("203.0.113.11", "10.10.0.4")},
	}
	if err := a.db.ReplaceDynamicEntries(folder.ID, rows); err != nil {
		t.Fatal(err)
	}
	byName := map[string]*store.DynamicEntry{}
	for i := range rows {
		e, err := a.db.GetDynamicEntry(rows[i].ID)
		if err != nil || e == nil {
			t.Fatal(err)
		}
		byName[e.Name] = e
	}
	return folder.ID, byName
}

func resolveDyn(t *testing.T, a *App, e *store.DynamicEntry) store.ResolvedSettings {
	t.Helper()
	folders, err := a.db.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	return resolver.ResolveWith(a.dynamicConnectionFor(e, ""), folders)
}

func TestBastionRoutesPrivateOnlyHosts(t *testing.T) {
	a := newResolveTestApp(t)
	cred, err := a.db.CreateCredential(store.NewCredential{
		Name: "bastion login", Kind: store.CredPassword, StorageMode: store.StorageManaged,
		DefaultUsername: strPtr("jump"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, e := newBastionFolder(t, a, map[string]any{
		"bastion_external_id":   "droplet:1",
		"bastion_name":          "bastion",
		"bastion_credential_id": cred.ID,
	})

	mon := resolveDyn(t, a, e["monitoring"])
	if mon.Hostname != "10.10.0.3" || mon.JumpHost == nil {
		t.Fatalf("monitoring: host %q jump %+v", mon.Hostname, mon.JumpHost)
	}
	if mon.JumpHost.Hostname != "203.0.113.10" || mon.JumpHost.AuthRef == nil || *mon.JumpHost.AuthRef != cred.ID ||
		mon.JumpHost.Username == nil || *mon.JumpHost.Username != "jump" {
		t.Fatalf("monitoring hop: %+v", mon.JumpHost)
	}
	if mon.Username == nil || *mon.Username != "ops" || mon.AuthRef != nil {
		t.Fatal("private host must keep the folder's login, not the bastion's")
	}

	if web := resolveDyn(t, a, e["web"]); web.JumpHost != nil || web.Hostname != "203.0.113.11" {
		t.Fatalf("public host must dial directly: %+v", web)
	}

	b := resolveDyn(t, a, e["bastion"])
	if b.JumpHost != nil || b.AuthRef == nil || *b.AuthRef != cred.ID {
		t.Fatalf("bastion: direct with its own credential, got %+v", b)
	}
}

// Without a bastion credential the hop inherits the target's login - the
// "same login everywhere" case.
func TestBastionWithoutOwnCredential(t *testing.T) {
	a := newResolveTestApp(t)
	_, e := newBastionFolder(t, a, map[string]any{"bastion_external_id": "droplet:1"})
	mon := resolveDyn(t, a, e["monitoring"])
	if mon.JumpHost == nil || mon.JumpHost.AuthRef != nil || mon.JumpHost.Username != nil {
		t.Fatalf("hop should carry only the address: %+v", mon.JumpHost)
	}
}

// A bastion rebuilt under a new id is still found by name.
func TestBastionFoundByNameAfterRebuild(t *testing.T) {
	a := newResolveTestApp(t)
	_, e := newBastionFolder(t, a, map[string]any{"bastion_external_id": "droplet:999", "bastion_name": "bastion"})
	if mon := resolveDyn(t, a, e["monitoring"]); mon.JumpHost == nil || mon.JumpHost.Hostname != "203.0.113.10" {
		t.Fatalf("monitoring: %+v", mon.JumpHost)
	}
}

func TestNoBastionConfiguredLeavesEntriesAlone(t *testing.T) {
	a := newResolveTestApp(t)
	_, e := newBastionFolder(t, a, map[string]any{})
	if mon := resolveDyn(t, a, e["monitoring"]); mon.JumpHost != nil {
		t.Fatalf("no bastion configured, got jump %+v", mon.JumpHost)
	}
}

func strPtr(s string) *string { return &s }
