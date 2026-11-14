package creds

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogTokenAccountIsMachineScoped(t *testing.T) {
	a := CatalogTokenAccount("cat-1")
	if !strings.HasPrefix(a, CatalogTokenAccountPrefix("cat-1")) {
		t.Fatalf("account %q lacks the catalog prefix", a)
	}
	if a != CatalogTokenAccount("cat-1") {
		t.Fatal("account not stable on one machine")
	}
	if id := StableMachineID(); id != "" && strings.Contains(a, id) {
		t.Fatalf("raw machine id in the account name: %q", a)
	}
	if !isVaultMachineLocalAccount(a) {
		t.Fatal("catalog token not machine-local")
	}
	if isVaultMachineLocalAccount("cred:catalog-token:x") {
		t.Fatal("prefix matched inside another account name")
	}
}

// A pull must leave this machine's catalog token alone and must not adopt
// the snapshot's copy of it, which can be older (already rotated) or
// another machine's.
func TestMergeSnapshotVaultSkipsCatalogTokens(t *testing.T) {
	const pass = "same passphrase"
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src-vault.enc")
	src := NewVault()
	src.SetPath(srcPath)
	if err := src.Init(pass, false); err != nil {
		t.Fatal(err)
	}
	mine := CatalogTokenAccount("cat-1")
	other := CatalogTokenAccountPrefix("cat-1") + "0000000000000000"
	for acct, val := range map[string]string{
		mine:              "stale-refresh-token",
		other:             "other-machine-token",
		"cred:abc":        "shared-secret",
		"sync:passphrase": "src-sync",
	} {
		if err := src.Put(acct, val); err != nil {
			t.Fatal(err)
		}
	}
	src.Lock(false)

	live := newVaultAt(t, pass)
	for acct, val := range map[string]string{
		mine:       "current-refresh-token",
		"cred:old": "gone-after-pull",
	} {
		if err := live.Put(acct, val); err != nil {
			t.Fatal(err)
		}
	}

	if err := live.MergeSnapshotVault(srcPath, pass); err != nil {
		t.Fatalf("merge: %v", err)
	}
	get := func(acct string) (string, bool) {
		v, ok, err := live.Get(acct)
		if err != nil {
			t.Fatal(err)
		}
		return v, ok
	}
	if v, _ := get(mine); v != "current-refresh-token" {
		t.Errorf("own catalog token overwritten by the snapshot: %q", v)
	}
	if _, ok := get(other); ok {
		t.Error("another machine's catalog token adopted")
	}
	if _, ok := get("sync:passphrase"); ok {
		t.Error("snapshot's sync passphrase adopted")
	}
	if v, _ := get("cred:abc"); v != "shared-secret" {
		t.Errorf("ordinary secret not merged: %q", v)
	}
	if _, ok := get("cred:old"); ok {
		t.Error("account absent from the snapshot survived")
	}
}
