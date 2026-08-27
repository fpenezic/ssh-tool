package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
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

// The copy-password button sits on every SSH pane - we can't know the auth
// kind without resolving - so pressing it on a key-auth host is a normal
// thing to do. It used to answer "credential is not a password (kind=key)",
// which leaks an internal enum and never says what the host actually uses.
func TestNoPasswordReasonIsHumanReadable(t *testing.T) {
	cases := []struct {
		kind store.CredentialKind
		name string
		want string
	}{
		{store.CredKey, "prod key", `"prod key" authenticates with an SSH key, so there is no password to copy`},
		{store.CredAgent, "agent", `"agent" authenticates through your SSH agent, so there is no password to copy`},
		{store.CredOpkssh, "sso", `"sso" authenticates with an opkssh certificate, so there is no password to copy`},
		{store.CredAPIToken, "pve", `"pve" is an API token (used by inventory providers, not for SSH login), so there is no password to copy`},
		// An unnamed credential must still read as a sentence.
		{store.CredKey, "", "this connection authenticates with an SSH key, so there is no password to copy"},
	}
	for _, tc := range cases {
		got := noPasswordReason(tc.kind, tc.name)
		if got != tc.want {
			t.Errorf("noPasswordReason(%s, %q):\n got  %q\n want %q", tc.kind, tc.name, got, tc.want)
		}
	}
}

// Whatever we add later, the message must never expose the internal enum
// the way the old "credential is not a password (kind=key)" did. Matching
// on the kind string itself would be wrong - "key" and "agent" are ordinary
// English words the prose is entitled to use - so this pins the leak shape.
func TestNoPasswordReasonLeaksNoInternals(t *testing.T) {
	kinds := []store.CredentialKind{
		store.CredKey, store.CredAgent, store.CredOpkssh,
		store.CredVault, store.CredAPIToken, store.CredentialKind("something-new"),
	}
	for _, k := range kinds {
		msg := noPasswordReason(k, "cred")
		if strings.Contains(msg, "kind=") {
			t.Errorf("noPasswordReason(%s) leaks the internal kind: %q", k, msg)
		}
		// Every kind, including one added after this test was written,
		// has to produce a sentence rather than an empty hint.
		if !strings.HasSuffix(msg, "no password to copy") {
			t.Errorf("noPasswordReason(%s) is not a readable sentence: %q", k, msg)
		}
	}
}

// The dynamic detail pane only ever offered "Copy host", reading
// entry.hostname straight off the row. Everything the saved-connection pane
// offers (user, password, ssh command, launch) goes through these IPCs, so
// pin that they now work on a "dyn:" id - that is what lets the dynamic pane
// carry the same Quick actions row.
func TestCopyInfoAndSshCommandOnDynamicEntry(t *testing.T) {
	a := newResolveTestApp(t)

	user := "ops"
	port := uint16(2022)
	folder, err := a.db.CreateFolder(store.NewFolder{
		Name:     "hetzner",
		Settings: store.InheritableSettings{Username: &user, Port: &port},
	})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := a.db.CreateDynamicFolder(store.DynamicFolder{
		FolderID: folder.ID, Provider: "hetzner", Config: map[string]any{},
	}); err != nil {
		t.Fatalf("create dynamic folder: %v", err)
	}
	if err := a.db.ReplaceDynamicEntries(folder.ID, []store.DynamicEntry{{
		ID: "e-1", FolderID: folder.ID, ExternalID: "srv/1",
		Name: "auth-01", Hostname: "10.239.248.62", Kind: "server",
		Raw: json.RawMessage(`{}`),
	}}); err != nil {
		t.Fatalf("replace entries: %v", err)
	}

	info, err := a.ConnectionCopyInfo("dyn:e-1")
	if err != nil {
		t.Fatalf("ConnectionCopyInfo: %v", err)
	}
	if info.Hostname != "10.239.248.62" {
		t.Errorf("hostname = %q", info.Hostname)
	}
	if info.Username != "ops" {
		t.Errorf("username = %q, want ops (inherited)", info.Username)
	}
	if info.Port != 2022 {
		t.Errorf("port = %d, want 2022 (inherited)", info.Port)
	}
	// The ssh command has to carry the inherited user and port, or the
	// copied line would not reproduce what Connect actually does.
	if !strings.Contains(info.SSHCommand, "ops@10.239.248.62") {
		t.Errorf("ssh command missing user@host: %q", info.SSHCommand)
	}
	if !strings.Contains(info.SSHCommand, "-p 2022") {
		t.Errorf("ssh command missing inherited port: %q", info.SSHCommand)
	}
}
