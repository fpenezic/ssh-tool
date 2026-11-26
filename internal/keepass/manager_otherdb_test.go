package keepass

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// A credential synced from another machine names that machine's database
// id, which is not registered here: the registered database holding the
// entry answers it.
func TestResolveDatabaseOfAnotherMachine(t *testing.T) {
	blob, uuid := smallKDBX(t, "m", "hunter2")
	kdbxPath := filepath.Join(t.TempDir(), "db.kdbx")
	if err := os.WriteFile(kdbxPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	m, db := newManagerForTest(t, &fakeSecrets{unlocked: true, master: "m"}, &countingFetcher{})
	if _, err := db.CreateKeepassDatabase(store.KeepassDatabase{Name: "local", Source: store.KeepassLocal, Path: kdbxPath, MasterRef: "acct"}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := m.Resolve(store.KeepassRef{DBID: "id-from-another-machine", EntryUUID: uuid, Field: "password"})
	if err != nil || secret != "hunter2" {
		t.Fatalf("resolve: %q %v", secret, err)
	}
	_, _, err = m.Resolve(store.KeepassRef{DBID: "id-from-another-machine", EntryUUID: "no-such-entry", Field: "password"})
	if !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing entry: %v", err)
	}
}
