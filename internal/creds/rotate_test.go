package creds

import (
	"path/filepath"
	"testing"
)

func TestChangePassphrase_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.enc")
	uv, err := InitVault(path, "old-pass")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := uv.Put("cred:a", "secret-a"); err != nil {
		t.Fatalf("put a: %v", err)
	}
	if err := uv.Put("cred:b", "secret-b"); err != nil {
		t.Fatalf("put b: %v", err)
	}

	if err := uv.ChangePassphrase("new-pass"); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// In-memory handle still works.
	if got, _, _ := uv.Get("cred:a"); got != "secret-a" {
		t.Fatalf("a after rotate: %q", got)
	}

	// Old passphrase rejected.
	if _, err := UnlockVault(path, "old-pass"); err != ErrWrongPassphrase {
		t.Fatalf("old should reject: got %v", err)
	}

	// New passphrase opens and reads.
	uv2, err := UnlockVault(path, "new-pass")
	if err != nil {
		t.Fatalf("unlock new: %v", err)
	}
	if got, _, _ := uv2.Get("cred:a"); got != "secret-a" {
		t.Fatalf("a after reopen: %q", got)
	}
	if got, _, _ := uv2.Get("cred:b"); got != "secret-b" {
		t.Fatalf("b after reopen: %q", got)
	}
}

func TestChangePassphrase_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.enc")
	uv, err := InitVault(path, "old")
	if err != nil {
		t.Fatal(err)
	}
	if err := uv.ChangePassphrase(""); err == nil {
		t.Fatal("expected error on empty new passphrase")
	}
	// Vault still usable with old passphrase.
	if _, err := UnlockVault(path, "old"); err != nil {
		t.Fatalf("old still works: %v", err)
	}
}
