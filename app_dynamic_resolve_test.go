package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// A dynamic-inventory entry has no `connections` row, so every quick-copy
// button in the pane toolbar (host / username / password / ssh command) and
// "open in system terminal" resolved through resolver.ResolveConnection and
// got "not found" - they silently did nothing on every dynamic host while
// saved connections worked. resolveAnyConnection resolves both id shapes.
func newResolveTestApp(t *testing.T) *App {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{db: db}
}

func TestResolveAnyConnectionDynamicEntry(t *testing.T) {
	a := newResolveTestApp(t)

	// The folder carries the inherited settings; the entry only has a
	// hostname. This is exactly the shape the connect path resolves.
	user := "deploy"
	port := uint16(2222)
	folder, err := a.db.CreateFolder(store.NewFolder{
		Name:     "proxmox",
		Settings: store.InheritableSettings{Username: &user, Port: &port},
	})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := a.db.CreateDynamicFolder(store.DynamicFolder{
		FolderID: folder.ID,
		Provider: "proxmox",
		Config:   map[string]any{},
	}); err != nil {
		t.Fatalf("create dynamic folder: %v", err)
	}
	entries := []store.DynamicEntry{{
		ID:         "entry-1",
		FolderID:   folder.ID,
		ExternalID: "vm/100",
		Name:       "web-01",
		Hostname:   "10.0.0.5",
		Kind:       "guest_vm",
		Raw:        json.RawMessage(`{}`),
	}}
	if err := a.db.ReplaceDynamicEntries(folder.ID, entries); err != nil {
		t.Fatalf("replace entries: %v", err)
	}

	got, err := a.resolveAnyConnection("dyn:entry-1")
	if err != nil {
		t.Fatalf("resolveAnyConnection: %v", err)
	}
	if got.Hostname != "10.0.0.5" {
		t.Errorf("hostname = %q, want 10.0.0.5", got.Hostname)
	}
	// The whole point: the folder cascade still applies to a synthetic
	// connection, so the copied ssh command matches an actual connect.
	if got.Username == nil || *got.Username != "deploy" {
		t.Errorf("username = %v, want deploy (inherited from folder)", got.Username)
	}
	if got.Port != 2222 {
		t.Errorf("port = %d, want 2222 (inherited from folder)", got.Port)
	}
}

// An inventory refresh can drop a host while its session is still open. The
// copy buttons should say so, not report a bare "not found".
func TestResolveAnyConnectionDynamicEntryGone(t *testing.T) {
	a := newResolveTestApp(t)
	_, err := a.resolveAnyConnection("dyn:does-not-exist")
	if err == nil {
		t.Fatal("expected an error for a vanished dynamic entry")
	}
}

// A saved connection must keep resolving exactly as before.
func TestResolveAnyConnectionSavedConnection(t *testing.T) {
	a := newResolveTestApp(t)
	user := "root"
	folder, err := a.db.CreateFolder(store.NewFolder{
		Name:     "static",
		Settings: store.InheritableSettings{Username: &user},
	})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	conn, err := a.db.CreateConnection(store.NewConnection{
		FolderID: &folder.ID,
		Name:     "box",
		Hostname: "192.0.2.10",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	got, err := a.resolveAnyConnection(conn.ID)
	if err != nil {
		t.Fatalf("resolveAnyConnection: %v", err)
	}
	if got.Hostname != "192.0.2.10" {
		t.Errorf("hostname = %q, want 192.0.2.10", got.Hostname)
	}
	if got.Username == nil || *got.Username != "root" {
		t.Errorf("username = %v, want root", got.Username)
	}
}
