package exporter

import (
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// An archive is untrusted input: a credential id it names must not be
// enough to point an imported host at a secret that already exists in
// the store. Linking needs ImportOptions.LinkExistingCredentials.
func linkExistingFixture(t *testing.T) (*store.DB, *Archive, string, string) {
	t.Helper()
	db := openTestDB(t)
	pw, err := db.CreateCredential(store.NewCredential{Name: "local-pw", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := db.CreateCredential(store.NewCredential{Name: "local-token", Kind: store.CredAPIToken, StorageMode: store.StorageManaged})
	if err != nil {
		t.Fatal(err)
	}
	text := `{"schema_version":1,
"folders":[{"id":"fx","name":"Lab","settings":{},"dynamic":{"provider":"proxmox","refresh_seconds":60,"config":{"base_url":"https://dyn.example.com:8006","api_token_credential_id":"` + tok.ID + `"}}}],
"credentials":[{"id":"` + tok.ID + `","name":"harmless","kind":"password","storage_mode":"managed"}],
"connections":[{"id":"cx","name":"db","hostname":"host.example.com","overrides":{"auth_ref":"` + pw.ID + `"}}]}`
	arc, err := Decode(text)
	if err != nil {
		t.Fatal(err)
	}
	return db, arc, pw.ID, tok.ID
}

func importedState(t *testing.T, db *store.DB) (authRef string, dynToken string) {
	t.Helper()
	conns, _ := db.ListConnections(nil)
	for _, c := range conns {
		if c.Name == "db" && c.Overrides.AuthRef != nil {
			authRef = *c.Overrides.AuthRef
		}
	}
	dyns, _ := db.ListDynamicFolders()
	for _, d := range dyns {
		if v, _ := d.Config["api_token_credential_id"].(string); v != "" {
			dynToken = v
		}
	}
	return
}

func TestImportDoesNotLinkExistingCredentialsByDefault(t *testing.T) {
	db, arc, _, _ := linkExistingFixture(t)

	dry, err := Apply(db, arc, ImportOptions{DryRun: true}, rejectSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.CredsNotLinked) != 2 {
		t.Fatalf("dry run should report both local credentials as not linked, got %v", dry.CredsNotLinked)
	}
	if !strings.Contains(strings.Join(dry.Warnings, "\n"), "Link to existing credentials") {
		t.Fatalf("dry run should warn about the link option, warnings=%v", dry.Warnings)
	}

	if _, err := Apply(db, arc, ImportOptions{}, rejectSecrets); err != nil {
		t.Fatal(err)
	}
	authRef, dynToken := importedState(t, db)
	if authRef != "" {
		t.Fatalf("imported connection must not use the existing local credential, auth_ref=%q", authRef)
	}
	if dynToken != "" {
		t.Fatalf("imported dynamic folder must not use the existing local token, got %q", dynToken)
	}
}

func TestImportLinksExistingCredentialsWhenAsked(t *testing.T) {
	db, arc, pwID, tokID := linkExistingFixture(t)
	sum, err := Apply(db, arc, ImportOptions{LinkExistingCredentials: true}, rejectSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.CredsLinked) != 2 || len(sum.CredsNotLinked) != 0 {
		t.Fatalf("linked=%v notLinked=%v", sum.CredsLinked, sum.CredsNotLinked)
	}
	authRef, dynToken := importedState(t, db)
	if authRef != pwID || dynToken != tokID {
		t.Fatalf("opt-in should link: auth_ref=%q (want %q), token=%q (want %q)", authRef, pwID, dynToken, tokID)
	}
}

func TestOverwriteReportsHostChange(t *testing.T) {
	db := openTestDB(t)
	c, err := db.CreateConnection(store.NewConnection{Name: "web", Hostname: "old.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	arc, err := Decode(`{"schema_version":1,"connections":[{"id":"` + c.ID + `","name":"web","hostname":"new.example.com"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := Apply(db, arc, ImportOptions{Conflict: ConflictOverwrite, DryRun: true}, rejectSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.HostChanges) != 1 || !strings.Contains(sum.HostChanges[0], "old.example.com -> new.example.com") {
		t.Fatalf("host change not reported: %v", sum.HostChanges)
	}
}

// Archive KDF parameters are bounded before argon2 runs: zero values used
// to panic and a huge memory value was a fatal out-of-memory.
func TestDecryptRejectsOutOfRangeKDF(t *testing.T) {
	for _, b := range []EncryptedSecretBlock{
		{KDF: "argon2id", Salt: "AAAAAAAAAAAAAAAAAAAAAA==", MemoryKiB: 64, Iterations: 1, Parallel: 0},
		{KDF: "argon2id", Salt: "AAAAAAAAAAAAAAAAAAAAAA==", MemoryKiB: 64, Iterations: 0, Parallel: 1},
		{KDF: "argon2id", Salt: "AAAAAAAAAAAAAAAAAAAAAA==", MemoryKiB: 8 * 1024 * 1024, Iterations: 1, Parallel: 1},
		{KDF: "argon2id", Salt: "AAAAAAAAAAAAAAAAAAAAAA==", MemoryKiB: 64, Iterations: 1 << 20, Parallel: 1},
	} {
		b.CipherBy = map[string]string{"c1": "AAAA"}
		blk := b
		if _, err := DecryptSecret("pw", &blk, "c1"); err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Errorf("params t=%d m=%d p=%d: want out-of-range error, got %v", b.Iterations, b.MemoryKiB, b.Parallel, err)
		}
	}
}
