package main

import (
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// The ssh command for the system terminal logs in with the same users as
// the in-app connect: a hop without its own user takes the target's, and
// either falls back to its credential's default username.
func TestSshSystemArgvJumpUserFromCredential(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	str := func(s string) *string { return &s }
	cred, err := db.CreateCredential(store.NewCredential{
		Name: "bastion", Kind: store.CredAgent, StorageMode: store.StorageManaged,
		DefaultUsername: str("jumper"),
	})
	if err != nil {
		t.Fatal(err)
	}
	bastion, err := db.CreateConnection(store.NewConnection{
		Name: "bastion", Hostname: "bastion.example.com",
		Overrides: store.InheritableSettings{AuthRef: &cred.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.CreateConnection(store.NewConnection{
		Name: "app", Hostname: "app.example.com",
		Overrides: store.InheritableSettings{
			AuthRef:  &cred.ID,
			JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}
	argv, err := a.sshSystemArgv(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(argv, " "), "ssh -J jumper@bastion.example.com jumper@app.example.com"; got != want {
		t.Fatalf("argv %q, want %q", got, want)
	}

	// An option-looking default username is refused, not passed to ssh.
	bad, err := db.CreateCredential(store.NewCredential{
		Name: "bad", Kind: store.CredAgent, StorageMode: store.StorageManaged,
		DefaultUsername: str("-oProxyCommand=x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateConnection(store.NewConnection{
		Name: "other", Hostname: "other.example.com",
		Overrides: store.InheritableSettings{AuthRef: &bad.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.sshSystemArgv(other.ID); err == nil {
		t.Fatal("option-looking credential username was accepted")
	}
}
