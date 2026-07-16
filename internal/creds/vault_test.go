package creds

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

// newVaultAt returns an initialized, unlocked vault backed by a tempdir file.
func newVaultAt(t *testing.T, passphrase string) *Vault {
	t.Helper()
	v := NewVault()
	v.SetPath(filepath.Join(t.TempDir(), "vault.enc"))
	if err := v.Init(passphrase, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return v
}

func TestVaultPutGetRoundTrip(t *testing.T) {
	v := newVaultAt(t, "correct horse")
	if err := v.Put("acct1", "s3cret"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := v.Get("acct1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got != "s3cret" {
		t.Fatalf("Get: got %q", got)
	}
	// Missing account.
	if _, ok, _ := v.Get("nope"); ok {
		t.Fatal("Get of a missing account should report ok=false")
	}
}

// TestVaultLockWipesAndRefuses covers the two v0.12.8 regressions the TODO
// calls out: Lock() must clear the memory mirror, and Put() must refuse when
// locked (so a "saved" secret can't live only in process memory).
func TestVaultLockWipesAndRefuses(t *testing.T) {
	v := newVaultAt(t, "pw")
	if err := v.Put("acct", "value"); err != nil {
		t.Fatal(err)
	}

	v.Lock(false)

	if v.Status().Kind != StatusLocked {
		t.Fatalf("after Lock, status = %v", v.Status().Kind)
	}
	// Memory mirror is wiped: a locked vault yields nothing from memory.
	if _, ok, _ := v.Get("acct"); ok {
		t.Fatal("Get returned a secret from a locked vault (memory not wiped)")
	}
	// Put refuses while locked.
	if err := v.Put("acct2", "x"); err == nil {
		t.Fatal("Put should fail on a locked vault")
	}
	// Seal/Open refuse while locked.
	if _, err := v.Seal([]byte("x")); err == nil {
		t.Fatal("Seal should fail on a locked vault")
	}
	if _, err := v.Open([]byte("x")); err == nil {
		t.Fatal("Open should fail on a locked vault")
	}
}

func TestVaultUnlockRestoresFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.enc")

	v := NewVault()
	v.SetPath(path)
	if err := v.Init("pw", false); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("acct", "persisted"); err != nil {
		t.Fatal(err)
	}
	v.Lock(false)

	// A fresh facade over the same file (simulates a restart).
	v2 := NewVault()
	v2.SetPath(path)
	if v2.Status().Kind != StatusLocked {
		t.Fatalf("reopened vault status = %v, want locked", v2.Status().Kind)
	}
	if err := v2.Unlock("pw", false); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	got, ok, _ := v2.Get("acct")
	if !ok || got != "persisted" {
		t.Fatalf("after Unlock: got %q ok=%v", got, ok)
	}
}

func TestVaultUnlockWrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.enc")
	v := NewVault()
	v.SetPath(path)
	if err := v.Init("right", false); err != nil {
		t.Fatal(err)
	}
	v.Lock(false)

	v2 := NewVault()
	v2.SetPath(path)
	err := v2.Unlock("wrong", false)
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Unlock with wrong passphrase: want ErrWrongPassphrase, got %v", err)
	}
}

func TestVaultChangePassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.enc")
	v := NewVault()
	v.SetPath(path)
	if err := v.Init("old", false); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("acct", "keepme"); err != nil {
		t.Fatal(err)
	}
	// Wrong old passphrase is rejected.
	if err := v.ChangePassphrase("nope", "new"); err == nil {
		t.Fatal("ChangePassphrase should reject a wrong old passphrase")
	}
	if err := v.ChangePassphrase("old", "new"); err != nil {
		t.Fatalf("ChangePassphrase: %v", err)
	}
	v.Lock(false)

	// Old passphrase no longer opens; new one does and the secret survives.
	v2 := NewVault()
	v2.SetPath(path)
	if err := v2.Unlock("old", false); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("old passphrase should fail after rotation, got %v", err)
	}
	if err := v2.Unlock("new", false); err != nil {
		t.Fatalf("new passphrase should unlock: %v", err)
	}
	if got, ok, _ := v2.Get("acct"); !ok || got != "keepme" {
		t.Fatalf("secret lost across passphrase change: got %q ok=%v", got, ok)
	}
}

// TestVaultSealOpen covers the Seal/Open blob envelope added for the Bitwarden
// sync cache: round-trip, cross-vault isolation, and tamper detection.
func TestVaultSealOpen(t *testing.T) {
	v := newVaultAt(t, "pw")

	plaintext := []byte("a larger blob than a single account secret\x00\x01\x02")
	sealed, err := v.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("sealed blob still contains the plaintext")
	}
	got, err := v.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("Open did not round-trip the plaintext")
	}

	// A different vault (different key) can't open it.
	other := newVaultAt(t, "different")
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("a different vault should not open another vault's sealed blob")
	}

	// Tampering with the ciphertext is detected (AEAD).
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := v.Open(tampered); err == nil {
		t.Fatal("Open should reject a tampered blob")
	}
}

func TestVaultDelete(t *testing.T) {
	v := newVaultAt(t, "pw")
	if err := v.Put("acct", "v"); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("acct"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := v.Get("acct"); ok {
		t.Fatal("account still present after Delete")
	}
}
