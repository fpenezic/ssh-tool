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

// OTP, Kafka and database credential references follow the same rule as
// auth_ref: an existing local id is not linked without the opt-in.
func TestImportOtherCredentialRefsNotLinked(t *testing.T) {
	db := openTestDB(t)
	pw, err := db.CreateCredential(store.NewCredential{Name: "local-pw", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	if err != nil {
		t.Fatal(err)
	}
	id := pw.ID
	arc, err := Decode(`{"schema_version":1,"connections":[{"id":"cx","name":"k","hostname":"h.example.com","overrides":{
"otp_ref":"` + id + `","kafka_credential_ref":"` + id + `","db_credential_ref":"` + id + `",
"db_targets":[{"id":"t1","name":"prod","credential_ref":"` + id + `"}]}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := Apply(db, arc, ImportOptions{}, rejectSecrets)
	if err != nil {
		t.Fatal(err)
	}
	conns, _ := db.ListConnections(nil)
	var o store.InheritableSettings
	for _, c := range conns {
		if c.Name == "k" {
			o = c.Overrides
		}
	}
	if o.OTPRef != nil || o.KafkaCredentialRef != nil || o.DbCredentialRef != nil {
		t.Fatalf("refs kept: otp=%v kafka=%v db=%v", o.OTPRef, o.KafkaCredentialRef, o.DbCredentialRef)
	}
	if len(o.DbTargets) != 1 || o.DbTargets[0].CredentialRef != nil {
		t.Fatalf("db target credential kept: %+v", o.DbTargets)
	}
	if len(sum.CredsNotLinked) != 1 {
		t.Fatalf("want the local credential reported once as not linked, got %v", sum.CredsNotLinked)
	}
	for _, w := range sum.Warnings {
		if strings.Contains(w, "does not carry were dropped") {
			t.Fatalf("a refused local link must not be reported as missing from the archive: %q", w)
		}
	}
}

// A skipped local folder that passes a credential down is not reused as the
// parent of new archive rows unless linking is on.
func TestImportDoesNotNestUnderCredentialFolder(t *testing.T) {
	db := openTestDB(t)
	pw, err := db.CreateCredential(store.NewCredential{Name: "local-pw", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	if err != nil {
		t.Fatal(err)
	}
	ref := pw.ID
	prod, err := db.CreateFolder(store.NewFolder{Name: "Prod", Settings: store.InheritableSettings{AuthRef: &ref}})
	if err != nil {
		t.Fatal(err)
	}
	text := `{"schema_version":1,"folders":[{"id":"` + prod.ID + `","name":"Prod","settings":{}}],
"connections":[{"id":"cx","name":"new-host","hostname":"h.example.com","folder_id":"` + prod.ID + `","overrides":{}}]}`
	arc, err := Decode(text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(db, arc, ImportOptions{}, rejectSecrets); err != nil {
		t.Fatal(err)
	}
	conns, _ := db.ListConnections(nil)
	for _, c := range conns {
		if c.Name == "new-host" && c.FolderID != nil && *c.FolderID == prod.ID {
			t.Fatal("new connection landed in the local folder that hands down a credential")
		}
	}

	// Opt-in: merged into the existing folder.
	db2 := openTestDB(t)
	pw2, _ := db2.CreateCredential(store.NewCredential{Name: "local-pw", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	ref2 := pw2.ID
	prod2, _ := db2.CreateFolder(store.NewFolder{Name: "Prod", Settings: store.InheritableSettings{AuthRef: &ref2}})
	arc2, _ := Decode(strings.ReplaceAll(text, prod.ID, prod2.ID))
	if _, err := Apply(db2, arc2, ImportOptions{LinkExistingCredentials: true}, rejectSecrets); err != nil {
		t.Fatal(err)
	}
	conns2, _ := db2.ListConnections(nil)
	found := false
	for _, c := range conns2 {
		if c.Name == "new-host" && c.FolderID != nil && *c.FolderID == prod2.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("with linking on, the connection should merge into the existing folder")
	}
}

// Rows a skip-mode import leaves untouched are not reported as links.
func TestLinkPreviewIgnoresSkippedRows(t *testing.T) {
	db := openTestDB(t)
	pw, _ := db.CreateCredential(store.NewCredential{Name: "local-pw", Kind: store.CredPassword, StorageMode: store.StorageManaged})
	ref := pw.ID
	c, err := db.CreateConnection(store.NewConnection{Name: "web", Hostname: "w.example.com", Overrides: store.InheritableSettings{AuthRef: &ref}})
	if err != nil {
		t.Fatal(err)
	}
	arc, _ := Decode(`{"schema_version":1,"connections":[{"id":"` + c.ID + `","name":"web","hostname":"w.example.com","overrides":{"auth_ref":"` + ref + `"}}]}`)
	sum, err := Apply(db, arc, ImportOptions{DryRun: true}, rejectSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.CredsNotLinked) != 0 || len(sum.CredsLinked) != 0 {
		t.Fatalf("skipped row reported: linked=%v notLinked=%v", sum.CredsLinked, sum.CredsNotLinked)
	}
}

func TestDecryptRejectsShortCipherBeforeKDF(t *testing.T) {
	b := EncryptedSecretBlock{KDF: "argon2id", Salt: "AAAAAAAAAAAAAAAAAAAAAA==", MemoryKiB: 64, Iterations: 1, Parallel: 1, CipherBy: map[string]string{"c1": "AAAA"}}
	if _, err := DecryptSecret("pw", &b, "c1"); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("want cipher too short, got %v", err)
	}
}
