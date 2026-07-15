package keepass

import (
	"bytes"
	"errors"
	"testing"

	kp "github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"
)

// buildKDBX encodes a small v4 database in memory: one group "Servers" with one
// entry carrying a title, username, password, a custom String field, and an
// attachment. Returns the raw bytes and the entry's base64 UUID.
func buildKDBX(t *testing.T, master string) (raw []byte, entryUUID string) {
	t.Helper()

	entry := kp.NewEntry()
	entry.Values = []kp.ValueData{
		{Key: "Title", Value: kp.V{Content: "prod-web"}},
		{Key: "UserName", Value: kp.V{Content: "deploy"}},
		{Key: "Password", Value: kp.V{Content: "s3cr3t", Protected: w.NewBoolWrapper(true)}},
		{Key: "PrivateKey", Value: kp.V{Content: "-----BEGIN KEY-----\nabc\n-----END KEY-----\n"}},
	}
	// An attachment named "id_ed25519". AddBinary routes to the InnerHeader
	// for v4, which is where the decoder reads binaries back from.
	db := kp.NewDatabase(kp.WithDatabaseKDBXVersion4())
	db.Content.Meta.DatabaseName = "test"
	bin := db.AddBinary([]byte("ATTACHMENT-KEY-BYTES"))
	ref := bin.CreateReference("id_ed25519")
	entry.Binaries = []kp.BinaryReference{ref}

	group := kp.NewGroup()
	group.Name = "Servers"
	group.Entries = []kp.Entry{entry}
	db.Content.Root.Groups = []kp.Group{group}

	db.Credentials = kp.NewPasswordCredentials(master)
	if err := db.LockProtectedEntries(); err != nil {
		t.Fatalf("lock: %v", err)
	}
	var buf bytes.Buffer
	if err := kp.NewEncoder(&buf).Encode(db); err != nil {
		t.Fatalf("encode: %v", err)
	}
	b64, _ := entry.UUID.MarshalText()
	return buf.Bytes(), string(b64)
}

func TestOpenAndResolvePassword(t *testing.T) {
	raw, uuid := buildKDBX(t, "master-pw")
	db, err := Open(raw, nil, "master-pw")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pw, err := db.Resolve(uuid, FieldPassword)
	if err != nil {
		t.Fatalf("resolve password: %v", err)
	}
	if pw != "s3cr3t" {
		t.Fatalf("got password %q, want s3cr3t", pw)
	}
}

func TestResolveCustomField(t *testing.T) {
	raw, uuid := buildKDBX(t, "master-pw")
	db, err := Open(raw, nil, "master-pw")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	key, err := db.Resolve(uuid, "PrivateKey")
	if err != nil {
		t.Fatalf("resolve custom field: %v", err)
	}
	if key == "" || key[:5] != "-----" {
		t.Fatalf("got %q, want a PEM-looking string", key)
	}
}

func TestResolveAttachment(t *testing.T) {
	raw, uuid := buildKDBX(t, "master-pw")
	db, err := Open(raw, nil, "master-pw")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	content, err := db.Resolve(uuid, "id_ed25519")
	if err != nil {
		t.Fatalf("resolve attachment: %v", err)
	}
	if content != "ATTACHMENT-KEY-BYTES" {
		t.Fatalf("got %q, want ATTACHMENT-KEY-BYTES", content)
	}
}

func TestWrongMasterFails(t *testing.T) {
	raw, _ := buildKDBX(t, "master-pw")
	if _, err := Open(raw, nil, "wrong-pw"); err == nil {
		t.Fatal("expected open to fail with wrong master password")
	}
}

func TestResolveUnknownUUID(t *testing.T) {
	raw, _ := buildKDBX(t, "master-pw")
	db, err := Open(raw, nil, "master-pw")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A valid-length but non-existent UUID (16 zero bytes -> base64).
	_, err = db.Resolve("AAAAAAAAAAAAAAAAAAAAAA==", FieldPassword)
	if !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("got %v, want ErrEntryNotFound", err)
	}
}

func TestBrowseListsEntry(t *testing.T) {
	raw, uuid := buildKDBX(t, "master-pw")
	db, err := Open(raw, nil, "master-pw")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tree := db.Browse()
	var found *EntryInfo
	var walk func(groups []GroupInfo)
	walk = func(groups []GroupInfo) {
		for i := range groups {
			for j := range groups[i].Entries {
				if groups[i].Entries[j].UUID == uuid {
					found = &groups[i].Entries[j]
				}
			}
			walk(groups[i].Groups)
		}
	}
	walk(tree)
	if found == nil {
		t.Fatal("entry not found in browse tree")
	}
	if found.Title != "prod-web" || found.Username != "deploy" || !found.HasPass {
		t.Fatalf("entry info wrong: %+v", found)
	}
	if len(found.Attachments) != 1 || found.Attachments[0] != "id_ed25519" {
		t.Fatalf("attachments wrong: %v", found.Attachments)
	}
	if len(found.CustomKeys) != 1 || found.CustomKeys[0] != "PrivateKey" {
		t.Fatalf("custom keys wrong: %v", found.CustomKeys)
	}
}
