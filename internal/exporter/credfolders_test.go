package exporter

import (
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// Round-trip regression for the flat-credentials import bug: archives
// used to carry credential FolderIDs but no credential-folder section,
// so the importer dropped every credential at the tree root.

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func noSecrets(credID, vaultKey string) ([]byte, bool, error) { return nil, false, nil }
func rejectSecrets(credID string, plain []byte) (string, error) {
	return "", nil
}

func TestCredentialFoldersRoundTrip(t *testing.T) {
	src := openTestDB(t)

	// Tree: Infra/ -> Prod/ -> cred "deploy"; second cred at root.
	infra, err := src.CreateCredentialFolder("Infra", nil)
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	prod, err := src.CreateCredentialFolder("Prod", &infra.ID)
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	deploy, err := src.CreateCredential(store.NewCredential{
		Name: "deploy", Kind: store.CredPassword,
		StorageMode: store.StorageManaged, FolderID: &prod.ID,
	})
	if err != nil {
		t.Fatalf("create cred: %v", err)
	}
	rootCred, err := src.CreateCredential(store.NewCredential{
		Name: "root-cred", Kind: store.CredPassword,
		StorageMode: store.StorageManaged,
	})
	if err != nil {
		t.Fatalf("create cred: %v", err)
	}
	// Connections referencing both creds so the exporter keeps them.
	for _, ref := range []string{deploy.ID, rootCred.ID} {
		r := ref
		if _, err := src.CreateConnection(store.NewConnection{
			Name: "conn-" + r[:8], Hostname: "h",
			Overrides: store.InheritableSettings{AuthRef: &r},
		}); err != nil {
			t.Fatalf("create conn: %v", err)
		}
	}

	arc, err := Build(src, nil, nil, Options{IncludeCredentials: true, Passphrase: "pw"}, noSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(arc.CredentialFolders) != 2 {
		t.Fatalf("want 2 credential folders in archive, got %d", len(arc.CredentialFolders))
	}

	dst := openTestDB(t)
	sum, err := Apply(dst, arc, ImportOptions{}, rejectSecrets)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(sum.CredFoldersCreated) != 2 {
		t.Fatalf("want 2 cred folders created, got %v (skipped %v)", sum.CredFoldersCreated, sum.CredFoldersSkipped)
	}

	folders, _ := dst.ListCredentialFolders()
	byName := map[string]store.CredentialFolder{}
	for _, f := range folders {
		byName[f.Name] = f
	}
	prodDst, ok := byName["Prod"]
	if !ok {
		t.Fatalf("Prod folder missing after import")
	}
	if prodDst.ParentID == nil || *prodDst.ParentID != byName["Infra"].ID {
		t.Fatalf("Prod should be child of Infra, got parent %v", prodDst.ParentID)
	}

	creds, _ := dst.ListCredentials()
	for _, c := range creds {
		switch c.Name {
		case "deploy":
			if c.FolderID == nil || *c.FolderID != prodDst.ID {
				t.Fatalf("deploy should sit in Prod, got %v", c.FolderID)
			}
		case "root-cred":
			if c.FolderID != nil {
				t.Fatalf("root-cred should stay at root, got %v", *c.FolderID)
			}
		}
	}

	// Re-import into the same DB: folders dedupe by name+parent, no
	// duplicates created.
	sum2, err := Apply(dst, arc, ImportOptions{}, rejectSecrets)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if len(sum2.CredFoldersCreated) != 0 {
		t.Fatalf("re-import should create no cred folders, created %v", sum2.CredFoldersCreated)
	}
	folders2, _ := dst.ListCredentialFolders()
	if len(folders2) != 2 {
		t.Fatalf("re-import duplicated folders: %d", len(folders2))
	}
}

// Pre-section archives (no credential_folders) must keep importing -
// credentials land flat, exactly the old behaviour, no FK failures.
func TestLegacyArchiveWithoutCredFolders(t *testing.T) {
	dst := openTestDB(t)
	ghost := "no-such-folder"
	arc := &Archive{
		SchemaVersion: 1,
		Credentials: []ArchiveCredential{{
			ID: "c1", Name: "legacy", Kind: store.CredPassword,
			StorageMode: store.StorageManaged, FolderID: &ghost,
		}},
	}
	sum, err := Apply(dst, arc, ImportOptions{}, rejectSecrets)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(sum.CredsCreated) != 1 {
		t.Fatalf("want 1 cred created, got %v", sum.CredsCreated)
	}
	creds, _ := dst.ListCredentials()
	if len(creds) != 1 || creds[0].FolderID != nil {
		t.Fatalf("legacy cred should land flat, got %+v", creds)
	}
}

// Dynamic folders: provider config must travel with the folder and the
// API-token credential it references must be exported + remapped.
func TestDynamicFolderRoundTrip(t *testing.T) {
	src := openTestDB(t)

	token, err := src.CreateCredential(store.NewCredential{
		Name: "pve-token", Kind: store.CredAPIToken,
		StorageMode: store.StorageManaged,
		Config:      map[string]any{"token_id": "root@pam!ssh-tool"},
	})
	if err != nil {
		t.Fatalf("create token cred: %v", err)
	}
	folder, err := src.CreateFolder(store.NewFolder{Name: "PVE Cluster"})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := src.CreateDynamicFolder(store.DynamicFolder{
		FolderID: folder.ID,
		Provider: "proxmox",
		Config: map[string]any{
			"base_url":                "https://192.0.2.10:8006",
			"api_token_credential_id": token.ID,
		},
		RefreshSeconds: 600,
	}); err != nil {
		t.Fatalf("create dyn folder: %v", err)
	}

	arc, err := Build(src, nil, nil, Options{IncludeCredentials: true, Passphrase: "pw"}, noSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(arc.Folders) != 1 || arc.Folders[0].Dynamic == nil {
		t.Fatalf("archive folder should carry dynamic config: %+v", arc.Folders)
	}
	if len(arc.Credentials) != 1 || arc.Credentials[0].Name != "pve-token" {
		t.Fatalf("token credential should be exported via dyn config ref, got %+v", arc.Credentials)
	}

	dst := openTestDB(t)
	if _, err := Apply(dst, arc, ImportOptions{}, rejectSecrets); err != nil {
		t.Fatalf("apply: %v", err)
	}
	dyns, _ := dst.ListDynamicFolders()
	if len(dyns) != 1 {
		t.Fatalf("want 1 dynamic folder after import, got %d", len(dyns))
	}
	if dyns[0].Provider != "proxmox" || dyns[0].RefreshSeconds != 600 {
		t.Fatalf("dyn config mangled: %+v", dyns[0])
	}
	creds, _ := dst.ListCredentials()
	if len(creds) != 1 {
		t.Fatalf("want 1 cred, got %d", len(creds))
	}
	if got, _ := dyns[0].Config["api_token_credential_id"].(string); got != creds[0].ID {
		t.Fatalf("api_token_credential_id should remap to %s, got %q", creds[0].ID, got)
	}
	if got, _ := dyns[0].Config["base_url"].(string); got != "https://192.0.2.10:8006" {
		t.Fatalf("base_url lost: %q", got)
	}
}

// Per-connection password overrides (password on the row, no
// credential entry) must travel inside the encrypted block and be
// restored + relinked on import - but never onto skipped rows.
func TestConnPasswordRoundTrip(t *testing.T) {
	src := openTestDB(t)
	conn, err := src.CreateConnection(store.NewConnection{
		Name: "web-01", Hostname: "192.0.2.10",
	})
	if err != nil {
		t.Fatalf("create conn: %v", err)
	}
	if err := src.SetConnectionPasswordKey(conn.ID, "conn_pass:"+conn.ID); err != nil {
		t.Fatalf("set pass key: %v", err)
	}

	fetch := func(id, vaultKey string) ([]byte, bool, error) {
		if vaultKey == "conn_pass:"+conn.ID {
			return []byte("s3cret"), true, nil
		}
		return nil, false, nil
	}
	arc, err := Build(src, nil, nil, Options{IncludeCredentials: true, Passphrase: "pw"}, fetch)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if arc.EncryptedSecrets == nil || len(arc.EncryptedSecrets.ConnCipherBy) != 1 {
		t.Fatalf("conn password not sealed: %+v", arc.EncryptedSecrets)
	}
	if plain, err := DecryptConnPassword("pw", arc.EncryptedSecrets, conn.ID); err != nil || string(plain) != "s3cret" {
		t.Fatalf("decrypt: %q %v", plain, err)
	}

	// Import into a fresh DB: password lands in the writer + row link.
	dst := openTestDB(t)
	written := map[string]string{}
	writer := func(id string, plain []byte) (string, error) {
		written[id] = string(plain)
		return "imp:" + id, nil
	}
	sum, err := Apply(dst, arc, ImportOptions{SecretPassphrase: "pw"}, writer)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sum.ConnPasswordsImported != 1 {
		t.Fatalf("want 1 conn password imported, got %+v", sum)
	}
	conns, _ := dst.ListConnections(nil)
	if len(conns) != 1 {
		t.Fatalf("want 1 conn, got %d", len(conns))
	}
	if conns[0].PasswordVaultKey == nil || *conns[0].PasswordVaultKey != "imp:"+conns[0].ID {
		t.Fatalf("password key not linked: %+v", conns[0].PasswordVaultKey)
	}
	if written[conns[0].ID] != "s3cret" {
		t.Fatalf("password not written to vault: %v", written)
	}

	// Dry-run reports the count without writing.
	dry := openTestDB(t)
	dryWritten := 0
	sumDry, err := Apply(dry, arc, ImportOptions{SecretPassphrase: "pw", DryRun: true}, func(string, []byte) (string, error) {
		dryWritten++
		return "x", nil
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if sumDry.ConnPasswordsImported != 1 || dryWritten != 0 {
		t.Fatalf("dry-run should count 1 / write 0, got %d / %d", sumDry.ConnPasswordsImported, dryWritten)
	}

	// Import back into the SOURCE db with ConflictSkip: the archive id
	// matches the existing row there, the row is skipped, and the
	// password must not be touched (skip = keep existing rows).
	written2 := 0
	sum2, err := Apply(src, arc, ImportOptions{SecretPassphrase: "pw", Conflict: ConflictSkip}, func(string, []byte) (string, error) {
		written2++
		return "y", nil
	})
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if len(sum2.ConnsSkipped) != 1 {
		t.Fatalf("conn should be skipped by id, got %+v", sum2)
	}
	if sum2.ConnPasswordsImported != 0 || written2 != 0 {
		t.Fatalf("skip must not restore passwords, got %d / %d", sum2.ConnPasswordsImported, written2)
	}
	srcConn, err := src.GetConnection(conn.ID)
	if err != nil {
		t.Fatalf("get src conn: %v", err)
	}
	if srcConn.PasswordVaultKey == nil || *srcConn.PasswordVaultKey != "conn_pass:"+conn.ID {
		t.Fatalf("skip changed the existing password link: %v", srcConn.PasswordVaultKey)
	}
}

// Custom icons (icon_image_id + image blobs) must travel in the
// archive and remap on import; StripIcon must drop both refs and
// blobs.
func TestIconRoundTrip(t *testing.T) {
	src := openTestDB(t)
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}
	imgID, err := src.PutImage(png, "image/png")
	if err != nil {
		t.Fatalf("put image: %v", err)
	}
	folder, _ := src.CreateFolder(store.NewFolder{Name: "Infra"})
	conn, _ := src.CreateConnection(store.NewConnection{Name: "web-01", Hostname: "h", FolderID: &folder.ID})
	if err := src.SetFolderIcon(folder.ID, imgID); err != nil {
		t.Fatalf("set folder icon: %v", err)
	}
	if err := src.SetConnectionIcon(conn.ID, imgID); err != nil {
		t.Fatalf("set conn icon: %v", err)
	}

	arc, err := Build(src, nil, nil, Options{}, noSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Shared icon: one blob, two references.
	if len(arc.Images) != 1 || arc.Images[0].ID != imgID {
		t.Fatalf("want 1 image in archive, got %+v", arc.Images)
	}
	if arc.Folders[0].IconImageID == nil || arc.Connections[0].IconImageID == nil {
		t.Fatalf("icon refs missing: %+v %+v", arc.Folders[0], arc.Connections[0])
	}

	dst := openTestDB(t)
	sum, err := Apply(dst, arc, ImportOptions{}, rejectSecrets)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sum.ImagesImported != 1 {
		t.Fatalf("want 1 image imported, got %+v", sum)
	}
	folders, _ := dst.ListFolders()
	conns, _ := dst.ListConnections(nil)
	if folders[0].IconImageID == nil || conns[0].IconImageID == nil {
		t.Fatalf("icons not linked after import: %+v %+v", folders[0].IconImageID, conns[0].IconImageID)
	}
	if *folders[0].IconImageID != *conns[0].IconImageID {
		t.Fatalf("shared icon should map to one image row")
	}
	mime, data, ok, _ := dst.GetImage(*conns[0].IconImageID)
	if !ok || mime != "image/png" || len(data) != len(png) {
		t.Fatalf("image content mangled: %v %s %d", ok, mime, len(data))
	}

	// Re-import: PutImage dedupes by md5 - still one image row.
	if _, err := Apply(dst, arc, ImportOptions{Conflict: ConflictRename}, rejectSecrets); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	imgs, _ := dst.ListImageIDs()
	if len(imgs) != 1 {
		t.Fatalf("re-import duplicated images: %d", len(imgs))
	}

	// StripIcon: no refs, no blobs.
	arcStripped, err := Build(src, nil, nil, Options{StripIcon: true}, noSecrets)
	if err != nil {
		t.Fatalf("build stripped: %v", err)
	}
	if len(arcStripped.Images) != 0 {
		t.Fatalf("StripIcon left image blobs: %d", len(arcStripped.Images))
	}
	if arcStripped.Folders[0].IconImageID != nil || arcStripped.Connections[0].IconImageID != nil {
		t.Fatalf("StripIcon left icon refs")
	}
}

// A connection whose auth_ref points at a credential NOT in the archive
// (credential-less share, or a stripped credential subtree) must import
// with auth_ref DROPPED to nil - inherit from the folder - not left as a
// dangling uuid the user has to hunt down and replace by hand.
func TestDanglingAuthRefDroppedToInherit(t *testing.T) {
	src := openTestDB(t)
	cred, err := src.CreateCredential(store.NewCredential{
		Name: "deploy", Kind: store.CredPassword, StorageMode: store.StorageManaged,
	})
	if err != nil {
		t.Fatalf("create cred: %v", err)
	}
	ref := cred.ID
	conn, err := src.CreateConnection(store.NewConnection{
		Name: "web-01", Hostname: "h",
		Overrides: store.InheritableSettings{AuthRef: &ref},
	})
	if err != nil {
		t.Fatalf("create conn: %v", err)
	}
	_ = conn

	// Export WITHOUT credentials - the auth_ref rides along but the
	// credential itself does not.
	arc, err := Build(src, nil, nil, Options{IncludeCredentials: false}, noSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(arc.Connections) != 1 || arc.Connections[0].Overrides.AuthRef == nil {
		t.Fatalf("archive should carry the auth_ref: %+v", arc.Connections)
	}

	// Import into a fresh DB that has never seen this credential id.
	dst := openTestDB(t)
	sum, err := Apply(dst, arc, ImportOptions{}, rejectSecrets)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	conns, _ := dst.ListConnections(nil)
	if len(conns) != 1 {
		t.Fatalf("want 1 conn, got %d", len(conns))
	}
	if conns[0].Overrides.AuthRef != nil {
		t.Fatalf("dangling auth_ref should be dropped to nil (inherit), got %q", *conns[0].Overrides.AuthRef)
	}
	// The drop should surface as a warning.
	found := false
	for _, w := range sum.Warnings {
		if strings.Contains(w, "credential reference") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a dropped-credential warning, got %v", sum.Warnings)
	}
}

// A local-shell connection round-trips: protocol + local_shell_kind
// survive export/import; StripLocal drops it entirely.
func TestLocalShellConnectionRoundTrip(t *testing.T) {
	src := openTestDB(t)
	kind := "wsl"
	if _, err := src.CreateConnection(store.NewConnection{
		Name: "telnet-sw1", Protocol: "local", LocalShellKind: &kind,
		Overrides: store.InheritableSettings{InitialCommand: strptr("telnet 10.0.0.5")},
	}); err != nil {
		t.Fatalf("create local conn: %v", err)
	}
	if _, err := src.CreateConnection(store.NewConnection{
		Name: "web-01", Hostname: "h",
	}); err != nil {
		t.Fatalf("create ssh conn: %v", err)
	}

	// Default export keeps both; protocol/kind survive.
	arc, err := Build(src, nil, nil, Options{}, noSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	dst := openTestDB(t)
	if _, err := Apply(dst, arc, ImportOptions{}, rejectSecrets); err != nil {
		t.Fatalf("apply: %v", err)
	}
	conns, _ := dst.ListConnections(nil)
	var local *store.Connection
	for i := range conns {
		if conns[i].Protocol == "local" {
			local = &conns[i]
		}
	}
	if local == nil {
		t.Fatalf("local connection missing after import: %+v", conns)
	}
	if local.LocalShellKind == nil || *local.LocalShellKind != "wsl" {
		t.Fatalf("local_shell_kind lost: %+v", local.LocalShellKind)
	}
	if local.Overrides.InitialCommand == nil || *local.Overrides.InitialCommand != "telnet 10.0.0.5" {
		t.Fatalf("initial command lost: %+v", local.Overrides.InitialCommand)
	}

	// StripLocal drops it - only the SSH connection survives.
	arcStripped, err := Build(src, nil, nil, Options{StripLocal: true}, noSecrets)
	if err != nil {
		t.Fatalf("build stripped: %v", err)
	}
	if len(arcStripped.Connections) != 1 || arcStripped.Connections[0].Protocol == "local" {
		t.Fatalf("StripLocal should leave only the SSH connection, got %+v", arcStripped.Connections)
	}
}

func strptr(s string) *string { return &s }

// Built-in (lucide) named icons + palette colour must travel in the
// archive and restore on import; StripIcon must drop them too.
func TestNamedIconRoundTrip(t *testing.T) {
	src := openTestDB(t)
	folder, _ := src.CreateFolder(store.NewFolder{Name: "Infra"})
	conn, _ := src.CreateConnection(store.NewConnection{Name: "web-01", Hostname: "h", FolderID: &folder.ID})
	if err := src.SetFolderNamedIcon(folder.ID, "server", "blue"); err != nil {
		t.Fatalf("set folder named icon: %v", err)
	}
	if err := src.SetConnectionNamedIcon(conn.ID, "database", "green"); err != nil {
		t.Fatalf("set conn named icon: %v", err)
	}

	arc, err := Build(src, nil, nil, Options{}, noSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if arc.Folders[0].IconName == nil || *arc.Folders[0].IconName != "server" || arc.Folders[0].IconColor == nil || *arc.Folders[0].IconColor != "blue" {
		t.Fatalf("folder named icon not in archive: %+v", arc.Folders[0])
	}
	if arc.Connections[0].IconName == nil || *arc.Connections[0].IconName != "database" {
		t.Fatalf("conn named icon not in archive: %+v", arc.Connections[0])
	}

	dst := openTestDB(t)
	if _, err := Apply(dst, arc, ImportOptions{}, rejectSecrets); err != nil {
		t.Fatalf("apply: %v", err)
	}
	folders, _ := dst.ListFolders()
	if len(folders) != 1 || folders[0].IconName == nil || *folders[0].IconName != "server" || folders[0].IconColor == nil || *folders[0].IconColor != "blue" {
		t.Fatalf("folder named icon lost on import: %+v", folders)
	}
	conns, _ := dst.ListConnections(nil)
	if len(conns) != 1 || conns[0].IconName == nil || *conns[0].IconName != "database" || conns[0].IconColor == nil || *conns[0].IconColor != "green" {
		t.Fatalf("conn named icon lost on import: %+v", conns)
	}

	// StripIcon drops the named icons from the archive.
	arcStripped, err := Build(src, nil, nil, Options{StripIcon: true}, noSecrets)
	if err != nil {
		t.Fatalf("build stripped: %v", err)
	}
	if arcStripped.Folders[0].IconName != nil || arcStripped.Connections[0].IconName != nil {
		t.Fatalf("StripIcon left named icons: %+v %+v", arcStripped.Folders[0], arcStripped.Connections[0])
	}
}
