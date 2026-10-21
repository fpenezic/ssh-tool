package main

import (
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// "Remember" pins the key the challenge asked about, whatever the reply
// carries, and a second reply does not block.
func TestHostKeyReplyPinsAskedKey(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db,
		pendingHostKeys:   map[string]chan bool{"c1": make(chan bool, 1)},
		pendingHostKeyIDs: map[string]knownHostEntry{"c1": {host: "h.example.com", port: 22, keyType: "ssh-ed25519", keyB64: "ASKED", fp: "SHA256:asked"}},
	}
	if err := a.SshRespondHostKey("c1", true, true, "evil.example.com", 2222, "ssh-rsa", "OTHER", "SHA256:other"); err != nil {
		t.Fatal(err)
	}
	if err := a.SshRespondHostKey("c1", true, true, "h.example.com", 22, "ssh-ed25519", "ASKED", "x"); err == nil {
		t.Fatal("second reply accepted")
	}
	got, err := db.GetKnownHost("h.example.com", 22)
	if err != nil || got == nil || got.KeyB64 != "ASKED" {
		t.Fatalf("pinned %+v (%v)", got, err)
	}
	if other, _ := db.GetKnownHost("evil.example.com", 2222); other != nil {
		t.Fatal("the reply's own host was pinned")
	}
}
