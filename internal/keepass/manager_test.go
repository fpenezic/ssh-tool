package keepass

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	kp "github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"

	"ssh-tool/internal/store"
)

// fakeSecrets is an always-unlocked vault returning one master password.
type fakeSecrets struct {
	unlocked bool
	master   string
}

func (f *fakeSecrets) Get(account string) (string, bool, error) {
	if !f.unlocked {
		return "", false, nil
	}
	return f.master, true, nil
}
func (f *fakeSecrets) Unlocked() bool { return f.unlocked }

// countingFetcher hands back a fixed .kdbx blob and counts real fetches. When
// fail is set it errors (simulating an unreachable remote).
type countingFetcher struct {
	blob  []byte
	calls int
	fail  bool
}

func (c *countingFetcher) Fetch(_ store.KeepassDatabase, _ string) ([]byte, string, bool, error) {
	c.calls++
	if c.fail {
		return nil, "", false, errors.New("remote down")
	}
	return c.blob, "etag-1", false, nil
}

// smallKDBX builds a one-entry v4 database, returns bytes + entry uuid.
func smallKDBX(t *testing.T, master, password string) ([]byte, string) {
	t.Helper()
	e := kp.NewEntry()
	e.Values = []kp.ValueData{
		{Key: "Title", Value: kp.V{Content: "x"}},
		{Key: "Password", Value: kp.V{Content: password, Protected: w.NewBoolWrapper(true)}},
	}
	g := kp.NewGroup()
	g.Name = "G"
	g.Entries = []kp.Entry{e}
	db := kp.NewDatabase(kp.WithDatabaseKDBXVersion4())
	db.Content.Root.Groups = []kp.Group{g}
	db.Credentials = kp.NewPasswordCredentials(master)
	if err := db.LockProtectedEntries(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := kp.NewEncoder(&buf).Encode(db); err != nil {
		t.Fatal(err)
	}
	b64, _ := e.UUID.MarshalText()
	return buf.Bytes(), string(b64)
}

// newManagerForTest wires a manager against an in-memory store DB plus fakes.
func newManagerForTest(t *testing.T, secrets SecretReader, fetcher Fetcher) (*Manager, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewManager(db, secrets, fetcher, t.TempDir()), db
}

func TestResolveLocalFile(t *testing.T) {
	blob, uuid := smallKDBX(t, "m", "hunter2")
	dir := t.TempDir()
	kdbxPath := filepath.Join(dir, "db.kdbx")
	if err := os.WriteFile(kdbxPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	m, db := newManagerForTest(t, &fakeSecrets{unlocked: true, master: "m"}, &countingFetcher{})
	kdb, err := db.CreateKeepassDatabase(store.KeepassDatabase{
		Name: "local", Source: store.KeepassLocal, Path: kdbxPath,
		MasterRef: "acct",
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, fresh, err := m.Resolve(store.KeepassRef{DBID: kdb.ID, EntryUUID: uuid, Field: "password"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if secret != "hunter2" {
		t.Fatalf("got %q, want hunter2", secret)
	}
	if fresh != FreshLocal {
		t.Fatalf("got freshness %q, want local", fresh)
	}
}

func TestRemoteFetchThenCache(t *testing.T) {
	blob, uuid := smallKDBX(t, "m", "pw")
	fetcher := &countingFetcher{blob: blob}
	m, db := newManagerForTest(t, &fakeSecrets{unlocked: true, master: "m"}, fetcher)
	kdb, _ := db.CreateKeepassDatabase(store.KeepassDatabase{
		Name: "remote", Source: store.KeepassWebDAV, URL: "https://x/db.kdbx", MasterRef: "acct",
	})
	ref := store.KeepassRef{DBID: kdb.ID, EntryUUID: uuid, Field: "password"}

	// First resolve fetches.
	_, fresh, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if fresh != FreshFetched {
		t.Fatalf("first resolve freshness %q, want fetched", fresh)
	}
	if fetcher.calls != 1 {
		t.Fatalf("expected 1 fetch, got %d", fetcher.calls)
	}
	// Second resolve within staleAfter uses the in-memory open, no fetch.
	_, fresh, err = m.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	if fresh != FreshCached {
		t.Fatalf("second resolve freshness %q, want cached", fresh)
	}
	if fetcher.calls != 1 {
		t.Fatalf("expected still 1 fetch, got %d", fetcher.calls)
	}
}

func TestRefreshForcesFetch(t *testing.T) {
	blob, _ := smallKDBX(t, "m", "pw")
	fetcher := &countingFetcher{blob: blob}
	m, db := newManagerForTest(t, &fakeSecrets{unlocked: true, master: "m"}, fetcher)
	kdb, _ := db.CreateKeepassDatabase(store.KeepassDatabase{
		Name: "remote", Source: store.KeepassWebDAV, URL: "https://x/db.kdbx", MasterRef: "acct",
	})
	if _, err := m.Refresh(kdb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(kdb.ID); err != nil {
		t.Fatal(err)
	}
	if fetcher.calls != 2 {
		t.Fatalf("expected 2 fetches from 2 refreshes, got %d", fetcher.calls)
	}
}

func TestStaleFallbackOnOffline(t *testing.T) {
	blob, uuid := smallKDBX(t, "m", "pw")
	fetcher := &countingFetcher{blob: blob}
	m, db := newManagerForTest(t, &fakeSecrets{unlocked: true, master: "m"}, fetcher)
	kdb, _ := db.CreateKeepassDatabase(store.KeepassDatabase{
		Name: "remote", Source: store.KeepassWebDAV, URL: "https://x/db.kdbx", MasterRef: "acct",
	})
	ref := store.KeepassRef{DBID: kdb.ID, EntryUUID: uuid, Field: "password"}

	// Prime the cache with a successful fetch.
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatal(err)
	}
	// Force staleness so the next resolve tries a re-check, then make the
	// remote fail: it must fall back to the still-open decrypted copy, marked
	// stale, not error out mid-connect.
	m.mu.Lock()
	m.open[kdb.ID].openedAt = m.open[kdb.ID].openedAt.Add(-staleAfter * 2)
	m.mu.Unlock()
	fetcher.fail = true

	secret, fresh, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("expected stale fallback, got error: %v", err)
	}
	if secret != "pw" {
		t.Fatalf("got %q, want pw", secret)
	}
	if fresh != FreshStale {
		t.Fatalf("got freshness %q, want stale", fresh)
	}
}

func TestForgetOnLock(t *testing.T) {
	blob, uuid := smallKDBX(t, "m", "pw")
	secrets := &fakeSecrets{unlocked: true, master: "m"}
	fetcher := &countingFetcher{blob: blob}
	m, db := newManagerForTest(t, secrets, fetcher)
	kdb, _ := db.CreateKeepassDatabase(store.KeepassDatabase{
		Name: "remote", Source: store.KeepassWebDAV, URL: "https://x/db.kdbx", MasterRef: "acct",
	})
	ref := store.KeepassRef{DBID: kdb.ID, EntryUUID: uuid, Field: "password"}
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatal(err)
	}
	// Lock: forget decrypted state and refuse further resolves.
	m.Forget()
	secrets.unlocked = false
	if _, _, err := m.Resolve(ref); !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("expected ErrVaultLocked after lock, got %v", err)
	}
}
